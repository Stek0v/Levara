package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"
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

type workspaceIndexJob struct {
	ID             string                   `json:"id"`
	IdempotencyKey string                   `json:"idempotency_key"`
	Status         workspaceIndexJobStatus  `json:"status"`
	Attempts       int                      `json:"attempts"`
	CreatedAt      string                   `json:"created_at"`
	UpdatedAt      string                   `json:"updated_at"`
	StartedAt      string                   `json:"started_at,omitempty"`
	FinishedAt     string                   `json:"finished_at,omitempty"`
	NextRunAt      string                   `json:"next_run_at,omitempty"`
	DeadLetterAt   string                   `json:"dead_letter_at,omitempty"`
	LastError      string                   `json:"last_error,omitempty"`
	Request        workspaceIndexJobPayload `json:"request"`
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

func beginWorkspaceIndexJob(cfg APIConfig, payload workspaceIndexJobPayload) (workspaceIndexJob, error) {
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
	idempotencyKey := workspaceIndexJobIdempotencyKey(payload)
	id := "job_" + idempotencyKey[:20]
	path := workspaceIndexJobPath(cfg, payload.ProjectID, payload.Branch, id)
	job, err := loadWorkspaceIndexJobPath(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return workspaceIndexJob{}, err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		job = workspaceIndexJob{
			ID:             id,
			IdempotencyKey: idempotencyKey,
			CreatedAt:      now,
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	job.Request = payload
	job.Status = workspaceIndexJobRunning
	job.Attempts++
	job.StartedAt = now
	job.FinishedAt = ""
	job.NextRunAt = ""
	job.DeadLetterAt = ""
	job.LastError = ""
	job.UpdatedAt = now
	if err := saveWorkspaceIndexJobPath(path, job); err != nil {
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
	err := saveWorkspaceIndexJobPath(workspaceIndexJobPath(cfg, job.Request.ProjectID, job.Request.Branch, job.ID), job)
	if err == nil {
		refreshWorkspaceOperationalMetrics(cfg)
	}
	if runErr != nil {
		return job, runErr
	}
	return job, err
}

func enqueueWorkspaceIndexJob(cfg APIConfig, payload workspaceIndexJobPayload) (workspaceIndexJob, error) {
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
	idempotencyKey := workspaceIndexJobIdempotencyKey(payload)
	id := "job_" + idempotencyKey[:20]
	path := workspaceIndexJobPath(cfg, payload.ProjectID, payload.Branch, id)
	if existing, err := loadWorkspaceIndexJobPath(path); err == nil {
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
	}
	if err := saveWorkspaceIndexJobPath(path, job); err != nil {
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
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []workspaceIndexJob{}, nil
		}
		return nil, err
	}
	var jobs []workspaceIndexJob
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		job, err := loadWorkspaceIndexJobPath(filepath.Join(dir, entry.Name()))
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

func enqueueWorkspaceIndexJobFromPayload(cfg APIConfig, payload workspaceIndexJobPayload) (workspaceIndexJob, error) {
	switch payload.Operation {
	case "reindex", "reconcile":
	default:
		return workspaceIndexJob{}, fmt.Errorf("unsupported job operation %q", payload.Operation)
	}
	return enqueueWorkspaceIndexJob(cfg, payload)
}

func retryWorkspaceIndexJob(ctx context.Context, cfg APIConfig, req workspaceRetryIndexJobRequest) (workspaceRetryIndexJobResponse, error) {
	branch := defaultBranch(req.Branch)
	if req.ProjectID == "" {
		return workspaceRetryIndexJobResponse{}, errors.New("project_id required")
	}
	if req.JobID == "" {
		return workspaceRetryIndexJobResponse{}, errors.New("job_id required")
	}
	job, err := loadWorkspaceIndexJobPath(workspaceIndexJobPath(cfg, req.ProjectID, branch, req.JobID))
	if err != nil {
		return workspaceRetryIndexJobResponse{}, err
	}
	job, result, err := runWorkspaceIndexJob(ctx, cfg, job, WorkspaceIndexWorkerOptions{
		MaxAttempts: job.Attempts + 1,
		Backoff:     0,
	})
	return workspaceRetryIndexJobResponse{Job: job, Result: result}, err
}

func runWorkspaceIndexJob(ctx context.Context, cfg APIConfig, job workspaceIndexJob, opts WorkspaceIndexWorkerOptions) (workspaceIndexJob, any, error) {
	opts = normalizeWorkspaceIndexWorkerOptions(opts)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	job.Status = workspaceIndexJobRunning
	job.Attempts++
	job.StartedAt = now
	job.FinishedAt = ""
	job.NextRunAt = ""
	job.UpdatedAt = now
	if err := saveWorkspaceIndexJobPath(workspaceIndexJobPath(cfg, job.Request.ProjectID, job.Request.Branch, job.ID), job); err != nil {
		return job, nil, err
	}
	refreshWorkspaceOperationalMetrics(cfg)

	var result any
	var runErr error
	switch job.Request.Operation {
	case "reindex":
		result, runErr = reindexWorkspaceMarkdownDirect(ctx, cfg, job.Request.toReindexRequest())
	case "reconcile":
		result, runErr = reconcileWorkspaceMarkdownDirect(ctx, cfg, job.Request.toReconcileRequest())
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
		if err := saveWorkspaceIndexJobPath(workspaceIndexJobPath(cfg, job.Request.ProjectID, job.Request.Branch, job.ID), job); err != nil {
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
	if err := saveWorkspaceIndexJobPath(workspaceIndexJobPath(cfg, job.Request.ProjectID, job.Request.Branch, job.ID), job); err != nil {
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
		if !workspaceIndexJobDue(job, now) {
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
	if job.Status != workspaceIndexJobRunning {
		return job, false, nil
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
	if err := saveWorkspaceIndexJobPath(workspaceIndexJobPath(cfg, recovered.Request.ProjectID, recovered.Request.Branch, recovered.ID), recovered); err != nil {
		return job, false, err
	}
	refreshWorkspaceOperationalMetrics(cfg)
	return recovered, true, nil
}

func listAllWorkspaceIndexJobs(cfg APIConfig) ([]workspaceIndexJob, error) {
	root := filepath.Join(workspaceRoot(cfg), ".kb", "jobs")
	projects, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []workspaceIndexJob{}, nil
		}
		return nil, err
	}
	var jobs []workspaceIndexJob
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		projectID := project.Name()
		branches, err := os.ReadDir(filepath.Join(root, project.Name()))
		if err != nil {
			return nil, err
		}
		for _, branch := range branches {
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

func workspaceIndexJobDue(job workspaceIndexJob, now time.Time) bool {
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

func loadWorkspaceIndexJobPath(path string) (workspaceIndexJob, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return workspaceIndexJob{}, err
	}
	var job workspaceIndexJob
	if err := json.Unmarshal(data, &job); err != nil {
		return workspaceIndexJob{}, err
	}
	return job, nil
}

func saveWorkspaceIndexJobPath(path string, job workspaceIndexJob) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(job, "", "  ")
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
