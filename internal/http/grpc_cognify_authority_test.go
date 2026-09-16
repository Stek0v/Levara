package http

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/runreg"
	pb "github.com/stek0v/levara/proto/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGRPCDocumentCognifyClaimRechecksAuthorityAndSource(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
		revision, hash, err := f.p.SourceVersion(ctx, f.r.DocumentRef)
		if err != nil {
			t.Fatal(err)
		}
		source := cognifySource{datasetID: "alpha", documentID: "blob", contentRevision: f.r.ContentRevision, sourceRevision: revision, rawContentHash: hash}
		stale := source
		stale.sourceRevision++
		if err := claimGRPCCognifySources(ctx, f.cfg, actor, []cognifySource{source, stale}, "docs", "stale"); !errors.Is(err, accesspkg.ErrDocumentVersionConflict) {
			t.Fatalf("stale claim=%v", err)
		}
		visibleRevision, visibleHash, err := f.p.SourceVersion(ctx, accesspkg.DocumentRef{DatasetID: "alpha", DataID: "visible"})
		if err != nil {
			t.Fatal(err)
		}
		if GetDBProvider() == DBSQLite {
			f.exec(`CREATE TRIGGER fail_second_claim BEFORE INSERT ON document_pipeline_statuses WHEN NEW.data_id='visible' BEGIN SELECT RAISE(ABORT,'injected second claim error'); END`)
		} else {
			f.exec(`CREATE FUNCTION fail_second_claim() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.data_id='visible' THEN RAISE EXCEPTION 'injected second claim error'; END IF; RETURN NEW; END $$`)
			f.exec(`CREATE TRIGGER fail_second_claim BEFORE INSERT ON document_pipeline_statuses FOR EACH ROW EXECUTE FUNCTION fail_second_claim()`)
		}
		visible := cognifySource{datasetID: "alpha", documentID: "visible", sourceRevision: visibleRevision, rawContentHash: visibleHash}
		if err := claimGRPCCognifySources(ctx, f.cfg, actor, []cognifySource{source, visible}, "docs", "rollback"); err == nil {
			t.Fatal("second claim DB failure was swallowed")
		}
		var rolledBack int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM document_pipeline_statuses").Scan(&rolledBack); err != nil || rolledBack != 0 {
			t.Fatalf("partial claim count=%d err=%v", rolledBack, err)
		}
		f.exec("INSERT INTO credential_epochs(user_id,epoch,revoked_before) VALUES('owner',1,0)")
		if err := claimGRPCCognifySources(ctx, f.cfg, actor, []cognifySource{source}, "docs", "revoked"); !errors.Is(err, accesspkg.ErrRevokedCredential) {
			t.Fatalf("revoked claim=%v", err)
		}
		actor.Credential.Epoch = 1
		if _, err := f.p.SetDocumentHold(ctx, f.owner, f.r.DocumentRef, f.r.ACLRevision, true); err != nil {
			t.Fatal(err)
		}
		if err := claimGRPCCognifySources(ctx, f.cfg, actor, []cognifySource{source}, "docs", "held"); !errors.Is(err, accesspkg.ErrDocumentForbidden) {
			t.Fatalf("held claim=%v", err)
		}
		var claims int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM document_pipeline_statuses").Scan(&claims); err != nil || claims != 0 {
			t.Fatalf("claims=%d err=%v", claims, err)
		}
	})
}

func TestGRPCDocumentCognifyStatusFenceThroughSend(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		revision, hash, err := f.p.SourceVersion(ctx, f.r.DocumentRef)
		if err != nil {
			t.Fatal(err)
		}
		proof, _ := json.Marshal([]searchDocumentSource{{DatasetID: "alpha", DocumentID: "blob", ContentRevision: f.r.ContentRevision, SourceRevision: revision, RawContentHash: hash}})
		cfg := f.cfg
		cfg.RequireAuth, cfg.Runs = true, runreg.New()
		cfg.Runs.Store("run", &runreg.Status{OwnerID: "owner", TenantID: "a", SourcesJSON: string(proof), RunID: "run", Status: "RUNNING", Stage: "starting", StartedAt: time.Now()})
		actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
		entered, release := make(chan struct{}), make(chan struct{})
		var releaseOnce sync.Once
		releaseSend := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(releaseSend)
		result := make(chan error, 1)
		go func() {
			result <- WatchGRPCDocumentCognify(ctx, cfg, actor, "run", func(frame *pb.DocumentCognifyStatus) error { close(entered); <-release; return nil })
		}()
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("status Send did not begin")
		}
		revoked := make(chan error, 1)
		go func() {
			_, err := f.db.ExecContext(ctx, "UPDATE users SET is_active=false WHERE id='owner'")
			revoked <- err
		}()
		select {
		case err := <-revoked:
			t.Fatalf("revocation passed active Send: %v", err)
		case <-time.After(30 * time.Millisecond):
		}
		releaseSend()
		if err := <-revoked; err != nil {
			t.Fatal(err)
		}
		if err := <-result; status.Code(err) != codes.NotFound {
			t.Fatalf("post-revoke status=%v", err)
		}
	})
}

func TestGRPCDocumentCognifyTransferFenceSurvivesObserverCancel(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(2)
		writerCtx, writerCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer writerCancel()
		writer, err := f.db.Conn(writerCtx)
		if err != nil {
			t.Fatal(err)
		}
		defer writer.Close()
		if GetDBProvider() == DBSQLite {
			if _, err := writer.ExecContext(writerCtx, "PRAGMA busy_timeout=3000"); err != nil {
				t.Fatal(err)
			}
		}
		observer, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, release, err := f.p.BeginTransferFence(observer, GetDBProvider() == DBSQLite)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		cancel()
		result := make(chan error, 1)
		go func() {
			_, err := writer.ExecContext(writerCtx, "UPDATE users SET is_active=false WHERE id='owner'")
			result <- err
		}()
		select {
		case err := <-result:
			t.Fatalf("revocation passed canceled observer before transfer drain: %v", err)
		case <-time.After(30 * time.Millisecond):
		}
		release()
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	})
}

func TestGRPCDocumentCognifyDetachedJobRechecksTenant(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		f.exec("UPDATE users SET is_superuser=true WHERE id='owner'")
		actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
		cfg := f.cfg
		cfg.RequireAuth = true
		verified := grpcCognifyContext(ctx, cfg, actor)
		jobCtx, jobCancel := context.WithTimeout(context.WithoutCancel(verified), 5*time.Second)
		defer jobCancel()
		jobCtx = context.WithValue(jobCtx, searchEvidenceKey{}, &searchEvidence{sources: make(map[searchDocumentSource]struct{})})
		cancel()
		f.exec("DELETE FROM user_tenant WHERE user_id='owner' AND tenant_id='a'")
		for _, doc := range []string{"blob", "visible"} {
			revision, hash, err := f.p.SourceVersion(jobCtx, accesspkg.DocumentRef{DatasetID: "alpha", DataID: doc})
			if err != nil {
				t.Fatal(err)
			}
			source := cognifySource{datasetID: "alpha", documentID: doc, sourceRevision: revision, rawContentHash: hash}
			if release, err := beginCognifyTransfer(jobCtx, cfg, source); status.Code(grpcCognifyError(err)) != codes.PermissionDenied {
				if release != nil {
					release()
				}
				t.Fatalf("detached %s model/write guard returned %v", doc, err)
			}
		}
	})
}

func TestGRPCDocumentCognifyAnonymousSelectedTenantDenied(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		actor := accesspkg.MetadataActor{Actor: accesspkg.Actor{TenantID: "a"}, TrustedLocal: true}
		verified := grpcCognifyContext(ctx, f.cfg, actor)
		if _, release, err := beginSearchReadFence(verified); status.Code(grpcCognifyError(err)) != codes.PermissionDenied {
			if release != nil {
				release()
			}
			t.Fatalf("anonymous selected tenant: %v", err)
		}
		actor.TenantID = ""
		local := grpcCognifyContext(ctx, f.cfg, actor)
		fenced, release, err := beginSearchReadFence(local)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if _, ok := fenced.Value(searchReadPolicyKey{}).(accesspkg.SQLPolicy); !ok {
			t.Fatal("metadata-backed local document scope bypassed SQL fence")
		}
	})
}
