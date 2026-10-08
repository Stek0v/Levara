package http

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/valyala/fasthttp"
)

// This harness uses already-verified credential facts, as the shared completed
// response sender receives from middleware. Transport admission is exercised
// independently in TestCommunityListAuthorityTransports.
func protectedResponseTestContext(ctx context.Context, cfg APIConfig, expiresAt int64) context.Context {
	actor := accesspkg.Actor{UserID: "root"}
	ctx = context.WithValue(ctx, mcp.UserIDKey, actor.UserID)
	ctx = context.WithValue(ctx, searchActorKey{}, actor)
	ctx = context.WithValue(ctx, searchEvidenceKey{}, &searchEvidence{sources: make(map[searchDocumentSource]struct{})})
	ctx = context.WithValue(ctx, searchEgressKey{}, searchEgress{cfg: cfg, actor: actor, kind: "jwt", expiresAt: expiresAt, global: true})
	return ctx
}

func TestProtectedResponseLifetimeActualClose(t *testing.T) {
	for _, boundary := range []string{"request-deadline", "credential-expiry"} {
		t.Run(boundary, func(t *testing.T) {
			documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
				f.db.SetMaxOpenConns(1)
				f.cfg.RequireAuth = true
				requestDeadline := time.Now().Add(250 * time.Millisecond)
				expiresAt := time.Now().Add(time.Hour).Unix()
				if boundary == "credential-expiry" {
					requestDeadline = time.Now().Add(5 * time.Second)
					expiresAt = time.Now().Unix() + 2
				}
				parent, cancel := context.WithDeadline(context.Background(), requestDeadline)
				defer cancel()
				ctx := protectedResponseTestContext(parent, f.cfg, expiresAt)
				app := fiber.New()
				c := app.AcquireCtx(&fasthttp.RequestCtx{})
				defer app.ReleaseCtx(c)
				if err := c.SendString(strings.Repeat("protected", 128)); err != nil {
					t.Fatal(err)
				}
				if err := sendProtectedResponseWithFence(c, ctx); err != nil {
					t.Fatal(err)
				}
				stream, ok := c.Response().BodyStream().(*fencedResponse)
				if !ok {
					t.Fatal("missing protected response stream")
				}
				defer stream.Close()
				if f.db.Stats().InUse != 1 {
					t.Fatal("missing retained SQL fence")
				}
				if n, err := stream.Read(make([]byte, 8)); n != 8 || err != nil {
					t.Fatalf("partial read=%d %v", n, err)
				}
				// Completed-body observers survive handler cancellation until the original
				// request deadline or verified credential expiry, whichever is earlier.
				cancel()
				if n, err := stream.Read(make([]byte, 8)); n != 8 || err != nil {
					t.Fatalf("handler cancellation reached completed observer: %d %v", n, err)
				}
				select {
				case <-stream.ctx.Done():
				case <-time.After(3 * time.Second):
					t.Fatal("response observer failed to expire")
				}
				if n, err := stream.Read(make([]byte, 8)); n != 0 || !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("expired observer read=%d %v", n, err)
				}
				if f.db.Stats().InUse != 1 {
					t.Fatal("observer expiry released SQL before actual Close")
				}
				if err := stream.Close(); err != nil {
					t.Fatal(err)
				}
				if err := stream.Close(); err != nil {
					t.Fatal(err)
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("repeated Close leaked SQL")
				}
			})
		})
	}
}

func TestProtectedResponseCommunityRecheckAfterMaterialization(t *testing.T) {
	for _, mutation := range []string{"admin-demoted", "key-revoked"} {
		t.Run(mutation, func(t *testing.T) {
			documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
				f.db.SetMaxOpenConns(1)
				f.cfg.RequireAuth = true
				f.exec("INSERT INTO graph_communities(id,member_count,summary) VALUES('global-proof',3,'protected community')")
				communityPublicationFixture(t, f, "global-proof")
				parent, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				ctx := protectedResponseTestContext(parent, f.cfg, time.Now().Add(time.Hour).Unix())
				if mutation == "key-revoked" {
					f.exec("INSERT INTO api_keys(id,key_hash,user_id,permissions) VALUES('materialized-key',$1,'root','read')", apikeyHash("materialized-key"))
					actor := accesspkg.Actor{UserID: "root", APIKeyPermissions: "read"}
					ctx = context.WithValue(ctx, mcpAPIKeyPermissionsKey, "read")
					ctx = context.WithValue(ctx, searchActorKey{}, actor)
					ctx = context.WithValue(ctx, searchEgressKey{}, searchEgress{cfg: f.cfg, actor: actor, kind: "api_key", keyID: "materialized-key", global: true})
				}
				result := (&mcpHandler{cfg: f.cfg}).toolListCommunities(ctx, nil)
				if result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "protected community") {
					t.Fatalf("materialization=%+v", result)
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("materialization retained SQL before completed-body admission")
				}
				if mutation == "admin-demoted" {
					f.exec("UPDATE users SET is_superuser=false WHERE id='root'")
				} else {
					f.exec("UPDATE api_keys SET revoked=true WHERE id='materialized-key'")
				}
				app := fiber.New()
				c := app.AcquireCtx(&fasthttp.RequestCtx{})
				defer app.ReleaseCtx(c)
				if err := c.JSON(result); err != nil {
					t.Fatal(err)
				}
				err := sendProtectedResponseWithFence(c, ctx)
				var denied *fiber.Error
				wantStatus := 403
				if mutation == "key-revoked" {
					wantStatus = 401
				}
				if !errors.As(err, &denied) || denied.Code != wantStatus {
					t.Fatalf("authority recheck error=%v want=%d", err, wantStatus)
				}
				if c.Response().IsBodyStream() {
					t.Fatal("revoked authority installed protected body stream")
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("revoked authority leaked SQL")
				}
			})
		})
	}
}

func TestProtectedResponseCancelledQueuedAcquisition(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		f.cfg.RequireAuth = true
		held, err := f.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer held.Close()
		parent, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		ctx := protectedResponseTestContext(parent, f.cfg, time.Now().Add(time.Hour).Unix())
		app := fiber.New()
		c := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(c)
		if err := c.SendString("protected"); err != nil {
			t.Fatal(err)
		}
		baseline := f.db.Stats().WaitCount
		done := make(chan error, 1)
		go func() { done <- sendProtectedResponseWithFence(c, ctx) }()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		barrier := time.NewTimer(time.Second)
		defer barrier.Stop()
	waiting:
		for {
			if f.db.Stats().WaitCount > baseline {
				break waiting
			}
			select {
			case err := <-done:
				t.Fatalf("acquisition completed before SQL queue barrier: %v", err)
			case <-ticker.C:
			case <-barrier.C:
				cancel()
				held.Close()
				select {
				case <-done:
				case <-time.After(time.Second):
				}
				t.Fatal("protected sender did not enter SQL acquisition queue")
			}
		}
		cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("cancelled acquisition succeeded")
			}
		case <-time.After(300 * time.Millisecond):
			held.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
			}
			t.Fatal("cancelled acquisition awaited connection release")
		}
		if c.Response().IsBodyStream() {
			if closer, ok := c.Response().BodyStream().(io.Closer); ok {
				closer.Close()
			}
			t.Fatal("cancelled acquisition installed protected stream")
		}
		if err := held.Close(); err != nil {
			t.Fatal(err)
		}
		if f.db.Stats().InUse != 0 {
			t.Fatal("cancelled acquisition leaked SQL")
		}
	})
}
