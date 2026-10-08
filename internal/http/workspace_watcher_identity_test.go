package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/workspace"
)

func TestWorkspaceWatcherProjectIdentity(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		for _, id := range []string{"uuid-with-hyphens", "legacy-uuid", "team-a", "team_a"} {
			f.exec("INSERT INTO datasets(id,name,owner_id) VALUES($1,$1,'owner')", id)
		}
		for _, branch := range []string{"main", "feature/topic"} {
			for _, async := range []bool{false, true} {
				t.Run(fmt.Sprintf("raw_uuid/%s/async=%v", branch, async), func(t *testing.T) {
					cfg, closeCfg := newWorkspaceTestConfig(t)
					defer closeCfg()
					cfg.DB, cfg.RequireAuth = f.db, true
					cfg.WorkspaceWatcher = NewWorkspaceWatchState()
					cfg.WorkspaceWatcher.markStarted(WorkspaceWatchOptions{})
					p := "uuid-with-hyphens"
					m := workspace.NewManifest(p, branch)
					if err := m.Save(workspace.ManifestPath(workspaceRoot(cfg), p, branch)); err != nil {
						t.Fatal(err)
					}
					path, _, err := workspaceFilePath(cfg, p, branch, "note.md")
					if err != nil {
						t.Fatal(err)
					}
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(strings.Repeat("Copper seventeen. Zinc twenty-three. Total forty units. ", 20)), 0600); err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					generation, err := workspaceWatchReconcile(ctx, cfg, WorkspaceWatchOptions{AsyncIndex: async, GenerationPrefix: "identity"}, workspaceWatchKey{ProjectID: workspace.SafeID(p), Branch: workspace.SafeID(branch)})
					if err != nil {
						t.Fatal(err)
					}
					if async {
						jobs, err := listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{ProjectID: p, Branch: branch})
						if err != nil || len(jobs) != 1 {
							t.Fatalf("jobs=%d error=%v", len(jobs), err)
						}
						if jobs[0].Request.ProjectID != p || jobs[0].Request.Branch != branch {
							t.Fatalf("saved target lost raw identity: %+v", jobs[0].Request)
						}
						if _, _, err := runWorkspaceIndexJob(ctx, cfg, jobs[0], WorkspaceIndexWorkerOptions{}); err != nil {
							t.Fatal(err)
						}
					}
					m, _, err = loadWorkspaceManifest(cfg, p, branch)
					if err != nil || m.ProjectID != p || m.Branch != branch || m.ActiveGeneration != generation {
						t.Fatalf("manifest target/generation mismatch: error=%v", err)
					}
					chunks := m.ListChunks(workspace.ChunkFilter{Generation: generation})
					if len(chunks) == 0 {
						t.Fatal("native watcher produced no chunks")
					}
					for _, c := range chunks {
						if c.ProjectID != p || c.Branch != branch {
							t.Fatal("chunk target disagrees with manifest")
						}
						collection, err := cfg.Collections.Get(c.Collection)
						if err != nil {
							t.Fatal(err)
						}
						_, data, ok := collection.Get(c.VectorID)
						var metadata map[string]any
						if err := json.Unmarshal(data, &metadata); err != nil {
							t.Fatal(err)
						}
						if !ok || metadata["project_id"] != p || metadata["dataset_id"] != p || metadata["branch"] != branch {
							t.Fatal("vector target disagrees with manifest")
						}
					}
					if f.db.Stats().InUse != 0 {
						t.Fatal("service retained SQL")
					}
				})
			}
		}
		for _, target := range []string{"team_a", "orphan", "legacy_uuid"} {
			for _, async := range []bool{false, true} {
				t.Run(fmt.Sprintf("reject/%s/async=%v", target, async), func(t *testing.T) {
					cfg, closeCfg := newWorkspaceTestConfig(t)
					defer closeCfg()
					cfg.DB, cfg.RequireAuth = f.db, true
					cfg.WorkspaceWatcher = NewWorkspaceWatchState()
					cfg.WorkspaceWatcher.markStarted(WorkspaceWatchOptions{})
					path, _, err := workspaceFilePath(cfg, target, "main", "note.md")
					if err != nil {
						t.Fatal(err)
					}
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(strings.Repeat("Existing document with sufficient index text. ", 20)), 0600); err != nil {
						t.Fatal(err)
					}
					if target == "legacy_uuid" {
						m := workspace.NewManifest(target, "main")
						m.ActiveGeneration = "old"
						m.Generations["old"] = workspace.Generation{ID: "old", Status: workspace.GenerationActive}
						m.Chunks["old"] = workspace.ChunkRecord{ProjectID: target, Branch: "main", Generation: "old", Path: "note.md", Collection: "old", VectorID: "old"}
						if err := m.Save(workspace.ManifestPath(workspaceRoot(cfg), target, "main")); err != nil {
							t.Fatal(err)
						}
					}
					endpoint, err := url.Parse(cfg.EmbedEndpoint)
					if err != nil {
						t.Fatal(err)
					}
					proxy := httputil.NewSingleHostReverseProxy(endpoint)
					var calls atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); proxy.ServeHTTP(w, r) }))
					defer server.Close()
					cfg.EmbedEndpoint = server.URL
					if _, err := workspaceWatchReconcile(context.Background(), cfg, WorkspaceWatchOptions{AsyncIndex: async, GenerationPrefix: "reject"}, workspaceWatchKey{ProjectID: target, Branch: "main"}); err == nil {
						t.Error("invalid service target accepted")
					}
					jobs, err := listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{ProjectID: target, Branch: "main"})
					if err != nil || len(jobs) != 0 || calls.Load() != 0 || f.db.Stats().InUse != 0 {
						t.Fatalf("rejected target produced effect: jobs=%d calls=%d error=%v", len(jobs), calls.Load(), err)
					}
				})
			}
		}
	})
}
