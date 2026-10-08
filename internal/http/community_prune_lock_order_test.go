package http

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/community"
)

func TestCommunityPruneLockOrderPostgres(t *testing.T) {
	for _, operation := range []string{"graph", "data"} {
		t.Run(operation, func(t *testing.T) {
			_, db := documentACLHTTPFixture(t, "postgres")
			db.SetMaxOpenConns(1)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := accesspkg.EnsureIdentitySchema(ctx, db, Q); err != nil {
				t.Fatal(err)
			}
			for _, statement := range []string{
				"INSERT INTO principals(id,type) VALUES('graph-prune-root','user')",
				"INSERT INTO users(id,email,hashed_password,is_superuser) VALUES('graph-prune-root','graph-prune-root@test.invalid','unused',true)",
				"INSERT INTO graph_nodes(id,name) VALUES('prune-a','A'),('prune-b','B')",
				"INSERT INTO graph_edges(id,source_id,target_id,relationship_name,superseded_by,valid_until) VALUES('prune-edge','prune-a','prune-b','owns','successor','2000-01-01')",
			} {
				if _, err := db.ExecContext(ctx, statement); err != nil {
					t.Fatal(err)
				}
			}
			var searchPath string
			if err := db.QueryRowContext(ctx, "SHOW search_path").Scan(&searchPath); err != nil {
				t.Fatal(err)
			}
			config, err := pgx.ParseConfig(os.Getenv("LEVARA_TEST_POSTGRES_DSN"))
			if err != nil {
				t.Fatal(err)
			}
			config.RuntimeParams["search_path"] = searchPath
			pruner := stdlib.OpenDB(*config)
			pruner.SetMaxOpenConns(1)
			defer pruner.Close()
			monitor := stdlib.OpenDB(*config)
			monitor.SetMaxOpenConns(1)
			defer monitor.Close()
			var pid int
			if err := pruner.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
				t.Fatal(err)
			}
			blocker, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback()
			if _, err := blocker.ExecContext(ctx, "LOCK TABLE graph_nodes IN SHARE MODE"); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if operation == "graph" {
					_, err := community.PruneGraph(ctx, pruner, community.PruneConfig{IncludeOrphans: true})
					done <- err
					return
				}
				actor := accesspkg.MetadataActor{Actor: accesspkg.Actor{UserID: "graph-prune-root"}, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
				done <- (accesspkg.SQLPolicy{DB: pruner, Q: Q, QA: QArgs}).PruneData(ctx, actor, true)
			}()
			// Always release the blocker and join the actual pruner before its DB closes.
			joined := false
			defer func() {
				_ = blocker.Rollback()
				cancel()
				if !joined {
					select {
					case <-done:
					case <-time.After(2 * time.Second):
						t.Error("pruner did not finish cleanup")
					}
				}
			}()
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				var waiting bool
				if err := monitor.QueryRowContext(ctx, "SELECT COALESCE(wait_event_type='Lock',false) FROM pg_stat_activity WHERE pid=$1", pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-done:
					joined = true
					t.Fatalf("pruner did not wait on held nodes: %v", err)
				case <-ctx.Done():
					t.Fatal("pruner never reached lock barrier")
				case <-ticker.C:
				}
			}
			// With the old edge-first delete, this fails immediately because prune
			// already owns edges while waiting for our node lock: the deadlock cycle.
			if _, err := blocker.ExecContext(ctx, "LOCK TABLE graph_edges IN SHARE MODE NOWAIT"); err != nil {
				t.Fatalf("pruner acquired edges before nodes: %v", err)
			}
			if err := blocker.Rollback(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				joined = true
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("pruner failed after graph locks released")
			}
			for _, table := range []string{"graph_nodes", "graph_edges"} {
				var count int
				if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count); err != nil || count != 0 {
					t.Fatalf("prune left %s count=%d err=%v", table, count, err)
				}
			}
		})
	}
}
