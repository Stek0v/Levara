package http

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/mcp"
)

func TestConsolidationRecoverySingleConnection(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		f.cfg.RequireAuth = true
		f.exec(consolidationJobsDDL)
		f.exec(`INSERT INTO consolidation_jobs(id,owner_id,status,args_json,created_at,updated_at) VALUES('interrupted','peer','running','{"collection":"main","dry_run":true}','old','old')`)
		before := f.db.Stats().WaitCount
		done := make(chan struct{})
		go func() { StartConsolidationRecovery(f.cfg); close(done) }()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		// Recovery already has a 30s SQL deadline; completion latency does
		// not establish a held-rows bug. Witness an actual pool self-wait.
		deadline := time.NewTimer(35 * time.Second)
		defer deadline.Stop()
	wait:
		for {
			select {
			case <-done:
				break wait
			case <-ticker.C:
				if f.db.Stats().WaitCount == before {
					continue
				}
				// Release the deliberately broken implementation in RED probes.
				f.db.SetMaxOpenConns(2)
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("recovery remained stuck after opening a cleanup connection")
				}
				t.Fatal("recovery waited for its own one-connection pool")
			case <-deadline.C:
				f.db.SetMaxOpenConns(2)
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("recovery remained stuck after deadline cleanup")
				}
				t.Fatalf("recovery exceeded its bounded SQL deadline: pool=%+v", f.db.Stats())
			}
		}
		if f.db.Stats().WaitCount != before {
			t.Fatal("recovery waited for its own one-connection pool")
		}
		var status, last string
		if err := f.db.QueryRow(`SELECT status,last_error FROM consolidation_jobs WHERE id='interrupted'`).Scan(&status, &last); err != nil {
			t.Fatal(err)
		}
		if status != "failed" || !strings.Contains(last, "unknown outcome") || !strings.Contains(last, "resubmit") {
			t.Fatalf("recovery invented authority/outcome: status=%q error=%q", status, last)
		}
	})
}

func TestConsolidationStatusExactOwner(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.exec(consolidationJobsDDL)
		f.exec(`INSERT INTO consolidation_jobs(id,owner_id,status,args_json,result_text,created_at,updated_at) VALUES('legacy-shared','','completed','{}','PRIVATE_JOB_RESULT','old','old'),('foreign','foreign','completed','{}','FOREIGN_JOB_RESULT','old','old'),('mine','peer','completed','{}','MINE','old','old')`)
		h := &mcpHandler{cfg: f.cfg, sessions: mcp.NewSessionStore()}
		ctx := context.WithValue(context.Background(), mcpUserIDKey, "peer")
		for _, id := range []string{"legacy-shared", "foreign", "mine"} {
			got := h.toolConsolidationStatus(ctx, map[string]any{"job_id": id, "owner_id": "foreign"})
			if got.IsError != (id != "mine") {
				t.Fatalf("job=%s result=%+v", id, got)
			}
		}
	})
}
