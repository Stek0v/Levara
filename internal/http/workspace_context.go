package http

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sort"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/workspace"
)

type workspaceContextRequest struct {
	ProjectID string `json:"project_id,omitempty"`
	Branch    string `json:"branch,omitempty"`
}

type workspaceContextResponse struct {
	Projects              []workspaceProjectContext `json:"projects"`
	DefaultProjectID      string                    `json:"default_project_id,omitempty"`
	RecommendedSearchType string                    `json:"recommended_search_type"`
	ExactReadRequired     bool                      `json:"exact_read_required"`
	Watcher               WorkspaceWatchStatus      `json:"watcher"`
	Guidance              []string                  `json:"guidance,omitempty"`
}

type workspaceProjectContext struct {
	ProjectID string                       `json:"project_id"`
	Access    workspaceAccessCheckResponse `json:"access"`
	Branches  []workspaceBranchContext     `json:"branches"`
	Guidance  []string                     `json:"guidance,omitempty"`
}

type workspaceBranchContext struct {
	Branch               string                     `json:"branch"`
	ManifestPath         string                     `json:"manifest_path,omitempty"`
	ManifestExists       bool                       `json:"manifest_exists"`
	ActiveGeneration     string                     `json:"active_generation,omitempty"`
	ActiveCollection     string                     `json:"active_collection,omitempty"`
	LastIndexedAt        string                     `json:"last_indexed_at,omitempty"`
	ActiveChunkCount     int                        `json:"active_chunk_count"`
	ActivePathCount      int                        `json:"active_path_count"`
	ContextArtifactCount int                        `json:"context_artifact_count,omitempty"`
	Watcher              WorkspaceBranchWatchStatus `json:"watcher"`
	JobsByStatus         map[string]int             `json:"jobs_by_status,omitempty"`
	InitializationPath   []string                   `json:"initialization_path,omitempty"`
	Error                string                     `json:"error,omitempty"`
}

func workspaceContextHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		req := workspaceContextRequest{
			ProjectID: c.Query("project_id"),
			Branch:    c.Query("branch"),
		}
		userID, _ := c.Locals("user_id").(string)
		resp, err := buildWorkspaceContext(c.UserContext(), cfg, userID, req)
		if err != nil {
			if errors.Is(err, errWorkspaceAccessDenied) {
				return fiber.NewError(fiber.StatusForbidden, errWorkspaceAccessDenied.Error())
			}
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(resp)
	}
}

func buildWorkspaceContext(ctx context.Context, cfg APIConfig, userID string, req workspaceContextRequest) (workspaceContextResponse, error) {
	projectIDs, err := workspaceContextProjectIDs(ctx, cfg, userID, req.ProjectID)
	if err != nil {
		return workspaceContextResponse{}, err
	}
	watch := workspaceWatchStatus(cfg)
	resp := workspaceContextResponse{
		RecommendedSearchType: "HYBRID",
		ExactReadRequired:     true,
		Watcher:               watch,
	}
	for _, projectID := range projectIDs {
		access, err := workspaceAccessCheck(ctx, cfg.DB, userID, projectID, workspaceAccessRead, "")
		if err != nil {
			return workspaceContextResponse{}, err
		}
		if !access.Allowed {
			if req.ProjectID != "" {
				return workspaceContextResponse{}, errWorkspaceAccessDenied
			}
			continue
		}
		project := workspaceProjectContext{
			ProjectID: projectID,
			Access:    access,
			Branches:  workspaceContextBranches(ctx, cfg, projectID, req.Branch, watch),
		}
		if len(project.Branches) == 0 {
			project.Guidance = workspaceContextInitializationPath(projectID, defaultBranch(req.Branch))
		}
		resp.Projects = append(resp.Projects, project)
	}
	if len(resp.Projects) > 0 {
		resp.DefaultProjectID = resp.Projects[0].ProjectID
	}
	if len(resp.Projects) == 0 {
		resp.Guidance = []string{
			"Create or share a workspace project.",
			"Write markdown with workspace_write, then run workspace_reconcile to publish an active generation.",
		}
	}
	return resp, nil
}

func workspaceContextProjectIDs(ctx context.Context, cfg APIConfig, userID, explicitProjectID string) ([]string, error) {
	if explicitProjectID != "" {
		return []string{explicitProjectID}, nil
	}
	seen := map[string]struct{}{}
	for _, projectID := range workspaceLocalProjectIDs(cfg) {
		seen[projectID] = struct{}{}
	}
	if cfg.DB != nil && userID != "" {
		dbIDs, err := workspaceDBProjectIDs(ctx, cfg.DB, userID)
		if err != nil {
			return nil, err
		}
		for _, projectID := range dbIDs {
			seen[projectID] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for projectID := range seen {
		out = append(out, projectID)
	}
	sort.Strings(out)
	return out, nil
}

func workspaceLocalProjectIDs(cfg APIConfig) []string {
	return workspace.ListLocalProjects(workspaceRoot(cfg))
}

func workspaceDBProjectIDs(ctx context.Context, db *sql.DB, userID string) ([]string, error) {
	return accesspkg.SQLPolicy{DB: db, Q: Q, QA: QArgs}.VisibleDatasetIDs(ctx, userID)
}

func workspaceContextBranches(ctx context.Context, cfg APIConfig, projectID, branchFilter string, watch WorkspaceWatchStatus) []workspaceBranchContext {
	branches := workspaceLocalBranches(cfg, projectID)
	if branchFilter != "" {
		branches = []string{defaultBranch(branchFilter)}
	} else if len(branches) == 0 {
		branches = []string{"main"}
	}
	var out []workspaceBranchContext
	for _, branch := range branches {
		out = append(out, workspaceContextBranch(ctx, cfg, projectID, branch, watch))
	}
	return out
}

func workspaceLocalBranches(cfg APIConfig, projectID string) []string {
	return workspace.ListLocalBranches(workspaceRoot(cfg), projectID)
}

func workspaceContextBranch(reqCtx context.Context, cfg APIConfig, projectID, branch string, watch WorkspaceWatchStatus) workspaceBranchContext {
	manifestPath := workspaceManifestPath(cfg, projectID, branch)
	_, statErr := os.Stat(manifestPath)
	out := workspaceBranchContext{
		Branch:         defaultBranch(branch),
		ManifestPath:   manifestPath,
		ManifestExists: statErr == nil,
		Watcher:        workspaceFreshnessBranchStatus(projectID, defaultBranch(branch), watch),
	}
	jobs, err := listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{ProjectID: projectID, Branch: branch})
	if err == nil && len(jobs) > 0 {
		out.JobsByStatus = workspaceJobStatusSummary(jobs)
	}
	if artifacts, err := listWorkspaceContextArtifacts(reqCtx, cfg, workspaceContextArtifactsRequest{
		ProjectID: projectID,
		Branch:    branch,
	}); err == nil {
		out.ContextArtifactCount = artifacts.Total
	}
	manifest, _, err := loadWorkspaceManifest(cfg, projectID, branch)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.ActiveGeneration = manifest.ActiveGeneration
	if manifest.ActiveGeneration == "" {
		out.InitializationPath = workspaceContextInitializationPath(projectID, branch)
		return out
	}
	chunks := manifest.ListChunks(workspace.ChunkFilter{
		ProjectID:  projectID,
		Branch:     defaultBranch(branch),
		Generation: manifest.ActiveGeneration,
	})
	out.ActiveChunkCount = len(chunks)
	out.ActivePathCount = workspaceSearchPathCount(chunks)
	out.LastIndexedAt = workspaceSearchLastIndexedAt(chunks)
	collection, err := workspaceSearchCollection(projectID, defaultBranch(branch), manifest.ActiveGeneration, chunks)
	if err != nil {
		out.Error = err.Error()
	} else {
		out.ActiveCollection = collection
	}
	return out
}

func workspaceContextInitializationPath(projectID, branch string) []string {
	return []string{
		"Create markdown under project " + projectID + " branch " + defaultBranch(branch) + " with workspace_write.",
		"Run workspace_reconcile with activate_generation=true.",
		"Use workspace_search, then workspace_read before answering from a hit.",
	}
}

func (h *mcpHandler) toolWorkspaceContext(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceContextRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	userID, _ := ctx.Value(mcpUserIDKey).(string)
	resp, err := buildWorkspaceContext(ctx, h.cfg, userID, req)
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}
