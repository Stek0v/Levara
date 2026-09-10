package access_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/access"
)

func pruneActorFixture(t *testing.T, f documentFixture) access.MetadataActor {
	t.Helper()
	if err := access.EnsureIdentitySchema(f.ctx, f.db, f.p.Q); err != nil {
		t.Fatal(err)
	}
	if err := access.EnsureBrowserSessionSchema(f.ctx, f.db, f.p.Q); err != nil {
		t.Fatal(err)
	}
	return access.MetadataActor{Actor: access.Actor{UserID: "root"}, Credential: access.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
}

func TestPruneDataOrphanHoldIsInstanceWide(t *testing.T) {
	documentDialects(t, func(t *testing.T, f documentFixture) {
		actor := pruneActorFixture(t, f)
		f.registered(f.ref, access.DocumentRestricted)
		f.exec("UPDATE document_resources SET dataset_id='gone',data_id='gone',hold=true,tombstoned=true")
		if err := f.p.PruneData(f.ctx, actor, true); !errors.Is(err, access.ErrDocumentForbidden) {
			t.Fatalf("orphan hold ignored: %v", err)
		}
		var count int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM datasets").Scan(&count); err != nil || count != 2 {
			t.Fatalf("datasets=%d err=%v", count, err)
		}
	})
}

func TestPruneRetiresStructuredArtifactsAfterHoldCheck(t *testing.T) {
	documentDialects(t, func(t *testing.T, f documentFixture) {
		actor := pruneActorFixture(t, f)
		r := f.registered(f.ref, access.DocumentRestricted)
		f.exec(`INSERT INTO document_structured_artifacts
			(id,data_id,source_revision,raw_content_hash,artifact_sha256,byte_size,storage_location,state)
			VALUES('artifact','blob',1,'source','artifact',2,'storage://artifact','active')`)
		r, err := f.p.SetDocumentHold(f.ctx, f.owner, f.ref, r.ACLRevision, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.p.PruneData(f.ctx, actor, true); !errors.Is(err, access.ErrDocumentForbidden) {
			t.Fatalf("held prune error=%v", err)
		}
		var state string
		if err := f.db.QueryRow("SELECT state FROM document_structured_artifacts WHERE id='artifact'").Scan(&state); err != nil || state != "active" {
			t.Fatalf("held prune changed artifact state=%q err=%v", state, err)
		}
		if _, err := f.p.SetDocumentHold(f.ctx, f.owner, f.ref, r.ACLRevision, false); err != nil {
			t.Fatal(err)
		}
		if err := f.p.PruneData(f.ctx, actor, true); err != nil {
			t.Fatal(err)
		}
		if err := f.db.QueryRow("SELECT state FROM document_structured_artifacts WHERE id='artifact'").Scan(&state); err != nil || state != "retired" {
			t.Fatalf("pruned artifact state=%q err=%v", state, err)
		}
	})
}

func TestPruneDataLockWaitDeadlineAndRevocation(t *testing.T) {
	documentDialects(t, func(t *testing.T, f documentFixture) {
		actor := pruneActorFixture(t, f)
		tx, err := f.db.BeginTx(f.ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = tx.Exec("UPDATE users SET is_active=false WHERE id='root'"); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(f.ctx, 30*time.Millisecond)
		start := time.Now()
		err = f.p.PruneData(ctx, actor, true)
		cancel()
		if err == nil || time.Since(start) > time.Second {
			t.Fatalf("lock wait ignored parent deadline: elapsed=%s err=%v", time.Since(start), err)
		}
		// The blocked operation never changes SQL. A credential revoked before
		// the next fence is acquired must be re-read after that lock is released.
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := f.p.PruneData(f.ctx, actor, true); err == nil {
			t.Fatal("revoked administrator pruned")
		}
		var count int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM datasets").Scan(&count); err != nil || count != 2 {
			t.Fatalf("datasets=%d err=%v", count, err)
		}
	})
}
