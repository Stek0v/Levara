package http

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/access"
	"github.com/valyala/fasthttp"
)

func TestWorkspaceResponseCancellationWhileAcquiring(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		conn, err := f.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		c := f.app.AcquireCtx(&fasthttp.RequestCtx{})
		defer f.app.ReleaseCtx(c)
		if err := c.JSON(map[string]string{"project_id": "alpha"}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		actor := access.MetadataActor{Actor: f.owner, Credential: access.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
		before := f.db.Stats().WaitCount
		done := make(chan error, 1)
		go func() { done <- sendWorkspaceProtectedResponse(c, f.cfg, ctx, actor, "alpha") }()
		deadline := time.Now().Add(time.Second)
		for f.db.Stats().WaitCount == before && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if f.db.Stats().WaitCount == before {
			t.Error("response never queued for SQL authority")
		}
		cancel()
		select {
		case err = <-done:
		case <-time.After(200 * time.Millisecond):
			t.Error("cancellation did not interrupt queued workspace response acquisition")
			_ = conn.Close()
			err = <-done
		}
		if stream, ok := c.Response().BodyStream().(io.Closer); ok {
			_ = stream.Close()
		}
		if err == nil {
			t.Error("cancelled workspace response succeeded")
		}
		_ = conn.Close()
		if f.db.Stats().InUse != 0 {
			t.Error("cancelled workspace response retained SQL")
		}
	})
}
