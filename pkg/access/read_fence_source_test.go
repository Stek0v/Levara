package access_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	httpapi "github.com/stek0v/levara/internal/http"
	"github.com/stek0v/levara/pkg/access"
)

func sourceHash(text string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(text))) }
func TestDocumentPublicationRejectsChangedRawSource(t *testing.T) {
	documentDialects(t, func(t *testing.T, f documentFixture) {
		sourceFixtureSetup(t, f)
		r := f.registered(f.ref, access.DocumentRestricted)
		f.exec("UPDATE data SET raw_content_hash=$1 WHERE id='blob'", sourceHash("A"))
		ctx, cancel := context.WithTimeout(f.ctx, 3*time.Second)
		defer cancel()
		p, release, err := f.p.BeginReadFence(ctx, httpapi.GetDBProvider() == httpapi.DBSQLite)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if err := p.CommitDocumentIndex(ctx, f.owner, f.ref, r.ContentRevision, "docs", "generation-A"); err != nil {
			t.Fatal(err)
		}
		f.exec("UPDATE data SET raw_content_hash=$1 WHERE id='blob'", sourceHash("B"))
		present, err := f.p.DocumentIndexPublished(f.ctx, f.ref, r.ContentRevision, "docs", "generation-A")
		if err != nil {
			t.Fatal(err)
		}
		if present {
			t.Fatal("published generation survived changed raw source")
		}
	})
}

func sourceFixtureSetup(t *testing.T, f documentFixture) {
	t.Helper()
	if err := access.EnsureIdentitySchema(f.ctx, f.db, f.p.Q); err != nil {
		t.Fatal(err)
	}
	if err := access.EnsureBrowserSessionSchema(f.ctx, f.db, f.p.Q); err != nil {
		t.Fatal(err)
	}
}

func publishSource(t *testing.T, f documentFixture, actor access.Actor, ref access.DocumentRef, contentRevision int64, generation string, lineage access.DocumentPublicationLineage) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 4*time.Second)
	defer cancel()
	p, release, err := f.p.BeginReadFence(ctx, httpapi.GetDBProvider() == httpapi.DBSQLite)
	if err != nil {
		return err
	}
	defer release()
	return p.CommitDocumentIndexVersioned(ctx, actor, ref, contentRevision, "docs", generation, lineage)
}
func currentSource(t *testing.T, f documentFixture, ref access.DocumentRef) access.DocumentPublicationLineage {
	t.Helper()
	revision, hash, err := f.p.SourceVersion(f.ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	return access.DocumentPublicationLineage{SourceRevision: revision, RawContentHash: hash, SourcesJSON: "[]"}
}
func TestDocumentSourceVersionAndLegacyPublication(t *testing.T) {
	documentDialects(t, func(t *testing.T, f documentFixture) {
		sourceFixtureSetup(t, f)
		hash := sourceHash("legacy bytes")
		f.exec("UPDATE data SET raw_content_hash=$1 WHERE id='blob'", strings.ToUpper(hash))
		lineage := currentSource(t, f, f.ref)
		if lineage.SourceRevision != 1 || lineage.RawContentHash != hash {
			t.Fatal(lineage)
		}
		actor := f.owner
		actor.TenantID = "" // legacy association has no invented tenant binding.
		if err := publishSource(t, f, actor, f.ref, 0, "legacy", lineage); err != nil {
			t.Fatal(err)
		}
		got, ok, err := f.p.DocumentIndexLineage(f.ctx, f.ref, 0, "docs", "legacy")
		if err != nil || !ok || got.SourceRevision != 1 || got.RawContentHash != hash {
			t.Fatalf("lineage %+v %v %v", got, ok, err)
		}
		var registered int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM document_resources").Scan(&registered); err != nil || registered != 0 {
			t.Fatal("legacy publication invented registration", err)
		}
		r := f.registered(f.ref, access.DocumentRestricted)
		if ok, err := f.p.DocumentIndexPublished(f.ctx, f.ref, 0, "docs", "legacy"); err != nil || ok {
			t.Fatalf("legacy generation bypassed registration: %v %v", ok, err)
		}
		if err := publishSource(t, f, f.owner, f.ref, 0, "bypass", lineage); !errors.Is(err, access.ErrDocumentVersionConflict) {
			t.Fatalf("legacy write bypass %v", err)
		}
		if err := publishSource(t, f, f.owner, f.ref, r.ContentRevision, "registered", lineage); err != nil {
			t.Fatal(err)
		}
		f.exec("DELETE FROM dataset_data WHERE dataset_id='alpha' AND data_id='blob'")
		if _, _, err := f.p.SourceVersion(f.ctx, f.ref); !errors.Is(err, access.ErrDocumentNotFound) {
			t.Fatalf("missing association %v", err)
		}
		if ok, err := f.p.DocumentIndexPublished(f.ctx, f.ref, r.ContentRevision, "docs", "registered"); err != nil || ok {
			t.Fatalf("unlinked source published: %v %v", ok, err)
		}
	})
}
func TestDocumentPublicationVersionedInputAndACL(t *testing.T) {
	documentDialects(t, func(t *testing.T, f documentFixture) {
		sourceFixtureSetup(t, f)
		f.exec("UPDATE data SET raw_content_hash=$1 WHERE id='blob'", sourceHash("source"))
		base := currentSource(t, f, f.ref)
		for _, kind := range []string{"zero-version", "negative-version", "empty-hash", "short-hash", "nonhex-hash", "wrong-hash", "stale-version", "negative-registry", "wrong-registry", "malformed-lineage", "viewer", "inactive", "read-key"} {
			t.Run(kind, func(t *testing.T) {
				lineage := base
				actor := f.owner
				revision := int64(0)
				switch kind {
				case "zero-version":
					lineage.SourceRevision = 0
				case "negative-version":
					lineage.SourceRevision = -1
				case "empty-hash":
					lineage.RawContentHash = ""
				case "short-hash":
					lineage.RawContentHash = strings.Repeat("a", 63)
				case "nonhex-hash":
					lineage.RawContentHash = strings.Repeat("g", 64)
				case "wrong-hash":
					lineage.RawContentHash = sourceHash("different")
				case "stale-version":
					lineage.SourceRevision = 2
				case "negative-registry":
					revision = -1
				case "wrong-registry":
					revision = 1
				case "malformed-lineage":
					lineage.SourcesJSON = "null"
				case "viewer":
					actor = f.viewer
				case "inactive":
					actor.UserID = "inactive"
				case "read-key":
					actor.APIKeyPermissions = "read-only"
				}
				if err := publishSource(t, f, actor, f.ref, revision, "invalid-"+kind, lineage); err == nil {
					t.Fatal("invalid publication accepted")
				}
			})
		}
		ctx, cancel := context.WithTimeout(f.ctx, time.Second)
		defer cancel()
		p, release, err := f.p.BeginReadFence(ctx, httpapi.GetDBProvider() == httpapi.DBSQLite)
		if err != nil {
			t.Fatal(err)
		}
		err = p.CommitDocumentIndex(ctx, f.owner, f.ref, 0, "docs", "legacy-empty")
		release()
		if !errors.Is(err, access.ErrDocumentInvalid) {
			t.Fatalf("compatibility accepted legacy wildcard: %v", err)
		}
		if err := f.p.CommitDocumentIndexVersioned(f.ctx, f.owner, f.ref, 0, "docs", "no-fence", base); !errors.Is(err, access.ErrDocumentInvalid) {
			t.Fatalf("missing fence %v", err)
		}
		var count int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM document_index_publications").Scan(&count); err != nil || count != 0 {
			t.Fatal("failed publication persisted", err, count)
		}
		for _, hash := range []string{"", strings.Repeat("z", 64), "short"} {
			f.exec("UPDATE data SET raw_content_hash=$1 WHERE id='blob'", hash)
			if _, _, err := f.p.SourceVersion(f.ctx, f.ref); !errors.Is(err, access.ErrDocumentVersionConflict) {
				t.Fatalf("malformed stored hash %q: %v", hash, err)
			}
		}
	})
}
func TestDocumentPublicationRequiresCurrentVersionOnRead(t *testing.T) {
	documentDialects(t, func(t *testing.T, f documentFixture) {
		sourceFixtureSetup(t, f)
		f.exec("UPDATE data SET raw_content_hash=$1 WHERE id='blob'", sourceHash("A"))
		old := currentSource(t, f, f.ref)
		if err := publishSource(t, f, f.owner, f.ref, 0, "old", old); err != nil {
			t.Fatal(err)
		}
		// A->B->A with monotonically advancing source revision cannot revive A's index.
		f.exec("UPDATE data SET raw_content_hash=$1,source_revision=source_revision+1 WHERE id='blob'", sourceHash("B"))
		f.exec("UPDATE data SET raw_content_hash=$1,source_revision=source_revision+1 WHERE id='blob'", sourceHash("A"))
		if ok, err := f.p.DocumentIndexPublished(f.ctx, f.ref, 0, "docs", "old"); err != nil || ok {
			t.Fatalf("old generation revived: %v %v", ok, err)
		}
		if err := publishSource(t, f, f.owner, f.ref, 0, "late-A", old); !errors.Is(err, access.ErrDocumentVersionConflict) {
			t.Fatalf("stale A published %v", err)
		}
		current := currentSource(t, f, f.ref)
		if current.SourceRevision != 3 {
			t.Fatal(current)
		}
		if err := publishSource(t, f, f.owner, f.ref, 0, "current", current); err != nil {
			t.Fatal(err)
		}
		f.exec("UPDATE document_index_publications SET raw_content_hash='' WHERE data_id='blob'")
		if ok, err := f.p.DocumentIndexPublished(f.ctx, f.ref, 0, "docs", "current"); err != nil || ok {
			t.Fatalf("empty-hash publication visible %v %v", ok, err)
		}
	})
}
func TestDocumentRenameSourceVersionAndPublications(t *testing.T) {
	documentDialects(t, func(t *testing.T, f documentFixture) {
		sourceFixtureSetup(t, f)
		f.exec("UPDATE data SET raw_content_hash=$1 WHERE id='blob'", sourceHash("bytes"))
		old := currentSource(t, f, f.ref)
		f.exec(`INSERT INTO document_structured_artifacts
			(id,data_id,source_revision,raw_content_hash,artifact_sha256,byte_size,storage_location,state)
			VALUES('rename-artifact','blob',$1,$2,'artifact',2,'storage://artifact','active')`, old.SourceRevision, old.RawContentHash)
		for _, ref := range []access.DocumentRef{f.ref, f.other} {
			if err := publishSource(t, f, f.owner, ref, 0, "legacy", old); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.p.RenameLegacyDocument(f.ctx, f.owner, f.ref, "renamed"); err != nil {
			t.Fatal(err)
		}
		renamed := currentSource(t, f, f.ref)
		if renamed.SourceRevision != 2 || renamed.RawContentHash != old.RawContentHash {
			t.Fatal(renamed)
		}
		var n int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM document_index_publications").Scan(&n); err != nil || n != 0 {
			t.Fatal("rename kept aliases", err, n)
		}
		var artifactState string
		if err := f.db.QueryRow("SELECT state FROM document_structured_artifacts WHERE id='rename-artifact'").Scan(&artifactState); err != nil || artifactState != "retired" {
			t.Fatal("rename kept structured artifact active", err, artifactState)
		}
		if err := publishSource(t, f, f.owner, f.ref, 0, "renamed", renamed); err != nil {
			t.Fatal(err)
		}
		if err := f.p.RenameLegacyDocument(f.ctx, f.owner, f.ref, "renamed"); err != nil {
			t.Fatal(err)
		}
		if got := currentSource(t, f, f.ref); got.SourceRevision != 2 {
			t.Fatal("same-name changed source", got)
		}
		if ok, err := f.p.DocumentIndexPublished(f.ctx, f.ref, 0, "docs", "renamed"); err != nil || !ok {
			t.Fatal("same-name invalidated", err)
		}
		f.exec("DELETE FROM dataset_data WHERE dataset_id='beta' AND data_id='blob'")
		r := f.registered(f.ref, access.DocumentRestricted)
		if err := publishSource(t, f, f.owner, f.ref, r.ContentRevision, "registered", renamed); err != nil {
			t.Fatal(err)
		}
		r, err := f.p.RenameDocument(f.ctx, f.owner, f.ref, r.ACLRevision, r.ContentRevision, "registered name")
		if err != nil {
			t.Fatal(err)
		}
		if got := currentSource(t, f, f.ref); got.SourceRevision != 3 {
			t.Fatal(got)
		}
		if err := f.db.QueryRow("SELECT COUNT(*) FROM document_index_publications").Scan(&n); err != nil || n != 0 {
			t.Fatal("registered rename kept publication", err, n)
		}
	})
}

func TestDocumentRenameCounterFaultRollsBack(t *testing.T) {
	documentDialects(t, func(t *testing.T, f documentFixture) {
		sourceFixtureSetup(t, f)
		f.exec("UPDATE data SET raw_content_hash=$1 WHERE id='blob'", sourceHash("bytes"))
		initial := currentSource(t, f, f.ref)
		if err := publishSource(t, f, f.owner, f.ref, 0, "legacy", initial); err != nil {
			t.Fatal(err)
		}
		f.exec("UPDATE source_revision_counter SET value=9223372036854775807 WHERE id=1")
		if err := f.p.RenameLegacyDocument(f.ctx, f.owner, f.ref, "must rollback"); err == nil {
			t.Fatal("overflow rename accepted")
		}
		var name string
		if err := f.db.QueryRow("SELECT name FROM data WHERE id='blob'").Scan(&name); err != nil || name != "Shared bytes" {
			t.Fatal("legacy name changed on failure", err, name)
		}
		if ok, err := f.p.DocumentIndexPublished(f.ctx, f.ref, 0, "docs", "legacy"); err != nil || !ok {
			t.Fatal("legacy publication invalidated on rollback", err)
		}
		f.exec("DELETE FROM dataset_data WHERE dataset_id='beta' AND data_id='blob'")
		r := f.registered(f.ref, access.DocumentRestricted)
		if err := publishSource(t, f, f.owner, f.ref, r.ContentRevision, "registered", initial); err != nil {
			t.Fatal(err)
		}
		if _, err := f.p.RenameDocument(f.ctx, f.owner, f.ref, r.ACLRevision, r.ContentRevision, "must rollback"); err == nil {
			t.Fatal("registered overflow rename accepted")
		}
		current, err := f.p.GetDocumentResource(f.ctx, f.ref)
		if err != nil || current.ACLRevision != r.ACLRevision || current.ContentRevision != r.ContentRevision {
			t.Fatal("registry revisions changed on failed rename", err, current)
		}
		if got := currentSource(t, f, f.ref); got.SourceRevision != initial.SourceRevision {
			t.Fatal("source changed on failed rename", got)
		}
		if ok, err := f.p.DocumentIndexPublished(f.ctx, f.ref, r.ContentRevision, "docs", "registered"); err != nil || !ok {
			t.Fatal("registered publication invalidated on rollback", err)
		}
	})
}
