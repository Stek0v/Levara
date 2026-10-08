package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/workspace"
)

type workspaceIndexJobStatus string

const (
	workspaceIndexJobPending    workspaceIndexJobStatus = "pending"
	workspaceIndexJobRunning    workspaceIndexJobStatus = "running"
	workspaceIndexJobCompleted  workspaceIndexJobStatus = "completed"
	workspaceIndexJobFailed     workspaceIndexJobStatus = "failed"
	workspaceIndexJobDeadLetter workspaceIndexJobStatus = "dead_letter"
)

type workspaceIndexJobPayload struct {
	Operation          string   `json:"operation"`
	ProjectID          string   `json:"project_id"`
	Branch             string   `json:"branch"`
	Generation         string   `json:"generation"`
	Collection         string   `json:"collection,omitempty"`
	CommitHash         string   `json:"commit_hash,omitempty"`
	ChunkStrategy      string   `json:"chunk_strategy,omitempty"`
	MinChunkChars      int      `json:"min_chunk_chars,omitempty"`
	MaxChunkChars      int      `json:"max_chunk_chars,omitempty"`
	OverlapChars       int      `json:"overlap_chars,omitempty"`
	SnapToSentence     *bool    `json:"snap_to_sentence,omitempty"`
	ActivateGeneration bool     `json:"activate_generation"`
	Paths              []string `json:"paths"`
	Room               string   `json:"room,omitempty"`
	Tags               []string `json:"tags,omitempty"`
	DeleteMissing      bool     `json:"delete_missing,omitempty"`
}

type workspaceIndexJobAuthority struct {
	Actor   *accesspkg.MetadataActor `json:"actor,omitempty"`
	Service bool                     `json:"service,omitempty"`
}

// Private authority is persisted separately and omitted from public job JSON.
type workspaceIndexJobDisk struct {
	workspaceIndexJob
	Authority *workspaceIndexJobAuthority `json:"authority,omitempty"`
}

type workspaceIndexJob struct {
	authority              *workspaceIndexJobAuthority
	ID                     string                   `json:"id"`
	IdempotencyKey         string                   `json:"idempotency_key"`
	Status                 workspaceIndexJobStatus  `json:"status"`
	Attempts               int                      `json:"attempts"`
	CreatedAt              string                   `json:"created_at"`
	UpdatedAt              string                   `json:"updated_at"`
	StartedAt              string                   `json:"started_at,omitempty"`
	FinishedAt             string                   `json:"finished_at,omitempty"`
	NextRunAt              string                   `json:"next_run_at,omitempty"`
	DeadLetterAt           string                   `json:"dead_letter_at,omitempty"`
	LastError              string                   `json:"last_error,omitempty"`
	SupersededByGeneration string                   `json:"superseded_by_generation,omitempty"`
	Request                workspaceIndexJobPayload `json:"request"`
}

type workspaceIndexJobsRequest struct {
	ProjectID string `json:"project_id"`
	Branch    string `json:"branch"`
	Status    string `json:"status,omitempty"`
}

type workspaceRetryIndexJobRequest struct {
	ProjectID string `json:"project_id"`
	Branch    string `json:"branch"`
	JobID     string `json:"job_id"`
}

type workspaceRetryIndexJobResponse struct {
	Job    workspaceIndexJob `json:"job"`
	Result any               `json:"result,omitempty"`
}

type WorkspaceIndexWorkerOptions struct {
	Interval     time.Duration
	Backoff      time.Duration
	RunningLease time.Duration
	MaxAttempts  int
	Logf         func(format string, args ...any)
}

func beginWorkspaceIndexJob(cfg APIConfig, payload workspaceIndexJobPayload, authorities ...*workspaceIndexJobAuthority) (workspaceIndexJob, error) {
	payload.Branch = defaultBranch(payload.Branch)
	payload.Paths = workspaceSortedPaths(payload.Paths)
	if payload.ProjectID == "" {
		return workspaceIndexJob{}, errors.New("project_id required")
	}
	if payload.Generation == "" {
		return workspaceIndexJob{}, errors.New("generation required")
	}
	if payload.Operation == "" {
		return workspaceIndexJob{}, errors.New("operation required")
	}
	if err := validateWorkspaceCollection(payload.Collection); err != nil {
		return workspaceIndexJob{}, err
	}
	var authority *workspaceIndexJobAuthority
	if len(authorities) > 0 {
		authority = authorities[0]
	}
	idempotencyKey := workspaceIndexJobAuthorityKey(payload, authority)
	id := "job_" + idempotencyKey[:20]
	path := workspaceIndexJobPath(cfg, payload.ProjectID, payload.Branch, id)
	job, err := loadWorkspaceIndexJobConfined(cfg, path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return workspaceIndexJob{}, err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		job = workspaceIndexJob{
			ID:             id,
			IdempotencyKey: idempotencyKey,
			CreatedAt:      now,
			authority:      authority,
		}
	}
	if !sameWorkspaceJobAuthorityScope(job.authority, authority) {
		return workspaceIndexJob{}, errors.New("workspace job authority differs")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	job.Request = payload
	job.SupersededByGeneration = ""
	job.Status = workspaceIndexJobRunning
	job.Attempts++
	job.StartedAt = now
	job.FinishedAt = ""
	job.NextRunAt = ""
	job.DeadLetterAt = ""
	job.LastError = ""
	job.UpdatedAt = now
	if err := saveWorkspaceIndexJobConfined(cfg, path, job); err != nil {
		return workspaceIndexJob{}, err
	}
	refreshWorkspaceOperationalMetrics(cfg)
	return job, nil
}

func finishWorkspaceIndexJob(cfg APIConfig, job workspaceIndexJob, runErr error) (workspaceIndexJob, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if runErr != nil {
		job.Status = workspaceIndexJobFailed
		job.LastError = runErr.Error()
	} else {
		job.Status = workspaceIndexJobCompleted
		job.LastError = ""
	}
	job.FinishedAt = now
	job.NextRunAt = ""
	if runErr == nil {
		job.DeadLetterAt = ""
	}
	job.UpdatedAt = now
	err := saveWorkspaceIndexJobConfined(cfg, workspaceIndexJobPath(cfg, job.Request.ProjectID, job.Request.Branch, job.ID), job)
	if err == nil {
		refreshWorkspaceOperationalMetrics(cfg)
	}
	if runErr != nil {
		return job, runErr
	}
	return job, err
}

func enqueueWorkspaceIndexJob(cfg APIConfig, payload workspaceIndexJobPayload, authorities ...*workspaceIndexJobAuthority) (workspaceIndexJob, error) {
	payload.Branch = defaultBranch(payload.Branch)
	payload.Paths = workspaceSortedPaths(payload.Paths)
	if payload.ProjectID == "" {
		return workspaceIndexJob{}, errors.New("project_id required")
	}
	if payload.Generation == "" {
		return workspaceIndexJob{}, errors.New("generation required")
	}
	if payload.Operation == "" {
		return workspaceIndexJob{}, errors.New("operation required")
	}
	if err := validateWorkspaceCollection(payload.Collection); err != nil {
		return workspaceIndexJob{}, err
	}
	var authority *workspaceIndexJobAuthority
	if len(authorities) > 0 {
		authority = authorities[0]
	}
	idempotencyKey := workspaceIndexJobAuthorityKey(payload, authority)
	id := "job_" + idempotencyKey[:20]
	path := workspaceIndexJobPath(cfg, payload.ProjectID, payload.Branch, id)
	if existing, err := loadWorkspaceIndexJobConfined(cfg, path); err == nil {
		if !sameWorkspaceJobAuthorityScope(existing.authority, authority) {
			return workspaceIndexJob{}, errors.New("workspace job authority differs")
		}
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return workspaceIndexJob{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	job := workspaceIndexJob{
		ID:             id,
		IdempotencyKey: idempotencyKey,
		Status:         workspaceIndexJobPending,
		CreatedAt:      now,
		UpdatedAt:      now,
		Request:        payload,
		authority:      authority,
	}
	if err := saveWorkspaceIndexJobConfined(cfg, path, job); err != nil {
		return workspaceIndexJob{}, err
	}
	refreshWorkspaceOperationalMetrics(cfg)
	return job, nil
}

func listWorkspaceIndexJobs(cfg APIConfig, req workspaceIndexJobsRequest) ([]workspaceIndexJob, error) {
	branch := defaultBranch(req.Branch)
	if req.ProjectID == "" {
		return nil, errors.New("project_id required")
	}
	dir := workspaceIndexJobDir(cfg, req.ProjectID, branch)
	entries, err := workspaceJobReadDir(cfg, dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []workspaceIndexJob{}, nil
		}
		return nil, err
	}
	var jobs []workspaceIndexJob
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, errors.New("workspace job symlink rejected")
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		job, err := loadWorkspaceIndexJobConfined(cfg, filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		if req.Status != "" && string(job.Status) != req.Status {
			continue
		}
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].UpdatedAt == jobs[j].UpdatedAt {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].UpdatedAt > jobs[j].UpdatedAt
	})
	return jobs, nil
}

func enqueueWorkspaceIndexJobFromPayload(cfg APIConfig, payload workspaceIndexJobPayload, authorities ...*workspaceIndexJobAuthority) (workspaceIndexJob, error) {
	switch payload.Operation {
	case "reindex", "reconcile":
	default:
		return workspaceIndexJob{}, fmt.Errorf("unsupported job operation %q", payload.Operation)
	}
	return enqueueWorkspaceIndexJob(cfg, payload, authorities...)
}

func enqueueWorkspaceIndexJobFromPayloadAuthorized(ctx context.Context, cfg APIConfig, payload workspaceIndexJobPayload, actor accesspkg.MetadataActor) (workspaceIndexJob, error) {
	ctx, cancel, err := workspaceRequestContext(ctx, actor, 30*time.Second)
	if err != nil {
		return workspaceIndexJob{}, err
	}
	defer cancel()
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(payload.ProjectID, payload.Branch))
	defer unlock()
	release, err := beginWorkspaceEffectFence(ctx, cfg, actor, payload.ProjectID, workspaceAccessWrite)
	if err != nil {
		return workspaceIndexJob{}, err
	}
	defer release()
	return enqueueWorkspaceIndexJobFromPayload(cfg, payload, &workspaceIndexJobAuthority{Actor: &actor})
}

func sameWorkspaceJobAuthorityScope(a, b *workspaceIndexJobAuthority) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Service != b.Service {
		return false
	}
	if a.Actor == nil || b.Actor == nil {
		return a.Actor == b.Actor
	}
	return a.Actor.UserID == b.Actor.UserID && a.Actor.TenantID == b.Actor.TenantID
}

func workspaceIndexJobAuthorityKey(payload workspaceIndexJobPayload, authority *workspaceIndexJobAuthority) string {
	if authority == nil {
		return workspaceIndexJobIdempotencyKey(payload)
	}
	scope := struct {
		Payload          workspaceIndexJobPayload
		UserID, TenantID string
		Service          bool
	}{Payload: payload, Service: authority.Service}
	if authority.Actor != nil {
		scope.UserID, scope.TenantID = authority.Actor.UserID, authority.Actor.TenantID
	}
	data, _ := json.Marshal(scope)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// A live watcher is explicit server authority over its configured filesystem root.
func beginWorkspaceServiceFence(ctx context.Context, cfg APIConfig, key workspaceWatchKey) (workspaceWatchKey, func(), error) {
	if err := ctx.Err(); err != nil {
		return workspaceWatchKey{}, nil, err
	}
	key.Branch = defaultBranch(key.Branch)
	release := func() {}
	if cfg.RequireAuth {
		watcher := cfg.WorkspaceWatcher
		if watcher == nil {
			return workspaceWatchKey{}, nil, errors.New("workspace service authority is not enabled")
		}
		// Keep service admission through drain; status callbacks use a separate mutex.
		watcher.serviceMu.RLock()
		watcher.mu.RLock()
		running := watcher.running
		watcher.mu.RUnlock()
		if !running {
			watcher.serviceMu.RUnlock()
			return workspaceWatchKey{}, nil, errors.New("workspace service authority is not enabled")
		}
		policy, releaseSQL, err := (accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}).BeginTransferFence(ctx, GetDBProvider() == DBSQLite)
		if err != nil {
			watcher.serviceMu.RUnlock()
			return workspaceWatchKey{}, nil, err
		}
		release = func() {
			releaseSQL()
			watcher.serviceMu.RUnlock()
		}
		project, err := policy.ResolveWorkspaceDirectory(ctx, safeWorkspaceID(key.ProjectID))
		if err != nil || project == "" {
			release()
			if err == nil {
				err = errWorkspaceAccessDenied
			}
			return workspaceWatchKey{}, nil, err
		}
		key.ProjectID = project
	}
	releaseFS, err := workspace.LockProject(ctx, workspaceRoot(cfg), key.ProjectID)
	if err != nil {
		release()
		return workspaceWatchKey{}, nil, err
	}
	releaseAuthority := release
	release = func() { releaseFS(); releaseAuthority() }
	if err := recoverWorkspaceRestores(ctx, cfg, key.ProjectID); err != nil {
		release()
		return workspaceWatchKey{}, nil, err
	}
	manifest, exists, err := readWorkspaceManifest(cfg, key.ProjectID, key.Branch)
	if err == nil && exists {
		if cfg.RequireAuth && manifest.ProjectID != key.ProjectID {
			err = errors.New("legacy workspace project identity requires index migration")
		} else {
			key.ProjectID, key.Branch = manifest.ProjectID, defaultBranch(manifest.Branch)
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		release()
		return workspaceWatchKey{}, nil, err
	}
	return key, release, nil
}

func beginWorkspaceJobFence(ctx context.Context, cfg APIConfig, job workspaceIndexJob) (func(), error) {
	if job.authority != nil {
		if job.authority.Service {
			if job.authority.Actor != nil {
				return nil, errors.New("invalid workspace job authority")
			}
			key, release, err := beginWorkspaceServiceFence(ctx, cfg, workspaceWatchKey{ProjectID: job.Request.ProjectID, Branch: job.Request.Branch})
			if err != nil {
				return nil, err
			}
			if key.ProjectID != job.Request.ProjectID || key.Branch != defaultBranch(job.Request.Branch) {
				release()
				return nil, errors.New("workspace service job target identity changed")
			}
			return release, nil
		}
		if job.authority.Actor != nil {
			return beginWorkspaceEffectFence(ctx, cfg, *job.authority.Actor, job.Request.ProjectID, workspaceAccessWrite)
		}
	}
	if cfg.RequireAuth {
		return nil, errors.New("workspace job has no submitting authority")
	}
	return beginWorkspaceEffectFence(ctx, cfg, accesspkg.MetadataActor{TrustedLocal: true}, job.Request.ProjectID, workspaceAccessWrite)
}

func retryWorkspaceIndexJobAuthorized(ctx context.Context, cfg APIConfig, req workspaceRetryIndexJobRequest, actor accesspkg.MetadataActor) (workspaceRetryIndexJobResponse, error) {
	ctx, cancel, err := workspaceRequestContext(ctx, actor, 30*time.Second)
	if err != nil {
		return workspaceRetryIndexJobResponse{}, err
	}
	defer cancel()
	branch := defaultBranch(req.Branch)
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, branch))
	defer unlock()
	if req.ProjectID == "" || req.JobID == "" {
		return workspaceRetryIndexJobResponse{}, errors.New("project_id and job_id required")
	}
	job, err := loadWorkspaceIndexJobConfined(cfg, workspaceIndexJobPath(cfg, req.ProjectID, branch, req.JobID))
	if err != nil {
		return workspaceRetryIndexJobResponse{}, err
	}
	if job.Request.ProjectID != req.ProjectID || defaultBranch(job.Request.Branch) != branch || job.ID != req.JobID {
		return workspaceRetryIndexJobResponse{}, errors.New("workspace job target does not match request")
	}
	if err := validateWorkspaceCollection(job.Request.Collection); err != nil {
		return workspaceRetryIndexJobResponse{}, err
	}
	release, err := beginWorkspaceEffectFence(ctx, cfg, actor, job.Request.ProjectID, workspaceAccessWrite)
	if err != nil {
		return workspaceRetryIndexJobResponse{}, err
	}
	defer release()
	fresh, err := reloadWorkspaceIndexJobFenced(cfg, job)
	if err != nil {
		return workspaceRetryIndexJobResponse{}, err
	}
	if fresh.Status != job.Status && (fresh.Status == workspaceIndexJobCompleted || fresh.Status == workspaceIndexJobRunning) {
		return workspaceRetryIndexJobResponse{Job: fresh}, nil
	}
	job = fresh
	job, result, err := runWorkspaceIndexJobLocked(ctx, cfg, job, WorkspaceIndexWorkerOptions{MaxAttempts: job.Attempts + 1, Backoff: 0})
	return workspaceRetryIndexJobResponse{Job: job, Result: result}, err
}

func retryWorkspaceIndexJob(ctx context.Context, cfg APIConfig, req workspaceRetryIndexJobRequest) (workspaceRetryIndexJobResponse, error) {
	branch := defaultBranch(req.Branch)
	if req.ProjectID == "" {
		return workspaceRetryIndexJobResponse{}, errors.New("project_id required")
	}
	if req.JobID == "" {
		return workspaceRetryIndexJobResponse{}, errors.New("job_id required")
	}
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, branch))
	defer unlock()
	job, err := loadWorkspaceIndexJobConfined(cfg, workspaceIndexJobPath(cfg, req.ProjectID, branch, req.JobID))
	if err != nil {
		return workspaceRetryIndexJobResponse{}, err
	}
	if job.Request.ProjectID != req.ProjectID || defaultBranch(job.Request.Branch) != branch || job.ID != req.JobID {
		return workspaceRetryIndexJobResponse{}, errors.New("workspace job target does not match request")
	}
	job, result, err := runWorkspaceIndexJobLocked(ctx, cfg, job, WorkspaceIndexWorkerOptions{
		MaxAttempts: job.Attempts + 1,
		Backoff:     0,
	})
	return workspaceRetryIndexJobResponse{Job: job, Result: result}, err
}

func runWorkspaceIndexJob(ctx context.Context, cfg APIConfig, job workspaceIndexJob, opts WorkspaceIndexWorkerOptions) (workspaceIndexJob, any, error) {

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(job.Request.ProjectID, job.Request.Branch))
	defer unlock()
	fresh, err := loadWorkspaceIndexJobConfined(cfg, workspaceIndexJobPath(cfg, job.Request.ProjectID, job.Request.Branch, job.ID))
	if err != nil {
		return job, nil, err
	}
	if fresh.ID != job.ID || fresh.Request.ProjectID != job.Request.ProjectID || defaultBranch(fresh.Request.Branch) != defaultBranch(job.Request.Branch) {
		return fresh, nil, errors.New("workspace job target changed")
	}
	if !workspaceIndexJobDue(fresh, time.Now().UTC()) {
		return fresh, nil, nil
	}
	if fresh.authority != nil && fresh.authority.Actor != nil {
		bounded, cancel, err := workspaceRequestContext(ctx, *fresh.authority.Actor, 30*time.Second)
		if err != nil {
			return fresh, nil, err
		}
		defer cancel()
		ctx = bounded
	}
	release, err := beginWorkspaceJobFence(ctx, cfg, fresh)
	if err != nil {
		return fresh, nil, err
	}
	defer release()
	fresh, err = reloadWorkspaceIndexJobFenced(cfg, fresh)
	if err != nil {
		return job, nil, err
	}
	if !workspaceIndexJobDue(fresh, time.Now().UTC()) {
		return fresh, nil, nil
	}
	return runWorkspaceIndexJobLocked(ctx, cfg, fresh, opts)
}

func runWorkspaceIndexJobLocked(ctx context.Context, cfg APIConfig, job workspaceIndexJob, opts WorkspaceIndexWorkerOptions) (workspaceIndexJob, any, error) {
	if err := validateWorkspaceCollection(job.Request.Collection); err != nil {
		return job, nil, err
	}

	opts = normalizeWorkspaceIndexWorkerOptions(opts)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	job.SupersededByGeneration = ""
	job.Status = workspaceIndexJobRunning
	job.Attempts++
	job.StartedAt = now
	job.FinishedAt = ""
	job.NextRunAt = ""
	job.UpdatedAt = now
	if err := saveWorkspaceIndexJobConfined(cfg, workspaceIndexJobPath(cfg, job.Request.ProjectID, job.Request.Branch, job.ID), job); err != nil {
		return job, nil, err
	}
	refreshWorkspaceOperationalMetrics(cfg)

	var result any
	var runErr error
	switch job.Request.Operation {
	case "reindex":
		result, runErr = reindexWorkspaceMarkdownDirectLocked(ctx, cfg, job.Request.toReindexRequest())
	case "reconcile":
		result, runErr = reconcileWorkspaceMarkdownDirectLocked(ctx, cfg, job.Request.toReconcileRequest())
	default:
		runErr = fmt.Errorf("unsupported job operation %q", job.Request.Operation)
	}

	finished := time.Now().UTC()
	job.FinishedAt = finished.Format(time.RFC3339Nano)
	job.UpdatedAt = job.FinishedAt
	if runErr == nil {
		job.Status = workspaceIndexJobCompleted
		job.LastError = ""
		job.NextRunAt = ""
		job.DeadLetterAt = ""
		if err := saveWorkspaceIndexJobConfined(cfg, workspaceIndexJobPath(cfg, job.Request.ProjectID, job.Request.Branch, job.ID), job); err != nil {
			return job, result, err
		}
		refreshWorkspaceOperationalMetrics(cfg)
		return job, result, nil
	}

	job.LastError = runErr.Error()
	if job.Attempts >= opts.MaxAttempts {
		job.Status = workspaceIndexJobDeadLetter
		job.DeadLetterAt = job.FinishedAt
		job.NextRunAt = ""
	} else {
		job.Status = workspaceIndexJobFailed
		job.NextRunAt = finished.Add(workspaceIndexJobBackoff(opts.Backoff, job.Attempts)).UTC().Format(time.RFC3339Nano)
	}
	if err := saveWorkspaceIndexJobConfined(cfg, workspaceIndexJobPath(cfg, job.Request.ProjectID, job.Request.Branch, job.ID), job); err != nil {
		return job, result, err
	}
	refreshWorkspaceOperationalMetrics(cfg)
	return job, result, runErr
}

func workspaceIndexJobPayloadFromReindex(operation string, req workspaceReindexRequest, deleteMissing bool) workspaceIndexJobPayload {
	return workspaceIndexJobPayload{
		Operation:          operation,
		ProjectID:          req.ProjectID,
		Branch:             defaultBranch(req.Branch),
		Generation:         req.Generation,
		Collection:         req.Collection,
		CommitHash:         req.CommitHash,
		ChunkStrategy:      req.ChunkStrategy,
		MinChunkChars:      req.MinChunkChars,
		MaxChunkChars:      req.MaxChunkChars,
		OverlapChars:       req.OverlapChars,
		SnapToSentence:     req.SnapToSentence,
		ActivateGeneration: req.ActivateGeneration,
		Paths:              workspaceSortedPaths(req.Paths),
		Room:               req.Room,
		Tags:               append([]string(nil), req.Tags...),
		DeleteMissing:      deleteMissing,
	}
}

func (p workspaceIndexJobPayload) toReindexRequest() workspaceReindexRequest {
	return workspaceReindexRequest{
		ProjectID:          p.ProjectID,
		Branch:             p.Branch,
		Generation:         p.Generation,
		Collection:         p.Collection,
		CommitHash:         p.CommitHash,
		ChunkStrategy:      p.ChunkStrategy,
		MinChunkChars:      p.MinChunkChars,
		MaxChunkChars:      p.MaxChunkChars,
		OverlapChars:       p.OverlapChars,
		SnapToSentence:     p.SnapToSentence,
		ActivateGeneration: p.ActivateGeneration,
		Paths:              append([]string(nil), p.Paths...),
		Room:               p.Room,
		Tags:               append([]string(nil), p.Tags...),
	}
}

func (p workspaceIndexJobPayload) toReconcileRequest() workspaceReconcileRequest {
	return workspaceReconcileRequest{
		workspaceReindexRequest: p.toReindexRequest(),
		DeleteMissing:           p.DeleteMissing,
	}
}

func workspaceIndexJobIdempotencyKey(payload workspaceIndexJobPayload) string {
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func workspaceIndexJobDir(cfg APIConfig, projectID, branch string) string {
	return filepath.Join(workspaceRoot(cfg), ".kb", "jobs", safeWorkspaceID(projectID), safeWorkspaceID(defaultBranch(branch)))
}

func workspaceIndexJobPath(cfg APIConfig, projectID, branch, jobID string) string {
	return filepath.Join(workspaceIndexJobDir(cfg, projectID, branch), safeWorkspaceID(jobID)+".json")
}

func StartWorkspaceIndexWorker(ctx context.Context, cfg APIConfig, opts WorkspaceIndexWorkerOptions) func() {
	opts = normalizeWorkspaceIndexWorkerOptions(opts)
	wctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go workspaceIndexWorkerLoop(wctx, cfg, opts, done)
	return func() {
		cancel()
		<-done
	}
}

func normalizeWorkspaceIndexWorkerOptions(opts WorkspaceIndexWorkerOptions) WorkspaceIndexWorkerOptions {
	if opts.Interval <= 0 {
		opts.Interval = 2 * time.Second
	}
	if opts.Backoff <= 0 {
		opts.Backoff = 5 * time.Second
	}
	if opts.RunningLease <= 0 {
		opts.RunningLease = 30 * time.Second
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 3
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	return opts
}

func workspaceIndexWorkerLoop(ctx context.Context, cfg APIConfig, opts WorkspaceIndexWorkerOptions, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(opts.Interval)
	defer ticker.Stop()
	workspaceIndexWorkerTick(ctx, cfg, opts)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			workspaceIndexWorkerTick(ctx, cfg, opts)
		}
	}
}

func workspaceIndexWorkerTick(ctx context.Context, cfg APIConfig, opts WorkspaceIndexWorkerOptions) {
	opts = normalizeWorkspaceIndexWorkerOptions(opts)
	jobs, err := listAllWorkspaceIndexJobs(cfg)
	if err != nil {
		opts.Logf("[workspace-index-worker] list jobs failed: %v", err)
		return
	}
	now := time.Now().UTC()
	for i := range jobs {
		recovered, changed, err := recoverWorkspaceRunningJob(cfg, jobs[i], now, opts)
		if err != nil {
			opts.Logf("[workspace-index-worker] recover running job %s failed: %v", jobs[i].ID, err)
			continue
		}
		if changed {
			opts.Logf("[workspace-index-worker] recovered orphaned running job %s", recovered.ID)
			jobs[i] = recovered
		}
	}
	for _, job := range jobs {
		if !workspaceIndexJobDue(job, time.Now().UTC()) {
			continue
		}
		done, _, err := runWorkspaceIndexJob(ctx, cfg, job, opts)
		if err != nil {
			opts.Logf("[workspace-index-worker] job %s failed attempt %d/%d: %v", done.ID, done.Attempts, opts.MaxAttempts, err)
			continue
		}
		opts.Logf("[workspace-index-worker] job %s completed", done.ID)
	}
}

func recoverWorkspaceRunningJob(cfg APIConfig, job workspaceIndexJob, now time.Time, opts WorkspaceIndexWorkerOptions) (workspaceIndexJob, bool, error) {
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(job.Request.ProjectID, job.Request.Branch))
	defer unlock()
	fresh, err := loadWorkspaceIndexJobConfined(cfg, workspaceIndexJobPath(cfg, job.Request.ProjectID, job.Request.Branch, job.ID))
	if err != nil {
		return job, false, err
	}
	if fresh.ID != job.ID || fresh.Request.ProjectID != job.Request.ProjectID || defaultBranch(fresh.Request.Branch) != defaultBranch(job.Request.Branch) {
		return fresh, false, errors.New("workspace job target changed")
	}
	job = fresh
	if job.Status != workspaceIndexJobRunning {
		return job, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if job.authority != nil && job.authority.Actor != nil {
		bounded, stop, err := workspaceRequestContext(ctx, *job.authority.Actor, 30*time.Second)
		if err != nil {
			return job, false, err
		}
		defer stop()
		ctx = bounded
	}
	release, err := beginWorkspaceJobFence(ctx, cfg, job)
	if err != nil {
		return job, false, err
	}
	defer release()
	fresh, err = reloadWorkspaceIndexJobFenced(cfg, job)
	if err != nil {
		return job, false, err
	}
	job = fresh
	if job.Status != workspaceIndexJobRunning {
		return job, false, nil
	}
	if err := ctx.Err(); err != nil {
		return job, false, err
	}
	if actual := time.Now().UTC(); actual.After(now) {
		now = actual
	}
	staleAt := job.UpdatedAt
	if staleAt == "" {
		staleAt = job.StartedAt
	}
	if staleAt == "" {
		staleAt = job.CreatedAt
	}
	lastSeen, err := parseWorkspaceIndexJobTime(staleAt)
	if err != nil {
		lastSeen = time.Time{}
	}
	if !lastSeen.IsZero() && lastSeen.Add(opts.RunningLease).After(now) {
		return job, false, nil
	}
	recovered := job
	recovered.Status = workspaceIndexJobFailed
	recovered.FinishedAt = now.Format(time.RFC3339Nano)
	recovered.UpdatedAt = recovered.FinishedAt
	recovered.NextRunAt = recovered.FinishedAt
	recovered.DeadLetterAt = ""
	if recovered.LastError == "" {
		recovered.LastError = "worker restart recovery: job was left running without completion"
	}
	if err := saveWorkspaceIndexJobConfined(cfg, workspaceIndexJobPath(cfg, recovered.Request.ProjectID, recovered.Request.Branch, recovered.ID), recovered); err != nil {
		return job, false, err
	}
	refreshWorkspaceOperationalMetrics(cfg)
	return recovered, true, nil
}

func listAllWorkspaceIndexJobs(cfg APIConfig) ([]workspaceIndexJob, error) {
	root := filepath.Join(workspaceRoot(cfg), ".kb", "jobs")
	projects, err := workspaceJobReadDir(cfg, root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []workspaceIndexJob{}, nil
		}
		return nil, err
	}
	var jobs []workspaceIndexJob
	for _, project := range projects {
		if project.Type()&os.ModeSymlink != 0 {
			return nil, errors.New("workspace job namespace symlink rejected")
		}
		if !project.IsDir() {
			continue
		}
		projectID := project.Name()
		branches, err := workspaceJobReadDir(cfg, filepath.Join(root, project.Name()))
		if err != nil {
			return nil, err
		}
		for _, branch := range branches {
			if branch.Type()&os.ModeSymlink != 0 {
				return nil, errors.New("workspace job namespace symlink rejected")
			}
			if !branch.IsDir() {
				continue
			}
			branchJobs, err := listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{
				ProjectID: projectID,
				Branch:    branch.Name(),
			})
			if err != nil {
				return nil, err
			}
			jobs = append(jobs, branchJobs...)
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt == jobs[j].CreatedAt {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].CreatedAt < jobs[j].CreatedAt
	})
	return jobs, nil
}

// Called only after publication, under the caller's verified project effect fence.
// Preserve failed history rather than pretending that a different job completed it.
func supersedeWorkspaceIndexFailures(cfg APIConfig, projectID, branch, generation string) error {
	if projectID == "" || generation == "" {
		return errors.New("workspace failure supersession requires project and generation")
	}
	branch = defaultBranch(branch)
	dir := workspaceIndexJobDir(cfg, projectID, branch)
	entries, err := workspaceJobReadDir(cfg, dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("workspace job symlink rejected")
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		job, err := loadWorkspaceIndexJobConfined(cfg, path)
		if err != nil {
			return err
		}
		if job.ID == "" || entry.Name() != filepath.Base(workspaceIndexJobPath(cfg, projectID, branch, job.ID)) || job.Request.ProjectID != projectID || defaultBranch(job.Request.Branch) != branch {
			return errors.New("workspace failure supersession target mismatch")
		}
		if job.SupersededByGeneration != "" || (job.Status != workspaceIndexJobFailed && job.Status != workspaceIndexJobDeadLetter) {
			continue
		}
		job.SupersededByGeneration = generation
		if err := saveWorkspaceIndexJobConfined(cfg, path, job); err != nil {
			return err
		}
	}
	return nil
}

func workspaceIndexJobDue(job workspaceIndexJob, now time.Time) bool {
	if job.SupersededByGeneration != "" && (job.Status == workspaceIndexJobFailed || job.Status == workspaceIndexJobDeadLetter) {
		return false
	}
	switch job.Status {
	case workspaceIndexJobPending:
		return true
	case workspaceIndexJobFailed:
		if job.NextRunAt == "" {
			return false
		}
		next, err := time.Parse(time.RFC3339Nano, job.NextRunAt)
		if err != nil {
			next, err = time.Parse(time.RFC3339, job.NextRunAt)
		}
		return err != nil || !next.After(now)
	default:
		return false
	}
}

func parseWorkspaceIndexJobTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, errors.New("empty time")
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err == nil {
		return t.UTC(), nil
	}
	t, err = time.Parse(time.RFC3339, value)
	if err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, err
}

func workspaceIndexJobBackoff(base time.Duration, attempts int) time.Duration {
	if base <= 0 {
		return 0
	}
	if attempts <= 1 {
		return base
	}
	mult := 1 << min(attempts-1, 6)
	return time.Duration(mult) * base
}

// Legacy path entrypoints are retained for fixture setup only; production uses cfg-anchored helpers.
func loadWorkspaceIndexJobPath(path string) (workspaceIndexJob, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return workspaceIndexJob{}, err
	}
	var disk workspaceIndexJobDisk
	if err := json.Unmarshal(data, &disk); err != nil {
		return workspaceIndexJob{}, err
	}
	job := disk.workspaceIndexJob
	job.authority = disk.Authority
	return job, nil
}

func saveWorkspaceIndexJobPath(path string, job workspaceIndexJob) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(workspaceIndexJobDisk{workspaceIndexJob: job, Authority: job.authority}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func workspaceSortedPaths(paths []string) []string {
	out := append([]string(nil), paths...)
	for i, p := range out {
		out[i] = filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))
	}
	sort.Strings(out)
	return out
}

func workspaceJobStatusSummary(jobs []workspaceIndexJob) map[string]int {
	out := map[string]int{}
	for _, job := range jobs {
		out[string(job.Status)]++
	}
	return out
}

// workspaceJobRoot binds persisted metadata to the configured workspace anchor.
func workspaceJobRoot(cfg APIConfig, path string, create bool) (*os.Root, error) {
	relative, err := filepath.Rel(workspaceRoot(cfg), filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	return workspace.OpenRoot(workspaceRoot(cfg), relative, create)
}

func workspaceJobReadDir(cfg APIConfig, path string) ([]fs.DirEntry, error) {
	root, err := workspaceJobRoot(cfg, filepath.Join(path, ".entries"), false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return dir.ReadDir(-1)
}

func loadWorkspaceIndexJobConfined(cfg APIConfig, path string) (workspaceIndexJob, error) {
	root, err := workspaceJobRoot(cfg, path, false)
	if err != nil {
		return workspaceIndexJob{}, err
	}
	defer root.Close()
	data, err := workspace.ReadFile(root, filepath.Base(path))
	if err != nil {
		return workspaceIndexJob{}, err
	}
	var disk workspaceIndexJobDisk
	if err := json.Unmarshal(data, &disk); err != nil {
		return workspaceIndexJob{}, err
	}
	job := disk.workspaceIndexJob
	job.authority = disk.Authority
	return job, nil
}

func saveWorkspaceIndexJobConfined(cfg APIConfig, path string, job workspaceIndexJob) error {
	root, err := workspaceJobRoot(cfg, path, true)
	if err != nil {
		return err
	}
	defer root.Close()
	data, err := json.MarshalIndent(workspaceIndexJobDisk{workspaceIndexJob: job, Authority: job.authority}, "", "  ")
	if err != nil {
		return err
	}
	// Status persistence intentionally survives cancellation of the indexing attempt.
	return workspace.WriteFile(context.Background(), root, filepath.Base(path), append(data, '\n'), 0644)
}

// Reload after acquiring the cross-process fence: a queued admission snapshot
// cannot authorize changed request/credentials or replay another worker's result.
func reloadWorkspaceIndexJobFenced(cfg APIConfig, expected workspaceIndexJob) (workspaceIndexJob, error) {
	fresh, err := loadWorkspaceIndexJobConfined(cfg, workspaceIndexJobPath(cfg, expected.Request.ProjectID, expected.Request.Branch, expected.ID))
	if err != nil {
		return fresh, err
	}
	type admitted struct {
		ID             string
		IdempotencyKey string
		Request        workspaceIndexJobPayload
		Authority      *workspaceIndexJobAuthority
	}
	oldProof, err := json.Marshal(admitted{expected.ID, expected.IdempotencyKey, expected.Request, expected.authority})
	if err != nil {
		return fresh, err
	}
	newProof, err := json.Marshal(admitted{fresh.ID, fresh.IdempotencyKey, fresh.Request, fresh.authority})
	if err != nil {
		return fresh, err
	}
	if string(oldProof) != string(newProof) {
		return fresh, errors.New("workspace job admission changed while waiting")
	}
	return fresh, nil
}
