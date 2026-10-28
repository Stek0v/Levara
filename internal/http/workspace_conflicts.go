// workspace_conflicts.go — transport adapters. The conflict-report core
// lives in internal/api/workspaceconflicts (Phase-2 workspace campaign,
// leaf 2; code-modernization brief §3); this file keeps the fiber handler,
// the MCP tool method, the ACL guards and the watcher/jobs wiring, and
// delegates the computation with the original package-local signature.
package http

import (
	"context"
	"errors"
	"os"

	"github.com/gofiber/fiber/v2"

	"github.com/stek0v/levara/internal/api/workspaceconflicts"
)

type workspaceConflictRequest = workspaceconflicts.Request
type workspaceConflictPath = workspaceconflicts.PathState

type workspaceConflictResponse struct {
	workspaceconflicts.Response
}

// workspaceConflicts keeps the original signature; it maps watcher/jobs
// facts onto the extracted core's Deps.
func workspaceConflicts(ctx context.Context, cfg APIConfig, req workspaceConflictRequest) (workspaceConflictResponse, error) {
	watch := workspaceWatchStatus(cfg)
	branchStatus := workspaceFreshnessBranchStatus(req.ProjectID, defaultBranch(req.Branch), watch)
	jobsByStatus := func(ctx context.Context, projectID, branch string) (map[string]int, error) {
		jobs, err := listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{ProjectID: projectID, Branch: branch})
		if err != nil {
			return nil, err
		}
		return workspaceJobStatusSummary(jobs), nil
	}
	resp, err := workspaceconflicts.Core(ctx, workspaceconflicts.Deps{
		Root:             workspaceRoot(cfg),
		ListIndexJobs:    jobsByStatus,
		JobFailedStatus:  string(workspaceIndexJobFailed),
		JobDeadLetter:    string(workspaceIndexJobDeadLetter),
		WatcherPending:   branchStatus.Pending,
		WatcherLastError: branchStatus.LastError,
	}, req)
	if err != nil {
		return workspaceConflictResponse{}, err
	}
	out := workspaceConflictResponse{Response: resp}
	return out, nil
}

func workspaceConflictsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		req := workspaceConflictRequest{
			ProjectID: c.Query("project_id"),
			Branch:    c.Query("branch", "main"),
		}
		if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessRead); err != nil {
			return err
		}
		resp, err := workspaceConflicts(c.UserContext(), cfg, req)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err := c.JSON(resp); err != nil {
			return err
		}
		return sendWorkspaceProtectedResponse(c, cfg, c.UserContext(), uploadMetadataActor(c, cfg, c.UserContext()), req.ProjectID)
	}
}

func (h *mcpHandler) toolWorkspaceConflicts(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceConflictRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessRead); err != nil {
		return workspaceMCPError(err)
	}
	resp, err := workspaceConflicts(ctx, h.cfg, req)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return workspaceMCPError(err)
		}
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}
