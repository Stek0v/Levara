package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/workspace"
)

const workspaceArtifactRegistryVersion = 1

type workspaceContextArtifactKind string

const (
	workspaceArtifactOpenAPI   workspaceContextArtifactKind = "openapi"
	workspaceArtifactDDL       workspaceContextArtifactKind = "ddl"
	workspaceArtifactTerraform workspaceContextArtifactKind = "terraform"
	workspaceArtifactADR       workspaceContextArtifactKind = "adr"
	workspaceArtifactRunbook   workspaceContextArtifactKind = "runbook"
	workspaceArtifactMarkdown  workspaceContextArtifactKind = "markdown"
	workspaceArtifactOther     workspaceContextArtifactKind = "other"
)

type workspaceContextArtifactRegistry struct {
	Version   int                               `json:"version"`
	Includes  []workspaceContextArtifactInclude `json:"includes,omitempty"`
	Artifacts []workspaceContextArtifactRequest `json:"artifacts,omitempty"`
}

type workspaceContextArtifactInclude struct {
	ProjectID        string   `json:"project_id"`
	Branch           string   `json:"branch,omitempty"`
	Glob             string   `json:"glob"`
	Kind             string   `json:"kind,omitempty"`
	Room             string   `json:"room,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	Index            *bool    `json:"index,omitempty"`
	IncludeInContext *bool    `json:"include_in_context,omitempty"`
}

type workspaceContextArtifactRequest struct {
	ID               string   `json:"id,omitempty"`
	ProjectID        string   `json:"project_id"`
	Branch           string   `json:"branch,omitempty"`
	Path             string   `json:"path"`
	Kind             string   `json:"kind,omitempty"`
	Title            string   `json:"title,omitempty"`
	Room             string   `json:"room,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	Index            *bool    `json:"index,omitempty"`
	IncludeInContext *bool    `json:"include_in_context,omitempty"`
}

type workspaceContextArtifact struct {
	ID               string   `json:"id"`
	ProjectID        string   `json:"project_id"`
	Branch           string   `json:"branch"`
	Path             string   `json:"path"`
	Kind             string   `json:"kind"`
	Title            string   `json:"title,omitempty"`
	Room             string   `json:"room,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	Index            bool     `json:"index"`
	IncludeInContext bool     `json:"include_in_context"`
	Exists           bool     `json:"exists"`
	Bytes            int64    `json:"bytes,omitempty"`
	Digest           string   `json:"digest,omitempty"`
	Source           string   `json:"source"`
}

type workspaceContextArtifactsRequest struct {
	ProjectID string   `json:"project_id,omitempty"`
	Branch    string   `json:"branch,omitempty"`
	Kind      string   `json:"kind,omitempty"`
	IDs       []string `json:"ids,omitempty"`
	IndexOnly bool     `json:"index_only,omitempty"`
}

type workspaceContextArtifactsResponse struct {
	Version   int                        `json:"version"`
	Path      string                     `json:"path"`
	Artifacts []workspaceContextArtifact `json:"artifacts"`
	Total     int                        `json:"total"`
}

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

func workspaceContextArtifactsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		req := workspaceContextArtifactsRequest{
			ProjectID: c.Query("project_id"),
			Branch:    c.Query("branch"),
			Kind:      c.Query("kind"),
			IndexOnly: strings.EqualFold(c.Query("index_only"), "true"),
		}
		if req.ProjectID != "" {
			if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessRead); err != nil {
				return err
			}
		}
		resp, err := listWorkspaceContextArtifacts(c.UserContext(), cfg, req)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(resp)
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
		resp, err := reindexWorkspaceContextArtifacts(c.UserContext(), cfg, req)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(resp)
	}
}

func listWorkspaceContextArtifacts(ctx context.Context, cfg APIConfig, req workspaceContextArtifactsRequest) (workspaceContextArtifactsResponse, error) {
	registry, path, err := loadWorkspaceContextArtifactRegistry(cfg)
	if err != nil {
		return workspaceContextArtifactsResponse{}, err
	}
	artifacts, err := resolveWorkspaceContextArtifacts(ctx, cfg, registry, req)
	if err != nil {
		return workspaceContextArtifactsResponse{}, err
	}
	return workspaceContextArtifactsResponse{
		Version:   registry.Version,
		Path:      path,
		Artifacts: artifacts,
		Total:     len(artifacts),
	}, nil
}

func reindexWorkspaceContextArtifacts(ctx context.Context, cfg APIConfig, req workspaceReindexArtifactsRequest) (workspaceReindexArtifactsResponse, error) {
	if req.ProjectID == "" {
		return workspaceReindexArtifactsResponse{}, workspace.ErrMissingProjectID
	}
	if req.Generation == "" {
		return workspaceReindexArtifactsResponse{}, workspace.ErrMissingGeneration
	}
	listReq := workspaceContextArtifactsRequest{
		ProjectID: req.ProjectID,
		Branch:    req.Branch,
		IDs:       req.ArtifactIDs,
		IndexOnly: true,
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
	for _, artifact := range resp.Artifacts {
		filePath, relPath, err := workspaceFilePath(cfg, artifact.ProjectID, artifact.Branch, artifact.Path)
		if err != nil {
			return workspaceReindexArtifactsResponse{}, err
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			return workspaceReindexArtifactsResponse{}, err
		}
		tags := append([]string{}, artifact.Tags...)
		tags = append(tags, "context-artifact", "kind:"+artifact.Kind)
		indexResp, err := indexWorkspaceMarkdown(ctx, cfg, workspaceIndexRequest{
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
		if err != nil {
			return workspaceReindexArtifactsResponse{}, err
		}
		results = append(results, indexResp.Result)
		manifestPath = indexResp.ManifestPath
		active = indexResp.ActiveGeneration
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

func loadWorkspaceContextArtifactRegistry(cfg APIConfig) (workspaceContextArtifactRegistry, string, error) {
	path := workspaceContextArtifactRegistryPath(cfg)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return workspaceContextArtifactRegistry{Version: workspaceArtifactRegistryVersion}, path, nil
		}
		return workspaceContextArtifactRegistry{}, path, err
	}
	var registry workspaceContextArtifactRegistry
	if err := json.Unmarshal(data, &registry); err != nil {
		return workspaceContextArtifactRegistry{}, path, err
	}
	if registry.Version == 0 {
		registry.Version = workspaceArtifactRegistryVersion
	}
	return registry, path, nil
}

func workspaceContextArtifactRegistryPath(cfg APIConfig) string {
	return filepath.Join(workspaceRoot(cfg), ".kb", "context-artifacts.json")
}

func resolveWorkspaceContextArtifacts(_ context.Context, cfg APIConfig, registry workspaceContextArtifactRegistry, req workspaceContextArtifactsRequest) ([]workspaceContextArtifact, error) {
	var artifacts []workspaceContextArtifact
	for _, include := range registry.Includes {
		if req.ProjectID != "" && include.ProjectID != req.ProjectID {
			continue
		}
		branch := defaultBranch(include.Branch)
		if req.Branch != "" && branch != defaultBranch(req.Branch) {
			continue
		}
		matches, err := expandWorkspaceArtifactInclude(cfg, include)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, matches...)
	}
	for _, raw := range registry.Artifacts {
		if req.ProjectID != "" && raw.ProjectID != req.ProjectID {
			continue
		}
		branch := defaultBranch(raw.Branch)
		if req.Branch != "" && branch != defaultBranch(req.Branch) {
			continue
		}
		artifact, err := materializeWorkspaceArtifact(cfg, raw, "explicit")
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	artifacts = filterWorkspaceArtifacts(artifacts, req)
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].ProjectID == artifacts[j].ProjectID {
			if artifacts[i].Branch == artifacts[j].Branch {
				return artifacts[i].Path < artifacts[j].Path
			}
			return artifacts[i].Branch < artifacts[j].Branch
		}
		return artifacts[i].ProjectID < artifacts[j].ProjectID
	})
	return dedupeWorkspaceArtifacts(artifacts), nil
}

func expandWorkspaceArtifactInclude(cfg APIConfig, include workspaceContextArtifactInclude) ([]workspaceContextArtifact, error) {
	if include.ProjectID == "" {
		return nil, errors.New("context artifact include project_id required")
	}
	if include.Glob == "" {
		return nil, errors.New("context artifact include glob required")
	}
	branch := defaultBranch(include.Branch)
	root := workspaceProjectRoot(cfg, include.ProjectID, branch)
	var artifacts []workspaceContextArtifact
	err := filepath.WalkDir(root, func(abs string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !workspaceArtifactGlobMatch(include.Glob, rel) {
			return nil
		}
		index := true
		if include.Index != nil {
			index = *include.Index
		}
		includeInContext := true
		if include.IncludeInContext != nil {
			includeInContext = *include.IncludeInContext
		}
		artifact, err := materializeWorkspaceArtifact(cfg, workspaceContextArtifactRequest{
			ProjectID:        include.ProjectID,
			Branch:           branch,
			Path:             rel,
			Kind:             include.Kind,
			Room:             include.Room,
			Tags:             include.Tags,
			Index:            &index,
			IncludeInContext: &includeInContext,
		}, "include:"+include.Glob)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, artifact)
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []workspaceContextArtifact{}, nil
		}
		return nil, err
	}
	return artifacts, nil
}

func materializeWorkspaceArtifact(cfg APIConfig, raw workspaceContextArtifactRequest, source string) (workspaceContextArtifact, error) {
	if raw.ProjectID == "" {
		return workspaceContextArtifact{}, workspace.ErrMissingProjectID
	}
	branch := defaultBranch(raw.Branch)
	_, relPath, err := workspaceFilePath(cfg, raw.ProjectID, branch, raw.Path)
	if err != nil {
		return workspaceContextArtifact{}, err
	}
	filePath, _, _ := workspaceFilePath(cfg, raw.ProjectID, branch, relPath)
	info, statErr := os.Stat(filePath)
	exists := statErr == nil && !info.IsDir()
	var digest string
	var size int64
	if exists {
		size = info.Size()
		data, err := os.ReadFile(filePath)
		if err != nil {
			return workspaceContextArtifact{}, err
		}
		digest = digestBytes(data)
	}
	kind := normalizeWorkspaceArtifactKind(raw.Kind, relPath)
	index := true
	if raw.Index != nil {
		index = *raw.Index
	}
	includeInContext := true
	if raw.IncludeInContext != nil {
		includeInContext = *raw.IncludeInContext
	}
	id := raw.ID
	if id == "" {
		id = stableWorkspaceArtifactID(raw.ProjectID, branch, relPath)
	}
	tags := append([]string{}, raw.Tags...)
	return workspaceContextArtifact{
		ID:               id,
		ProjectID:        raw.ProjectID,
		Branch:           branch,
		Path:             relPath,
		Kind:             kind,
		Title:            raw.Title,
		Room:             raw.Room,
		Tags:             uniqueStrings(tags),
		Index:            index,
		IncludeInContext: includeInContext,
		Exists:           exists,
		Bytes:            size,
		Digest:           digest,
		Source:           source,
	}, nil
}

func filterWorkspaceArtifacts(artifacts []workspaceContextArtifact, req workspaceContextArtifactsRequest) []workspaceContextArtifact {
	idSet := stringSet(req.IDs)
	var out []workspaceContextArtifact
	for _, artifact := range artifacts {
		if req.Kind != "" && artifact.Kind != normalizeWorkspaceArtifactKind(req.Kind, artifact.Path) {
			continue
		}
		if len(idSet) > 0 {
			if _, ok := idSet[artifact.ID]; !ok {
				continue
			}
		}
		if req.IndexOnly && !artifact.Index {
			continue
		}
		out = append(out, artifact)
	}
	return out
}

func dedupeWorkspaceArtifacts(artifacts []workspaceContextArtifact) []workspaceContextArtifact {
	seen := map[string]struct{}{}
	var out []workspaceContextArtifact
	for _, artifact := range artifacts {
		key := artifact.ProjectID + "\x00" + artifact.Branch + "\x00" + artifact.Path
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, artifact)
	}
	return out
}

func workspaceArtifactGlobMatch(pattern, rel string) bool {
	pattern = filepath.ToSlash(filepath.Clean(pattern))
	rel = filepath.ToSlash(filepath.Clean(rel))
	if pattern == "." || pattern == "" {
		return false
	}
	if ok, _ := path.Match(pattern, rel); ok {
		return true
	}
	re := regexp.QuoteMeta(pattern)
	re = strings.ReplaceAll(re, `\*\*/`, `(?:.*/)?`)
	re = strings.ReplaceAll(re, `\*\*`, `.*`)
	re = strings.ReplaceAll(re, `\*`, `[^/]*`)
	re = strings.ReplaceAll(re, `\?`, `[^/]`)
	ok, _ := regexp.MatchString("^"+re+"$", rel)
	return ok
}

func normalizeWorkspaceArtifactKind(kind, artifactPath string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case string(workspaceArtifactOpenAPI):
		return string(workspaceArtifactOpenAPI)
	case "sql", "schema", string(workspaceArtifactDDL):
		return string(workspaceArtifactDDL)
	case "tf", "hcl", string(workspaceArtifactTerraform):
		return string(workspaceArtifactTerraform)
	case string(workspaceArtifactADR):
		return string(workspaceArtifactADR)
	case string(workspaceArtifactRunbook):
		return string(workspaceArtifactRunbook)
	case "md", string(workspaceArtifactMarkdown):
		return string(workspaceArtifactMarkdown)
	case "", string(workspaceArtifactOther):
	default:
		return strings.ToLower(strings.TrimSpace(kind))
	}
	ext := strings.ToLower(filepath.Ext(artifactPath))
	base := strings.ToLower(filepath.Base(artifactPath))
	switch {
	case strings.Contains(base, "openapi") || strings.Contains(base, "swagger"):
		return string(workspaceArtifactOpenAPI)
	case ext == ".sql":
		return string(workspaceArtifactDDL)
	case ext == ".tf" || ext == ".hcl":
		return string(workspaceArtifactTerraform)
	case strings.Contains(strings.ToLower(artifactPath), "/adr/"):
		return string(workspaceArtifactADR)
	case strings.Contains(strings.ToLower(artifactPath), "/runbook"):
		return string(workspaceArtifactRunbook)
	case ext == ".md" || ext == ".mdx":
		return string(workspaceArtifactMarkdown)
	default:
		return string(workspaceArtifactOther)
	}
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

func stableWorkspaceArtifactID(projectID, branch, path string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00" + branch + "\x00" + path))
	return "artifact_" + hex.EncodeToString(sum[:])[:20]
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

func uniqueStrings(xs []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x == "" {
			continue
		}
		if _, ok := seen[x]; ok {
			continue
		}
		seen[x] = struct{}{}
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (h *mcpHandler) toolWorkspaceContextArtifacts(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceContextArtifactsRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if req.ProjectID != "" {
		if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessRead); err != nil {
			return workspaceMCPError(err)
		}
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
	resp, err := reindexWorkspaceContextArtifacts(ctx, h.cfg, req)
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}
