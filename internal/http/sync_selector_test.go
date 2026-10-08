package http

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestSyncSelectorsBeforeRemoteNative(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		var mu sync.Mutex
		requests := make(map[string]int)
		remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			requests[r.URL.Path]++
			mu.Unlock()
			switch r.URL.Path {
			case "/sync/manifest":
				fmt.Fprint(w, `{"version":"remote"}`)
			case "/sync/export/memories":
				fmt.Fprint(w, `{"protocol_version":3,"memories":[],"deletions":[],"incarnations":[],"aliases":[]}`)
			case "/sync/export/graph":
				fmt.Fprint(w, `{"nodes":[],"edges":[]}`)
			default:
				fmt.Fprint(w, `[]`)
			}
		}))
		defer remote.Close()
		cfg := f.cfg
		cfg.Version = "local"
		h := &mcpHandler{cfg: cfg}
		invalid := []struct {
			name         string
			types, names []string
		}{
			{"unknown_only", []string{"unknown"}, nil},
			{"mixed", []string{"memories", "unknown"}, nil},
			{"blank_type", []string{""}, nil},
			{"case_is_exact", []string{"Memories"}, nil},
			{"collections_missing", []string{"collections"}, nil},
			{"collections_empty", []string{"collections"}, []string{}},
			{"collections_blank", []string{"collections"}, []string{" \t "}},
			{"collections_mixed_blank", []string{"collections"}, []string{"docs", ""}},
		}
		for _, tc := range invalid {
			t.Run(tc.name, func(t *testing.T) {
				result, manifest, err := h.DoSync(context.Background(), remote.URL, "pull", tc.types, "", tc.names)
				if err == nil || result != nil || manifest != nil {
					t.Fatalf("invalid selector returned success: %v %v %v", result, manifest, err)
				}
				mu.Lock()
				count := len(requests)
				mu.Unlock()
				if count != 0 {
					t.Fatal("invalid selector contacted remote before validation")
				}
			})
		}
		for _, types := range [][]string{nil, {}, {"memories", "memories"}} {
			mu.Lock()
			requests = make(map[string]int)
			mu.Unlock()
			result, _, err := h.DoSync(context.Background(), remote.URL, "pull", types, "", []string{"docs", "docs"})
			if err != nil || result["status"] != "ok" || result["version_warning"] == nil {
				t.Fatalf("supported selector/default/version control failed: %v %v", result, err)
			}
			mu.Lock()
			count, memories, interactions, graph := len(requests), requests["/sync/export/memories"], requests["/sync/export/interactions"], requests["/sync/export/graph"]
			for path := range requests {
				if strings.Contains(path, "collection") {
					t.Errorf("names implicitly opted in collections: %s", path)
				}
			}
			mu.Unlock()
			if len(types) == 0 {
				if count != 4 || memories != 1 || interactions != 1 || graph != 1 {
					t.Fatalf("empty selector changed default: calls=%d memories=%d interactions=%d graph=%d", count, memories, interactions, graph)
				}
			} else if count != 2 || memories != 1 || interactions != 0 || graph != 0 {
				t.Fatalf("explicit duplicate type expanded selection: calls=%d memories=%d interactions=%d graph=%d", count, memories, interactions, graph)
			}
		}
	})
}
