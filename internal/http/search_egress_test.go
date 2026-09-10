package http

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/gofiber/fiber/v2"
	"io"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	accesspkg "github.com/stek0v/levara/pkg/access"
)

func TestGlobalSearchRejectsBeforeEffectAndRechecksDemotion(t *testing.T) {
	for _, strategy := range []string{"COMMUNITY_GLOBAL", "COMMUNITY_LOCAL", "TEMPORAL", "NATURAL_LANGUAGE", "CYPHER"} {
		for _, state := range []string{"denied", "allowed", "demoted"} {
			admin := state != "denied"
			t.Run(fmt.Sprintf("%s/%s", strategy, state), func(t *testing.T) {
				env := newSearchTestEnv(t)
				env.insertUser("reader", "reader@test.invalid", admin)
				var calls atomic.Int32
				r := NewDefaultStrategyRegistry()
				r.Register(funcStrategy{name: strategy, fn: func(c *fiber.Ctx, _ APIConfig, _ UnifiedSearchRequest) error {
					calls.Add(1)
					if state == "demoted" {
						if _, err := env.db.Exec("UPDATE users SET is_superuser=0 WHERE id='reader'"); err != nil {
							return err
						}
					}
					return c.JSON(fiber.Map{"private": "global-source"})
				}})
				env.cfg.SearchStrategies = r
				env.startWithUser("reader")
				req := httptest.NewRequest("POST", "/search/text", bytes.NewBufferString(fmt.Sprintf(`{"query_text":"test","query_type":%q}`, strategy)))
				req.Header.Set("Content-Type", "application/json")
				resp, err := env.app.Test(req, -1)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				body, _ := io.ReadAll(resp.Body)
				wantStatus := 403
				if state == "allowed" {
					wantStatus = 200
				}
				if resp.StatusCode != wantStatus || (bytes.Contains(body, []byte("global-source")) != (state == "allowed")) {
					t.Fatalf("status=%d body=%s", resp.StatusCode, body)
				}
				want := int32(0)
				if admin {
					want = 1
				}
				if calls.Load() != want {
					t.Fatalf("calls=%d want=%d", calls.Load(), want)
				}
			})
		}
	}
}

func TestDocumentEgressRechecksGrantAndCredentialBeforeModel(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		if err := accesspkg.EnsureIdentitySchema(context.Background(), f.db, Q); err != nil {
			t.Fatal(err)
		}
		if err := accesspkg.EnsureBrowserSessionSchema(context.Background(), f.db, Q); err != nil {
			t.Fatal(err)
		}
		f.cfg.RequireAuth = true
		f.db.SetMaxOpenConns(1)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		reader := accesspkg.Actor{UserID: "viewer", TenantID: "a"}
		r, err := f.p.GrantDocument(ctx, f.owner, f.r.DocumentRef, f.r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "viewer"}, accesspkg.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		ctx = context.WithValue(ctx, searchActorKey{}, reader)
		ctx = context.WithValue(ctx, searchEvidenceKey{}, &searchEvidence{sources: map[searchDocumentSource]struct{}{
			{DatasetID: "alpha", DocumentID: "blob", ContentRevision: r.ContentRevision}: {},
		}})
		ctx = context.WithValue(ctx, searchEgressKey{}, searchEgress{cfg: f.cfg, actor: reader, kind: "jwt", expiresAt: time.Now().Add(time.Hour).Unix()})
		provider := &recordingLLM{responses: []string{"allowed answer", "forbidden answer"}}
		if got := callLLMFromAPI(ctx, "", "test", "private source", provider); got != "allowed answer" {
			t.Fatalf("initial answer=%q", got)
		}
		if _, err := f.p.RevokeDocument(ctx, f.owner, r.DocumentRef, r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "viewer"}); err != nil {
			t.Fatal(err)
		}
		if got := callLLMFromAPI(ctx, "", "test", "private source", provider); got != "" {
			t.Fatalf("revoked answer=%q", got)
		}
		if got := len(provider.promptsSnapshot()); got != 1 {
			t.Fatalf("revoked text sent to model, calls=%d", got)
		}
		// No collected document is needed to reject an old JWT after reactivation.
		f.exec("INSERT INTO credential_epochs(user_id,epoch,revoked_before) VALUES('viewer',1,1)")
		if err := withSearchReadFence(ctx, func(context.Context) error { t.Fatal("revoked credential reached effect"); return nil }); err == nil {
			t.Fatal("revoked credential accepted")
		}
	})
}

func TestFencedResponseDoesNotBypassCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	released := 0
	r := &fencedResponse{reader: bytes.NewReader([]byte("private")), ctx: ctx, release: func() { released++ }}
	if _, ok := any(r).(io.WriterTo); ok {
		t.Fatal("WriterTo bypasses the cancellation-aware Read")
	}
	cancel()
	var dst bytes.Buffer
	if _, err := io.Copy(&dst, r); !errors.Is(err, context.Canceled) || dst.Len() != 0 {
		t.Fatalf("cancelled transfer bytes=%q err=%v", dst.String(), err)
	}
	_ = r.Close()
	_ = r.Close()
	if released != 1 {
		t.Fatalf("release calls=%d", released)
	}
}

func TestDocumentStatsHonorsSuppliedContext(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err := documentDatasetStats(ctx, nil, f.cfg, "alpha")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stats ignored request cancellation: %v", err)
		}
	})
}
