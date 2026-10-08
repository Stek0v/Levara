package http

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/access"
	"github.com/valyala/fasthttp"
)

func TestWorkspaceDiscoveryCancellationWhileAcquiring(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		conn, err := f.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		raw := &fasthttp.RequestCtx{}
		c := f.app.AcquireCtx(raw)
		defer f.app.ReleaseCtx(c)
		c.Locals("user_id", "owner")
		c.Locals("verified_jwt", jwtPayload{Sub: "owner", Exp: time.Now().Add(time.Hour).Unix()})
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		before := f.db.Stats().WaitCount
		done := make(chan error, 1)
		built := make(chan struct{}, 1)
		go func() {
			done <- withProtectedPolicyResponse(c, f.cfg, ctx, func(context.Context, access.SQLPolicy) error {
				built <- struct{}{}
				return c.JSON(map[string]string{"project_id": "alpha"})
			})
		}()
		deadline := time.Now().Add(time.Second)
		for f.db.Stats().WaitCount == before && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		cancel()
		select {
		case err = <-done:
		case <-time.After(200 * time.Millisecond):
			t.Error("cancellation did not interrupt queued discovery acquisition")
			_ = conn.Close()
			err = <-done
		}
		if stream, ok := c.Response().BodyStream().(io.Closer); ok {
			_ = stream.Close()
		}
		if err == nil {
			t.Error("cancelled discovery succeeded")
		}
		select {
		case <-built:
			t.Error("cancelled discovery reached builder")
		default:
		}
	})
}
