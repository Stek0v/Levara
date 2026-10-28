// ops.go — workspace operational-status aggregation.
//
// Extracted verbatim from internal/http/workspace_ops.go (Phase-2
// workspace campaign, leaf 4; code-modernization brief §3). The audit
// JSONL scanner and the watcher-status projection are pure and live here;
// the jobs aggregation stays in internal/http (it sits on an unexported
// job type) and feeds facts in through a collector callback.
package workspaceops

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"

	"github.com/stek0v/levara/internal/api/workspacesvc"
)

// AuditEvent is one workspace audit JSONL record. Defined here to mirror
// the internal/http type shape — the scanner only reads the wire fields.
type AuditEvent struct {
	Scope     any             `json:"-"`
	ID        string          `json:"id"`
	At        string          `json:"at"`
	Source    string          `json:"source"`
	Operation string          `json:"operation"`
	ProjectID string          `json:"project_id"`
	Branch    string          `json:"branch,omitempty"`
	UserID    string          `json:"user_id,omitempty"`
	Access    string          `json:"access,omitempty"`
	Result    string          `json:"result"`
	Status    int             `json:"status,omitempty"`
	Error     string          `json:"error,omitempty"`
	Metadata  map[string]any  `json:"metadata,omitempty"`
}

// Request scopes the ops snapshot.
type Request struct {
	ProjectID string `json:"project_id,omitempty"`
	Branch    string `json:"branch,omitempty"`
}

// AuditStatus is the aggregated audit-file summary.
type AuditStatus struct {
	TotalEvents int            `json:"total_events"`
	Files       int            `json:"files"`
	BySource    map[string]int `json:"by_source"`
	ByResult    map[string]int `json:"by_result"`
	LastEventAt string         `json:"last_event_at,omitempty"`
}

// AuditDirs resolves the per-project audit directory or, with an empty
// project, every project directory under the root's .kb/audit.
func AuditDirs(root, projectID string) ([]string, error) {
	if projectID != "" {
		return []string{filepath.Join(root, ".kb", "audit", workspacesvc.SafeID(projectID))}, nil
	}
	auditRoot := filepath.Join(root, ".kb", "audit")
	entries, err := os.ReadDir(auditRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, err
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, filepath.Join(auditRoot, entry.Name()))
		}
	}
	return dirs, nil
}

// ScanAuditFiles aggregates the .jsonl audit files under dirs, filtered by
// the request scope. decodeEvent lets the caller keep its own event type.
func ScanAuditFiles(dirs []string, req Request, decodeEvent func([]byte) (projectID, branch, at, source, result string, ok bool)) (AuditStatus, error) {
	out := AuditStatus{
		BySource: map[string]int{},
		ByResult: map[string]int{},
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return AuditStatus{}, err
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
				continue
			}
			out.Files++
			if err := scanAuditFile(filepath.Join(dir, entry.Name()), req, decodeEvent, &out); err != nil {
				return AuditStatus{}, err
			}
		}
	}
	return out, nil
}

func scanAuditFile(path string, req Request, decodeEvent func([]byte) (projectID, branch, at, source, result string, ok bool), out *AuditStatus) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		projectID, branch, at, source, result, ok := decodeEvent(scanner.Bytes())
		if !ok {
			continue
		}
		if req.ProjectID != "" && projectID != workspacesvc.SafeID(req.ProjectID) {
			continue
		}
		if req.Branch != "" && workspacesvc.DefaultBranch(branch) != workspacesvc.DefaultBranch(req.Branch) {
			continue
		}
		out.TotalEvents++
		out.BySource[source]++
		out.ByResult[result]++
		out.LastEventAt = workspacesvc.NewerTimeString(out.LastEventAt, at)
	}
	return scanner.Err()
}

// WatcherSnapshot is a neutral mirror of the watcher status snapshot
// (internal/http WorkspaceWatchStatus shape).
type WatcherSnapshot struct {
	Enabled          bool                      `json:"enabled"`
	StartedAt        string                    `json:"started_at,omitempty"`
	StoppedAt        string                    `json:"stopped_at,omitempty"`
	LastScanAt       string                    `json:"last_scan_at,omitempty"`
	LastChangeAt     string                    `json:"last_change_at,omitempty"`
	LastReconcileAt  string                    `json:"last_reconcile_at,omitempty"`
	LastErrorAt      string                    `json:"last_error_at,omitempty"`
	LastError        string                    `json:"last_error,omitempty"`
	LastGeneration   string                    `json:"last_generation,omitempty"`
	LastProjectID    string                    `json:"last_project_id,omitempty"`
	LastBranch       string                    `json:"last_branch,omitempty"`
	ScanCount        int64                     `json:"scan_count"`
	ReconcileCount   int64                     `json:"reconcile_count"`
	ErrorCount       int64                     `json:"error_count"`
	WatchedBranches  int                       `json:"watched_branches"`
	PendingBranches  int                       `json:"pending_branches"`
	IntervalMs       int64                     `json:"interval_ms,omitempty"`
	DebounceMs       int64                     `json:"debounce_ms,omitempty"`
	GenerationPrefix string                    `json:"generation_prefix,omitempty"`
	Branches         map[string]BranchSnapshot `json:"branches,omitempty"`
}

// BranchSnapshot mirrors WorkspaceBranchWatchStatus.
type BranchSnapshot struct {
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

// ProjectWatchStatus filters a watcher snapshot to one project/branch,
// re-aggregating the counters over the surviving branches.
func ProjectWatchStatus(
	watch WatcherSnapshot, projectID, branch string, newerTimeString func(current, candidate string) string,
) WatcherSnapshot {
	out := WatcherSnapshot{
		Enabled: watch.Enabled, StartedAt: watch.StartedAt, StoppedAt: watch.StoppedAt,
		IntervalMs: watch.IntervalMs, DebounceMs: watch.DebounceMs, GenerationPrefix: watch.GenerationPrefix,
		Branches: map[string]BranchSnapshot{},
	}
	for key, status := range watch.Branches {
		if (projectID != "" && workspacesvc.SafeID(status.ProjectID) != workspacesvc.SafeID(projectID)) ||
			(branch != "" && workspacesvc.SafeID(workspacesvc.DefaultBranch(status.Branch)) != workspacesvc.SafeID(workspacesvc.DefaultBranch(branch))) {
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
