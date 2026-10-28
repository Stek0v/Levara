// workspace_artifacts.go — transport adapters over the extracted
// context-artifact library (internal/api/workspaceartifacts, Phase-2
// workspace campaign leaf 6). The resolution core (registry load, include
// expansion, materialization, filter/dedupe, kind normalization) lives in
// the package; this file keeps the HTTP/MCP handlers, the reindex write
// path (indexer/lexical/fence) and the artifact-content reader backed by
// workspacesvc. Package-local type names are preserved as aliases so the
// god-file and workspace_context.go consumers are unchanged.
package http

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/workspace"

	"github.com/stek0v/levara/internal/api/workspaceartifacts"
	"github.com/stek0v/levara/internal/api/workspacesvc"
)

const workspaceArtifactRegistryVersion = workspaceartifacts.RegistryVersion

type workspaceContextArtifactRegistry = workspaceartifacts.Registry
type workspaceContextArtifactInclude = workspaceartifacts.Include
type workspaceContextArtifactRequest = workspaceartifacts.Request
type workspaceContextArtifact = workspaceartifacts.Artifact

type workspaceContextArtifactsRequest = workspaceartifacts.ListRequest

type workspaceReindexArtifactsRequest struct {
	workspaceReindexRequest
	ArtifactIDs []string `json:"artifact_ids,omitempty"`
	Kinds       []string `json:"kinds,omitempty"`
}

type workspaceReindexArtifactsResponse struct {
	workspaceResponse
	Artifacts []workspaceContextArtifact `json:"artifacts"`
	Results   []workspace.IndexResult    `json:"results"`
}

type workspaceContextArtifactsResponse = workspaceartifacts.ListResponse

// artifactDeps builds the extraction package's filesystem plane.
func artifactDeps(cfg APIConfig) workspaceartifacts.Deps {
	return workspaceartifacts.Deps{
		Root: workspaceRoot(cfg),
		ReadFile: func(projectID, branch, relPath string) ([]byte, error) {
			return readWorkspaceArtifactFile(cfg, projectID, branch, relPath)
		},
	}
}

func workspaceContextArtifactsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		req := workspaceContextArtifactsRequest{
			ProjectID: c.Query("project_id"),
			Branch:    c.Query("branch"),
			Kind:      c.Query("kind"),
			IndexOnly: c.Query("index_only") == "true",
		}
		if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessRead); err != nil {
			return err
		}
		resp, err := listWorkspaceContextArtifacts(c.UserContext(), cfg, req)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err := c.JSON(resp); err != nil {
			return err
		}
		return sendWorkspaceProtectedResponse(c, cfg, c.UserContext(), uploadMetadataActor(c, cfg, c.UserContext()), req.ProjectID)
	}
}

func workspaceReindexArtifactsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req workspaceReindexArtifactsRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessWrite); err != nil {
			return err
		}
		resp, err := reindexWorkspaceContextArtifactsAuthorized(c.UserContext(), cfg, req, uploadMetadataActor(c, cfg, c.UserContext()))
		if err != nil {
			var fiberErr *fiber.Error
			if errors.As(err, &fiberErr) {
				return fiberErr
			}
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(resp)
	}
}

func listWorkspaceContextArtifacts(ctx context.Context, cfg APIConfig, req workspaceContextArtifactsRequest) (workspaceContextArtifactsResponse, error) {
	return workspaceartifacts.List(artifactDeps(cfg), req)
}

func loadWorkspaceContextArtifactRegistry(cfg APIConfig) (workspaceContextArtifactRegistry, string, error) {
	return workspaceartifacts.LoadRegistry(workspaceRoot(cfg))
}

func workspaceContextArtifactRegistryPath(cfg APIConfig) string {
	return filepath.Join(workspaceRoot(cfg), ".kb", "context-artifacts.json")
}

func materializeWorkspaceArtifact(cfg APIConfig, raw workspaceContextArtifactRequest, source string, metadataOnly bool) (workspaceContextArtifact, error) {
	return workspaceartifacts.Materialize(artifactDeps(cfg), raw, source, metadataOnly)
}

func readWorkspaceArtifactFile(cfg APIConfig, projectID, branch, path string) ([]byte, error) {
	_, relativePath, err := workspaceFilePath(cfg, projectID, branch, path)
	if err != nil {
		return nil, err
	}
	relativeRoot, err := filepath.Rel(workspaceRoot(cfg), workspaceProjectRoot(cfg, projectID, branch))
	if err != nil {
		return nil, err
	}
	root, err := workspace.OpenRoot(workspaceRoot(cfg), relativeRoot, false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return workspace.ReadFile(root, relativePath)
}

func (h *mcpHandler) toolWorkspaceContextArtifacts(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceContextArtifactsRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessRead); err != nil {
		return workspaceMCPError(err)
	}
	resp, err := listWorkspaceContextArtifacts(ctx, h.cfg, req)
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}

func (h *mcpHandler) toolWorkspaceReindexArtifacts(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceReindexArtifactsRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessWrite); err != nil {
		return workspaceMCPError(err)
	}
	resp, err := reindexWorkspaceContextArtifactsAuthorized(ctx, h.cfg, req, h.MetadataActor(ctx))
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}

func stringSet(xs []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x != "" {
			out[x] = struct{}{}
		}
	}
	return out
}

func artifactTitle(artifact workspaceContextArtifact) string {
	if artifact.Title != "" {
		return artifact.Title
	}
	return filepath.Base(artifact.Path)
}

func artifactRoom(artifact workspaceContextArtifact) string {
	if artifact.Room != "" {
		return artifact.Room
	}
	return artifact.Kind
}

func uniqueStrings(xs []string) []string {
	return workspaceartifacts.UniqueStrings(xs)
}

// reindexWorkspaceArtifact indexes one resolved artifact through the
// markdown write path (manifest lock + effect fence inside the Locked fn).
func reindexWorkspaceArtifact(ctx context.Context, cfg APIConfig, actor accesspkg.MetadataActor, artifact workspaceContextArtifact, req workspaceReindexArtifactsRequest) (workspaceIndexResponse, error) {
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(artifact.ProjectID, artifact.Branch))
	defer unlock()
	release, err := beginWorkspaceEffectFence(ctx, cfg, actor, artifact.ProjectID, workspaceAccessWrite)
	if err != nil {
		return workspaceIndexResponse{}, err
	}
	defer release()
	_, relPath, err := workspaceFilePath(cfg, artifact.ProjectID, artifact.Branch, artifact.Path)
	if err != nil {
		return workspaceIndexResponse{}, err
	}
	data, err := readWorkspaceArtifactFile(cfg, artifact.ProjectID, artifact.Branch, relPath)
	if err != nil {
		return workspaceIndexResponse{}, err
	}
	tags := append([]string{}, artifact.Tags...)
	tags = append(tags, "context-artifact", "kind:"+artifact.Kind)
	artifact.Exists = true
	artifact.Bytes = int64(len(data))
	artifact.Digest = digestBytes(data)
	return indexWorkspaceMarkdownLocked(ctx, cfg, workspaceIndexRequest{
		ProjectID:          artifact.ProjectID,
		Branch:             artifact.Branch,
		Generation:         req.Generation,
		Collection:         req.Collection,
		CommitHash:         req.CommitHash,
		ChunkStrategy:      req.ChunkStrategy,
		MinChunkChars:      req.MinChunkChars,
		MaxChunkChars:      req.MaxChunkChars,
		OverlapChars:       req.OverlapChars,
		SnapToSentence:     req.SnapToSentence,
		ActivateGeneration: req.ActivateGeneration,
		Path:               relPath,
		Text:               string(data),
		FileDigest:         digestBytes(data),
		DocumentID:         artifact.ID,
		Title:              artifactTitle(artifact),
		Room:               artifactRoom(artifact),
		Tags:               uniqueStrings(tags),
	})
}
func reindexWorkspaceContextArtifactsAuthorized(ctx context.Context, cfg APIConfig, req workspaceReindexArtifactsRequest, actor accesspkg.MetadataActor) (workspaceReindexArtifactsResponse, error) {
	ctx, cancel, err := workspaceRequestContext(ctx, actor, 30*time.Second)
	if err != nil {
		return workspaceReindexArtifactsResponse{}, err
	}
	defer cancel()
	if err := validateWorkspaceCollection(req.Collection); err != nil {
		return workspaceReindexArtifactsResponse{}, err
	}
	if req.ProjectID == "" {
		return workspaceReindexArtifactsResponse{}, workspace.ErrMissingProjectID
	}
	if req.Generation == "" {
		return workspaceReindexArtifactsResponse{}, workspace.ErrMissingGeneration
	}
	listReq := workspaceartifacts.ListRequest{
		ProjectID: req.ProjectID,
		Branch:    req.Branch,
		IDs:       req.ArtifactIDs,
		IndexOnly: true,
		MetadataOnly: true,
	}
	if len(req.Kinds) == 1 {
		listReq.Kind = req.Kinds[0]
	}
	resp, err := listWorkspaceContextArtifacts(ctx, cfg, listReq)
	if err != nil {
		return workspaceReindexArtifactsResponse{}, err
	}
	if len(req.Kinds) > 1 {
		allowed := stringSet(req.Kinds)
		filtered := resp.Artifacts[:0]
		for _, artifact := range resp.Artifacts {
			if _, ok := allowed[artifact.Kind]; ok {
				filtered = append(filtered, artifact)
			}
		}
		resp.Artifacts = filtered
	}
	if len(resp.Artifacts) == 0 {
		return workspaceReindexArtifactsResponse{}, errors.New("no matching context artifacts")
	}
	var results []workspace.IndexResult
	var manifestPath string
	var active string
	for i, artifact := range resp.Artifacts {
		if err := ctx.Err(); err != nil {
			return workspaceReindexArtifactsResponse{}, err
		}
		indexResp, err := reindexWorkspaceArtifact(ctx, cfg, actor, artifact, req)
		if err != nil {
			return workspaceReindexArtifactsResponse{}, err
		}
		results = append(results, indexResp.Result)
		if i == 0 {
			manifestPath = indexResp.ManifestPath
			active = indexResp.ActiveGeneration
		}
	}
	return workspaceReindexArtifactsResponse{
		workspaceResponse: workspaceResponse{
			ProjectID:        req.ProjectID,
			Branch:           defaultBranch(req.Branch),
			ManifestPath:     manifestPath,
			ActiveGeneration: active,
		},
		Artifacts: resp.Artifacts,
		Results:   results,
	}, nil
}

var _ = workspacesvc.Root
