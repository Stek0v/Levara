package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	httpapi "github.com/stek0v/levara/internal/http"
	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/ingest"
)

func TestVerifiedSQLiteHoldRetentionLifecycle(t *testing.T) {
	o := verifiedFixture(t)
	ctx := context.Background()
	previousProvider := httpapi.GetDBProvider()
	httpapi.SetDBProvider(httpapi.DBSQLite)
	ingest.SetSQLiteMode(true)
	t.Cleanup(func() {
		httpapi.SetDBProvider(previousProvider)
		ingest.SetSQLiteMode(false)
	})
	dbPath := filepath.Join(o.DataDir, "levara.db")
	if err := os.Remove(dbPath); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := httpapi.MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := access.EnsureIdentitySchema(ctx, db, httpapi.Q); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"INSERT INTO principals(id,type) VALUES('owner','user')",
		"INSERT INTO users(id,email,hashed_password) VALUES('owner','owner@hold.invalid','locked')",
		"INSERT INTO tenants(id,name,owner_id) VALUES('held-tenant','Held Tenant','owner')",
		"INSERT INTO user_tenant(user_id,tenant_id) VALUES('owner','held-tenant')",
		"INSERT INTO datasets(id,name,owner_id) VALUES('held-project','Held Project','owner')",
		"INSERT INTO api_keys(id,key_hash,user_id,permissions) VALUES('hold-key','fixture-hash','owner','read-write')",
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	actor := access.Actor{UserID: "owner", TenantID: "held-tenant", APIKeyPermissions: "read-write", AuthMethod: "api_key"}
	proof := access.MetadataActor{Actor: actor, Credential: access.MetadataCredential{Kind: "api_key", KeyID: "hold-key"}}
	raw, original, structured := []byte("derived text\n"), []byte{0, 1, 255, 13, 0}, []byte(`{"held":"evidence"}`)
	writer := ingest.NewMetadataWriterFromDB(db)
	defer writer.Close()
	results, count, err := writer.IngestAuthorized(ctx,
		[]ingest.Item{{ID: "held-source", Text: string(raw), StructuredArtifact: structured}},
		[]ingest.Item{{ID: "held-source", Filename: "original.bin", FileData: original}},
		o.UploadsPath, nil, proof, "held-project", "Held Project")
	if err != nil || count != 1 || len(results) != 1 {
		t.Fatalf("ingest held evidence: results=%+v count=%d err=%v", results, count, err)
	}
	result := results[0]
	ref := access.DocumentRef{DatasetID: "held-project", DataID: result.ID}
	policy := access.SQLPolicy{DB: db, Q: httpapi.Q, QA: httpapi.QArgs}
	resource, err := policy.RegisterDocument(ctx, actor, ref, actor.TenantID, access.DocumentRestricted)
	if err != nil {
		t.Fatal(err)
	}
	held, err := policy.SetDocumentHold(ctx, actor, ref, resource.ACLRevision, true)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes := func(location string, want []byte) {
		t.Helper()
		got, err := os.ReadFile(strings.TrimPrefix(location, "file://"))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("retained bytes %q: got=%q err=%v", location, got, err)
		}
	}
	assertHeld := func(db *sql.DB) {
		t.Helper()
		policy := access.SQLPolicy{DB: db, Q: httpapi.Q, QA: httpapi.QArgs}
		got, err := policy.GetDocumentResource(ctx, ref)
		if err != nil || got != held || !got.Hold || got.Tombstoned {
			t.Fatalf("held policy changed: got=%+v want=%+v err=%v", got, held, err)
		}
		decision, err := policy.AuthorizeDocument(ctx, actor, ref, access.ActionDelete)
		if err != nil || decision.Allowed || decision.Reason != "document_hold" {
			t.Fatalf("held delete authorization: decision=%+v err=%v", decision, err)
		}
		if err := policy.DeleteDocumentAssociation(ctx, actor, ref, held.ACLRevision, held.ContentRevision); !errors.Is(err, access.ErrDocumentForbidden) {
			t.Fatalf("held delete result: %v", err)
		}
		got, err = policy.GetDocumentResource(ctx, ref)
		if err != nil || got != held {
			t.Fatalf("blocked delete changed held policy: %+v err=%v", got, err)
		}
		var membership int
		if err := db.QueryRow("SELECT COUNT(*) FROM user_tenant WHERE user_id='owner' AND tenant_id='held-tenant'").Scan(&membership); err != nil || membership != 1 {
			t.Fatalf("held tenant membership changed: count=%d err=%v", membership, err)
		}
		var rawLocation, originalLocation, artifactLocation, state, artifactHash string
		var revision, artifactRevision int64
		err = db.QueryRow(`SELECT d.raw_data_location,d.original_data_location,d.source_revision,
		 a.storage_location,a.state,a.source_revision,a.artifact_sha256
		 FROM data d JOIN dataset_data dd ON dd.data_id=d.id
		 JOIN document_structured_artifacts a ON a.data_id=d.id
		 WHERE dd.dataset_id='held-project' AND d.id='held-source'`).
			Scan(&rawLocation, &originalLocation, &revision, &artifactLocation, &state, &artifactRevision, &artifactHash)
		if err != nil || revision != result.SourceRevision || artifactRevision != result.SourceRevision || artifactHash != result.StructuredArtifactHash || state != "active" {
			t.Fatalf("held source lineage changed: revision=%d artifact_revision=%d state=%q hash=%q err=%v", revision, artifactRevision, state, artifactHash, err)
		}
		assertBytes(rawLocation, raw)
		assertBytes(originalLocation, original)
		assertBytes(artifactLocation, structured)
	}
	assertHeld(db)
	if err := writer.CleanupRetiredStructuredArtifacts(ctx, result.ID, o.UploadsPath, nil); err != nil {
		t.Fatal(err)
	}
	assertHeld(db)
	if err := errors.Join(writer.Close(), db.Close()); err != nil {
		t.Fatal(err)
	}
	receipt, err := CreateVerifiedBackup(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	archiveBefore, err := os.ReadFile(receipt.Archive)
	if err != nil {
		t.Fatal(err)
	}
	assertRestoredHold := func() {
		t.Helper()
		defaults, err := verifiedDefaults(o)
		if err != nil {
			t.Fatal(err)
		}
		sandbox := t.TempDir()
		manifest, _, _, err := extractVerified(ctx, receipt.Archive, sandbox, defaults)
		if err != nil {
			t.Fatal(err)
		}
		restored, closeRestored, err := snapshotSQLForProof(ctx, defaults, filepath.Join(sandbox, "sql", "sqlite.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := closeRestored(); err != nil {
				t.Error(err)
			}
		}()
		if err := rebaseAndCheckReferences(ctx, restored, manifest, sandbox, defaults.MaxRows); err != nil {
			t.Fatal(err)
		}
		assertHeld(restored)
	}
	assertRestoredHold()
	db, err = sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	policy.DB = db
	writer = ingest.NewMetadataWriterFromDB(db)
	defer writer.Close()
	released, err := policy.SetDocumentHold(ctx, actor, ref, held.ACLRevision, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.DeleteDocumentAssociation(ctx, actor, ref, released.ACLRevision, released.ContentRevision); err != nil {
		t.Fatal(err)
	}
	deleted, err := policy.GetDocumentResource(ctx, ref)
	if err != nil || !deleted.Tombstoned || deleted.Hold || deleted.ContentRevision <= held.ContentRevision {
		t.Fatalf("released deletion policy: %+v err=%v", deleted, err)
	}
	var state string
	if err := db.QueryRow("SELECT state FROM document_structured_artifacts WHERE data_id='held-source'").Scan(&state); err != nil || state != "retired" {
		t.Fatalf("deleted source artifact not retired: state=%q err=%v", state, err)
	}
	var sourceLinks int
	if err := db.QueryRow("SELECT (SELECT COUNT(*) FROM data WHERE id='held-source')+(SELECT COUNT(*) FROM dataset_data WHERE data_id='held-source')").Scan(&sourceLinks); err != nil || sourceLinks != 0 {
		t.Fatalf("deleted source SQL remains: count=%d err=%v", sourceLinks, err)
	}
	if err := writer.CleanupRetiredStructuredArtifacts(ctx, result.ID, o.UploadsPath, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(strings.TrimPrefix(result.StructuredArtifactPath, "file://")); !os.IsNotExist(err) {
		t.Fatalf("retired structured bytes remain: %v", err)
	}
	var remaining int
	if err := db.QueryRow("SELECT COUNT(*) FROM document_structured_artifacts WHERE data_id='held-source'").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("retired structured inventory remains: count=%d err=%v", remaining, err)
	}
	// SQL unlink and structured cleanup do not promise raw/original blob erasure.
	assertBytes(result.FilePath, raw)
	assertBytes(result.OriginalFilePath, original)
	if err := errors.Join(writer.Close(), db.Close()); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{o.DataDir, o.WorkspacePath, o.UploadsPath} {
		if err := os.Rename(root, root+"-unavailable"); err != nil {
			t.Fatal(err)
		}
	}
	verified, err := VerifyArchive(ctx, receipt.Archive, o)
	if err != nil || verified.ArchiveSHA256 != receipt.ArchiveSHA256 {
		t.Fatalf("prior held archive without live roots: receipt=%+v err=%v", verified, err)
	}
	assertRestoredHold()
	archiveAfter, err := os.ReadFile(receipt.Archive)
	if err != nil || sha256.Sum256(archiveBefore) != sha256.Sum256(archiveAfter) {
		t.Fatalf("historical archive changed after live deletion: %v", err)
	}
}
