package http

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/metrics"
)

type workspaceOpsStatusRequest struct {
	ProjectID string `json:"project_id,omitempty"`
	Branch    string `json:"branch,omitempty"`
}

type workspaceOpsStatus struct {
	GeneratedAt string                  `json:"generated_at"`
	ProjectID   string                  `json:"project_id,omitempty"`
	Branch      string                  `json:"branch,omitempty"`
	Watcher     WorkspaceWatchStatus    `json:"watcher"`
	Jobs        workspaceOpsJobStatus   `json:"jobs"`
	Audit       workspaceOpsAuditStatus `json:"audit"`
}

type workspaceOpsJobStatus struct {
	Total            int            `json:"total"`
	ByStatus         map[string]int `json:"by_status"`
	DeadLetterCount  int            `json:"dead_letter_count"`
	MaxLagSeconds    float64        `json:"max_lag_seconds"`
	OldestPendingAt  string         `json:"oldest_pending_at,omitempty"`
	NewestUpdatedAt  string         `json:"newest_updated_at,omitempty"`
	OldestDeadLetter string         `json:"oldest_dead_letter_at,omitempty"`
}

type workspaceOpsAuditStatus struct {
	TotalEvents int            `json:"total_events"`
	Files       int            `json:"files"`
	BySource    map[string]int `json:"by_source"`
	ByResult    map[string]int `json:"by_result"`
	LastEventAt string         `json:"last_event_at,omitempty"`
}

func workspaceOpsStatusHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		req := workspaceOpsStatusRequest{
			ProjectID: c.Query("project_id"),
			Branch:    c.Query("branch"),
		}
		if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessRead); err != nil {
			return err
		}
		status, err := collectWorkspaceOpsStatus(cfg, req)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		refreshWorkspaceOperationalMetrics(cfg)
		if err := c.JSON(status); err != nil {
			return err
		}
		return sendWorkspaceProtectedResponse(c, cfg, c.UserContext(), uploadMetadataActor(c, cfg, c.UserContext()), req.ProjectID)
	}
}

func collectWorkspaceOpsStatus(cfg APIConfig, req workspaceOpsStatusRequest) (workspaceOpsStatus, error) {
	now := time.Now().UTC()
	jobs, err := workspaceOpsJobs(cfg, req)
	if err != nil {
		return workspaceOpsStatus{}, err
	}
	audit, err := workspaceOpsAudit(cfg, req)
	if err != nil {
		return workspaceOpsStatus{}, err
	}
	watch := workspaceWatchStatus(cfg)
	if req.ProjectID != "" {
		watch = workspaceProjectWatchStatus(watch, req.ProjectID, req.Branch)
	}
	return workspaceOpsStatus{
		GeneratedAt: now.Format(time.RFC3339Nano),
		ProjectID:   req.ProjectID,
		Branch:      req.Branch,
		Watcher:     watch,
		Jobs:        jobs,
		Audit:       audit,
	}, nil
}

func refreshWorkspaceOperationalMetrics(cfg APIConfig) {
	status, err := collectWorkspaceOpsStatus(cfg, workspaceOpsStatusRequest{})
	if err != nil {
		return
	}
	refreshWorkspaceOpsMetrics(status)
}

func refreshWorkspaceOpsMetrics(status workspaceOpsStatus) {
	for _, key := range []string{
		string(workspaceIndexJobPending),
		string(workspaceIndexJobRunning),
		string(workspaceIndexJobCompleted),
		string(workspaceIndexJobFailed),
		string(workspaceIndexJobDeadLetter),
	} {
		metrics.WorkspaceIndexJobs.WithLabelValues(key).Set(float64(status.Jobs.ByStatus[key]))
	}
	metrics.WorkspaceIndexJobMaxLagSeconds.Set(status.Jobs.MaxLagSeconds)
	metrics.WorkspaceIndexDeadLetters.Set(float64(status.Jobs.DeadLetterCount))
	metrics.WorkspaceWatcherPendingBranches.Set(float64(status.Watcher.PendingBranches))
	metrics.WorkspaceWatcherErrors.Set(float64(status.Watcher.ErrorCount))
	metrics.WorkspaceAuditStoredEvents.Set(float64(status.Audit.TotalEvents))
}

func workspaceOpsJobs(cfg APIConfig, req workspaceOpsStatusRequest) (workspaceOpsJobStatus, error) {
	var jobs []workspaceIndexJob
	var err error
	if req.ProjectID != "" && req.Branch != "" {
		jobs, err = listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{
			ProjectID: req.ProjectID,
			Branch:    defaultBranch(req.Branch),
		})
	} else {
		jobs, err = listAllWorkspaceIndexJobs(cfg)
		if req.ProjectID != "" {
			filtered := jobs[:0]
			for _, job := range jobs {
				if safeWorkspaceID(job.Request.ProjectID) == safeWorkspaceID(req.ProjectID) {
					filtered = append(filtered, job)
				}
			}
			jobs = filtered
		}
		if req.Branch != "" {
			filtered := jobs[:0]
			for _, job := range jobs {
				if defaultBranch(job.Request.Branch) == defaultBranch(req.Branch) {
					filtered = append(filtered, job)
				}
			}
			jobs = filtered
		}
	}
	if err != nil {
		return workspaceOpsJobStatus{}, err
	}
	now := time.Now().UTC()
	out := workspaceOpsJobStatus{ByStatus: map[string]int{}}
	for _, job := range jobs {
		status := string(job.Status)
		out.Total++
		out.ByStatus[status]++
		if job.Status == workspaceIndexJobDeadLetter {
			out.DeadLetterCount++
			out.OldestDeadLetter = olderTimeString(out.OldestDeadLetter, job.DeadLetterAt)
		}
		if job.Status == workspaceIndexJobPending || job.Status == workspaceIndexJobFailed {
			if created, ok := parseWorkspaceOpsTime(job.CreatedAt); ok {
				lag := now.Sub(created).Seconds()
				if lag > out.MaxLagSeconds {
					out.MaxLagSeconds = lag
				}
			}
			out.OldestPendingAt = olderTimeString(out.OldestPendingAt, job.CreatedAt)
		}
		out.NewestUpdatedAt = newerTimeString(out.NewestUpdatedAt, job.UpdatedAt)
	}
	return out, nil
}

func workspaceOpsAudit(cfg APIConfig, req workspaceOpsStatusRequest) (workspaceOpsAuditStatus, error) {
	dirs, err := workspaceOpsAuditDirs(cfg, req.ProjectID)
	if err != nil {
		return workspaceOpsAuditStatus{}, err
	}
	out := workspaceOpsAuditStatus{
		BySource: map[string]int{},
		ByResult: map[string]int{},
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return workspaceOpsAuditStatus{}, err
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
				continue
			}
			out.Files++
			if err := workspaceOpsReadAuditSummary(filepath.Join(dir, entry.Name()), req, &out); err != nil {
				return workspaceOpsAuditStatus{}, err
			}
		}
	}
	return out, nil
}

func workspaceOpsAuditDirs(cfg APIConfig, projectID string) ([]string, error) {
	if projectID != "" {
		return []string{workspaceAuditDir(cfg, projectID)}, nil
	}
	root := filepath.Join(workspaceRoot(cfg), ".kb", "audit")
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, err
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, filepath.Join(root, entry.Name()))
		}
	}
	return dirs, nil
}

func workspaceOpsReadAuditSummary(path string, req workspaceOpsStatusRequest, out *workspaceOpsAuditStatus) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var event workspaceAuditEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if req.ProjectID != "" && event.ProjectID != safeWorkspaceID(req.ProjectID) {
			continue
		}
		if req.Branch != "" && defaultBranch(event.Branch) != defaultBranch(req.Branch) {
			continue
		}
		out.TotalEvents++
		out.BySource[event.Source]++
		out.ByResult[event.Result]++
		out.LastEventAt = newerTimeString(out.LastEventAt, event.At)
	}
	return scanner.Err()
}

func olderTimeString(current, candidate string) string {
	if candidate == "" {
		return current
	}
	if current == "" {
		return candidate
	}
	ct, cok := parseWorkspaceOpsTime(current)
	nt, nok := parseWorkspaceOpsTime(candidate)
	if !cok {
		return candidate
	}
	if !nok {
		return current
	}
	if nt.Before(ct) {
		return candidate
	}
	return current
}

func newerTimeString(current, candidate string) string {
	if candidate == "" {
		return current
	}
	if current == "" {
		return candidate
	}
	ct, cok := parseWorkspaceOpsTime(current)
	nt, nok := parseWorkspaceOpsTime(candidate)
	if !cok {
		return candidate
	}
	if !nok {
		return current
	}
	if nt.After(ct) {
		return candidate
	}
	return current
}

func parseWorkspaceOpsTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err == nil {
		return t, true
	}
	t, err = time.Parse(time.RFC3339, s)
	return t, err == nil
}

func (h *mcpHandler) toolWorkspaceOpsStatus(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceOpsStatusRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessRead); err != nil {
		return workspaceMCPError(err)
	}
	status, err := collectWorkspaceOpsStatus(h.cfg, req)
	if err != nil {
		return workspaceMCPError(err)
	}
	refreshWorkspaceOperationalMetrics(h.cfg)
	return workspaceMCPJSON(status)
}

func workspaceProjectWatchStatus(watch WorkspaceWatchStatus, projectID, branch string) WorkspaceWatchStatus {
	out := WorkspaceWatchStatus{
		Enabled: watch.Enabled, StartedAt: watch.StartedAt, StoppedAt: watch.StoppedAt,
		IntervalMs: watch.IntervalMs, DebounceMs: watch.DebounceMs, GenerationPrefix: watch.GenerationPrefix,
		Branches: map[string]WorkspaceBranchWatchStatus{},
	}
	for key, status := range watch.Branches {
		if (projectID != "" && safeWorkspaceID(status.ProjectID) != safeWorkspaceID(projectID)) ||
			(branch != "" && safeWorkspaceID(defaultBranch(status.Branch)) != safeWorkspaceID(defaultBranch(branch))) {
			continue
		}
		out.Branches[key] = status
		out.WatchedBranches++
		if status.Pending {
			out.PendingBranches++
		}
		out.ScanCount += status.ScanCount
		out.ReconcileCount += status.ReconcileCount
		out.ErrorCount += status.ErrorCount
		out.LastScanAt = newerTimeString(out.LastScanAt, status.LastScanAt)
		out.LastChangeAt = newerTimeString(out.LastChangeAt, status.LastChangeAt)
		if status.LastReconcileAt != "" && newerTimeString(out.LastReconcileAt, status.LastReconcileAt) == status.LastReconcileAt {
			out.LastReconcileAt, out.LastGeneration = status.LastReconcileAt, status.LastGeneration
			out.LastProjectID, out.LastBranch = status.ProjectID, status.Branch
		}
		if status.LastError != "" && (out.LastError == "" || newerTimeString(out.LastErrorAt, status.LastErrorAt) == status.LastErrorAt) {
			out.LastErrorAt, out.LastError = status.LastErrorAt, status.LastError
		}
	}
	return out
}
