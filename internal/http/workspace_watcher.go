package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/workspace"
)

type WorkspaceWatchOptions struct {
	Interval         time.Duration
	Debounce         time.Duration
	GenerationPrefix string
	ChunkStrategy    string
	MinChunkChars    int
	MaxChunkChars    int
	AsyncIndex       bool
	Logf             func(format string, args ...any)
}

type workspaceWatchKey struct {
	ProjectID string
	Branch    string
}

type WorkspaceWatchState struct {
	serviceMu        sync.RWMutex
	mu               sync.RWMutex
	enabled          bool
	running          bool
	startedAt        time.Time
	stoppedAt        time.Time
	lastScanAt       time.Time
	lastChangeAt     time.Time
	lastReconcileAt  time.Time
	lastErrorAt      time.Time
	lastError        string
	lastGeneration   string
	lastProjectID    string
	lastBranch       string
	scanCount        int64
	reconcileCount   int64
	errorCount       int64
	watchedBranches  int
	pendingBranches  int
	interval         time.Duration
	debounce         time.Duration
	generationPrefix string
	persistPath      string
	branches         map[string]WorkspaceBranchWatchStatus
}

type WorkspaceWatchStatus struct {
	Enabled          bool                                  `json:"enabled"`
	StartedAt        string                                `json:"started_at,omitempty"`
	StoppedAt        string                                `json:"stopped_at,omitempty"`
	LastScanAt       string                                `json:"last_scan_at,omitempty"`
	LastChangeAt     string                                `json:"last_change_at,omitempty"`
	LastReconcileAt  string                                `json:"last_reconcile_at,omitempty"`
	LastErrorAt      string                                `json:"last_error_at,omitempty"`
	LastError        string                                `json:"last_error,omitempty"`
	LastGeneration   string                                `json:"last_generation,omitempty"`
	LastProjectID    string                                `json:"last_project_id,omitempty"`
	LastBranch       string                                `json:"last_branch,omitempty"`
	ScanCount        int64                                 `json:"scan_count"`
	ReconcileCount   int64                                 `json:"reconcile_count"`
	ErrorCount       int64                                 `json:"error_count"`
	WatchedBranches  int                                   `json:"watched_branches"`
	PendingBranches  int                                   `json:"pending_branches"`
	IntervalMs       int64                                 `json:"interval_ms,omitempty"`
	DebounceMs       int64                                 `json:"debounce_ms,omitempty"`
	GenerationPrefix string                                `json:"generation_prefix,omitempty"`
	Branches         map[string]WorkspaceBranchWatchStatus `json:"branches,omitempty"`
}

type WorkspaceBranchWatchStatus struct {
	ProjectID       string `json:"project_id"`
	Branch          string `json:"branch"`
	Pending         bool   `json:"pending"`
	LastScanAt      string `json:"last_scan_at,omitempty"`
	LastChangeAt    string `json:"last_change_at,omitempty"`
	LastReconcileAt string `json:"last_reconcile_at,omitempty"`
	LastErrorAt     string `json:"last_error_at,omitempty"`
	LastError       string `json:"last_error,omitempty"`
	LastGeneration  string `json:"last_generation,omitempty"`
	ScanCount       int64  `json:"scan_count"`
	ReconcileCount  int64  `json:"reconcile_count"`
	ErrorCount      int64  `json:"error_count"`
}

func NewWorkspaceWatchState() *WorkspaceWatchState {
	return &WorkspaceWatchState{branches: map[string]WorkspaceBranchWatchStatus{}}
}

func (s *WorkspaceWatchState) Snapshot() WorkspaceWatchStatus {
	if s == nil {
		return WorkspaceWatchStatus{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

func (s *WorkspaceWatchState) snapshotLocked() WorkspaceWatchStatus {
	branches := make(map[string]WorkspaceBranchWatchStatus, len(s.branches))
	for key, status := range s.branches {
		branches[key] = status
	}
	return WorkspaceWatchStatus{
		Enabled:          s.enabled,
		StartedAt:        formatWatchTime(s.startedAt),
		StoppedAt:        formatWatchTime(s.stoppedAt),
		LastScanAt:       formatWatchTime(s.lastScanAt),
		LastChangeAt:     formatWatchTime(s.lastChangeAt),
		LastReconcileAt:  formatWatchTime(s.lastReconcileAt),
		LastErrorAt:      formatWatchTime(s.lastErrorAt),
		LastError:        s.lastError,
		LastGeneration:   s.lastGeneration,
		LastProjectID:    s.lastProjectID,
		LastBranch:       s.lastBranch,
		ScanCount:        s.scanCount,
		ReconcileCount:   s.reconcileCount,
		ErrorCount:       s.errorCount,
		WatchedBranches:  s.watchedBranches,
		PendingBranches:  s.pendingBranches,
		IntervalMs:       s.interval.Milliseconds(),
		DebounceMs:       s.debounce.Milliseconds(),
		GenerationPrefix: s.generationPrefix,
		Branches:         branches,
	}
}

func (s *WorkspaceWatchState) markStarted(opts WorkspaceWatchOptions) {
	if s == nil {
		return
	}
	s.serviceMu.Lock()
	defer s.serviceMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = true
	s.running = true
	s.startedAt = time.Now().UTC()
	s.stoppedAt = time.Time{}
	s.interval = opts.Interval
	s.debounce = opts.Debounce
	s.generationPrefix = opts.GenerationPrefix
	s.persistLocked()
}

func (s *WorkspaceWatchState) markStopped() {
	if s == nil {
		return
	}
	s.serviceMu.Lock()
	defer s.serviceMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = false
	s.running = false
	s.stoppedAt = time.Now().UTC()
	s.pendingBranches = 0
	s.persistLocked()
}

func (s *WorkspaceWatchState) recordScan(branches, pending int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastScanAt = time.Now().UTC()
	s.scanCount++
	s.watchedBranches = branches
	s.pendingBranches = pending
	s.persistLocked()
}

func (s *WorkspaceWatchState) recordScanBranches(current map[workspaceWatchKey]string, dirty map[workspaceWatchKey]time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.lastScanAt = now
	s.scanCount++
	s.watchedBranches = len(current)
	s.pendingBranches = len(dirty)
	if s.branches == nil {
		s.branches = map[string]WorkspaceBranchWatchStatus{}
	}
	seen := make(map[string]struct{}, len(current)+len(dirty))
	for key := range current {
		statusKey := workspaceWatchStatusKey(key)
		seen[statusKey] = struct{}{}
		status := s.branchStatusLocked(key)
		status.LastScanAt = formatWatchTime(now)
		status.ScanCount++
		_, status.Pending = dirty[key]
		s.branches[statusKey] = status
	}
	for key := range dirty {
		statusKey := workspaceWatchStatusKey(key)
		if _, ok := seen[statusKey]; ok {
			continue
		}
		status := s.branchStatusLocked(key)
		status.LastScanAt = formatWatchTime(now)
		status.ScanCount++
		status.Pending = true
		s.branches[statusKey] = status
	}
	s.persistLocked()
}

func (s *WorkspaceWatchState) recordChange(pending int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastChangeAt = time.Now().UTC()
	s.pendingBranches = pending
	s.persistLocked()
}

func (s *WorkspaceWatchState) recordBranchChange(key workspaceWatchKey, pending int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.lastChangeAt = now
	s.pendingBranches = pending
	statusKey := workspaceWatchStatusKey(key)
	status := s.branchStatusLocked(key)
	status.Pending = true
	status.LastChangeAt = formatWatchTime(now)
	s.branches[statusKey] = status
	s.persistLocked()
}

func (s *WorkspaceWatchState) recordReconcile(key workspaceWatchKey, generation string, pending int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastReconcileAt = time.Now().UTC()
	s.lastProjectID = key.ProjectID
	s.lastBranch = key.Branch
	s.lastGeneration = generation
	s.reconcileCount++
	s.pendingBranches = pending
	statusKey := workspaceWatchStatusKey(key)
	status := s.branchStatusLocked(key)
	status.Pending = false
	status.LastReconcileAt = formatWatchTime(s.lastReconcileAt)
	status.LastGeneration = generation
	status.ReconcileCount++
	status.LastError = ""
	status.LastErrorAt = ""
	s.branches[statusKey] = status
	s.persistLocked()
}

func (s *WorkspaceWatchState) recordError(err error) {
	if s == nil || err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErrorAt = time.Now().UTC()
	s.lastError = err.Error()
	s.errorCount++
	s.persistLocked()
}

func (s *WorkspaceWatchState) recordBranchError(key workspaceWatchKey, err error) {
	if s == nil || err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.lastErrorAt = now
	s.lastError = err.Error()
	s.errorCount++
	statusKey := workspaceWatchStatusKey(key)
	status := s.branchStatusLocked(key)
	status.Pending = true
	status.LastErrorAt = formatWatchTime(now)
	status.LastError = err.Error()
	status.ErrorCount++
	s.branches[statusKey] = status
	s.persistLocked()
}

func (s *WorkspaceWatchState) branchStatusLocked(key workspaceWatchKey) WorkspaceBranchWatchStatus {
	if s.branches == nil {
		s.branches = map[string]WorkspaceBranchWatchStatus{}
	}
	status := s.branches[workspaceWatchStatusKey(key)]
	status.ProjectID = key.ProjectID
	status.Branch = key.Branch
	return status
}

func workspaceWatchStatusKey(key workspaceWatchKey) string {
	return safeWorkspaceID(key.ProjectID) + "/" + safeWorkspaceID(defaultBranch(key.Branch))
}

func formatWatchTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (s *WorkspaceWatchState) configurePersistence(path string) {
	if s == nil || path == "" {
		return
	}
	_ = s.loadPersisted(path)
}

func (s *WorkspaceWatchState) loadPersisted(path string) error {
	root, name, err := openWatcherStateParent(path, false)
	if os.IsNotExist(err) {
		s.mu.Lock()
		s.persistPath = path
		s.mu.Unlock()
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	data, err := workspace.ReadFile(root, name)
	if err != nil {
		if os.IsNotExist(err) {
			s.mu.Lock()
			s.persistPath = path
			s.mu.Unlock()
			return nil
		}
		return err
	}
	var status WorkspaceWatchStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persistPath = path
	s.enabled = status.Enabled
	s.startedAt = parseWatchTime(status.StartedAt)
	s.stoppedAt = parseWatchTime(status.StoppedAt)
	s.lastScanAt = parseWatchTime(status.LastScanAt)
	s.lastChangeAt = parseWatchTime(status.LastChangeAt)
	s.lastReconcileAt = parseWatchTime(status.LastReconcileAt)
	s.lastErrorAt = parseWatchTime(status.LastErrorAt)
	s.lastError = status.LastError
	s.lastGeneration = status.LastGeneration
	s.lastProjectID = status.LastProjectID
	s.lastBranch = status.LastBranch
	s.scanCount = status.ScanCount
	s.reconcileCount = status.ReconcileCount
	s.errorCount = status.ErrorCount
	s.watchedBranches = status.WatchedBranches
	s.pendingBranches = status.PendingBranches
	s.interval = time.Duration(status.IntervalMs) * time.Millisecond
	s.debounce = time.Duration(status.DebounceMs) * time.Millisecond
	s.generationPrefix = status.GenerationPrefix
	s.branches = status.Branches
	if s.branches == nil {
		s.branches = map[string]WorkspaceBranchWatchStatus{}
	}
	return nil
}

func (s *WorkspaceWatchState) persistLocked() {
	if s.persistPath == "" {
		return
	}
	data, err := json.MarshalIndent(s.snapshotLocked(), "", "  ")
	if err != nil {
		return
	}
	root, name, err := openWatcherStateParent(s.persistPath, true)
	if err != nil {
		return
	}
	defer root.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = workspace.WriteFile(ctx, root, name, data, 0644)
}

func parseWatchTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// StartWorkspaceWatcher starts a polling watcher over workspace markdown files.
// It debounces save bursts and reconciles the affected project/branch into a
// fresh active generation. The returned function stops the watcher.
func StartWorkspaceWatcher(ctx context.Context, cfg APIConfig, opts WorkspaceWatchOptions) func() {
	opts = normalizeWorkspaceWatchOptions(opts)
	if cfg.WorkspaceWatcher != nil {
		cfg.WorkspaceWatcher.configurePersistence(workspaceWatchStatusPath(cfg))
		cfg.WorkspaceWatcher.markStarted(opts)
	}
	initial, err := workspaceWatchFingerprints(cfg)
	if err != nil {
		opts.logf("[workspace-watch] initial scan failed: %v", err)
		if cfg.WorkspaceWatcher != nil {
			cfg.WorkspaceWatcher.recordError(err)
		}
		initial = map[workspaceWatchKey]string{}
	}
	dirty, bootstrapErr := workspaceWatchDirty(cfg, initial)
	if bootstrapErr != nil {
		opts.logf("[workspace-watch] inventory check failed: %v", bootstrapErr)
		if cfg.WorkspaceWatcher != nil {
			cfg.WorkspaceWatcher.recordError(bootstrapErr)
		}
	}
	if cfg.WorkspaceWatcher != nil {
		cfg.WorkspaceWatcher.recordScanBranches(initial, dirty)
	}
	wctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go workspaceWatchLoop(wctx, cfg, opts, initial, dirty, done)
	return func() {
		cancel()
		<-done
		if cfg.WorkspaceWatcher != nil {
			cfg.WorkspaceWatcher.markStopped()
		}
	}
}

func workspaceWatchStatusPath(cfg APIConfig) string {
	return filepath.Join(workspaceRoot(cfg), ".kb", "watch-status.json")
}

func normalizeWorkspaceWatchOptions(opts WorkspaceWatchOptions) WorkspaceWatchOptions {
	if opts.Interval <= 0 {
		opts.Interval = 2 * time.Second
	}
	if opts.Debounce <= 0 {
		opts.Debounce = 1500 * time.Millisecond
	}
	if opts.GenerationPrefix == "" {
		opts.GenerationPrefix = "watch"
	}
	if opts.ChunkStrategy == "" {
		opts.ChunkStrategy = "merged"
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	return opts
}

func (opts WorkspaceWatchOptions) logf(format string, args ...any) {
	if opts.Logf != nil {
		opts.Logf(format, args...)
	}
}

func workspaceWatchLoop(ctx context.Context, cfg APIConfig, opts WorkspaceWatchOptions, previous map[workspaceWatchKey]string, dirty map[workspaceWatchKey]time.Time, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			current, err := workspaceWatchFingerprints(cfg)
			if err != nil {
				opts.logf("[workspace-watch] scan failed: %v", err)
				if cfg.WorkspaceWatcher != nil {
					cfg.WorkspaceWatcher.recordError(err)
				}
				continue
			}
			for key, fingerprint := range current {
				if previous[key] != fingerprint {
					dirty[key] = now
				}
			}
			for key := range previous {
				if _, ok := current[key]; !ok {
					dirty[key] = now
				}
			}
			previous = current
			if cfg.WorkspaceWatcher != nil {
				cfg.WorkspaceWatcher.recordScanBranches(current, dirty)
				if len(dirty) > 0 {
					cfg.WorkspaceWatcher.recordChange(len(dirty))
					for key := range dirty {
						cfg.WorkspaceWatcher.recordBranchChange(key, len(dirty))
					}
				}
			}
			for key, lastChanged := range dirty {
				if now.Sub(lastChanged) < opts.Debounce {
					continue
				}
				// Continue scanning while a provider holds the project's effect fence.
				outstanding, err := workspaceWatchOutstanding(cfg, key)
				if err != nil {
					if cfg.WorkspaceWatcher != nil {
						cfg.WorkspaceWatcher.recordBranchError(key, err)
					}
					continue
				}
				if outstanding != nil {
					if outstanding.Status == workspaceIndexJobDeadLetter || (outstanding.Status == workspaceIndexJobFailed && outstanding.NextRunAt == "") {
						if cfg.WorkspaceWatcher != nil {
							cfg.WorkspaceWatcher.recordBranchError(key, errors.New("workspace indexing requires manual retry"))
						}
					}
					continue
				}
				generation, err := workspaceWatchReconcile(ctx, cfg, opts, key)
				if err != nil {
					opts.logf("[workspace-watch] reconcile %s/%s failed: %v", key.ProjectID, key.Branch, err)
					if cfg.WorkspaceWatcher != nil {
						cfg.WorkspaceWatcher.recordBranchError(key, err)
					}
					continue
				}
				matched, err := workspaceWatchInventoryMatches(cfg, key)
				if err != nil {
					if cfg.WorkspaceWatcher != nil {
						cfg.WorkspaceWatcher.recordBranchError(key, err)
					}
					continue
				}
				if !matched {
					continue
				}
				delete(dirty, key)
				if cfg.WorkspaceWatcher != nil {
					cfg.WorkspaceWatcher.recordReconcile(key, generation, len(dirty))
				}
			}
		}
	}
}

func workspaceWatchReconcile(ctx context.Context, cfg APIConfig, opts WorkspaceWatchOptions, key workspaceWatchKey) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(key.ProjectID, key.Branch))
	defer unlock()
	target, release, err := beginWorkspaceServiceFence(ctx, cfg, key)
	if err != nil {
		return "", err
	}
	defer release()
	outstanding, err := workspaceWatchOutstanding(cfg, target)
	if err != nil {
		return "", err
	}
	if outstanding != nil {
		if outstanding.Status == workspaceIndexJobDeadLetter || (outstanding.Status == workspaceIndexJobFailed && outstanding.NextRunAt == "") {
			return outstanding.Request.Generation, errors.New("workspace indexing requires manual retry")
		}
		return outstanding.Request.Generation, nil
	}
	matched, err := workspaceWatchInventoryMatches(cfg, target)
	if err != nil {
		return "", err
	}
	if matched {
		manifest, _, err := readWorkspaceManifest(cfg, target.ProjectID, target.Branch)
		if err != nil {
			return "", err
		}
		return manifest.ActiveGeneration, nil
	}
	generation := fmt.Sprintf("%s-%s", opts.GenerationPrefix, time.Now().UTC().Format("20060102T150405.000000000Z"))
	req := workspaceReconcileRequest{
		workspaceReindexRequest: workspaceReindexRequest{
			ProjectID: target.ProjectID, Branch: target.Branch, Generation: generation,
			ChunkStrategy: opts.ChunkStrategy, MinChunkChars: opts.MinChunkChars, MaxChunkChars: opts.MaxChunkChars,
			ActivateGeneration: true,
		},
		DeleteMissing: true,
	}
	authority := &workspaceIndexJobAuthority{Service: true}
	if opts.AsyncIndex {
		_, err = enqueueWorkspaceIndexJobFromPayload(cfg, workspaceIndexJobPayloadFromReindex("reconcile", req.workspaceReindexRequest, req.DeleteMissing), authority)
	} else {
		_, err = reconcileWorkspaceMarkdownLocked(ctx, cfg, req, authority)
	}
	return generation, err
}

// Retryable failures already own their branch's work; the worker applies its backoff.
func workspaceWatchOutstanding(cfg APIConfig, key workspaceWatchKey) (*workspaceIndexJob, error) {
	jobs, err := listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{ProjectID: key.ProjectID, Branch: key.Branch})
	if err != nil {
		return nil, err
	}
	for _, job := range jobs {
		if job.SupersededByGeneration != "" && (job.Status == workspaceIndexJobFailed || job.Status == workspaceIndexJobDeadLetter) {
			continue
		}
		if job.Request.Operation != "reconcile" && job.Request.Operation != "reindex" {
			continue
		}
		if job.Status == workspaceIndexJobPending || job.Status == workspaceIndexJobRunning || job.Status == workspaceIndexJobFailed || job.Status == workspaceIndexJobDeadLetter {
			return &job, nil
		}
	}
	return nil, nil
}

func workspaceWatchInventoryMatches(cfg APIConfig, key workspaceWatchKey) (bool, error) {
	manifest, exists, err := readWorkspaceManifest(cfg, key.ProjectID, key.Branch)
	if err != nil || !exists {
		return false, err
	}
	inventory, known := manifest.Files[manifest.ActiveGeneration]
	if manifest.ActiveGeneration == "" || !known || inventory == nil {
		return false, nil
	}
	actual, err := workspaceBranchInventory(cfg, workspaceProjectRoot(cfg, key.ProjectID, key.Branch))
	if err != nil {
		return false, err
	}
	if len(actual) != len(inventory) {
		return false, nil
	}
	for path, digest := range actual {
		if inventory[path] != digest {
			return false, nil
		}
	}
	return true, nil
}

func workspaceWatchDirty(cfg APIConfig, current map[workspaceWatchKey]string) (map[workspaceWatchKey]time.Time, error) {
	dirty := make(map[workspaceWatchKey]time.Time)
	now := time.Now().UTC()
	if state := cfg.WorkspaceWatcher; state != nil {
		state.mu.RLock()
		for _, status := range state.branches {
			if status.Pending {
				dirty[workspaceWatchKey{ProjectID: status.ProjectID, Branch: status.Branch}] = now
			}
		}
		state.mu.RUnlock()
	}
	var firstErr error
	for key, fingerprint := range current {
		if strings.HasPrefix(fingerprint, "restore:") {
			dirty[key] = now
			continue
		}
		matched, err := workspaceWatchInventoryMatches(cfg, key)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if !matched {
			dirty[key] = now
		}
	}
	return dirty, firstErr
}

func workspaceWatchFingerprints(cfg APIConfig) (map[workspaceWatchKey]string, error) {
	projectsRoot := filepath.Join(workspaceRoot(cfg), "projects")
	projectsDir, err := openWorkspaceDirectory(cfg, projectsRoot, false)
	if os.IsNotExist(err) {
		return map[workspaceWatchKey]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer projectsDir.Close()
	projects, err := fs.ReadDir(projectsDir.FS(), ".")
	if err != nil {
		if os.IsNotExist(err) {
			return map[workspaceWatchKey]string{}, nil
		}
		return nil, err
	}
	out := make(map[workspaceWatchKey]string)
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		projectRoot := filepath.Join(projectsRoot, project.Name())
		projectDir, err := openWorkspaceDirectory(cfg, projectRoot, false)
		if err != nil {
			return nil, err
		}
		branches, err := fs.ReadDir(projectDir.FS(), ".")
		if err != nil {
			projectDir.Close()
			return nil, err
		}
		signals, err := workspaceWatchRestoreSignals(cfg, projectDir, project.Name(), branches)
		projectDir.Close()
		if err != nil {
			return nil, err
		}
		journalBranches := make(map[string]bool, len(signals))
		for key, fingerprint := range signals {
			out[key] = fingerprint
			journalBranches[safeWorkspaceID(key.Branch)] = true
		}
		for _, branch := range branches {
			if journalBranches[branch.Name()] || strings.HasPrefix(branch.Name(), ".restore-") || strings.HasPrefix(branch.Name(), ".backup-") {
				continue
			}
			if !branch.IsDir() {
				continue
			}
			branchRoot := filepath.Join(projectRoot, branch.Name())
			fingerprint, _, err := workspaceBranchFingerprint(cfg, branchRoot)
			if err != nil {
				return nil, err
			}
			out[workspaceWatchKey{ProjectID: project.Name(), Branch: branch.Name()}] = fingerprint
		}
	}
	return out, nil
}

// A restore can crash before the watcher observes a file change. Journals are
// discovery signals only; the service fence resolves SQL authority and recovers.
func workspaceWatchRestoreSignals(cfg APIConfig, dir *os.Root, projectDirectory string, entries []fs.DirEntry) (map[workspaceWatchKey]string, error) {
	signals := make(map[workspaceWatchKey]string)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".restore-state-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		raw, err := workspace.ReadFile(dir, name)
		if err != nil {
			return nil, err
		}
		if len(raw) > 64*1024 {
			return nil, errors.New("workspace restore journal too large")
		}
		var journal workspaceRestoreJournal
		if err := json.Unmarshal(raw, &journal); err != nil {
			return nil, err
		}
		if journal.ProjectID == "" || safeWorkspaceID(journal.ProjectID) != projectDirectory || journal.Branch == "" ||
			name != ".restore-state-"+safeWorkspaceID(journal.Branch)+".json" ||
			journal.Live != filepath.Base(workspaceProjectRoot(cfg, journal.ProjectID, journal.Branch)) ||
			!strings.HasPrefix(journal.Stage, ".restore-") || !strings.HasPrefix(journal.Backup, ".backup-") {
			return nil, errors.New("workspace restore journal target mismatch")
		}
		if _, err := uuid.Parse(strings.TrimPrefix(journal.Stage, ".restore-")); err != nil {
			return nil, errors.New("invalid restore stage identity")
		}
		if _, err := uuid.Parse(strings.TrimPrefix(journal.Backup, ".backup-")); err != nil {
			return nil, errors.New("invalid restore backup identity")
		}
		signals[workspaceWatchKey{ProjectID: projectDirectory, Branch: journal.Branch}] = "restore:" + digestBytes(raw)
	}
	return signals, nil
}

func workspaceBranchFingerprint(cfg APIConfig, root string) (string, bool, error) {
	inventory, err := workspaceBranchInventory(cfg, root)
	if err != nil {
		return "", false, err
	}
	raw, err := json.Marshal(inventory)
	return string(raw), len(inventory) > 0, err
}

func workspaceBranchInventory(cfg APIConfig, root string) (map[string]string, error) {
	paths, err := listWorkspaceMarkdownPaths(cfg, root)
	if err != nil {
		return nil, err
	}
	inventory := make(map[string]string, len(paths))
	if len(paths) == 0 {
		return inventory, nil
	}
	dir, err := openWorkspaceDirectory(cfg, root, false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	for _, rel := range paths {
		data, err := workspace.ReadFile(dir, filepath.FromSlash(rel))
		if err != nil {
			return nil, err
		}
		inventory[rel] = digestBytes(data)
	}
	return inventory, nil
}

func openWatcherStateParent(absolute string, create bool) (*os.Root, string, error) {
	base := filepath.Dir(absolute)
	relative := "."
	// Production state is a workspace-owned .kb sidecar; preserve standalone test/operator paths.
	if filepath.Base(base) == ".kb" {
		base, relative = filepath.Dir(base), ".kb"
	}
	root, err := workspace.OpenRoot(base, relative, create)
	return root, filepath.Base(absolute), err
}
