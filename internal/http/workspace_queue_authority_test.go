package http

import (
	"context"
	"encoding/json"
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

func TestWorkspaceQueuedAuthority(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB, cfg.RequireAuth = f.db, true
		f.db.SetMaxOpenConns(1)
		actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
		payload := workspaceIndexJobPayload{Operation: "reindex", ProjectID: "alpha", Generation: "queued", Paths: []string{"ledger.md"}}
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
		target, err := url.Parse(cfg.EmbedEndpoint)
		if err != nil {
			t.Fatal(err)
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if f.db.Stats().InUse != 1 {
				t.Error("queued embedding lacks authority transaction")
			}
			proxy.ServeHTTP(w, r)
		}))
		defer server.Close()
		cfg.EmbedEndpoint = server.URL
		t.Run("viewer_enqueue", func(t *testing.T) {
			viewer := actor
			viewer.UserID = "viewer"
			if _, err := enqueueWorkspaceIndexJobFromPayloadAuthorized(context.Background(), cfg, payload, viewer); err == nil {
				t.Fatal("viewer enqueue accepted")
			}
			if _, err := os.Stat(workspaceIndexJobDir(cfg, "alpha", "main")); !os.IsNotExist(err) {
				t.Fatal("denied enqueue created job state")
			}
		})
		job, err := enqueueWorkspaceIndexJobFromPayloadAuthorized(context.Background(), cfg, payload, actor)
		if err != nil {
			t.Fatal(err)
		}
		t.Run("private_roundtrip_and_scope", func(t *testing.T) {
			loaded, err := loadWorkspaceIndexJobPath(workspaceIndexJobPath(cfg, "alpha", "main", job.ID))
			if err != nil {
				t.Fatal(err)
			}
			if loaded.authority == nil || loaded.authority.Actor == nil || *loaded.authority.Actor != actor {
				t.Fatal("persisted submitting authority differs")
			}
			public, err := json.Marshal(loaded)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(public), "authority") || strings.Contains(string(public), "Credential") {
				t.Fatal("public job exposes private credential metadata")
			}
			refreshed := actor
			refreshed.Credential.ExpiresAt++
			duplicate, err := enqueueWorkspaceIndexJobFromPayloadAuthorized(context.Background(), cfg, payload, refreshed)
			if err != nil {
				t.Fatal(err)
			}
			if duplicate.ID != job.ID || duplicate.authority.Actor.Credential != actor.Credential {
				t.Fatal("duplicate replaced original delegation")
			}
			for _, changed := range []accesspkg.MetadataActor{{Actor: accesspkg.Actor{UserID: "other", TenantID: actor.TenantID}}, {Actor: accesspkg.Actor{UserID: actor.UserID, TenantID: "other"}}} {
				if workspaceIndexJobAuthorityKey(payload, &workspaceIndexJobAuthority{Actor: &changed}) == job.IdempotencyKey {
					t.Fatal("different authority shares job key")
				}
			}
			if workspaceIndexJobAuthorityKey(payload, &workspaceIndexJobAuthority{Service: true}) == job.IdempotencyKey {
				t.Fatal("service shares user job key")
			}
		})
		t.Run("native_worker_and_stale_snapshot", func(t *testing.T) {
			done, _, err := runWorkspaceIndexJob(context.Background(), cfg, job, WorkspaceIndexWorkerOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if done.Status != workspaceIndexJobCompleted || done.Attempts != 1 || calls.Load() == 0 {
				t.Fatal("queued native job did not complete")
			}
			before := calls.Load()
			stale := job
			stale.Status = workspaceIndexJobRunning
			stale.UpdatedAt = time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
			recovered, changed, err := recoverWorkspaceRunningJob(cfg, stale, time.Now(), normalizeWorkspaceIndexWorkerOptions(WorkspaceIndexWorkerOptions{}))
			if err != nil || changed || recovered.Status != workspaceIndexJobCompleted {
				t.Fatalf("stale recovery overwrote completion: changed=%v error=%v", changed, err)
			}
			again, _, err := runWorkspaceIndexJob(context.Background(), cfg, job, WorkspaceIndexWorkerOptions{})
			if err != nil || again.Attempts != done.Attempts || calls.Load() != before {
				t.Fatal("stale worker reran completed job")
			}
		})
		for _, mode := range []string{"legacy", "local_mode_flip", "expired"} {
			t.Run(mode, func(t *testing.T) {
				next := payload
				next.Generation = mode
				var authority *workspaceIndexJobAuthority
				if mode != "legacy" {
					bound := actor
					if mode == "local_mode_flip" {
						bound = accesspkg.MetadataActor{TrustedLocal: true}
					} else {
						bound.Credential.ExpiresAt = time.Now().Add(-time.Hour).Unix()
					}
					authority = &workspaceIndexJobAuthority{Actor: &bound}
				}
				denied, err := enqueueWorkspaceIndexJobFromPayload(cfg, next, authority)
				if err != nil {
					t.Fatal(err)
				}
				jobPath := workspaceIndexJobPath(cfg, "alpha", "main", denied.ID)
				before, err := os.ReadFile(jobPath)
				if err != nil {
					t.Fatal(err)
				}
				beforeCalls := calls.Load()
				if _, _, err := runWorkspaceIndexJob(context.Background(), cfg, denied, WorkspaceIndexWorkerOptions{}); err == nil {
					t.Fatal("unavailable authority executed queued job")
				}
				after, err := os.ReadFile(jobPath)
				if err != nil {
					t.Fatal(err)
				}
				if string(before) != string(after) || calls.Load() != beforeCalls || f.db.Stats().InUse != 0 {
					t.Fatal("denied job mutated state, called provider, or leaked SQL")
				}
			})
		}
		t.Run("service_admission_drain", func(t *testing.T) {
			cfg.WorkspaceWatcher = NewWorkspaceWatchState()
			cfg.WorkspaceWatcher.enabled = true // Persisted status alone grants no live service authority.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, release, err := beginWorkspaceServiceFence(ctx, cfg, workspaceWatchKey{ProjectID: payload.ProjectID, Branch: payload.Branch}); err == nil {
				release()
				t.Fatal("persisted enabled status grants authority")
			}
			cfg.WorkspaceWatcher.markStarted(WorkspaceWatchOptions{})
			servicePayload := payload
			servicePayload.Generation = "service"
			serviceJob, err := enqueueWorkspaceIndexJobFromPayload(cfg, servicePayload, &workspaceIndexJobAuthority{Service: true})
			if err != nil {
				t.Fatal(err)
			}
			beforeCalls := calls.Load()
			serviceDone, _, err := runWorkspaceIndexJob(ctx, cfg, serviceJob, WorkspaceIndexWorkerOptions{})
			if err != nil || serviceDone.Status != workspaceIndexJobCompleted || calls.Load() <= beforeCalls || f.db.Stats().InUse != 0 {
				t.Fatalf("native service job did not drain: status=%s error=%v", serviceDone.Status, err)
			}
			_, release, err := beginWorkspaceServiceFence(ctx, cfg, workspaceWatchKey{ProjectID: payload.ProjectID, Branch: payload.Branch})
			if err != nil {
				t.Fatal(err)
			}
			stopped := make(chan struct{})
			go func() { cfg.WorkspaceWatcher.markStopped(); close(stopped) }()
			select {
			case <-stopped:
				release()
				t.Fatal("service stopped before admitted operation drained")
			case <-time.After(50 * time.Millisecond):
			}
			// Status callbacks must remain usable while a stop writer is waiting.
			snapshot := make(chan struct{})
			go func() { cfg.WorkspaceWatcher.Snapshot(); close(snapshot) }()
			select {
			case <-snapshot:
			case <-ctx.Done():
				release()
				t.Fatal("status callback blocked by admission lock")
			}
			if f.db.Stats().InUse != 1 {
				release()
				t.Fatal("service fence lost SQL before drain")
			}
			release()
			select {
			case <-stopped:
			case <-ctx.Done():
				t.Fatal("service stop did not finish after drain")
			}
			if _, release, err := beginWorkspaceServiceFence(ctx, cfg, workspaceWatchKey{ProjectID: payload.ProjectID, Branch: payload.Branch}); err == nil {
				release()
				t.Fatal("stopped watcher grants service authority")
			}
			if f.db.Stats().InUse != 0 {
				t.Fatal("service fence leaked connection")
			}
		})
	})
}
