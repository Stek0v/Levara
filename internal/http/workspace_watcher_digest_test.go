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
	"sync"
	"testing"
	"time"

	accesspkg "github.com/stek0v/levara/pkg/access"
)

func waitWorkspaceGenerationCondition(t *testing.T, condition func() bool) {
	t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for !condition() {
		select {
		case <-timer.C:
			t.Fatal("workspace lifecycle condition did not become true")
		case <-tick.C:
		}
	}
}

func TestWorkspaceWatcherDigestAndEmptyInventory(t *testing.T) {
	cfg := APIConfig{WorkspacePath: t.TempDir()}
	path := filepath.Join(workspaceProjectRoot(cfg, "alpha", "main"), "note.md")
	sidecarFixtureWrite(t, path, []byte("alpha"))
	before, has, err := workspaceBranchFingerprint(cfg, filepath.Dir(path))
	if err != nil || !has {
		t.Fatalf("fingerprint %v %v", has, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("bravo"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, _, err := workspaceBranchFingerprint(cfg, filepath.Dir(path))
	if err != nil || before == after {
		t.Fatal("equal-size preserved-mtime edit was invisible")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	all, err := workspaceWatchFingerprints(cfg)
	key := workspaceWatchKey{ProjectID: "alpha", Branch: "main"}
	if err != nil || all[key] != "{}" {
		t.Fatalf("empty branch disappeared: %v %v", all, err)
	}
	manifest, _, err := loadWorkspaceManifest(cfg, key.ProjectID, key.Branch)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ActiveGeneration = "empty"
	manifest.Files = map[string]map[string]string{"empty": {}}
	if err := saveWorkspaceManifest(context.Background(), cfg, workspaceManifestPath(cfg, key.ProjectID, key.Branch), manifest); err != nil {
		t.Fatal(err)
	}
	if matched, err := workspaceWatchInventoryMatches(cfg, key); err != nil || !matched {
		t.Fatalf("known empty not matched: %v %v", matched, err)
	}
	cfg.WorkspaceWatcher = NewWorkspaceWatchState()
	cfg.WorkspaceWatcher.configurePersistence(workspaceWatchStatusPath(cfg))
	cfg.WorkspaceWatcher.recordBranchChange(key, 1)
	cfg.WorkspaceWatcher = NewWorkspaceWatchState()
	cfg.WorkspaceWatcher.configurePersistence(workspaceWatchStatusPath(cfg))
	dirty, err := workspaceWatchDirty(cfg, all)
	if err != nil || len(dirty) != 1 {
		t.Fatalf("persisted pending discarded on restart: %v %v", dirty, err)
	}
	manifest.Files["empty"] = nil
	if err := saveWorkspaceManifest(context.Background(), cfg, workspaceManifestPath(cfg, key.ProjectID, key.Branch), manifest); err != nil {
		t.Fatal(err)
	}
	if matched, err := workspaceWatchInventoryMatches(cfg, key); err != nil || matched {
		t.Fatalf("null inventory accepted as empty: %v %v", matched, err)
	}
	delete(manifest.Files, "empty")
	if err := saveWorkspaceManifest(context.Background(), cfg, workspaceManifestPath(cfg, key.ProjectID, key.Branch), manifest); err != nil {
		t.Fatal(err)
	}
	if matched, err := workspaceWatchInventoryMatches(cfg, key); err != nil || matched {
		t.Fatalf("unknown mistaken for empty: %v %v", matched, err)
	}
}

func TestWorkspaceWatcherNativeRestartAndBlockedJob(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB, cfg.RequireAuth = f.db, true
		cfg.WorkspaceWatcher = NewWorkspaceWatchState()
		cfg.WorkspaceWatcher.markStarted(WorkspaceWatchOptions{})
		key := workspaceWatchKey{ProjectID: "alpha", Branch: "main"}
		path := filepath.Join(workspaceProjectRoot(cfg, key.ProjectID, key.Branch), "note.md")
		first := []byte(strings.Repeat("Original copper publication. ", 20))
		second := []byte(strings.Repeat("Modified copper publication. ", 20))
		third := []byte(strings.Repeat("Followup copper publication. ", 20))
		if len(first) != len(second) || len(second) != len(third) {
			t.Fatal("fixture edit must preserve size")
		}
		sidecarFixtureWrite(t, path, first)
		opts := WorkspaceWatchOptions{Interval: 10 * time.Millisecond, Debounce: 10 * time.Millisecond, GenerationPrefix: "restart", ChunkStrategy: "paragraph", MinChunkChars: 1}
		priorGeneration, err := workspaceWatchReconcile(context.Background(), cfg, opts, key)
		if err != nil {
			t.Fatal(err)
		}
		stop := StartWorkspaceWatcher(context.Background(), cfg, opts)
		waitWorkspaceGenerationCondition(t, func() bool { return cfg.WorkspaceWatcher.Snapshot().ScanCount >= 2 })
		stop()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, second, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}

		originalURL, err := url.Parse(cfg.EmbedEndpoint)
		if err != nil {
			t.Fatal(err)
		}
		proxy := httputil.NewSingleHostReverseProxy(originalURL)
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		var unblock sync.Once
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			once.Do(func() { close(entered); <-release })
			proxy.ServeHTTP(w, r)
		}))
		defer server.Close()
		defer unblock.Do(func() { close(release) })
		cfg.EmbedEndpoint = server.URL
		cfg.WorkspaceWatcher = NewWorkspaceWatchState()
		opts.AsyncIndex = true
		stop = StartWorkspaceWatcher(context.Background(), cfg, opts)
		defer func() { unblock.Do(func() { close(release) }); stop() }()
		var job workspaceIndexJob
		waitWorkspaceGenerationCondition(t, func() bool {
			jobs, err := listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{ProjectID: key.ProjectID, Branch: key.Branch})
			if err != nil || len(jobs) != 2 {
				return false
			}
			for _, candidate := range jobs {
				if candidate.Status == workspaceIndexJobPending {
					job = candidate
					return true
				}
			}
			return false
		})
		workerOptions := WorkspaceIndexWorkerOptions{Backoff: time.Millisecond, MaxAttempts: 3}
		done := make(chan error, 1)
		go func() {
			_, _, err := runWorkspaceIndexJob(context.Background(), cfg, job, workerOptions)
			done <- err
		}()
		select {
		case <-entered:
		case err := <-done:
			t.Fatalf("worker finished before provider: %v", err)
		case <-time.After(8 * time.Second):
			t.Fatal("provider did not start")
		}
		scans := cfg.WorkspaceWatcher.Snapshot().ScanCount
		if err := os.WriteFile(path, third, 0600); err != nil {
			t.Fatal(err)
		}
		waitWorkspaceGenerationCondition(t, func() bool { return cfg.WorkspaceWatcher.Snapshot().ScanCount >= scans+3 })
		jobs, err := listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{ProjectID: key.ProjectID, Branch: key.Branch})
		if err != nil || len(jobs) != 2 || cfg.WorkspaceWatcher.Snapshot().PendingBranches != 1 {
			t.Fatalf("blocked job duplicated or pending lost: %v %v %+v", jobs, err, cfg.WorkspaceWatcher.Snapshot())
		}
		unblock.Do(func() { close(release) })
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "workspace source changed during indexing") {
				t.Fatalf("expected source conflict: %v", err)
			}
		case <-time.After(8 * time.Second):
			t.Fatal("worker did not finish")
		}
		prior, _, err := loadWorkspaceManifest(cfg, key.ProjectID, key.Branch)
		if err != nil || prior.ActiveGeneration != priorGeneration || prior.Files[priorGeneration]["note.md"] != digestBytes(first) {
			t.Fatalf("conflict changed prior publication: %v", err)
		}
		failed, err := loadWorkspaceIndexJobConfined(cfg, workspaceIndexJobPath(cfg, key.ProjectID, key.Branch, job.ID))
		if err != nil || failed.Status != workspaceIndexJobFailed || failed.Attempts != 1 || failed.NextRunAt == "" {
			t.Fatalf("conflict did not preserve retryable job: %+v %v", failed, err)
		}
		scans = cfg.WorkspaceWatcher.Snapshot().ScanCount
		waitWorkspaceGenerationCondition(t, func() bool {
			return cfg.WorkspaceWatcher.Snapshot().ScanCount >= scans+3 && workspaceIndexJobDue(failed, time.Now().UTC())
		})
		jobs, err = listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{ProjectID: key.ProjectID, Branch: key.Branch})
		if err != nil || len(jobs) != 2 || cfg.WorkspaceWatcher.Snapshot().PendingBranches != 1 {
			t.Fatalf("retryable conflict allocated duplicate work: %v %v", jobs, err)
		}
		completed, _, err := runWorkspaceIndexJob(context.Background(), cfg, failed, workerOptions)
		if err != nil || completed.ID != job.ID || completed.Attempts != 2 || completed.Status != workspaceIndexJobCompleted {
			t.Fatalf("fresh retry did not complete same work: %+v %v", completed, err)
		}
		waitWorkspaceGenerationCondition(t, func() bool { return cfg.WorkspaceWatcher.Snapshot().PendingBranches == 0 })
		stop()
		stop = func() {}
		if matched, err := workspaceWatchInventoryMatches(cfg, key); err != nil || !matched {
			t.Fatalf("followup inventory mismatch %v %v", matched, err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		cfg.WorkspaceWatcher = NewWorkspaceWatchState()
		opts.AsyncIndex = false
		stop = StartWorkspaceWatcher(context.Background(), cfg, opts)
		waitWorkspaceGenerationCondition(t, func() bool {
			matched, err := workspaceWatchInventoryMatches(cfg, key)
			return err == nil && matched && cfg.WorkspaceWatcher.Snapshot().ReconcileCount > 0
		})
		stop()
		stop = func() {}
		manifest, _, err := loadWorkspaceManifest(cfg, key.ProjectID, key.Branch)
		if err != nil || len(manifest.Files[manifest.ActiveGeneration]) != 0 {
			t.Fatalf("last deletion not committed: %v", err)
		}
	})
}

func TestWorkspaceWatcherNativeCrashJournalAndDeadLetter(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB, cfg.RequireAuth = f.db, true
		cfg.WorkspaceWatcher = NewWorkspaceWatchState()
		key := workspaceWatchKey{ProjectID: "alpha", Branch: "main"}
		parent := filepath.Dir(workspaceProjectRoot(cfg, key.ProjectID, key.Branch))
		stage := ".restore-11111111-1111-4111-8111-111111111111"
		backup := ".backup-22222222-2222-4222-8222-222222222222"
		original := []byte(strings.Repeat("Previously committed copper bytes. ", 20))
		sidecarFixtureWrite(t, filepath.Join(parent, backup, "note.md"), original)
		sidecarFixtureWrite(t, filepath.Join(parent, stage, "note.md"), []byte("UNCOMMITTED-STAGE"))
		journal := workspaceRestoreJournal{ProjectID: key.ProjectID, Branch: key.Branch, Stage: stage, Backup: backup, Live: "main", HadLive: true}
		raw, err := json.Marshal(journal)
		if err != nil {
			t.Fatal(err)
		}
		journalPath := filepath.Join(parent, ".restore-state-main.json")
		sidecarFixtureWrite(t, journalPath, raw)
		fingerprints, err := workspaceWatchFingerprints(cfg)
		if err != nil || len(fingerprints) != 1 || !strings.HasPrefix(fingerprints[key], "restore:") {
			t.Fatalf("journal absent or private restore trees entered watcher: %v %v", fingerprints, err)
		}
		stop := StartWorkspaceWatcher(context.Background(), cfg, WorkspaceWatchOptions{Interval: 10 * time.Millisecond, Debounce: 10 * time.Millisecond, GenerationPrefix: "crash", ChunkStrategy: "paragraph", MinChunkChars: 1})
		defer func() { stop() }()
		waitWorkspaceGenerationCondition(t, func() bool { matched, err := workspaceWatchInventoryMatches(cfg, key); return err == nil && matched })
		restored, err := readWorkspaceFile(cfg, filepath.Join(workspaceProjectRoot(cfg, key.ProjectID, key.Branch), "note.md"))
		if err != nil || string(restored) != string(original) {
			t.Fatalf("service bypassed crash recovery: %q %v", restored, err)
		}
		for _, name := range []string{stage, backup, ".restore-state-main.json"} {
			if _, err := os.Stat(filepath.Join(parent, name)); !os.IsNotExist(err) {
				t.Fatalf("recovery artifact remained %s: %v", name, err)
			}
		}
		stop()
		// Terminal failure must preserve pending and require an explicit manual retry.
		sidecarFixtureWrite(t, filepath.Join(workspaceProjectRoot(cfg, key.ProjectID, key.Branch), "note.md"), []byte(strings.Repeat("Changed copper bytes. ", 20)))
		dead := workspaceIndexJob{ID: "terminal", Status: workspaceIndexJobDeadLetter, Attempts: 3, LastError: "provider failure",
			Request:   workspaceIndexJobPayload{ProjectID: key.ProjectID, Branch: key.Branch, Operation: "reconcile", Generation: "failed", ActivateGeneration: true, ChunkStrategy: "paragraph", MinChunkChars: 1},
			authority: &workspaceIndexJobAuthority{Service: true}}
		if err := saveWorkspaceIndexJobConfined(cfg, workspaceIndexJobPath(cfg, key.ProjectID, key.Branch, dead.ID), dead); err != nil {
			t.Fatal(err)
		}
		cfg.WorkspaceWatcher = NewWorkspaceWatchState()
		stop = StartWorkspaceWatcher(context.Background(), cfg, WorkspaceWatchOptions{Interval: 10 * time.Millisecond, Debounce: 10 * time.Millisecond, AsyncIndex: true})
		defer func() { stop() }()
		scans := cfg.WorkspaceWatcher.Snapshot().ScanCount
		waitWorkspaceGenerationCondition(t, func() bool { return cfg.WorkspaceWatcher.Snapshot().ScanCount >= scans+4 })
		jobs, err := listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{ProjectID: key.ProjectID, Branch: key.Branch})
		if err != nil || len(jobs) != 2 || cfg.WorkspaceWatcher.Snapshot().PendingBranches != 1 {
			t.Fatalf("dead-letter bypassed: %v %v %+v", jobs, err, cfg.WorkspaceWatcher.Snapshot())
		}
		if _, err := workspaceWatchReconcile(context.Background(), cfg, WorkspaceWatchOptions{AsyncIndex: true}, key); err == nil {
			t.Fatal("dead-letter did not require manual retry")
		}
		persisted, err := loadWorkspaceIndexJobConfined(cfg, workspaceIndexJobPath(cfg, key.ProjectID, key.Branch, dead.ID))
		if err != nil || persisted.Status != workspaceIndexJobDeadLetter || persisted.Attempts != 3 {
			t.Fatalf("terminal job revived: %+v %v", persisted, err)
		}
		manualGeneration := "manual_success"
		_, err = reconcileWorkspaceMarkdownService(context.Background(), cfg, workspaceReconcileRequest{
			workspaceReindexRequest: workspaceReindexRequest{ProjectID: key.ProjectID, Branch: key.Branch, Generation: manualGeneration, ActivateGeneration: true, ChunkStrategy: "paragraph", MinChunkChars: 1},
			DeleteMissing:           true,
		})
		if err != nil {
			t.Fatal(err)
		}
		waitWorkspaceGenerationCondition(t, func() bool { return cfg.WorkspaceWatcher.Snapshot().PendingBranches == 0 })
		persisted, err = loadWorkspaceIndexJobConfined(cfg, workspaceIndexJobPath(cfg, key.ProjectID, key.Branch, dead.ID))
		if err != nil || persisted.Status != workspaceIndexJobDeadLetter || persisted.Attempts != dead.Attempts || persisted.LastError != dead.LastError || persisted.SupersededByGeneration != manualGeneration {
			t.Fatalf("manual publication rewrote failure history: %+v %v", persisted, err)
		}
		sidecarFixtureWrite(t, filepath.Join(workspaceProjectRoot(cfg, key.ProjectID, key.Branch), "note.md"), []byte(strings.Repeat("Newly edited copper bytes. ", 20)))
		var fresh workspaceIndexJob
		waitWorkspaceGenerationCondition(t, func() bool {
			jobs, err := listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{ProjectID: key.ProjectID, Branch: key.Branch})
			if err != nil || len(jobs) != 4 {
				return false
			}
			for _, candidate := range jobs {
				if candidate.Status == workspaceIndexJobPending {
					fresh = candidate
					return true
				}
			}
			return false
		})
		if _, _, err := runWorkspaceIndexJob(context.Background(), cfg, fresh, WorkspaceIndexWorkerOptions{}); err != nil {
			t.Fatal(err)
		}
		waitWorkspaceGenerationCondition(t, func() bool { return cfg.WorkspaceWatcher.Snapshot().PendingBranches == 0 })
		if matched, err := workspaceWatchInventoryMatches(cfg, key); err != nil || !matched {
			t.Fatalf("historical dead-letter blocked new edit: %v %v", matched, err)
		}
		persisted, err = loadWorkspaceIndexJobConfined(cfg, workspaceIndexJobPath(cfg, key.ProjectID, key.Branch, dead.ID))
		if err != nil || persisted.Status != workspaceIndexJobDeadLetter || persisted.SupersededByGeneration != manualGeneration || persisted.Attempts != 3 || persisted.LastError != dead.LastError {
			t.Fatalf("later publication rewrote original supersession: %+v %v", persisted, err)
		}
		markedRetry := workspaceIndexJob{ID: "marked_retry", Status: workspaceIndexJobFailed, Attempts: 1, SupersededByGeneration: manualGeneration,
			Request:   workspaceIndexJobPayload{ProjectID: key.ProjectID, Branch: key.Branch, Operation: "reindex", Generation: "retry_marker", Paths: []string{"missing.md"}},
			authority: &workspaceIndexJobAuthority{Service: true}}
		if err := saveWorkspaceIndexJobConfined(cfg, workspaceIndexJobPath(cfg, key.ProjectID, key.Branch, markedRetry.ID), markedRetry); err != nil {
			t.Fatal(err)
		}
		if workspaceIndexJobDue(markedRetry, time.Now()) {
			t.Fatal("superseded failed job automatically became due")
		}
		actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
		if _, err := retryWorkspaceIndexJobAuthorized(context.Background(), cfg, workspaceRetryIndexJobRequest{ProjectID: key.ProjectID, Branch: key.Branch, JobID: markedRetry.ID}, actor); err == nil {
			t.Fatal("missing source retry succeeded")
		}
		retried, err := loadWorkspaceIndexJobConfined(cfg, workspaceIndexJobPath(cfg, key.ProjectID, key.Branch, markedRetry.ID))
		if err != nil || retried.SupersededByGeneration != "" || retried.Status != workspaceIndexJobDeadLetter || retried.Attempts != 2 {
			t.Fatalf("manual retry hid fresh failure: %+v %v", retried, err)
		}

	})
}

func TestWorkspaceWatcherMalformedRestoreJournalNoWork(t *testing.T) {
	for _, invalid := range []string{"json", "project", "branch", "live", "stage", "backup", "symlink"} {
		t.Run(invalid, func(t *testing.T) {
			cfg := APIConfig{WorkspacePath: t.TempDir()}
			project := filepath.Dir(workspaceProjectRoot(cfg, "alpha", "main"))
			journal := workspaceRestoreJournal{ProjectID: "alpha", Branch: "main", Live: "main", Stage: ".restore-11111111-1111-4111-8111-111111111111", Backup: ".backup-22222222-2222-4222-8222-222222222222", HadLive: true}
			switch invalid {
			case "project":
				journal.ProjectID = "foreign"
			case "branch":
				journal.Branch = "other"
			case "live":
				journal.Live = "../foreign"
			case "stage":
				journal.Stage = "../stage"
			case "backup":
				journal.Backup = ".backup-invalid"
			}
			raw, err := json.Marshal(journal)
			if err != nil {
				t.Fatal(err)
			}
			if invalid == "json" {
				raw = []byte("{broken")
			}
			path := filepath.Join(project, ".restore-state-main.json")
			if invalid == "symlink" {
				external := filepath.Join(t.TempDir(), "journal.json")
				sidecarFixtureWrite(t, external, raw)
				if err := os.MkdirAll(project, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, path); err != nil {
					t.Fatal(err)
				}
			} else {
				sidecarFixtureWrite(t, path, raw)
			}
			fingerprints, err := workspaceWatchFingerprints(cfg)
			if err == nil || len(fingerprints) != 0 {
				t.Fatalf("invalid journal became work: %v %v", fingerprints, err)
			}
			if _, err := os.Stat(workspaceIndexJobDir(cfg, "alpha", "main")); !os.IsNotExist(err) {
				t.Fatalf("discovery created job metadata: %v", err)
			}
		})
	}
}
