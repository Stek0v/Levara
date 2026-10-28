// workspace_ops.go — transport adapters. The audit JSONL scanner and the
// watcher projection live in internal/api/workspaceops (Phase-2 workspace
// campaign, leaf 4); the jobs aggregation stays here (unexported job
// type); internal/http keeps the handlers and metrics refresh.
package http

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/api/workspaceops"
	"github.com/stek0v/levara/internal/api/workspacesvc"
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
	dirs, err := workspaceops.AuditDirs(workspaceRoot(cfg), req.ProjectID)
	if err != nil {
		return workspaceOpsAuditStatus{}, err
	}
	decode := func(line []byte) (projectID, branch, at, source, result string, ok bool) {
		var event workspaceAuditEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return "", "", "", "", "", false
		}
		return event.ProjectID, event.Branch, event.At, event.Source, event.Result, true
	}
	audit, err := workspaceops.ScanAuditFiles(dirs, workspaceops.Request{ProjectID: req.ProjectID, Branch: req.Branch}, decode)
	if err != nil {
		return workspaceOpsAuditStatus{}, err
	}
	return workspaceOpsAuditStatus(audit), nil
}

func olderTimeString(current, candidate string) string {
	return workspacesvc.OlderTimeString(current, candidate)
}

func parseWorkspaceOpsTime(s string) (time.Time, bool) {
	return workspacesvc.ParseOpsTime(s)
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
	snap := workspaceops.WatcherSnapshot{
		Enabled: watch.Enabled, StartedAt: watch.StartedAt, StoppedAt: watch.StoppedAt,
		IntervalMs: watch.IntervalMs, DebounceMs: watch.DebounceMs, GenerationPrefix: watch.GenerationPrefix,
		Branches: map[string]workspaceops.BranchSnapshot{},
	}
	for key, status := range watch.Branches {
		snap.Branches[key] = workspaceops.BranchSnapshot(status)
	}
	out := workspaceops.ProjectWatchStatus(snap, projectID, branch, newerTimeString)
	result := WorkspaceWatchStatus{
		Enabled: out.Enabled, StartedAt: out.StartedAt, StoppedAt: out.StoppedAt,
		LastScanAt: out.LastScanAt, LastChangeAt: out.LastChangeAt,
		LastReconcileAt: out.LastReconcileAt, LastErrorAt: out.LastErrorAt,
		LastError: out.LastError, LastGeneration: out.LastGeneration,
		LastProjectID: out.LastProjectID, LastBranch: out.LastBranch,
		ScanCount: out.ScanCount, ReconcileCount: out.ReconcileCount, ErrorCount: out.ErrorCount,
		WatchedBranches: out.WatchedBranches, PendingBranches: out.PendingBranches,
		IntervalMs: out.IntervalMs, DebounceMs: out.DebounceMs, GenerationPrefix: out.GenerationPrefix,
		Branches: map[string]WorkspaceBranchWatchStatus{},
	}
	for key, status := range out.Branches {
		result.Branches[key] = WorkspaceBranchWatchStatus(status)
	}
	return result
}
