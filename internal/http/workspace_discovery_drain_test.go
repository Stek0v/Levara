package http

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/access"
	"github.com/valyala/fasthttp"
)

func TestWorkspaceDiscoveryDeadlineRetainsFenceUntilClose(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		raw := &fasthttp.RequestCtx{}
		c := f.app.AcquireCtx(raw)
		defer f.app.ReleaseCtx(c)
		c.Locals("user_id", "owner")
		c.Locals("tenant_id", "a")
		c.Locals("verified_jwt", jwtPayload{Sub: "owner", Exp: time.Now().Add(time.Hour).Unix()})
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if err := withProtectedPolicyResponse(c, f.cfg, ctx, func(ctx context.Context, p access.SQLPolicy) error {
			if _, err := p.VisibleDatasetIDs(ctx, "owner"); err != nil {
				return err
			}
			return c.JSON(map[string]string{"project_id": "alpha"})
		}); err != nil {
			t.Fatal(err)
		}
		stream, ok := c.Response().BodyStream().(io.ReadCloser)
		if !ok {
			t.Fatal("discovery lacks body stream")
		}
		defer stream.Close()
		<-ctx.Done()
		// Wait for an erroneous automatic rollback to become observable.
		time.Sleep(50 * time.Millisecond)
		if f.db.Stats().InUse != 1 {
			t.Error("deadline released SQL before actual body close")
		}
		if n, err := stream.Read(make([]byte, 1)); n != 0 || err == nil {
			t.Error("expired body still readable")
		}
		if err := stream.Close(); err != nil {
			t.Fatal(err)
		}
		if f.db.Stats().InUse != 0 {
			t.Fatal("closed discovery retained SQL")
		}
	})
}
