package access_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	httpapi "github.com/stek0v/levara/internal/http"
	"github.com/stek0v/levara/pkg/access"
)

func TestDocumentReadFenceDrainsBeforeIndependentRevocation(t *testing.T) {
	documentDialects(t, func(t *testing.T, f documentFixture) {
		if err := access.EnsureIdentitySchema(f.ctx, f.db, httpapi.Q); err != nil {
			t.Fatal(err)
		}
		if err := access.EnsureBrowserSessionSchema(f.ctx, f.db, httpapi.Q); err != nil {
			t.Fatal(err)
		}
		f.registered(f.ref, access.DocumentInherit)
		var peer *sql.DB
		sqlite := httpapi.GetDBProvider() == httpapi.DBSQLite
		if sqlite {
			var sequence int
			var name, path string
			if err := f.db.QueryRow("PRAGMA database_list").Scan(&sequence, &name, &path); err != nil {
				t.Fatal(err)
			}
			var err error
			peer, err = sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			config, err := pgx.ParseConfig(os.Getenv("LEVARA_TEST_POSTGRES_DSN"))
			if err != nil {
				t.Fatal(err)
			}
			var schema string
			if err := f.db.QueryRow("SELECT current_schema()").Scan(&schema); err != nil {
				t.Fatal(err)
			}
			config.RuntimeParams["search_path"] = schema
			peer = stdlib.OpenDB(*config)
		}
		defer peer.Close()
		f.db.SetMaxOpenConns(1)
		ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
		defer cancel()
		locked, release, err := f.p.BeginReadFence(ctx, sqlite)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		d, err := locked.AuthorizeDocument(ctx, f.viewer, f.ref, access.ActionRead)
		if err != nil || !d.Allowed {
			t.Fatalf("locked policy: %+v %v", d, err)
		}
		finished := make(chan error, 1)
		go func() {
			_, err := peer.ExecContext(ctx, "UPDATE users SET is_active=false WHERE id='viewer'")
			finished <- err
		}()
		select {
		case err := <-finished:
			t.Fatalf("revoker finished before protected transfer drained: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		release()
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
		f.allowed(f.viewer, f.ref, access.ActionRead, false)
		// Cancellation bounds a stalled transfer and frees the SQL fence even
		// if a client forgets to call release until its deferred cleanup.
		short, stop := context.WithTimeout(f.ctx, 50*time.Millisecond)
		_, releaseExpired, err := f.p.BeginReadFence(short, sqlite)
		if err != nil {
			stop()
			t.Fatal(err)
		}
		<-short.Done()
		stop()
		if _, err := peer.ExecContext(ctx, "UPDATE users SET is_active=true WHERE id='viewer'"); err != nil {
			t.Fatal(err)
		}
		releaseExpired()
	})
}
