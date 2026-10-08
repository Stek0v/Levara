package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"time"

	accesspkg "github.com/stek0v/levara/pkg/access"
	workspacepkg "github.com/stek0v/levara/pkg/workspace"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceWriteReservedDestinationPreservesFile(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB = f.db
		for _, collection := range []string{"_memories", "_memories_alpha"} {
			t.Run(collection, func(t *testing.T) {
				path, _, err := workspaceFilePath(cfg, "alpha", "main", "ledger.md")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				const original = "Copper count 17. Zinc count 23. Total units 40."
				if err := os.WriteFile(path, []byte(original), 0600); err != nil {
					t.Fatal(err)
				}
				_, err = writeWorkspaceMarkdown(context.Background(), cfg, workspaceWriteRequest{
					workspaceIndexRequest: workspaceIndexRequest{ProjectID: "alpha", Path: "ledger.md", Text: "Replacement must be rejected before file mutation.", Generation: "g1", Collection: collection},
				})
				if err == nil || !strings.Contains(err.Error(), "reserved memory collection") {
					t.Fatalf("destination error = %v", err)
				}
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(body) != original {
					t.Fatal("rejected indexing destination modified the source file")
				}
			})
		}
	})
}

func TestWorkspaceOrdinaryEffectsAuthorityFence(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB, cfg.RequireAuth = f.db, true
		f.db.SetMaxOpenConns(1)
		actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
		viewer := actor
		viewer.UserID = "viewer"
		for _, effect := range []string{"write", "delete", "gc"} {
			t.Run("viewer_"+effect, func(t *testing.T) {
				var err error
				switch effect {
				case "write":
					_, err = writeWorkspaceMarkdownAuthorized(context.Background(), cfg, workspaceWriteRequest{workspaceIndexRequest: workspaceIndexRequest{ProjectID: "alpha", Path: "denied.md", Text: "Must not be written."}}, viewer)
				case "delete":
					_, err = deleteWorkspaceMarkdownAuthorized(context.Background(), cfg, workspaceDeleteRequest{ProjectID: "alpha", Path: "denied.md"}, viewer)
				case "gc":
					_, err = gcWorkspaceGenerationsAuthorized(context.Background(), cfg, workspaceGCRequest{ProjectID: "alpha"}, viewer)
				}
				if err == nil {
					t.Fatal("viewer mutation accepted")
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("denied effect retained connection")
				}
				path, _, err := workspaceFilePath(cfg, "alpha", "main", "denied.md")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("denied effect created a file")
				}
			})
		}
		target, err := url.Parse(cfg.EmbedEndpoint)
		if err != nil {
			t.Fatal(err)
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if f.db.Stats().InUse != 1 {
				t.Error("native embedding ran without authority transaction")
			}
			proxy.ServeHTTP(w, r)
		}))
		defer server.Close()
		cfg.EmbedEndpoint = server.URL
		var collections []string
		var vectors [][]string
		for _, generation := range []string{"g1", "g2"} {
			t.Run("write_"+generation, func(t *testing.T) {
				resp, err := writeWorkspaceMarkdownAuthorized(context.Background(), cfg, workspaceWriteRequest{workspaceIndexRequest: workspaceIndexRequest{
					ProjectID: "alpha", Generation: generation, Path: generation + ".md", Text: strings.Repeat("Copper seventeen. Zinc twenty-three. Total forty units. ", 20), ActivateGeneration: true,
				}}, actor)
				if err != nil {
					t.Fatal(err)
				}
				if resp.Indexed == nil || len(resp.Indexed.Result.VectorIDs) == 0 {
					t.Fatal("authorized write did not index")
				}
				collections = append(collections, resp.Indexed.Result.Collection)
				vectors = append(vectors, resp.Indexed.Result.VectorIDs)
				if f.db.Stats().InUse != 0 {
					t.Fatal("native write retained connection")
				}
			})
		}
		if len(collections) != 2 || calls.Load() < 2 {
			t.Fatal("native write fixtures did not finish")
		}

		for _, effect := range []string{"delete", "gc"} {
			t.Run("viewer_preserves_existing_"+effect, func(t *testing.T) {
				_, manifestPath, err := loadWorkspaceManifest(cfg, "alpha", "main")
				if err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(manifestPath)
				if err != nil {
					t.Fatal(err)
				}
				if effect == "delete" {
					_, err = deleteWorkspaceMarkdownAuthorized(context.Background(), cfg, workspaceDeleteRequest{ProjectID: "alpha", Generation: "g2", Path: "g2.md"}, viewer)
				} else {
					_, err = gcWorkspaceGenerationsAuthorized(context.Background(), cfg, workspaceGCRequest{ProjectID: "alpha"}, viewer)
				}
				if err == nil {
					t.Fatal("viewer destructive effect accepted")
				}
				after, err := os.ReadFile(manifestPath)
				if err != nil {
					t.Fatal(err)
				}
				if string(before) != string(after) {
					t.Fatal("denied effect changed manifest")
				}
				for i, ids := range vectors {
					for _, id := range ids {
						if !cfg.Collections.HasRecord(collections[i], id) {
							t.Fatal("denied effect removed vector")
						}
					}
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("denied effect retained connection")
				}
			})
		}
		t.Run("gc", func(t *testing.T) {
			resp, err := gcWorkspaceGenerationsAuthorized(context.Background(), cfg, workspaceGCRequest{ProjectID: "alpha"}, actor)
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.Result.Generations) != 1 || resp.Result.Generations[0] != "g1" || len(resp.Result.DroppedCollections) != 0 || len(resp.Result.DeletedVectorIDs) != len(vectors[0]) {
				t.Fatal("GC did not retire exact pending generation records")
			}
			if !cfg.Collections.Has(collections[0]) || !cfg.Collections.Has(collections[1]) {
				t.Fatal("GC removed a collection")
			}
			for i, ids := range vectors {
				for _, id := range ids {
					if cfg.Collections.HasRecord(collections[i], id) != (i == 1) {
						t.Fatal("GC changed the wrong generation records")
					}
				}
			}
			manifest, _, err := loadWorkspaceManifest(cfg, "alpha", "main")
			if err != nil {
				t.Fatal(err)
			}
			if _, remains := manifest.Generations["g1"]; remains || manifest.ActiveGeneration != "g2" || len(manifest.ListChunks(workspacepkg.ChunkFilter{Generation: "g1"})) != 0 || len(manifest.PendingRetirements) != 0 {
				t.Fatal("GC manifest retirement mismatch")
			}
			if f.db.Stats().InUse != 0 {
				t.Fatal("GC retained connection")
			}
		})
		t.Run("delete", func(t *testing.T) {
			resp, err := deleteWorkspaceMarkdownAuthorized(context.Background(), cfg, workspaceDeleteRequest{ProjectID: "alpha", Generation: "g2", Path: "g2.md"}, actor)
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.DeletedVectorIDs) == 0 {
				t.Fatal("delete removed no vectors")
			}
			for _, id := range vectors[1] {
				if cfg.Collections.HasRecord(collections[1], id) {
					t.Fatal("deleted vector remains in collection")
				}
			}
			manifest, _, err := loadWorkspaceManifest(cfg, "alpha", "main")
			if err != nil {
				t.Fatal(err)
			}
			if len(manifest.ListChunks(workspacepkg.ChunkFilter{Generation: "g2", Path: "g2.md"})) != 0 {
				t.Fatal("deleted chunks remain in manifest")
			}
			if f.db.Stats().InUse != 0 {
				t.Fatal("delete retained connection")
			}
		})
	})
}

func TestWorkspaceSynchronousJobsAuthorityFence(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB, cfg.RequireAuth = f.db, true
		f.db.SetMaxOpenConns(1)
		actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
		viewer := actor
		viewer.UserID = "viewer"
		path, _, err := workspaceFilePath(cfg, "alpha", "main", "ledger.md")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Repeat("Copper seventeen. Zinc twenty-three. Total forty units. ", 20)), 0600); err != nil {
			t.Fatal(err)
		}
		req := workspaceReindexRequest{ProjectID: "alpha", Generation: "g1", Paths: []string{"ledger.md"}}
		for _, effect := range []string{"reindex", "reconcile"} {
			t.Run("viewer_"+effect, func(t *testing.T) {
				var err error
				if effect == "reindex" {
					_, err = reindexWorkspaceMarkdownAuthorized(context.Background(), cfg, req, viewer)
				} else {
					_, err = reconcileWorkspaceMarkdownAuthorized(context.Background(), cfg, workspaceReconcileRequest{workspaceReindexRequest: req}, viewer)
				}
				if err == nil {
					t.Fatal("viewer job accepted")
				}
				if _, err := os.Stat(workspaceIndexJobDir(cfg, "alpha", "main")); !os.IsNotExist(err) {
					t.Fatal("denied job created state")
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("denied job retained SQL connection")
				}
			})
		}
		target, err := url.Parse(cfg.EmbedEndpoint)
		if err != nil {
			t.Fatal(err)
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		var embeddingCalls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			embeddingCalls.Add(1)
			if f.db.Stats().InUse != 1 {
				t.Error("job embedding ran without authority transaction")
			}
			proxy.ServeHTTP(w, r)
		}))
		defer server.Close()
		cfg.EmbedEndpoint = server.URL
		t.Run("reindex", func(t *testing.T) {
			resp, err := reindexWorkspaceMarkdownAuthorized(context.Background(), cfg, req, actor)
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.Results) != 1 || len(resp.Results[0].VectorIDs) == 0 {
				t.Fatal("reindex produced no native vectors")
			}
			if f.db.Stats().InUse != 0 {
				t.Fatal("reindex retained SQL connection")
			}
		})
		t.Run("reconcile", func(t *testing.T) {
			next := req
			next.Generation = "g2"
			resp, err := reconcileWorkspaceMarkdownAuthorized(context.Background(), cfg, workspaceReconcileRequest{workspaceReindexRequest: next}, actor)
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.Results) != 1 || len(resp.Results[0].VectorIDs) == 0 {
				t.Fatal("reconcile produced no native vectors")
			}
			if f.db.Stats().InUse != 0 {
				t.Fatal("reconcile retained SQL connection")
			}
		})
		jobs, err := listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{ProjectID: "alpha"})
		if err != nil || len(jobs) != 2 {
			t.Fatalf("finished jobs=%d error=%v", len(jobs), err)
		}
		job := jobs[0]
		retry := workspaceRetryIndexJobRequest{ProjectID: "alpha", JobID: job.ID}
		t.Run("viewer_retry", func(t *testing.T) {
			before, err := os.ReadFile(workspaceIndexJobPath(cfg, "alpha", "main", job.ID))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := retryWorkspaceIndexJobAuthorized(context.Background(), cfg, retry, viewer); err == nil {
				t.Fatal("viewer retry accepted")
			}
			after, err := os.ReadFile(workspaceIndexJobPath(cfg, "alpha", "main", job.ID))
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) || f.db.Stats().InUse != 0 {
				t.Fatal("denied retry changed job or retained connection")
			}
		})
		t.Run("owner_retry", func(t *testing.T) {
			resp, err := retryWorkspaceIndexJobAuthorized(context.Background(), cfg, retry, actor)
			if err != nil {
				t.Fatal(err)
			}
			if resp.Job.Status != workspaceIndexJobCompleted || resp.Job.Attempts != job.Attempts+1 {
				t.Fatal("authorized retry did not complete native job")
			}
			if f.db.Stats().InUse != 0 {
				t.Fatal("retry retained SQL connection")
			}
		})

		t.Run("branch_before_sql", func(t *testing.T) {
			unlock := workspaceManifestLocks.lock("alpha/main")
			started, done := make(chan struct{}), make(chan error, 1)
			before := embeddingCalls.Load()
			go func() {
				close(started)
				_, err := reindexWorkspaceMarkdownAuthorized(context.Background(), cfg, req, actor)
				done <- err
			}()
			<-started
			var early bool
			var runErr error
			select {
			case runErr = <-done:
				early = true
			case <-time.After(100 * time.Millisecond):
			}
			callsWhileLocked, inUseWhileLocked := embeddingCalls.Load(), f.db.Stats().InUse
			unlock()
			if !early {
				runErr = <-done
			}
			if runErr != nil {
				t.Fatal(runErr)
			}
			if early || callsWhileLocked != before || inUseWhileLocked != 0 {
				t.Fatal("job reached SQL/provider before acquiring branch lock")
			}
			if embeddingCalls.Load() <= before || f.db.Stats().InUse != 0 {
				t.Fatal("job did not drain after branch release")
			}
		})
		t.Run("saved_target_integrity", func(t *testing.T) {
			job.Request.ProjectID = "beta"
			jobPath := workspaceIndexJobPath(cfg, "alpha", "main", job.ID)
			if err := saveWorkspaceIndexJobPath(jobPath, job); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(jobPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := retryWorkspaceIndexJobAuthorized(context.Background(), cfg, retry, actor); err == nil || !strings.Contains(err.Error(), "target") {
				t.Fatalf("mismatched persisted target error=%v", err)
			}
			after, err := os.ReadFile(jobPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) || f.db.Stats().InUse != 0 {
				t.Fatal("invalid target changed job or retained connection")
			}
		})
	})
}
