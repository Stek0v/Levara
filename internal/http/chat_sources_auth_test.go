package http

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stek0v/levara/pkg/chatimport"
)

func TestChatSourcesAuthenticatedMode(t *testing.T) {
	previous := GetDBProvider()
	SetDBProvider(DBSQLite)
	t.Cleanup(func() { SetDBProvider(previous) })
	t.Setenv("LEVARA_CHAT_SOURCES_DATASET", "chat-imports")
	t.Setenv("LEVARA_AUTO_DISTILL", "1")
	for _, requireAuth := range []bool{true, false} {
		t.Run(fmt.Sprintf("require-auth-%t", requireAuth), func(t *testing.T) {
			db := chatSourcesTestDB(t)
			ctx := context.Background()
			if err := chatimport.EnsureRagSchema(ctx, db, Q); err != nil {
				t.Fatal(err)
			}
			if err := chatimport.EnsureDistillSchema(ctx, db, Q); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			transcript := claudeFixture
			for i := 0; i < 6; i++ {
				transcript += fmt.Sprintf("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"private local turn %d\"},\"uuid\":\"extra-%d\",\"timestamp\":\"2026-09-21T10:01:00.000Z\",\"sessionId\":\"p2-sess-1\"}\n", i, i)
			}
			if err := os.WriteFile(filepath.Join(root, "rollout.jsonl"), []byte(transcript), 0600); err != nil {
				t.Fatal(err)
			}
			var requests, adds atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/add":
					adds.Add(1)
					_, _ = w.Write([]byte(`{"dataset_name":"chat-imports"}`))
				case "/api/v1/datasets":
					_, _ = w.Write([]byte(`[]`))
				default:
					_, _ = w.Write([]byte(`{}`))
				}
			}))
			t.Cleanup(server.Close)
			d := configuredChatSourcesDaemon(db, server.URL, requireAuth)
			d.scanOnce(ctx, []chatSourceDefinition{{Platform: chatimport.PlatformClaudeCode, Root: root}})
			if got := countMessages(t, db, "claude-code", "p2-sess-1"); got != 8 {
				t.Fatalf("raw messages=%d want 8", got)
			}
			var owner, tenant string
			var local bool
			if err := db.QueryRow(`SELECT owner_id,tenant_id,trusted_local FROM chat_import_sessions WHERE id='p2-sess-1' AND platform='claude-code'`).Scan(&owner, &tenant, &local); err != nil {
				t.Fatal(err)
			}
			if owner != "" || tenant != "" || !local {
				t.Fatalf("daemon assigned authenticated scope: %q %q %t", owner, tenant, local)
			}
			if requireAuth {
				d.refreshStaleRenders(ctx)
				d.distillNewSessions(ctx)
				if requests.Load() != 0 || d.ragEnabled() {
					t.Fatalf("uncredentialed egress: requests=%d", requests.Load())
				}
				for _, table := range []string{"chat_import_rag", "chat_import_distill"} {
					var n int
					if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
						t.Fatal(err)
					}
					if n != 0 {
						t.Fatalf("%s mutated: %d rows", table, n)
					}
				}
			} else if adds.Load() == 0 || !d.ragEnabled() {
				t.Fatalf("explicit local RAG control did not publish: adds=%d", adds.Load())
			}
		})
	}
}
