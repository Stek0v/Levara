package http

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/workspace"
)

type workspaceConflictRequest struct {
	ProjectID string `json:"project_id"`
	Branch    string `json:"branch,omitempty"`
}

type workspaceConflictPath struct {
	Path          string `json:"path"`
	State         string `json:"state"`
	FileDigest    string `json:"file_digest,omitempty"`
	IndexedDigest string `json:"indexed_digest,omitempty"`
	IndexedAt     string `json:"indexed_at,omitempty"`
	Detail        string `json:"detail,omitempty"`
}

type workspaceConflictResponse struct {
	ProjectID           string                     `json:"project_id"`
	Branch              string                     `json:"branch"`
	ActiveGeneration    string                     `json:"active_generation,omitempty"`
	ManifestPath        string                     `json:"manifest_path,omitempty"`
	HasConflicts        bool                       `json:"has_conflicts"`
	Policy              string                     `json:"policy"`
	DirtyPaths          []workspaceConflictPath    `json:"dirty_paths,omitempty"`
	UnindexedPaths      []workspaceConflictPath    `json:"unindexed_paths,omitempty"`
	MissingIndexedPaths []workspaceConflictPath    `json:"missing_indexed_paths,omitempty"`
	Watcher             WorkspaceBranchWatchStatus `json:"watcher"`
	JobsByStatus        map[string]int             `json:"jobs_by_status,omitempty"`
	RecommendedActions  []string                   `json:"recommended_actions,omitempty"`
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
		return c.JSON(resp)
	}
}

func workspaceConflicts(_ context.Context, cfg APIConfig, req workspaceConflictRequest) (workspaceConflictResponse, error) {
	branch := defaultBranch(req.Branch)
	if req.ProjectID == "" {
		return workspaceConflictResponse{}, workspace.ErrMissingProjectID
	}
	manifest, manifestPath, err := loadWorkspaceManifest(cfg, req.ProjectID, branch)
	if err != nil {
		return workspaceConflictResponse{}, err
	}
	resp := workspaceConflictResponse{
		ProjectID:        req.ProjectID,
		Branch:           branch,
		ActiveGeneration: manifest.ActiveGeneration,
		ManifestPath:     manifestPath,
		Policy:           "filesystem_truth_wins; writes are last-writer-wins until workspace_commit; reconcile publishes a new active generation; revert should be followed by reconcile or reindex=true",
		Watcher:          workspaceFreshnessBranchStatus(req.ProjectID, branch, workspaceWatchStatus(cfg)),
	}
	jobs, err := listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{ProjectID: req.ProjectID, Branch: branch})
	if err == nil && len(jobs) > 0 {
		resp.JobsByStatus = workspaceJobStatusSummary(jobs)
	}
	if manifest.ActiveGeneration == "" {
		resp.HasConflicts = true
		resp.RecommendedActions = []string{"Run workspace_reconcile with activate_generation=true."}
		return resp, nil
	}

	root := workspaceProjectRoot(cfg, req.ProjectID, branch)
	paths, err := listWorkspaceMarkdownPaths(root)
	if err != nil {
		return workspaceConflictResponse{}, err
	}
	activeChunks := manifest.ListChunks(workspace.ChunkFilter{
		ProjectID:  req.ProjectID,
		Branch:     branch,
		Generation: manifest.ActiveGeneration,
	})
	indexed := workspaceIndexedPathDigests(activeChunks)
	seen := map[string]struct{}{}
	for _, rel := range paths {
		seen[rel] = struct{}{}
		filePath := filepath.Join(root, filepath.FromSlash(rel))
		data, err := os.ReadFile(filePath)
		if err != nil {
			return workspaceConflictResponse{}, err
		}
		digest := digestBytes(data)
		digests := indexed[rel]
		switch {
		case len(digests) == 0:
			resp.UnindexedPaths = append(resp.UnindexedPaths, workspaceConflictPath{
				Path:       rel,
				State:      "unindexed",
				FileDigest: digest,
				Detail:     "Markdown file is present in filesystem truth but absent from active generation.",
			})
		case !digests[digest]:
			resp.DirtyPaths = append(resp.DirtyPaths, workspaceConflictPath{
				Path:          rel,
				State:         "dirty",
				FileDigest:    digest,
				IndexedDigest: firstDigest(digests),
				IndexedAt:     workspaceIndexedPathUpdatedAt(activeChunks, rel),
				Detail:        "Markdown file digest differs from active generation digest.",
			})
		}
	}
	for rel, digests := range indexed {
		if _, ok := seen[rel]; ok {
			continue
		}
		resp.MissingIndexedPaths = append(resp.MissingIndexedPaths, workspaceConflictPath{
			Path:          rel,
			State:         "missing_from_filesystem",
			IndexedDigest: firstDigest(digests),
			IndexedAt:     workspaceIndexedPathUpdatedAt(activeChunks, rel),
			Detail:        "Active generation references a path that no longer exists in filesystem truth.",
		})
	}
	sortWorkspaceConflictPaths(resp.DirtyPaths)
	sortWorkspaceConflictPaths(resp.UnindexedPaths)
	sortWorkspaceConflictPaths(resp.MissingIndexedPaths)
	resp.HasConflicts = len(resp.DirtyPaths) > 0 || len(resp.UnindexedPaths) > 0 || len(resp.MissingIndexedPaths) > 0 || resp.Watcher.Pending
	resp.RecommendedActions = workspaceConflictRecommendedActions(resp)
	return resp, nil
}

func workspaceIndexedPathDigests(chunks []workspace.ChunkRecord) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, ch := range chunks {
		if ch.Path == "" || ch.FileDigest == "" {
			continue
		}
		if out[ch.Path] == nil {
			out[ch.Path] = map[string]bool{}
		}
		out[ch.Path][ch.FileDigest] = true
	}
	return out
}

func workspaceIndexedPathUpdatedAt(chunks []workspace.ChunkRecord, rel string) string {
	latest := ""
	for _, ch := range chunks {
		if ch.Path != rel {
			continue
		}
		latest = newerTimeString(latest, ch.UpdatedAt)
	}
	return latest
}

func firstDigest(digests map[string]bool) string {
	xs := make([]string, 0, len(digests))
	for digest := range digests {
		xs = append(xs, digest)
	}
	sort.Strings(xs)
	if len(xs) == 0 {
		return ""
	}
	return xs[0]
}

func sortWorkspaceConflictPaths(paths []workspaceConflictPath) {
	sort.Slice(paths, func(i, j int) bool { return paths[i].Path < paths[j].Path })
}

func workspaceConflictRecommendedActions(resp workspaceConflictResponse) []string {
	var actions []string
	if len(resp.DirtyPaths) > 0 || len(resp.UnindexedPaths) > 0 || len(resp.MissingIndexedPaths) > 0 {
		actions = append(actions, "Run workspace_reconcile with a fresh generation and activate_generation=true.")
	}
	if resp.Watcher.Pending {
		actions = append(actions, "Wait for watcher debounce/worker drain or manually reconcile before answering from search.")
	}
	if resp.JobsByStatus[string(workspaceIndexJobDeadLetter)] > 0 {
		actions = append(actions, "Inspect workspace_index_jobs dead_letter entries, fix the root cause, then retry or enqueue a fresh generation.")
	}
	if len(actions) == 0 {
		actions = append(actions, "No action required; active generation matches current Markdown files.")
	}
	return actions
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
