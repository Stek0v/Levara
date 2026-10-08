package http

import (
	"context"

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
		req := workspaceContextRequest{ProjectID: c.Query("project_id"), Branch: c.Query("branch")}
		actor := uploadMetadataActor(c, cfg, c.UserContext())
		ctx, cancel, err := workspaceRequestContext(c.UserContext(), actor, timeoutFromEnvMs("SEARCH_REQUEST_TIMEOUT_MS", defaultSearchRequestTimeout))
		if err != nil {
			return err
		}
		defer cancel()
		return withProtectedPolicyResponse(c, cfg, ctx, func(fenced context.Context, policy accesspkg.SQLPolicy) error {
			resp, err := buildWorkspaceContextWithPolicy(fenced, cfg, actor, req, policy)
			if err != nil {
				return err
			}
			return c.JSON(resp)
		})
	}
}

func buildWorkspaceContextAuthorized(ctx context.Context, cfg APIConfig, actor accesspkg.MetadataActor, req workspaceContextRequest) (workspaceContextResponse, error) {
	ctx, cancel, err := workspaceRequestContext(ctx, actor, timeoutFromEnvMs("SEARCH_REQUEST_TIMEOUT_MS", defaultSearchRequestTimeout))
	if err != nil {
		return workspaceContextResponse{}, err
	}
	defer cancel()
	policy, installed := ctx.Value(searchReadPolicyKey{}).(accesspkg.SQLPolicy)
	if !installed {
		policy = accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}
		if !actor.TrustedLocal || actor.UserID != "" || actor.TenantID != "" || cfg.RequireAuth {
			locked, release, err := policy.BeginTransferFence(ctx, GetDBProvider() == DBSQLite)
			if err != nil {
				return workspaceContextResponse{}, fiber.NewError(fiber.StatusServiceUnavailable, "workspace authorization unavailable")
			}
			defer release()
			policy = locked
		}
	}
	return buildWorkspaceContextWithPolicy(ctx, cfg, actor, req, policy)
}

func buildWorkspaceContextWithPolicy(ctx context.Context, cfg APIConfig, actor accesspkg.MetadataActor, req workspaceContextRequest, policy accesspkg.SQLPolicy) (workspaceContextResponse, error) {
	local := actor.TrustedLocal && actor.UserID == "" && actor.TenantID == "" && !cfg.RequireAuth
	if !local {
		if actor.UserID == "" || (cfg.RequireAuth && actor.TrustedLocal) {
			return workspaceContextResponse{}, fiber.NewError(fiber.StatusForbidden, "workspace requires verified authority")
		}
		if err := recheckDatasetShareActor(ctx, policy, actor); err != nil {
			return workspaceContextResponse{}, fiber.NewError(fiber.StatusForbidden, "workspace access revoked")
		}
		if !accesspkg.APIKeyAllows(actor.APIKeyPermissions, accesspkg.ActionRead) {
			return workspaceContextResponse{}, fiber.NewError(fiber.StatusForbidden, errWorkspaceAccessDenied.Error())
		}
	}
	var projectIDs []string
	if req.ProjectID != "" {
		projectIDs = []string{req.ProjectID}
	} else if local {
		projectIDs = workspaceLocalProjectIDs(cfg)
	} else {
		var err error
		projectIDs, err = policy.VisibleDatasetIDs(ctx, actor.UserID)
		if err != nil {
			return workspaceContextResponse{}, err
		}
	}
	watch := workspaceWatchStatus(cfg)
	resp := workspaceContextResponse{RecommendedSearchType: "HYBRID", ExactReadRequired: true}
	admitted := map[string]bool{}
	for _, projectID := range projectIDs {
		decision, err := policy.AuthorizeWorkspace(ctx, accesspkg.WorkspaceRequest{
			UserID: actor.UserID, TenantID: actor.TenantID, ProjectID: projectID, Action: string(workspaceAccessRead), APIKeyPermissions: actor.APIKeyPermissions,
		})
		if err != nil {
			return workspaceContextResponse{}, err
		}
		if !decision.Allowed {
			if req.ProjectID != "" {
				return workspaceContextResponse{}, fiber.NewError(fiber.StatusForbidden, errWorkspaceAccessDenied.Error())
			}
			continue
		}
		admitted[safeWorkspaceID(projectID)] = true
		resp.Projects = append(resp.Projects, workspaceProjectContext{
			ProjectID: projectID,
			Access: workspaceAccessCheckResponse{
				ProjectID: projectID, UserID: actor.UserID, Access: string(workspaceAccessRead),
				Allowed: decision.Allowed, Role: decision.Role, Reason: decision.Reason,
				DevMode: decision.DevMode, Authenticated: decision.Authenticated, APIKeyAllowed: decision.APIKeyAllowed,
			},
		})
	}
	filtered := watch
	filtered.Branches = map[string]WorkspaceBranchWatchStatus{}
	for key, status := range watch.Branches {
		if admitted[safeWorkspaceID(status.ProjectID)] && (req.Branch == "" || safeWorkspaceID(defaultBranch(status.Branch)) == safeWorkspaceID(defaultBranch(req.Branch))) {
			filtered.Branches[key] = status
		}
	}
	resp.Watcher = workspaceProjectWatchStatus(filtered, "", req.Branch)
	for i := range resp.Projects {
		project := &resp.Projects[i]
		project.Branches = workspaceContextBranches(ctx, cfg, project.ProjectID, req.Branch, resp.Watcher)
		if len(project.Branches) == 0 {
			project.Guidance = workspaceContextInitializationPath(project.ProjectID, defaultBranch(req.Branch))
		}
	}
	if len(resp.Projects) > 0 {
		resp.DefaultProjectID = resp.Projects[0].ProjectID
	} else {
		resp.Guidance = []string{
			"Create or share a workspace project.",
			"Write markdown with workspace_write, then run workspace_reconcile to publish an active generation.",
		}
	}
	return resp, nil
}

func workspaceLocalProjectIDs(cfg APIConfig) []string {
	return workspace.ListLocalProjects(workspaceRoot(cfg))
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
	out := workspaceBranchContext{
		Branch:       defaultBranch(branch),
		ManifestPath: manifestPath,
		Watcher:      workspaceFreshnessBranchStatus(projectID, defaultBranch(branch), watch),
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
	manifest, exists, err := readWorkspaceManifest(cfg, projectID, branch)
	out.ManifestExists = exists
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
		ProjectID:  manifest.ProjectID,
		Branch:     defaultBranch(manifest.Branch),
		Generation: manifest.ActiveGeneration,
	})
	out.ActiveChunkCount = len(chunks)
	out.ActivePathCount = workspaceSearchPathCount(chunks)
	out.LastIndexedAt = workspaceSearchLastIndexedAt(chunks)
	collection, err := workspaceSearchCollection(manifest.ProjectID, defaultBranch(manifest.Branch), manifest.ActiveGeneration, chunks)
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
	resp, err := buildWorkspaceContextAuthorized(ctx, h.cfg, h.MetadataActor(ctx), req)
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}
