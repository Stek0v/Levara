package http

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestMCPAdminSQLParity(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			cfg := APIConfig{DB: syncConvergenceDB(t, dialect)}
			ctx := context.Background()
			empty, err := listMCPAdminSessions(ctx, cfg, 20)
			if err != nil || empty == nil || len(empty) != 0 {
				t.Fatalf("empty sessions=%#v err=%v", empty, err)
			}
			for _, row := range []struct {
				id     string
				pinned bool
			}{{"pinned-one", true}, {"unpinned", false}, {"pinned-two", true}} {
				_, err := cfg.DB.Exec(Q(`INSERT INTO memories(id,key,value,is_pinned,room,hall) VALUES($1,$2,'value',$3,'observability','fact')`), row.id, row.id, row.pinned)
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, row := range []struct {
				id, session, stamp string
			}{
				{"older-one", "older", "2026-10-07T09:00:00Z"},
				{"older-two", "older", "2026-10-07T10:00:00Z"},
				{"newer", "newer", "2026-10-07T11:00:00Z"},
				{"unscoped", "", "2026-10-07T12:00:00Z"},
			} {
				_, err := cfg.DB.Exec(Q(`INSERT INTO interactions(id,session_id,search_type,created_at) VALUES($1,$2,'vector',$3)`), row.id, row.session, row.stamp)
				if err != nil {
					t.Fatal(err)
				}
			}
			sessions, err := listMCPAdminSessions(ctx, cfg, 20)
			if err != nil {
				t.Fatal(err)
			}
			checkSessions := func(got []mcpAdminSession) {
				t.Helper()
				if len(got) != 2 || got[0].SessionID != "newer" || got[0].Count != 1 || got[1].SessionID != "older" || got[1].Count != 2 {
					t.Fatalf("grouped chronological sessions=%#v", got)
				}
				for _, session := range got {
					if session.LastAt == "" || session.SearchType != "vector" {
						t.Fatalf("missing native timestamp/type: %#v", session)
					}
				}
				// Compare presentation against native SQL, without imposing a
				// PostgreSQL/SQLite timestamp text-format contract.
				var latest string
				if err := cfg.DB.QueryRow(Q(`SELECT CAST(MAX(created_at) AS TEXT) FROM interactions WHERE session_id=$1`), "older").Scan(&latest); err != nil {
					t.Fatal(err)
				}
				if got[1].LastAt != latest {
					t.Fatalf("last_at=%q want native maximum %q", got[1].LastAt, latest)
				}
			}
			checkSessions(sessions)
			app := fiber.New()
			app.Get("/summary", mcpAdminSummaryHandler(cfg))
			resp, err := app.Test(httptest.NewRequest("GET", "/summary", nil))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var summary struct {
				Pinned   int               `json:"pinned_memories"`
				Sessions []mcpAdminSession `json:"recent_sessions"`
			}
			if resp.StatusCode != 200 {
				t.Fatalf("summary status=%d", resp.StatusCode)
			}
			if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
				t.Fatal(err)
			}
			if summary.Pinned != 2 {
				t.Fatalf("pinned=%d want 2", summary.Pinned)
			}
			checkSessions(summary.Sessions)
		})
	}
}
