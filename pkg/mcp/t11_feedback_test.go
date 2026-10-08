package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func t11FeedbackDeps(t *testing.T, pg bool) Deps {
	t.Helper()
	var deps Deps
	if pg {
		deps = &postgresMemoryDeps{fakeDeps: &fakeDeps{db: openPostgresMemoryTestDB(t)}}
	} else {
		d := setupFeedbackTestDB(t)
		if _, err := d.DB().Exec("DROP TABLE search_feedback"); err != nil {
			t.Fatal(err)
		}
		deps = d
	}
	deps.DB().SetMaxOpenConns(1)
	if _, err := deps.DB().Exec(`CREATE TABLE search_feedback(id TEXT PRIMARY KEY,query TEXT CHECK(query<>'reject'),result_id TEXT DEFAULT '',collection TEXT DEFAULT '',search_type TEXT DEFAULT '',rating INTEGER,comment TEXT DEFAULT '',user_id TEXT DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	return deps
}

func TestT11FeedbackMCPPersistenceAndStats(t *testing.T) {
	for _, dialect := range []struct {
		name string
		pg   bool
	}{{"sqlite", false}, {"postgres", true}} {
		t.Run(dialect.name, func(t *testing.T) {
			deps := t11FeedbackDeps(t, dialect.pg)
			ctx := context.WithValue(context.Background(), UserIDKey, "verified-user")
			result := ToolAddFeedback(ctx, deps, map[string]any{"query": "reject", "rating": float64(5)})
			if !result.IsError {
				t.Errorf("rejected INSERT reported success %v", result.Content)
			}
			var n int
			if err := deps.DB().QueryRow("SELECT COUNT(*) FROM search_feedback").Scan(&n); err != nil || n != 0 {
				t.Fatalf("rejected row persisted %d %v", n, err)
			}
			for _, rating := range []float64{1, 2} {
				if got := ToolAddFeedback(ctx, deps, map[string]any{"query": "valid", "rating": rating, "collection": "chosen"}); got.IsError {
					t.Fatalf("valid INSERT failed %v", got.Content)
				}
			}
			if _, err := deps.DB().Exec("INSERT INTO search_feedback(id,query,collection,rating) VALUES('control','elsewhere','other',5)"); err != nil {
				t.Fatal(err)
			}
			result = ToolGetFeedbackStats(ctx, deps, map[string]any{"collection": "chosen"})
			if result.IsError {
				t.Fatalf("stats failed %v", result.Content)
			}
			var stats map[string]any
			if err := json.Unmarshal([]byte(result.Content[0].Text), &stats); err != nil {
				t.Fatal(err)
			}
			if stats["total"] != float64(2) || stats["avg_rating"] != 1.5 || stats["worst_query"] != "valid" {
				t.Errorf("fractional stats/filter %v", stats)
			}
			result = ToolGetFeedbackStats(ctx, deps, map[string]any{"collection": "empty"})
			if result.IsError {
				t.Fatalf("empty stats %v", result.Content)
			}
			if err := json.Unmarshal([]byte(result.Content[0].Text), &stats); err != nil {
				t.Fatal(err)
			}
			if stats["total"] != float64(0) || stats["worst_query"] != "" {
				t.Errorf("empty stats %v", stats)
			}
			if _, err := deps.DB().Exec("ALTER TABLE search_feedback RENAME COLUMN query TO missing_query"); err != nil {
				t.Fatal(err)
			}
			if got := ToolGetFeedbackStats(ctx, deps, map[string]any{}); !got.IsError {
				t.Errorf("second stats query failure reported success %v", got.Content)
			}
			if _, err := deps.DB().Exec("DROP TABLE search_feedback"); err != nil {
				t.Fatal(err)
			}
			if got := ToolGetFeedbackStats(ctx, deps, map[string]any{}); !got.IsError {
				t.Errorf("aggregate query failure reported success %v", got.Content)
			}
		})
	}
}

func TestT11FeedbackMCPCancellationAndPoolWait(t *testing.T) {
	for _, dialect := range []struct {
		name string
		pg   bool
	}{{"sqlite", false}, {"postgres", true}} {
		t.Run(dialect.name, func(t *testing.T) {
			deps := t11FeedbackDeps(t, dialect.pg)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if got := ToolAddFeedback(ctx, deps, map[string]any{"query": "valid", "rating": float64(5)}); !got.IsError {
				t.Errorf("canceled INSERT success %v", got.Content)
			}
			if got := ToolGetFeedbackStats(ctx, deps, map[string]any{}); !got.IsError {
				t.Errorf("canceled stats success %v", got.Content)
			}
			held, err := deps.DB().Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			before := deps.DB().Stats().WaitCount
			waitCtx, waitCancel := context.WithCancel(context.Background())
			defer waitCancel()
			result := make(chan ToolResult, 1)
			go func() {
				result <- ToolAddFeedback(waitCtx, deps, map[string]any{"query": "valid", "rating": float64(5)})
			}()
			deadline := time.Now().Add(2 * time.Second)
			for deps.DB().Stats().WaitCount == before && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if deps.DB().Stats().WaitCount == before {
				held.Close()
				t.Fatal("request did not reach pool wait")
			}
			waitCancel()
			select {
			case got := <-result:
				if !got.IsError {
					t.Errorf("pool cancellation success %v", got.Content)
				}
			case <-time.After(time.Second):
				held.Close()
				<-result
				t.Error("SQL wait ignored cancellation")
			}
			held.Close()
			var n int
			if err := deps.DB().QueryRow("SELECT COUNT(*) FROM search_feedback").Scan(&n); err != nil || n != 0 {
				t.Errorf("canceled rows=%d error=%v", n, err)
			}
		})
	}
}
