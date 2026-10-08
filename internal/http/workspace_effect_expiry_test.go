package http

import (
	"context"
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

	accesspkg "github.com/stek0v/levara/pkg/access"
)

func TestWorkspaceEffectExpiryBoundary(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		for _, operation := range []string{"index", "indexed_write", "reindex", "reconcile", "job_reindex", "job_reconcile", "retry_reindex", "retry_reconcile"} {
			t.Run(operation, func(t *testing.T) {
				cfg, closeCfg := newWorkspaceTestConfig(t)
				defer closeCfg()
				cfg.DB, cfg.RequireAuth = f.db, true
				text := strings.Repeat("Copper seventeen. Zinc twenty-three. Total forty units. ", 20)
				file, _, err := workspaceFilePath(cfg, "alpha", "main", "note.md")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
				target, err := url.Parse(cfg.EmbedEndpoint)
				if err != nil {
					t.Fatal(err)
				}
				expires := time.Now().Unix() + 2
				actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: expires}}
				request := workspaceReindexRequest{
					ProjectID: "alpha", Branch: "main", Generation: "expiry", Collection: "expiry_" + operation,
					Paths: []string{"note.md"}, ActivateGeneration: true,
				}
				ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
				defer cancel()
				submissionActor := actor
				if strings.HasPrefix(operation, "retry_") {
					submissionActor.Credential.ExpiresAt += 3600
				}
				var queued workspaceIndexJob
				if strings.HasPrefix(operation, "job_") || strings.HasPrefix(operation, "retry_") {
					payload := workspaceIndexJobPayloadFromReindex(strings.TrimPrefix(strings.TrimPrefix(operation, "job_"), "retry_"), request, false)
					queued, err = enqueueWorkspaceIndexJobFromPayloadAuthorized(ctx, cfg, payload, submissionActor)
					if err != nil {
						t.Fatal(err)
					}
					queued, err = loadWorkspaceIndexJobPath(workspaceIndexJobPath(cfg, "alpha", "main", queued.ID))
					if err != nil {
						t.Fatal(err)
					}
					if queued.authority == nil || queued.authority.Actor == nil || *queued.authority.Actor != submissionActor {
						t.Fatal("persisted job did not retain submitting authority")
					}
				}
				proxy := httputil.NewSingleHostReverseProxy(target)
				proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
					w.WriteHeader(http.StatusBadGateway)
				}
				var calls atomic.Int32
				var unfenced atomic.Bool
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if f.db.Stats().InUse != 1 {
						unfenced.Store(true)
					}
					time.Sleep(time.Until(time.Unix(expires, 0)) + 100*time.Millisecond)
					proxy.ServeHTTP(w, r)
				}))
				defer server.Close()
				cfg.EmbedEndpoint = server.URL
				switch operation {
				case "index":
					_, err = indexWorkspaceMarkdownAuthorized(ctx, cfg, workspaceIndexRequest{
						ProjectID: request.ProjectID, Branch: request.Branch, Generation: request.Generation,
						Collection: request.Collection, Path: "note.md", Text: text, ActivateGeneration: true,
					}, actor)
				case "indexed_write":
					_, err = writeWorkspaceMarkdownAuthorized(ctx, cfg, workspaceWriteRequest{workspaceIndexRequest: workspaceIndexRequest{
						ProjectID: request.ProjectID, Branch: request.Branch, Generation: request.Generation,
						Collection: request.Collection, Path: "note.md", Text: text, ActivateGeneration: true,
					}}, actor)
				case "reindex":
					_, err = reindexWorkspaceMarkdownAuthorized(ctx, cfg, request, actor)
				case "reconcile":
					_, err = reconcileWorkspaceMarkdownAuthorized(ctx, cfg, workspaceReconcileRequest{workspaceReindexRequest: request}, actor)
				case "retry_reindex", "retry_reconcile":
					_, err = retryWorkspaceIndexJobAuthorized(ctx, cfg, workspaceRetryIndexJobRequest{ProjectID: "alpha", Branch: "main", JobID: queued.ID}, actor)
				default:
					var done workspaceIndexJob
					done, _, err = runWorkspaceIndexJob(ctx, cfg, queued, WorkspaceIndexWorkerOptions{})
					if done.Status == workspaceIndexJobCompleted {
						t.Error("expired persisted job completed")
					}
				}
				if err == nil {
					t.Error("expired provider result accepted")
				}
				if calls.Load() == 0 {
					t.Fatal("native embedding fixture was never reached")
				}
				if unfenced.Load() {
					t.Error("provider entered without SQL authority fence")
				}
				manifest, _, err := loadWorkspaceManifest(cfg, "alpha", "main")
				if err != nil {
					t.Fatal(err)
				}
				if manifest.ActiveGeneration != "" || len(manifest.Chunks) != 0 {
					t.Error("expiry published chunks or active generation")
				}
				for _, name := range cfg.Collections.List() {
					collection, err := cfg.Collections.Get(name)
					if err != nil {
						t.Fatal(err)
					}
					if collection.Count() != 0 {
						t.Errorf("expired vectors remain in native collection %q", name)
					}
				}
				jobs, err := listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{ProjectID: "alpha", Branch: "main"})
				if err != nil {
					t.Fatal(err)
				}
				for _, job := range jobs {
					if job.Status == workspaceIndexJobCompleted {
						t.Error("expired job persisted completion")
					}
				}
				if queued.ID != "" {
					fresh, err := loadWorkspaceIndexJobPath(workspaceIndexJobPath(cfg, "alpha", "main", queued.ID))
					if err != nil {
						t.Fatal(err)
					}
					if fresh.Status == workspaceIndexJobCompleted || fresh.Attempts != 1 {
						t.Errorf("expired worker outcome status=%s attempts=%d", fresh.Status, fresh.Attempts)
					}
					if fresh.authority == nil || fresh.authority.Actor == nil || *fresh.authority.Actor != submissionActor {
						t.Error("worker changed persisted delegation")
					}
				}
				if f.db.Stats().InUse != 0 {
					t.Error("expired operation retained SQL connection")
				}
			})
		}
	})
}
