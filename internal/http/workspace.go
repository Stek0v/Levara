package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/bm25"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/vectorstore"
	"github.com/stek0v/levara/pkg/workspace"
)

type workspaceAccessLevel string

const (
	workspaceAccessRead  workspaceAccessLevel = "read"
	workspaceAccessWrite workspaceAccessLevel = "write"
)

var errWorkspaceAccessDenied = errors.New("workspace access denied")

type workspaceIndexRequest struct {
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
	ActivateGeneration bool     `json:"activate_generation,omitempty"`
	Path               string   `json:"path"`
	Text               string   `json:"text"`
	FileDigest         string   `json:"file_digest,omitempty"`
	DocumentID         string   `json:"document_id,omitempty"`
	Title              string   `json:"title,omitempty"`
	Room               string   `json:"room,omitempty"`
	Tags               []string `json:"tags,omitempty"`
}

type workspaceDeleteRequest struct {
	ProjectID  string `json:"project_id"`
	Branch     string `json:"branch"`
	Generation string `json:"generation"`
	Collection string `json:"collection,omitempty"`
	Path       string `json:"path"`
}

type workspaceGCRequest struct {
	ProjectID string `json:"project_id"`
	Branch    string `json:"branch"`
	DryRun    bool   `json:"dry_run,omitempty"`
}

type workspaceReadRequest struct {
	ProjectID string `json:"project_id"`
	Branch    string `json:"branch"`
	Path      string `json:"path"`
}

type workspaceWriteRequest struct {
	workspaceIndexRequest
	Index              *bool   `json:"index,omitempty"`
	ExpectedFileDigest *string `json:"expected_file_digest,omitempty"`
}

type workspaceReindexRequest struct {
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
	ActivateGeneration bool     `json:"activate_generation,omitempty"`
	Paths              []string `json:"paths"`
	Room               string   `json:"room,omitempty"`
	Tags               []string `json:"tags,omitempty"`
}

type workspaceReconcileRequest struct {
	workspaceReindexRequest
	DeleteMissing bool `json:"delete_missing,omitempty"`
}

type workspaceRunStartRequest struct {
	ProjectID string         `json:"project_id"`
	Branch    string         `json:"branch"`
	RunID     string         `json:"run_id,omitempty"`
	Prompt    string         `json:"prompt,omitempty"`
	Command   string         `json:"command,omitempty"`
	Result    string         `json:"result,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type workspaceRunGetRequest struct {
	ProjectID string `json:"project_id"`
	Branch    string `json:"branch"`
	RunID     string `json:"run_id"`
}

type workspaceCommitRequest struct {
	ProjectID string `json:"project_id"`
	Branch    string `json:"branch"`
	Message   string `json:"message,omitempty"`
	Author    string `json:"author,omitempty"`
}

type workspaceRevertRequest struct {
	ProjectID string `json:"project_id"`
	Branch    string `json:"branch"`
	CommitID  string `json:"commit_id"`
	// ExpectedCurrentCommitID is an optimistic-concurrency guard (finding
	// H7, 2026-09-03 review): when non-empty, revert is refused unless it
	// matches the branch's latest commit, so a stale client cannot discard
	// commits it never saw.
	ExpectedCurrentCommitID string `json:"expected_current_commit_id,omitempty"`
	Force                   bool   `json:"force,omitempty"`
	Reindex                 bool   `json:"reindex,omitempty"`
	Generation              string `json:"generation,omitempty"`
	Collection              string `json:"collection,omitempty"`
	ChunkStrategy           string `json:"chunk_strategy,omitempty"`
	MinChunkChars           int    `json:"min_chunk_chars,omitempty"`
	MaxChunkChars           int    `json:"max_chunk_chars,omitempty"`
	OverlapChars            int    `json:"overlap_chars,omitempty"`
	SnapToSentence          *bool  `json:"snap_to_sentence,omitempty"`
	ActivateGeneration      *bool  `json:"activate_generation,omitempty"`
}

type workspaceSearchRequest struct {
	ProjectID    string   `json:"project_id"`
	Branch       string   `json:"branch"`
	Generation   string   `json:"generation,omitempty"`
	Collection   string   `json:"collection,omitempty"`
	SearchQuery  string   `json:"search_query,omitempty"`
	Query        string   `json:"query,omitempty"`
	SearchType   string   `json:"search_type,omitempty"`
	TopK         int      `json:"top_k,omitempty"`
	Room         string   `json:"room,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	Mode         string   `json:"mode,omitempty"`
	Rerank       bool     `json:"rerank,omitempty"`
	ParentChild  bool     `json:"parent_child,omitempty"`
	MultiQuery   bool     `json:"multi_query,omitempty"`
	Dedup        *bool    `json:"dedup,omitempty"`
	GraphRerank  bool     `json:"graph_rerank,omitempty"`
	VectorWeight float64  `json:"vector_weight,omitempty"`
	BM25Weight   float64  `json:"bm25_weight,omitempty"`
}

type workspaceSearchFreshness struct {
	Stale                      bool   `json:"stale"`
	PotentiallyStale           bool   `json:"potentially_stale"`
	Reason                     string `json:"reason,omitempty"`
	ActiveGeneration           string `json:"active_generation,omitempty"`
	RequestedGeneration        string `json:"requested_generation,omitempty"`
	ResolvedGeneration         string `json:"resolved_generation,omitempty"`
	LastIndexedAt              string `json:"last_indexed_at,omitempty"`
	ActiveChunkCount           int    `json:"active_chunk_count"`
	ActivePathCount            int    `json:"active_path_count"`
	WatcherEnabled             bool   `json:"watcher_enabled"`
	WatcherPending             int    `json:"watcher_pending_branches"`
	WatcherLastReconcile       string `json:"watcher_last_reconcile_at,omitempty"`
	WatcherLastError           string `json:"watcher_last_error,omitempty"`
	WatcherBranchPending       bool   `json:"watcher_branch_pending"`
	WatcherBranchLastScan      string `json:"watcher_branch_last_scan_at,omitempty"`
	WatcherBranchLastChange    string `json:"watcher_branch_last_change_at,omitempty"`
	WatcherBranchLastReconcile string `json:"watcher_branch_last_reconcile_at,omitempty"`
	WatcherBranchLastError     string `json:"watcher_branch_last_error,omitempty"`
}

type workspaceSearchTarget struct {
	Manifest     *workspace.Manifest
	ManifestPath string
	Branch       string
	Generation   string
	Collection   string
	Chunks       []workspace.ChunkRecord
}

type workspaceResponse struct {
	ProjectID        string `json:"project_id"`
	Branch           string `json:"branch"`
	ManifestPath     string `json:"manifest_path"`
	ActiveGeneration string `json:"active_generation,omitempty"`
}

type workspaceIndexResponse struct {
	workspaceResponse
	Result workspace.IndexResult `json:"result"`
}

type workspaceDeleteResponse struct {
	workspaceResponse
	DeletedVectorIDs []string `json:"deleted_vector_ids"`
}

type workspaceGCResponse struct {
	workspaceResponse
	Result workspace.GCResult `json:"result"`
}

type workspaceReadResponse struct {
	ProjectID  string                    `json:"project_id"`
	Branch     string                    `json:"branch"`
	Path       string                    `json:"path"`
	Text       string                    `json:"text"`
	FileDigest string                    `json:"file_digest"`
	Citation   workspaceSourceCitation   `json:"citation"`
	Citations  []workspaceSourceCitation `json:"citations,omitempty"`
	Chunks     []workspace.ChunkRecord   `json:"chunks,omitempty"`
}

type workspaceWriteResponse struct {
	ProjectID string                  `json:"project_id"`
	Branch    string                  `json:"branch"`
	Path      string                  `json:"path"`
	Bytes     int                     `json:"bytes"`
	Indexed   *workspaceIndexResponse `json:"indexed,omitempty"`
}

type workspaceReindexResponse struct {
	workspaceResponse
	Results []workspace.IndexResult `json:"results"`
}

type workspaceReconcileResponse struct {
	workspaceResponse
	Paths   []string                `json:"paths"`
	Results []workspace.IndexResult `json:"results"`
}

type workspaceRunResponse struct {
	ProjectID string            `json:"project_id"`
	Branch    string            `json:"branch"`
	RunID     string            `json:"run_id"`
	Path      string            `json:"path"`
	Files     map[string]string `json:"files"`
}

type workspaceCommitFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type workspaceCommitRecord struct {
	ProjectID string                `json:"project_id"`
	Branch    string                `json:"branch"`
	CommitID  string                `json:"commit_id"`
	Message   string                `json:"message,omitempty"`
	Author    string                `json:"author,omitempty"`
	CreatedAt string                `json:"created_at"`
	Path      string                `json:"path,omitempty"`
	Files     []workspaceCommitFile `json:"files"`
}

type workspaceLogResponse struct {
	ProjectID string                  `json:"project_id"`
	Branch    string                  `json:"branch"`
	Commits   []workspaceCommitRecord `json:"commits"`
}

type workspaceRevertResponse struct {
	ProjectID string                      `json:"project_id"`
	Branch    string                      `json:"branch"`
	CommitID  string                      `json:"commit_id"`
	Files     []workspaceCommitFile       `json:"files"`
	Indexed   *workspaceReconcileResponse `json:"indexed,omitempty"`
}

// RegisterWorkspaceAPI exposes the markdown-native workspace index lifecycle.
func RegisterWorkspaceAPI(app fiber.Router, cfg APIConfig) {
	app.Use("/workspace", workspaceAuditMiddleware(cfg))
	app.Get("/workspace/context", workspaceContextHandler(cfg))
	app.Post("/workspace/access/check", workspaceAccessCheckHandler(cfg))
	app.Get("/workspace/audit", workspaceAuditLogHandler(cfg))
	app.Get("/workspace/ops/status", workspaceOpsStatusHandler(cfg))
	app.Get("/workspace/context/artifacts", workspaceContextArtifactsHandler(cfg))
	app.Post("/workspace/context/artifacts/reindex", workspaceReindexArtifactsHandler(cfg))
	app.Get("/workspace/conflicts", workspaceConflictsHandler(cfg))
	app.Post("/workspace/index", workspaceIndexHandler(cfg))
	app.Post("/workspace/delete", workspaceDeleteHandler(cfg))
	app.Post("/workspace/gc", workspaceGCHandler(cfg))
	app.Get("/workspace/manifest", workspaceManifestHandler(cfg))
	app.Get("/workspace/read", workspaceReadHandler(cfg))
	app.Post("/workspace/search", workspaceSearchHandler(cfg))
	app.Post("/workspace/write", workspaceWriteHandler(cfg))
	app.Post("/workspace/reindex", workspaceReindexHandler(cfg))
	app.Post("/workspace/reconcile", workspaceReconcileHandler(cfg))
	app.Get("/workspace/jobs", workspaceIndexJobsHandler(cfg))
	app.Post("/workspace/jobs/enqueue", workspaceEnqueueIndexJobHandler(cfg))
	app.Post("/workspace/jobs/retry", workspaceRetryIndexJobHandler(cfg))
	app.Get("/workspace/watch/status", workspaceWatchStatusHandler(cfg))
	app.Post("/workspace/runs/start", workspaceRunStartHandler(cfg))
	app.Get("/workspace/runs/get", workspaceRunGetHandler(cfg))
	app.Post("/workspace/commit", workspaceCommitHandler(cfg))
	app.Get("/workspace/log", workspaceLogHandler(cfg))
	app.Post("/workspace/revert", workspaceRevertHandler(cfg))
}

func workspaceIndexHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req workspaceIndexRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessWrite); err != nil {
			return err
		}
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		resp, err := indexWorkspaceMarkdownAuthorized(ctx, cfg, req, uploadMetadataActor(c, cfg, ctx))
		if err != nil {
			var authorityError *fiber.Error
			if errors.As(err, &authorityError) {
				return authorityError
			}
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(resp)
	}
}

func workspaceDeleteHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req workspaceDeleteRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessWrite); err != nil {
			return err
		}
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		resp, err := deleteWorkspaceMarkdownAuthorized(ctx, cfg, req, uploadMetadataActor(c, cfg, ctx))
		if err != nil {
			var authorityErr *fiber.Error
			if errors.As(err, &authorityErr) {
				return authorityErr
			}
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(resp)
	}
}

func workspaceGCHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req workspaceGCRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessWrite); err != nil {
			return err
		}
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		resp, err := gcWorkspaceGenerationsAuthorized(ctx, cfg, req, uploadMetadataActor(c, cfg, ctx))
		if err != nil {
			var authorityErr *fiber.Error
			if errors.As(err, &authorityErr) {
				return authorityErr
			}
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(resp)
	}
}

func workspaceManifestHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		projectID := c.Query("project_id")
		branch := c.Query("branch", "main")
		if err := authorizeWorkspaceFiber(c, cfg, projectID, workspaceAccessRead); err != nil {
			return err
		}
		manifest, path, err := loadWorkspaceManifest(cfg, projectID, branch)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err := c.JSON(fiber.Map{
			"project_id":          manifest.ProjectID,
			"branch":              manifest.Branch,
			"manifest_path":       path,
			"active_generation":   manifest.ActiveGeneration,
			"generations":         manifest.Generations,
			"chunks":              manifest.ListChunks(workspace.ChunkFilter{}),
			"chunks_count":        len(manifest.Chunks),
			"workspace_manifest":  manifest,
			"manifest_version":    manifest.Version,
			"workspace_root_path": workspaceRoot(cfg),
		}); err != nil {
			return err
		}
		return sendWorkspaceProtectedResponse(c, cfg, c.UserContext(), uploadMetadataActor(c, cfg, c.UserContext()), projectID)
	}
}

func workspaceReadHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		req := workspaceReadRequest{
			ProjectID: c.Query("project_id"),
			Branch:    c.Query("branch", "main"),
			Path:      c.Query("path"),
		}
		if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessRead); err != nil {
			return err
		}
		resp, err := readWorkspaceMarkdownAuthorized(c.UserContext(), cfg, req, uploadMetadataActor(c, cfg, c.UserContext()))
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err := c.JSON(resp); err != nil {
			return err
		}
		return sendWorkspaceProtectedResponse(c, cfg, c.UserContext(), uploadMetadataActor(c, cfg, c.UserContext()), req.ProjectID)
	}
}

func workspaceSearchHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := searchRequestContext(c)
		defer cancel()
		actor := workspaceActorFromFiber(c)
		ctx = context.WithValue(ctx, mcpUserIDKey, actor.UserID)
		ctx = context.WithValue(ctx, mcpAPIKeyPermissionsKey, actor.APIKeyPermissions)
		ctx = context.WithValue(ctx, mcp.TenantIDKey, actor.TenantID)
		ctx = searchEgressContext(c, cfg, ctx)
		c.SetUserContext(ctx)
		var req workspaceSearchRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessRead); err != nil {
			return err
		}
		target, err := resolveWorkspaceSearchTarget(cfg, req)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		searchArgs, err := workspaceSearchArgs(req, target)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		trackWorkspaceSearchProject(ctx, req.ProjectID)
		searchCtx := context.WithValue(c.UserContext(), workspaceSearchScopeKey{}, target)
		inner := (&mcpHandler{cfg: cfg}).toolSearch(searchCtx, searchArgs)
		resp := workspaceSearchResponse(req, target, workspaceSearchFreshnessFor(req, target, workspaceWatchStatus(cfg)), inner)
		if inner.IsError {
			c.Status(fiber.StatusBadRequest)
		}
		if err := c.JSON(resp); err != nil {
			return err
		}
		return sendProtectedResponseWithFence(c, ctx)
	}
}

func workspaceWriteHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req workspaceWriteRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessWrite); err != nil {
			return err
		}
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		resp, err := writeWorkspaceMarkdownAuthorized(ctx, cfg, req, uploadMetadataActor(c, cfg, ctx))
		if err != nil {
			var authorityErr *fiber.Error
			if errors.As(err, &authorityErr) {
				return authorityErr
			}
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(resp)
	}
}

func workspaceReindexHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req workspaceReindexRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessWrite); err != nil {
			return err
		}
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		resp, err := reindexWorkspaceMarkdownAuthorized(ctx, cfg, req, uploadMetadataActor(c, cfg, ctx))
		if err != nil {
			var authorityErr *fiber.Error
			if errors.As(err, &authorityErr) {
				return authorityErr
			}
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(resp)
	}
}

func workspaceReconcileHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req workspaceReconcileRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessWrite); err != nil {
			return err
		}
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		resp, err := reconcileWorkspaceMarkdownAuthorized(ctx, cfg, req, uploadMetadataActor(c, cfg, ctx))
		if err != nil {
			var authorityErr *fiber.Error
			if errors.As(err, &authorityErr) {
				return authorityErr
			}
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(resp)
	}
}

func workspaceIndexJobsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		req := workspaceIndexJobsRequest{
			ProjectID: c.Query("project_id"),
			Branch:    c.Query("branch", "main"),
			Status:    c.Query("status"),
		}
		if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessRead); err != nil {
			return err
		}
		jobs, err := listWorkspaceIndexJobs(cfg, req)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err := c.JSON(fiber.Map{
			"project_id": req.ProjectID,
			"branch":     defaultBranch(req.Branch),
			"jobs":       jobs,
			"total":      len(jobs),
			"by_status":  workspaceJobStatusSummary(jobs),
		}); err != nil {
			return err
		}
		return sendWorkspaceProtectedResponse(c, cfg, c.UserContext(), uploadMetadataActor(c, cfg, c.UserContext()), req.ProjectID)
	}
}

func workspaceEnqueueIndexJobHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var payload workspaceIndexJobPayload
		if err := c.BodyParser(&payload); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err := authorizeWorkspaceFiber(c, cfg, payload.ProjectID, workspaceAccessWrite); err != nil {
			return err
		}
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		job, err := enqueueWorkspaceIndexJobFromPayloadAuthorized(ctx, cfg, payload, uploadMetadataActor(c, cfg, ctx))
		if err != nil {
			var authorityErr *fiber.Error
			if errors.As(err, &authorityErr) {
				return authorityErr
			}
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"job": job})
	}
}

func workspaceRetryIndexJobHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req workspaceRetryIndexJobRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessWrite); err != nil {
			return err
		}
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		resp, err := retryWorkspaceIndexJobAuthorized(ctx, cfg, req, uploadMetadataActor(c, cfg, ctx))
		if err != nil {
			var authorityErr *fiber.Error
			if errors.As(err, &authorityErr) {
				return authorityErr
			}
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(resp)
	}
}

func workspaceWatchStatusHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		projectID, branch := c.Query("project_id"), c.Query("branch")
		if err := authorizeWorkspaceFiber(c, cfg, projectID, workspaceAccessRead); err != nil {
			return err
		}
		watch := workspaceWatchStatus(cfg)
		if projectID != "" {
			watch = workspaceProjectWatchStatus(watch, projectID, branch)
		}
		if err := c.JSON(watch); err != nil {
			return err
		}
		return sendWorkspaceProtectedResponse(c, cfg, c.UserContext(), uploadMetadataActor(c, cfg, c.UserContext()), projectID)
	}
}

func workspaceRunStartHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req workspaceRunStartRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessWrite); err != nil {
			return err
		}
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		resp, err := startWorkspaceRunAuthorized(ctx, cfg, req, uploadMetadataActor(c, cfg, ctx))
		if err != nil {
			var authorityErr *fiber.Error
			if errors.As(err, &authorityErr) {
				return authorityErr
			}
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(resp)
	}
}

func workspaceRunGetHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		req := workspaceRunGetRequest{
			ProjectID: c.Query("project_id"),
			Branch:    c.Query("branch", "main"),
			RunID:     c.Query("run_id"),
		}
		if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessRead); err != nil {
			return err
		}
		resp, err := getWorkspaceRun(cfg, req)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err := c.JSON(resp); err != nil {
			return err
		}
		return sendWorkspaceProtectedResponse(c, cfg, c.UserContext(), uploadMetadataActor(c, cfg, c.UserContext()), req.ProjectID)
	}
}

func workspaceCommitHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req workspaceCommitRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessWrite); err != nil {
			return err
		}
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		resp, err := commitWorkspaceAuthorized(ctx, cfg, req, uploadMetadataActor(c, cfg, ctx))
		if err != nil {
			var authorityErr *fiber.Error
			if errors.As(err, &authorityErr) {
				return authorityErr
			}
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(resp)
	}
}

func workspaceLogHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		req := workspaceCommitRequest{
			ProjectID: c.Query("project_id"),
			Branch:    c.Query("branch", "main"),
		}
		if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessRead); err != nil {
			return err
		}
		resp, err := logWorkspaceCommits(cfg, req)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err := c.JSON(resp); err != nil {
			return err
		}
		return sendWorkspaceProtectedResponse(c, cfg, c.UserContext(), uploadMetadataActor(c, cfg, c.UserContext()), req.ProjectID)
	}
}

func workspaceRevertHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req workspaceRevertRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessWrite); err != nil {
			return err
		}
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		resp, err := revertWorkspaceAuthorized(ctx, cfg, req, uploadMetadataActor(c, cfg, ctx))
		if err != nil {
			var authorityErr *fiber.Error
			if errors.As(err, &authorityErr) {
				return authorityErr
			}
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(resp)
	}
}

// workspaceActorFromFiber builds the transport-independent access.Actor from
// Fiber locals so REST handlers carry the same principal shape as MCP.
func workspaceActorFromFiber(c *fiber.Ctx) accesspkg.Actor {
	userID, _ := c.Locals("user_id").(string)
	perms, _ := c.Locals("api_key_permissions").(string)
	tenantID, _ := c.Locals("tenant_id").(string)
	// Actor snapshots may outlive Fiber's pooled request buffers.
	return accesspkg.Actor{UserID: strings.Clone(userID), APIKeyPermissions: strings.Clone(perms), TenantID: strings.Clone(tenantID)}
}

// workspaceActorFromMCP builds the access.Actor from MCP call context.
func workspaceActorFromMCP(ctx context.Context) accesspkg.Actor {
	userID, _ := ctx.Value(mcpUserIDKey).(string)
	permissions, _ := ctx.Value(mcpAPIKeyPermissionsKey).(string)
	tenantID, _ := ctx.Value(mcp.TenantIDKey).(string)
	return accesspkg.Actor{UserID: userID, APIKeyPermissions: permissions, TenantID: tenantID}
}

// authorizeWorkspace is the single shared decision used by both transports.
// REST and MCP differ only in how they build the Actor; the policy code is the
// same, satisfying REST/MCP workspace parity.
func authorizeWorkspace(ctx context.Context, db accessDB, actor accesspkg.Actor, projectID string, level workspaceAccessLevel) (accesspkg.Decision, error) {
	if level == "" {
		level = workspaceAccessRead
	}
	return accesspkg.SQLPolicy{DB: db, Q: Q}.Authorize(ctx, actor, accesspkg.Resource{
		Kind: accesspkg.ResourceWorkspace,
		ID:   projectID,
	}, string(level))
}

func authorizeWorkspaceFiber(c *fiber.Ctx, cfg APIConfig, projectID string, level workspaceAccessLevel) error {
	if projectID == "" {
		if cfg.RequireAuth || workspaceActorFromFiber(c).UserID != "" {
			return fiber.NewError(fiber.StatusBadRequest, workspace.ErrMissingProjectID.Error())
		}
		return nil
	}
	decision, err := authorizeWorkspace(c.UserContext(), cfg.DB, workspaceActorFromFiber(c), projectID, level)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "workspace access check failed")
	}
	if !decision.Allowed {
		return fiber.NewError(fiber.StatusForbidden, errWorkspaceAccessDenied.Error())
	}
	return nil
}

func authorizeWorkspaceMCP(ctx context.Context, cfg APIConfig, projectID string, level workspaceAccessLevel) error {
	if projectID == "" {
		if cfg.RequireAuth || workspaceActorFromMCP(ctx).UserID != "" {
			return workspace.ErrMissingProjectID
		}
		return nil
	}
	decision, err := authorizeWorkspace(ctx, cfg.DB, workspaceActorFromMCP(ctx), projectID, level)
	if err != nil {
		return err
	}
	if !decision.Allowed {
		return errWorkspaceAccessDenied
	}
	return nil
}

// Recheck authority after response construction and retain it through body drain.
func sendWorkspaceProtectedResponse(c *fiber.Ctx, cfg APIConfig, ctx context.Context, actor accesspkg.MetadataActor, projectID string) error {
	if err := ctx.Err(); err != nil {
		return fiber.NewError(fiber.StatusGatewayTimeout, "workspace request canceled")
	}
	deadline := time.Now().Add(timeoutFromEnvMs("SEARCH_REQUEST_TIMEOUT_MS", defaultSearchRequestTimeout))
	if incoming, ok := ctx.Deadline(); ok && incoming.Before(deadline) {
		deadline = incoming
	}
	if actor.Credential.ExpiresAt > 0 {
		expires := time.Unix(actor.Credential.ExpiresAt, 0)
		if !time.Now().Before(expires) {
			return fiber.NewError(fiber.StatusForbidden, "workspace credential expired")
		}
		if expires.Before(deadline) {
			deadline = expires
		}
	}
	fenceCtx, cancel := context.WithDeadline(ctx, deadline)
	release, err := beginWorkspaceEffectFence(fenceCtx, cfg, actor, projectID, workspaceAccessRead)
	if err != nil {
		cancel()
		return err
	}
	if err := fenceCtx.Err(); err != nil {
		release()
		cancel()
		return fiber.NewError(fiber.StatusGatewayTimeout, "workspace request canceled")
	}
	streamCtx, streamCancel := context.WithDeadline(context.WithoutCancel(fenceCtx), deadline)
	return sendFencedResponse(c, streamCtx, func() { release(); streamCancel(); cancel() })
}

func workspaceRequestContext(ctx context.Context, actor accesspkg.MetadataActor, timeout time.Duration) (context.Context, context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, fiber.NewError(fiber.StatusGatewayTimeout, "workspace request canceled")
	}
	deadline := time.Now().Add(timeout)
	if actor.Credential.ExpiresAt > 0 {
		expires := time.Unix(actor.Credential.ExpiresAt, 0)
		if !time.Now().Before(expires) {
			return nil, nil, fiber.NewError(fiber.StatusForbidden, "workspace credential expired")
		}
		if expires.Before(deadline) {
			deadline = expires
		}
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	return bounded, cancel, nil
}

// Keep workspace index authority stable until the bounded provider/effect drains.
// Reads use the fence transaction, including with a single-connection SQL pool.
func beginWorkspaceEffectFence(ctx context.Context, cfg APIConfig, actor accesspkg.MetadataActor, projectID string, level workspaceAccessLevel) (func(), error) {
	if actor.TrustedLocal && actor.UserID == "" && actor.TenantID == "" && !cfg.RequireAuth {
		// Global local diagnostics have no project file tree to lock.
		if projectID == "" && level == workspaceAccessRead {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return func() {}, nil
		}
		release, err := workspace.LockProject(ctx, workspaceRoot(cfg), projectID)
		if err != nil {
			return nil, err
		}
		if err := recoverWorkspaceRestores(ctx, cfg, projectID); err != nil {
			release()
			return nil, err
		}
		return release, nil
	}
	if actor.UserID == "" || (cfg.RequireAuth && actor.TrustedLocal) {
		return nil, fiber.NewError(fiber.StatusForbidden, "workspace requires verified authority")
	}
	policy, release, err := (accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}).BeginTransferFence(ctx, GetDBProvider() == DBSQLite)
	if err != nil {
		return nil, fiber.NewError(fiber.StatusServiceUnavailable, "workspace authorization unavailable")
	}
	if err := recheckDatasetShareActor(ctx, policy, actor); err != nil {
		release()
		return nil, fiber.NewError(fiber.StatusForbidden, "workspace access revoked")
	}
	decision, err := policy.AuthorizeWorkspace(ctx, accesspkg.WorkspaceRequest{
		UserID: actor.UserID, TenantID: actor.TenantID, ProjectID: projectID, Action: string(level), APIKeyPermissions: actor.APIKeyPermissions,
	})
	if err != nil || !decision.Allowed {
		release()
		return nil, fiber.NewError(fiber.StatusForbidden, errWorkspaceAccessDenied.Error())
	}
	filesRelease, err := workspace.LockProject(ctx, workspaceRoot(cfg), projectID)
	if err != nil {
		release()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		filesRelease()
		release()
		return nil, err
	}
	if err := recoverWorkspaceRestores(ctx, cfg, projectID); err != nil {
		filesRelease()
		release()
		return nil, err
	}
	return func() { filesRelease(); release() }, nil
}

func validateWorkspaceCollection(collection string) error {
	if collection == "_memories" || strings.HasPrefix(collection, "_memories_") {
		return errors.New("reserved memory collection is not a workspace index destination")
	}
	return nil
}

func indexWorkspaceMarkdownAuthorized(ctx context.Context, cfg APIConfig, req workspaceIndexRequest, actor accesspkg.MetadataActor) (workspaceIndexResponse, error) {
	ctx, cancel, err := workspaceRequestContext(ctx, actor, 30*time.Second)
	if err != nil {
		return workspaceIndexResponse{}, err
	}
	defer cancel()
	// Match task writes: acquire the branch lock before the SQL authority fence.
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, req.Branch))
	defer unlock()
	release, err := beginWorkspaceEffectFence(ctx, cfg, actor, req.ProjectID, workspaceAccessWrite)
	if err != nil {
		return workspaceIndexResponse{}, err
	}
	defer release()
	return indexWorkspaceMarkdownLocked(ctx, cfg, req)
}

// indexWorkspaceMarkdownLocked is the index core; caller must already hold
// the branch manifest lock (reconcile holds it across the whole batch).
func indexWorkspaceMarkdownLocked(ctx context.Context, cfg APIConfig, req workspaceIndexRequest) (workspaceIndexResponse, error) {
	if req.Generation == "" {
		return workspaceIndexResponse{}, workspace.ErrMissingGeneration
	}
	if !utf8.ValidString(req.Text) {
		return workspaceIndexResponse{}, errors.New("workspace text must be valid UTF-8")
	}
	_, relative, err := workspaceFilePath(cfg, req.ProjectID, defaultBranch(req.Branch), req.Path)
	if err != nil {
		return workspaceIndexResponse{}, err
	}
	result, err := publishWorkspaceMarkdownBatchLocked(ctx, cfg, workspaceReindexRequest{
		ProjectID: req.ProjectID, Branch: defaultBranch(req.Branch), Generation: req.Generation, Collection: req.Collection,
		CommitHash: req.CommitHash, ChunkStrategy: req.ChunkStrategy, MinChunkChars: req.MinChunkChars, MaxChunkChars: req.MaxChunkChars,
		OverlapChars: req.OverlapChars, SnapToSentence: req.SnapToSentence, ActivateGeneration: req.ActivateGeneration,
	}, []workspace.MarkdownFile{{Path: relative, Text: req.Text, FileDigest: digestText(req.Text), DocumentID: req.DocumentID, Title: req.Title, Room: req.Room, Tags: req.Tags}}, nil, false, false)
	if err != nil {
		return workspaceIndexResponse{}, err
	}
	return workspaceIndexResponse{workspaceResponse: result.workspaceResponse, Result: result.Results[0]}, nil
}
func readWorkspaceMarkdown(cfg APIConfig, req workspaceReadRequest) (workspaceReadResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, req.Branch))
	defer unlock()
	release, err := workspace.LockProject(ctx, workspaceRoot(cfg), req.ProjectID)
	if err != nil {
		return workspaceReadResponse{}, err
	}
	defer release()
	if err := recoverWorkspaceRestores(ctx, cfg, req.ProjectID); err != nil {
		return workspaceReadResponse{}, err
	}
	return readWorkspaceMarkdownLocked(cfg, req)
}

func readWorkspaceMarkdownAuthorized(ctx context.Context, cfg APIConfig, req workspaceReadRequest, actor accesspkg.MetadataActor) (workspaceReadResponse, error) {
	ctx, cancel, err := workspaceRequestContext(ctx, actor, 30*time.Second)
	if err != nil {
		return workspaceReadResponse{}, err
	}
	defer cancel()
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, req.Branch))
	defer unlock()
	release, err := beginWorkspaceEffectFence(ctx, cfg, actor, req.ProjectID, workspaceAccessRead)
	if err != nil {
		return workspaceReadResponse{}, err
	}
	defer release()
	return readWorkspaceMarkdownLocked(cfg, req)
}

func readWorkspaceMarkdownLocked(cfg APIConfig, req workspaceReadRequest) (workspaceReadResponse, error) {
	branch := defaultBranch(req.Branch)
	filePath, relPath, err := workspaceFilePath(cfg, req.ProjectID, branch, req.Path)
	if err != nil {
		return workspaceReadResponse{}, err
	}
	data, err := readWorkspaceFile(cfg, filePath)
	if err != nil {
		return workspaceReadResponse{}, err
	}
	if !utf8.Valid(data) {
		return workspaceReadResponse{}, errors.New("workspace text must be valid UTF-8")
	}
	manifest, _, err := loadWorkspaceManifest(cfg, req.ProjectID, branch)
	if err != nil {
		return workspaceReadResponse{}, err
	}
	chunks := manifest.ListChunks(workspace.ChunkFilter{ProjectID: req.ProjectID, Branch: branch, Path: relPath})
	return workspaceReadResponse{ProjectID: req.ProjectID, Branch: branch, Path: relPath, Text: string(data), FileDigest: digestBytes(data),
		Citation: workspaceFileCitation(req.ProjectID, branch, relPath), Citations: workspaceCitationsFromChunks(req.ProjectID, branch, relPath, chunks), Chunks: chunks}, nil
}

func writeWorkspaceMarkdown(ctx context.Context, cfg APIConfig, req workspaceWriteRequest) (workspaceWriteResponse, error) {
	if execution := mcp.TaskExecutionFromContext(ctx); execution != nil {
		return taskWriteWorkspaceMarkdown(ctx, cfg, req, execution)
	}
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, req.Branch))
	defer unlock()
	release, err := workspace.LockProject(ctx, workspaceRoot(cfg), req.ProjectID)
	if err != nil {
		return workspaceWriteResponse{}, err
	}
	defer release()
	if err := recoverWorkspaceRestores(ctx, cfg, req.ProjectID); err != nil {
		return workspaceWriteResponse{}, err
	}
	return writeWorkspaceMarkdownLocked(ctx, cfg, req)
}

func writeWorkspaceMarkdownAuthorized(ctx context.Context, cfg APIConfig, req workspaceWriteRequest, actor accesspkg.MetadataActor) (workspaceWriteResponse, error) {
	if mcp.TaskExecutionFromContext(ctx) != nil {
		return writeWorkspaceMarkdown(ctx, cfg, req)
	}
	ctx, cancel, err := workspaceRequestContext(ctx, actor, 30*time.Second)
	if err != nil {
		return workspaceWriteResponse{}, err
	}
	defer cancel()
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, req.Branch))
	defer unlock()
	release, err := beginWorkspaceEffectFence(ctx, cfg, actor, req.ProjectID, workspaceAccessWrite)
	if err != nil {
		return workspaceWriteResponse{}, err
	}
	defer release()
	return writeWorkspaceMarkdownLocked(ctx, cfg, req)
}

func writeWorkspaceMarkdownLocked(ctx context.Context, cfg APIConfig, req workspaceWriteRequest) (workspaceWriteResponse, error) {
	branch := defaultBranch(req.Branch)
	filePath, relPath, err := workspaceFilePath(cfg, req.ProjectID, branch, req.Path)
	if err != nil {
		return workspaceWriteResponse{}, err
	}
	shouldIndex := req.Generation != ""
	if req.Index != nil {
		shouldIndex = *req.Index
	}
	if shouldIndex {
		if err := validateWorkspaceCollection(req.Collection); err != nil {
			return workspaceWriteResponse{}, err
		}
	}
	if req.ExpectedFileDigest != nil {
		currentDigest := ""
		if current, err := readWorkspaceFile(cfg, filePath); err == nil {
			currentDigest = digestBytes(current)
		} else if !errors.Is(err, os.ErrNotExist) {
			return workspaceWriteResponse{}, err
		}
		if currentDigest != *req.ExpectedFileDigest {
			return workspaceWriteResponse{}, fmt.Errorf("workspace write conflict: current file digest %q does not match expected_file_digest %q", currentDigest, *req.ExpectedFileDigest)
		}
	}
	if !utf8.ValidString(req.Text) {
		return workspaceWriteResponse{}, errors.New("workspace text must be valid UTF-8")
	}
	if err := writeWorkspaceFile(ctx, cfg, filePath, []byte(req.Text)); err != nil {
		return workspaceWriteResponse{}, err
	}
	resp := workspaceWriteResponse{
		ProjectID: req.ProjectID,
		Branch:    branch,
		Path:      relPath,
		Bytes:     len([]byte(req.Text)),
	}
	if shouldIndex {
		// Lock already held (M15): use the locked variant.
		indexResp, err := indexWorkspaceMarkdownLocked(ctx, cfg, workspaceIndexRequest{
			ProjectID:          req.ProjectID,
			Branch:             branch,
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
			Text:               req.Text,
			FileDigest:         req.FileDigest,
			DocumentID:         req.DocumentID,
			Title:              req.Title,
			Room:               req.Room,
			Tags:               req.Tags,
		})
		if err != nil {
			return workspaceWriteResponse{}, err
		}
		resp.Indexed = &indexResp
	}
	return resp, nil
}

func reindexWorkspaceMarkdownAuthorized(ctx context.Context, cfg APIConfig, req workspaceReindexRequest, actor accesspkg.MetadataActor) (workspaceReindexResponse, error) {
	ctx, cancel, err := workspaceRequestContext(ctx, actor, 30*time.Second)
	if err != nil {
		return workspaceReindexResponse{}, err
	}
	defer cancel()
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, req.Branch))
	defer unlock()
	release, err := beginWorkspaceEffectFence(ctx, cfg, actor, req.ProjectID, workspaceAccessWrite)
	if err != nil {
		return workspaceReindexResponse{}, err
	}
	defer release()
	return reindexWorkspaceMarkdownLocked(ctx, cfg, req, &workspaceIndexJobAuthority{Actor: &actor})
}

func reindexWorkspaceMarkdownLocked(ctx context.Context, cfg APIConfig, req workspaceReindexRequest, authorities ...*workspaceIndexJobAuthority) (workspaceReindexResponse, error) {
	branch := defaultBranch(req.Branch)
	req.Branch = branch
	if req.ProjectID == "" {
		return workspaceReindexResponse{}, workspace.ErrMissingProjectID
	}
	if req.Generation == "" {
		return workspaceReindexResponse{}, workspace.ErrMissingGeneration
	}
	if len(req.Paths) == 0 {
		return workspaceReindexResponse{}, errors.New("paths required")
	}
	if err := validateWorkspaceCollection(req.Collection); err != nil {
		return workspaceReindexResponse{}, err
	}
	job, err := beginWorkspaceIndexJob(cfg, workspaceIndexJobPayloadFromReindex("reindex", req, false), authorities...)
	if err != nil {
		return workspaceReindexResponse{}, err
	}
	resp, runErr := reindexWorkspaceMarkdownDirectLocked(ctx, cfg, req)
	if _, finishErr := finishWorkspaceIndexJob(cfg, job, runErr); finishErr != nil {
		return workspaceReindexResponse{}, finishErr
	}
	if err := ctx.Err(); err != nil {
		return resp, err
	}
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		return resp, runErr
	}
	return resp, nil
}

func reindexWorkspaceMarkdownDirectLocked(ctx context.Context, cfg APIConfig, req workspaceReindexRequest) (workspaceReindexResponse, error) {
	if req.ProjectID == "" {
		return workspaceReindexResponse{}, workspace.ErrMissingProjectID
	}
	if req.Generation == "" {
		return workspaceReindexResponse{}, workspace.ErrMissingGeneration
	}
	if len(req.Paths) == 0 {
		return workspaceReindexResponse{}, errors.New("paths required")
	}
	files := make([]workspace.MarkdownFile, 0, len(req.Paths))
	seen := map[string]bool{}
	for _, name := range req.Paths {
		if err := ctx.Err(); err != nil {
			return workspaceReindexResponse{}, err
		}
		file, err := workspaceMarkdownFile(cfg, req.ProjectID, defaultBranch(req.Branch), name)
		if err != nil {
			return workspaceReindexResponse{}, err
		}
		if seen[file.Path] {
			continue
		}
		seen[file.Path] = true
		file.Room, file.Tags = req.Room, req.Tags
		files = append(files, file)
	}
	return publishWorkspaceMarkdownBatchLocked(ctx, cfg, req, files, nil, true, false)
}
func reconcileWorkspaceMarkdownAuthorized(ctx context.Context, cfg APIConfig, req workspaceReconcileRequest, actor accesspkg.MetadataActor) (workspaceReconcileResponse, error) {
	ctx, cancel, err := workspaceRequestContext(ctx, actor, 30*time.Second)
	if err != nil {
		return workspaceReconcileResponse{}, err
	}
	defer cancel()
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, req.Branch))
	defer unlock()
	release, err := beginWorkspaceEffectFence(ctx, cfg, actor, req.ProjectID, workspaceAccessWrite)
	if err != nil {
		return workspaceReconcileResponse{}, err
	}
	defer release()
	return reconcileWorkspaceMarkdownLocked(ctx, cfg, req, &workspaceIndexJobAuthority{Actor: &actor})
}

func reconcileWorkspaceMarkdownLocked(ctx context.Context, cfg APIConfig, req workspaceReconcileRequest, authorities ...*workspaceIndexJobAuthority) (workspaceReconcileResponse, error) {
	branch := defaultBranch(req.Branch)
	req.Branch = branch
	if req.ProjectID == "" {
		return workspaceReconcileResponse{}, workspace.ErrMissingProjectID
	}
	if req.Generation == "" {
		return workspaceReconcileResponse{}, workspace.ErrMissingGeneration
	}
	if err := validateWorkspaceCollection(req.Collection); err != nil {
		return workspaceReconcileResponse{}, err
	}
	payload := workspaceIndexJobPayloadFromReindex("reconcile", req.workspaceReindexRequest, req.DeleteMissing)
	job, err := beginWorkspaceIndexJob(cfg, payload, authorities...)
	if err != nil {
		return workspaceReconcileResponse{}, err
	}
	resp, runErr := reconcileWorkspaceMarkdownDirectLocked(ctx, cfg, req)
	if _, finishErr := finishWorkspaceIndexJob(cfg, job, runErr); finishErr != nil {
		return workspaceReconcileResponse{}, finishErr
	}
	if err := ctx.Err(); err != nil {
		return resp, err
	}
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		return resp, runErr
	}
	return resp, nil
}

func reconcileWorkspaceMarkdownService(ctx context.Context, cfg APIConfig, req workspaceReconcileRequest) (workspaceReconcileResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, req.Branch))
	defer unlock()
	target, release, err := beginWorkspaceServiceFence(ctx, cfg, workspaceWatchKey{ProjectID: req.ProjectID, Branch: req.Branch})
	if err != nil {
		return workspaceReconcileResponse{}, err
	}
	defer release()
	req.ProjectID, req.Branch = target.ProjectID, target.Branch
	return reconcileWorkspaceMarkdownLocked(ctx, cfg, req, &workspaceIndexJobAuthority{Service: true})
}

func reconcileWorkspaceMarkdownDirectLocked(ctx context.Context, cfg APIConfig, req workspaceReconcileRequest) (workspaceReconcileResponse, error) {
	req.Branch = defaultBranch(req.Branch)
	if req.ProjectID == "" {
		return workspaceReconcileResponse{}, workspace.ErrMissingProjectID
	}
	if req.Generation == "" {
		return workspaceReconcileResponse{}, workspace.ErrMissingGeneration
	}
	if err := validateWorkspaceCollection(req.Collection); err != nil {
		return workspaceReconcileResponse{}, err
	}
	manifest, _, err := loadWorkspaceManifest(cfg, req.ProjectID, req.Branch)
	if err != nil {
		return workspaceReconcileResponse{}, err
	}
	full := len(req.Paths) == 0
	paths := append([]string(nil), req.Paths...)
	if full {
		paths, err = listWorkspaceMarkdownPaths(cfg, workspaceProjectRoot(cfg, req.ProjectID, req.Branch))
		if err != nil {
			return workspaceReconcileResponse{}, err
		}
	}
	removed := map[string]bool{}
	files := make([]workspace.MarkdownFile, 0, len(paths))
	seen := map[string]bool{}
	selected := map[string]bool{}
	for _, name := range paths {
		if err := ctx.Err(); err != nil {
			return workspaceReconcileResponse{}, err
		}
		_, relative, err := workspaceFilePath(cfg, req.ProjectID, req.Branch, name)
		if err != nil {
			return workspaceReconcileResponse{}, err
		}
		selected[relative] = true
		if seen[relative] {
			continue
		}
		seen[relative] = true
		file, err := workspaceMarkdownFile(cfg, req.ProjectID, req.Branch, relative)
		if errors.Is(err, os.ErrNotExist) && req.DeleteMissing {
			removed[relative] = true
			continue
		}
		if err != nil {
			return workspaceReconcileResponse{}, err
		}
		file.Room, file.Tags = req.Room, req.Tags
		files = append(files, file)
	}
	if full && req.DeleteMissing {
		for _, record := range manifest.ListChunks(workspace.ChunkFilter{Generation: req.Generation}) {
			if !seen[record.Path] {
				removed[record.Path] = true
			}
		}
		for name := range manifest.Files[req.Generation] {
			if !seen[name] {
				removed[name] = true
			}
		}
	}
	// A selected-path activation of a new generation retains the other committed
	// paths by preparing their current bytes in that generation as well.
	if !full && req.ActivateGeneration && manifest.ActiveGeneration != "" && manifest.ActiveGeneration != req.Generation {
		keep := map[string]bool{}
		if manifest.Files[manifest.ActiveGeneration] == nil {
			all, err := listWorkspaceMarkdownPaths(cfg, workspaceProjectRoot(cfg, req.ProjectID, req.Branch))
			if err != nil {
				return workspaceReconcileResponse{}, err
			}
			for _, name := range all {
				keep[name] = true
			}
		}
		for name := range manifest.Files[manifest.ActiveGeneration] {
			keep[name] = true
		}
		for _, record := range manifest.ListChunks(workspace.ChunkFilter{Generation: manifest.ActiveGeneration}) {
			keep[record.Path] = true
		}
		names := make([]string, 0, len(keep))
		for name := range keep {
			if !selected[name] {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			file, err := workspaceMarkdownFile(cfg, req.ProjectID, req.Branch, name)
			if err != nil {
				return workspaceReconcileResponse{}, err
			}
			file.Room, file.Tags = req.Room, req.Tags
			files = append(files, file)
		}
	}
	result, err := publishWorkspaceMarkdownBatchLocked(ctx, cfg, req.workspaceReindexRequest, files, removed, true, full)
	if err != nil {
		return workspaceReconcileResponse{}, err
	}
	return workspaceReconcileResponse{workspaceResponse: result.workspaceResponse, Paths: paths, Results: result.Results}, nil
}
func startWorkspaceRun(cfg APIConfig, req workspaceRunStartRequest) (workspaceRunResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, req.Branch))
	defer unlock()
	release, err := workspace.LockProject(ctx, workspaceRoot(cfg), req.ProjectID)
	if err != nil {
		return workspaceRunResponse{}, err
	}
	defer release()
	if err := recoverWorkspaceRestores(ctx, cfg, req.ProjectID); err != nil {
		return workspaceRunResponse{}, err
	}
	return startWorkspaceRunLocked(ctx, cfg, req)
}

func startWorkspaceRunAuthorized(ctx context.Context, cfg APIConfig, req workspaceRunStartRequest, actor accesspkg.MetadataActor) (workspaceRunResponse, error) {
	ctx, cancel, err := workspaceRequestContext(ctx, actor, 30*time.Second)
	if err != nil {
		return workspaceRunResponse{}, err
	}
	defer cancel()
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, req.Branch))
	defer unlock()
	release, err := beginWorkspaceEffectFence(ctx, cfg, actor, req.ProjectID, workspaceAccessWrite)
	if err != nil {
		return workspaceRunResponse{}, err
	}
	defer release()
	return startWorkspaceRunLocked(ctx, cfg, req)
}

func startWorkspaceRunLocked(ctx context.Context, cfg APIConfig, req workspaceRunStartRequest) (workspaceRunResponse, error) {
	branch := defaultBranch(req.Branch)
	if req.ProjectID == "" {
		return workspaceRunResponse{}, workspace.ErrMissingProjectID
	}
	runID := req.RunID
	if runID == "" {
		runID = uuid.NewString()
	}
	runDir, err := workspaceRunDir(cfg, req.ProjectID, branch, runID)
	if err != nil {
		return workspaceRunResponse{}, err
	}
	root, err := openWorkspaceDirectory(cfg, runDir, true)
	if err != nil {
		return workspaceRunResponse{}, err
	}
	defer root.Close()
	files := map[string]string{"metadata.md": runMetadataMarkdown(req, runID)}
	if req.Prompt != "" {
		files["prompt.md"] = req.Prompt
	}
	if req.Command != "" {
		files["command.md"] = req.Command
	}
	if req.Result != "" {
		files["result.md"] = req.Result
	}
	for name, content := range files {
		if !utf8.ValidString(content) {
			return workspaceRunResponse{}, errors.New("workspace text must be valid UTF-8")
		}
		if err := workspace.WriteFile(ctx, root, name, []byte(content), 0644); err != nil {
			return workspaceRunResponse{}, err
		}
	}
	return workspaceRunResponse{ProjectID: req.ProjectID, Branch: branch, RunID: runID, Path: runDir, Files: files}, nil
}

func getWorkspaceRun(cfg APIConfig, req workspaceRunGetRequest) (workspaceRunResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, req.Branch))
	defer unlock()
	release, err := workspace.LockProject(ctx, workspaceRoot(cfg), req.ProjectID)
	if err != nil {
		return workspaceRunResponse{}, err
	}
	defer release()
	if err := recoverWorkspaceRestores(ctx, cfg, req.ProjectID); err != nil {
		return workspaceRunResponse{}, err
	}
	branch := defaultBranch(req.Branch)
	if req.ProjectID == "" {
		return workspaceRunResponse{}, workspace.ErrMissingProjectID
	}
	if req.RunID == "" {
		return workspaceRunResponse{}, errors.New("run_id required")
	}
	runDir, err := workspaceRunDir(cfg, req.ProjectID, branch, req.RunID)
	if err != nil {
		return workspaceRunResponse{}, err
	}
	root, err := openWorkspaceDirectory(cfg, runDir, false)
	if err != nil {
		return workspaceRunResponse{}, err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return workspaceRunResponse{}, err
	}
	files := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		data, err := workspace.ReadFile(root, entry.Name())
		if err != nil {
			return workspaceRunResponse{}, err
		}
		if !utf8.Valid(data) {
			return workspaceRunResponse{}, errors.New("workspace text must be valid UTF-8")
		}
		files[entry.Name()] = string(data)
	}
	return workspaceRunResponse{ProjectID: req.ProjectID, Branch: branch, RunID: req.RunID, Path: runDir, Files: files}, nil
}

func commitWorkspace(cfg APIConfig, req workspaceCommitRequest) (workspaceCommitRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, req.Branch))
	defer unlock()
	release, err := workspace.LockProject(ctx, workspaceRoot(cfg), req.ProjectID)
	if err != nil {
		return workspaceCommitRecord{}, err
	}
	defer release()
	if err := recoverWorkspaceRestores(ctx, cfg, req.ProjectID); err != nil {
		return workspaceCommitRecord{}, err
	}
	return commitWorkspaceLocked(ctx, cfg, req)
}

func commitWorkspaceAuthorized(ctx context.Context, cfg APIConfig, req workspaceCommitRequest, actor accesspkg.MetadataActor) (workspaceCommitRecord, error) {
	ctx, cancel, err := workspaceRequestContext(ctx, actor, 30*time.Second)
	if err != nil {
		return workspaceCommitRecord{}, err
	}
	defer cancel()
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, req.Branch))
	defer unlock()
	release, err := beginWorkspaceEffectFence(ctx, cfg, actor, req.ProjectID, workspaceAccessWrite)
	if err != nil {
		return workspaceCommitRecord{}, err
	}
	defer release()
	return commitWorkspaceLocked(ctx, cfg, req)
}

func commitWorkspaceLocked(ctx context.Context, cfg APIConfig, req workspaceCommitRequest) (workspaceCommitRecord, error) {
	if req.ProjectID == "" {
		return workspaceCommitRecord{}, workspace.ErrMissingProjectID
	}
	return prepareWorkspaceCommit(ctx, cfg, req, uuid.NewString())
}

func logWorkspaceCommits(cfg APIConfig, req workspaceCommitRequest) (workspaceLogResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, req.Branch))
	defer unlock()
	release, err := workspace.LockProject(ctx, workspaceRoot(cfg), req.ProjectID)
	if err != nil {
		return workspaceLogResponse{}, err
	}
	defer release()
	if err := recoverWorkspaceRestores(ctx, cfg, req.ProjectID); err != nil {
		return workspaceLogResponse{}, err
	}
	return logWorkspaceCommitsLocked(cfg, req)
}
func logWorkspaceCommitsLocked(cfg APIConfig, req workspaceCommitRequest) (workspaceLogResponse, error) {
	if req.ProjectID == "" {
		return workspaceLogResponse{}, workspace.ErrMissingProjectID
	}
	return listWorkspaceCommitRecordsConfined(cfg, req.ProjectID, defaultBranch(req.Branch))
}

func revertWorkspace(ctx context.Context, cfg APIConfig, req workspaceRevertRequest) (workspaceRevertResponse, error) {
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, req.Branch))
	defer unlock()
	release, err := workspace.LockProject(ctx, workspaceRoot(cfg), req.ProjectID)
	if err != nil {
		return workspaceRevertResponse{}, err
	}
	defer release()
	if err := recoverWorkspaceRestores(ctx, cfg, req.ProjectID); err != nil {
		return workspaceRevertResponse{}, err
	}
	return revertWorkspaceLocked(ctx, cfg, req)
}

func revertWorkspaceAuthorized(ctx context.Context, cfg APIConfig, req workspaceRevertRequest, actor accesspkg.MetadataActor) (workspaceRevertResponse, error) {
	ctx, cancel, err := workspaceRequestContext(ctx, actor, 30*time.Second)
	if err != nil {
		return workspaceRevertResponse{}, err
	}
	defer cancel()
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, req.Branch))
	defer unlock()
	release, err := beginWorkspaceEffectFence(ctx, cfg, actor, req.ProjectID, workspaceAccessWrite)
	if err != nil {
		return workspaceRevertResponse{}, err
	}
	defer release()
	return revertWorkspaceLocked(ctx, cfg, req, &workspaceIndexJobAuthority{Actor: &actor})
}

func revertWorkspaceLocked(ctx context.Context, cfg APIConfig, req workspaceRevertRequest, authorities ...*workspaceIndexJobAuthority) (workspaceRevertResponse, error) {
	branch := defaultBranch(req.Branch)
	if req.ProjectID == "" {
		return workspaceRevertResponse{}, workspace.ErrMissingProjectID
	}
	if req.CommitID == "" {
		return workspaceRevertResponse{}, errors.New("commit_id required")
	}
	if req.Reindex {
		if err := validateWorkspaceCollection(req.Collection); err != nil {
			return workspaceRevertResponse{}, err
		}
	}
	commitDir, err := workspaceCommitDir(cfg, req.ProjectID, branch, req.CommitID)
	if err != nil {
		return workspaceRevertResponse{}, err
	}
	record, err := loadWorkspaceCommitRecordConfined(cfg, commitDir)
	if err != nil {
		return workspaceRevertResponse{}, err
	}
	if record.ProjectID != req.ProjectID || defaultBranch(record.Branch) != branch || record.CommitID != req.CommitID {
		return workspaceRevertResponse{}, errors.New("workspace commit target does not match request")
	}
	for _, file := range record.Files {
		if _, _, err := workspaceFilePath(cfg, req.ProjectID, branch, file.Path); err != nil {
			return workspaceRevertResponse{}, err
		}
	}
	// Optimistic-concurrency guard (finding H7, 2026-09-03 review): revert
	// replaces the whole branch tree, destroying any commits made after the
	// caller last looked. Without an explicit expected_current_commit_id
	// match (or force), refuse instead of silently discarding newer work.
	if !req.Force {
		head := workspaceLogResponse{}
		log, logErr := logWorkspaceCommitsLocked(cfg, workspaceCommitRequest{ProjectID: req.ProjectID, Branch: branch})
		if logErr != nil {
			return workspaceRevertResponse{}, logErr
		}
		head = log
		if len(head.Commits) > 0 && head.Commits[0].CommitID != req.CommitID {
			if req.ExpectedCurrentCommitID == "" {
				return workspaceRevertResponse{}, fmt.Errorf(
					"branch has commits newer than %s; pass expected_current_commit_id=%q to confirm, or force=true to discard newer commits",
					req.CommitID, head.Commits[0].CommitID)
			}
			if req.ExpectedCurrentCommitID != head.Commits[0].CommitID {
				return workspaceRevertResponse{}, errors.New(
					"expected_current_commit_id does not match branch head " + head.Commits[0].CommitID)
			}
		}
	}
	if err := restoreWorkspaceSnapshot(ctx, cfg, req.ProjectID, branch, commitDir, record); err != nil {
		return workspaceRevertResponse{}, err
	}
	resp := workspaceRevertResponse{
		ProjectID: req.ProjectID,
		Branch:    branch,
		CommitID:  req.CommitID,
		Files:     record.Files,
	}
	if !req.Reindex {
		return resp, nil
	}
	generation := req.Generation
	if generation == "" {
		generation = "revert-" + req.CommitID
	}
	activate := true
	if req.ActivateGeneration != nil {
		activate = *req.ActivateGeneration
	}
	indexed, err := reconcileWorkspaceMarkdownLocked(ctx, cfg, workspaceReconcileRequest{
		workspaceReindexRequest: workspaceReindexRequest{
			ProjectID:          req.ProjectID,
			Branch:             branch,
			Generation:         generation,
			Collection:         req.Collection,
			ChunkStrategy:      req.ChunkStrategy,
			MinChunkChars:      req.MinChunkChars,
			MaxChunkChars:      req.MaxChunkChars,
			OverlapChars:       req.OverlapChars,
			SnapToSentence:     req.SnapToSentence,
			ActivateGeneration: activate,
		},
	}, authorities...)
	if err != nil {
		return workspaceRevertResponse{}, err
	}
	resp.Indexed = &indexed
	return resp, nil
}

func deleteWorkspaceMarkdownAuthorized(ctx context.Context, cfg APIConfig, req workspaceDeleteRequest, actor accesspkg.MetadataActor) (workspaceDeleteResponse, error) {
	ctx, cancel, err := workspaceRequestContext(ctx, actor, 30*time.Second)
	if err != nil {
		return workspaceDeleteResponse{}, err
	}
	defer cancel()
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, req.Branch))
	defer unlock()
	release, err := beginWorkspaceEffectFence(ctx, cfg, actor, req.ProjectID, workspaceAccessWrite)
	if err != nil {
		return workspaceDeleteResponse{}, err
	}
	defer release()
	return deleteWorkspaceMarkdownLocked(ctx, cfg, req)
}

func deleteWorkspaceMarkdownLocked(ctx context.Context, cfg APIConfig, req workspaceDeleteRequest) (workspaceDeleteResponse, error) {
	if err := ctx.Err(); err != nil {
		return workspaceDeleteResponse{}, err
	}
	manifest, path, err := loadWorkspaceManifest(cfg, req.ProjectID, defaultBranch(req.Branch))
	if err != nil {
		return workspaceDeleteResponse{}, err
	}
	if req.Collection == "" {
		req.Collection = workspace.DefaultCollectionName(req.ProjectID, defaultBranch(req.Branch), req.Generation)
	}
	if err := validateWorkspaceCollection(req.Collection); err != nil {
		return workspaceDeleteResponse{}, err
	}
	store, err := workspaceVectorStore(cfg)
	if err != nil {
		return workspaceDeleteResponse{}, err
	}
	indexer := &workspace.Indexer{
		Store:    store,
		Manifest: manifest,
		Lexical:  workspaceLexicalIndex(cfg, req.Collection),
	}
	if err := ctx.Err(); err != nil {
		return workspaceDeleteResponse{}, err
	}
	ids, err := indexer.DeleteMarkdown(req.Path, workspace.IndexOptions{
		ProjectID:  req.ProjectID,
		Branch:     defaultBranch(req.Branch),
		Generation: req.Generation,
		Collection: req.Collection,
	})
	if err != nil {
		return workspaceDeleteResponse{}, err
	}
	if err := saveWorkspaceManifest(ctx, cfg, path, manifest); err != nil {
		return workspaceDeleteResponse{}, err
	}
	return workspaceDeleteResponse{
		workspaceResponse: workspaceBaseResponse(manifest, path),
		DeletedVectorIDs:  ids,
	}, nil
}

func gcWorkspaceGenerationsAuthorized(ctx context.Context, cfg APIConfig, req workspaceGCRequest, actor accesspkg.MetadataActor) (workspaceGCResponse, error) {
	ctx, cancel, err := workspaceRequestContext(ctx, actor, 30*time.Second)
	if err != nil {
		return workspaceGCResponse{}, err
	}
	defer cancel()
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, req.Branch))
	defer unlock()
	release, err := beginWorkspaceEffectFence(ctx, cfg, actor, req.ProjectID, workspaceAccessWrite)
	if err != nil {
		return workspaceGCResponse{}, err
	}
	defer release()
	return gcWorkspaceGenerationsLocked(ctx, cfg, req)
}

func gcWorkspaceGenerationsLocked(ctx context.Context, cfg APIConfig, req workspaceGCRequest) (workspaceGCResponse, error) {
	if err := ctx.Err(); err != nil {
		return workspaceGCResponse{}, err
	}
	manifest, path, err := loadWorkspaceManifest(cfg, req.ProjectID, defaultBranch(req.Branch))
	if err != nil {
		return workspaceGCResponse{}, err
	}
	pendingChunks := pendingGCChunks(manifest)
	plan, err := workspace.PlanGCGenerations(manifest)
	if err != nil {
		return workspaceGCResponse{}, err
	}
	for _, collection := range append(plan.ExclusiveCollections, plan.SharedCollections...) {
		if err := validateWorkspaceCollection(collection); err != nil {
			return workspaceGCResponse{}, err
		}
	}
	if req.DryRun {
		result := plan
		return workspaceGCResponse{
			workspaceResponse: workspaceBaseResponse(manifest, path),
			Result:            result,
		}, nil
	}
	store, err := workspaceVectorStore(cfg)
	if err != nil {
		return workspaceGCResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return workspaceGCResponse{}, err
	}
	result, err := workspace.GCGenerations(manifest, store)
	if err != nil {
		if saveErr := saveWorkspaceManifest(ctx, cfg, path, manifest); saveErr != nil {
			return workspaceGCResponse{}, fmt.Errorf("%w (save retirement state: %v)", err, saveErr)
		}
		return workspaceGCResponse{}, err
	}
	cleanupLexicalAfterGC(cfg, pendingChunks, result)
	if err := saveWorkspaceManifest(ctx, cfg, path, manifest); err != nil {
		return workspaceGCResponse{}, err
	}
	return workspaceGCResponse{
		workspaceResponse: workspaceBaseResponse(manifest, path),
		Result:            result,
	}, nil
}

func newWorkspaceIndexer(cfg APIConfig, manifest *workspace.Manifest, collection string) (*workspace.Indexer, error) {
	store, err := workspaceVectorStore(cfg)
	if err != nil {
		return nil, err
	}
	embedder, err := workspaceEmbedder(cfg)
	if err != nil {
		return nil, err
	}
	return &workspace.Indexer{
		Store:    store,
		Embedder: embedder,
		Manifest: manifest,
		Lexical:  workspaceLexicalIndex(cfg, collection),
	}, nil
}

func workspaceVectorStore(cfg APIConfig) (vectorstore.VectorStore, error) {
	if cfg.Collections == nil {
		return nil, errors.New("collections manager required")
	}
	return vectorstore.NewHNSWStore(cfg.Collections), nil
}

func workspaceEmbedder(cfg APIConfig) (workspace.Embedder, error) {
	if cfg.EmbedClient != nil {
		return cfg.EmbedClient, nil
	}
	if cfg.EmbedEndpoint == "" {
		return nil, errors.New("embed endpoint required")
	}
	model := cfg.EmbedModel
	if model == "" {
		model = "text-embedding-3-small"
	}
	return embed.NewClient(cfg.EmbedEndpoint, model, 16, 3), nil
}

func workspaceLexicalIndex(cfg APIConfig, collection string) *bm25.Index {
	if cfg.BM25Indexes == nil || collection == "" {
		return nil
	}
	return cfg.BM25Indexes.GetOrCreate(collection, func(coll string, idx *bm25.Index) {
		if cfg.BM25Store != nil {
			cfg.BM25Store.Attach(coll, idx)
		}
	})
}

func loadWorkspaceManifest(cfg APIConfig, projectID, branch string) (*workspace.Manifest, string, error) {
	manifest, _, err := readWorkspaceManifest(cfg, projectID, branch)
	if err != nil {
		return nil, "", err
	}
	return manifest, workspaceManifestPath(cfg, projectID, defaultBranch(branch)), nil
}

func readWorkspaceManifest(cfg APIConfig, projectID, branch string) (*workspace.Manifest, bool, error) {
	if projectID == "" {
		return nil, false, workspace.ErrMissingProjectID
	}
	branch = defaultBranch(branch)
	paths := []string{
		workspaceManifestPath(cfg, projectID, branch),
		workspace.LegacyManifestPath(workspaceRoot(cfg), projectID, branch),
	}
	for i, path := range paths {
		root, name, err := openWorkspaceFileParent(cfg, path, false)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		manifest, err := workspace.LoadManifestRoot(root, name)
		root.Close()
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		if manifest.ProjectID != "" &&
			workspace.SafeID(manifest.ProjectID) == workspace.SafeID(projectID) &&
			workspace.SafeID(defaultBranch(manifest.Branch)) == workspace.SafeID(branch) {
			return manifest, true, nil
		}
		if i == 0 {
			return nil, false, errors.New("workspace manifest target mismatch")
		}
	}
	return workspace.NewManifest(projectID, branch), false, nil
}

func workspaceManifestPath(cfg APIConfig, projectID, branch string) string {
	return workspace.ManifestPath(workspaceRoot(cfg), projectID, branch)
}

func workspaceRoot(cfg APIConfig) string {
	if cfg.WorkspacePath != "" {
		return cfg.WorkspacePath
	}
	return "data/workspace"
}

func workspaceProjectRoot(cfg APIConfig, projectID, branch string) string {
	return workspace.ProjectRoot(workspaceRoot(cfg), projectID, branch)
}

func workspaceFilePath(cfg APIConfig, projectID, branch, path string) (string, string, error) {
	if projectID == "" {
		return "", "", workspace.ErrMissingProjectID
	}
	if path == "" {
		return "", "", workspace.ErrMissingPath
	}
	if strings.IndexByte(path, 0) >= 0 {
		return "", "", errors.New("workspace path contains NUL")
	}
	if filepath.IsAbs(path) {
		return "", "", errors.New("workspace path must be relative")
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", "", errors.New("workspace path escapes project root")
	}
	rel := filepath.ToSlash(clean)
	return filepath.Join(workspaceProjectRoot(cfg, projectID, branch), clean), rel, nil
}

func listWorkspaceMarkdownPaths(cfg APIConfig, absolute string) ([]string, error) {
	root, err := openWorkspaceDirectory(cfg, absolute, false)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var paths []string
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("workspace contains unsafe nonregular file")
		}
		if strings.EqualFold(filepath.Ext(name), ".md") {
			paths = append(paths, filepath.ToSlash(name))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func workspaceRunDir(cfg APIConfig, projectID, branch, runID string) (string, error) {
	if runID == "" {
		return "", errors.New("run_id required")
	}
	if filepath.IsAbs(runID) || strings.Contains(runID, "/") || strings.Contains(runID, string(os.PathSeparator)) || strings.Contains(runID, "..") {
		return "", errors.New("run_id must be a simple identifier")
	}
	return filepath.Join(workspaceProjectRoot(cfg, projectID, branch), "runs", safeWorkspaceID(runID)), nil
}

func workspaceCommitsRoot(cfg APIConfig, projectID, branch string) string {
	return filepath.Join(workspaceRoot(cfg), ".kb", "commits", safeWorkspaceID(projectID), safeWorkspaceID(branch))
}

func workspaceCommitDir(cfg APIConfig, projectID, branch, commitID string) (string, error) {
	if commitID == "" {
		return "", errors.New("commit_id required")
	}
	if filepath.IsAbs(commitID) || strings.Contains(commitID, "/") || strings.Contains(commitID, string(os.PathSeparator)) || strings.Contains(commitID, "..") {
		return "", errors.New("commit_id must be a simple identifier")
	}
	return filepath.Join(workspaceCommitsRoot(cfg, projectID, branch), safeWorkspaceID(commitID)), nil
}

func runMetadataMarkdown(req workspaceRunStartRequest, runID string) string {
	meta := map[string]any{
		"run_id":     runID,
		"project_id": req.ProjectID,
		"branch":     defaultBranch(req.Branch),
		"created_at": time.Now().UTC().Format(time.RFC3339),
	}
	for k, v := range req.Metadata {
		meta[k] = v
	}
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("---\n")
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(": ")
		fmt.Fprint(&b, meta[k])
		b.WriteByte('\n')
	}
	b.WriteString("---\n")
	return b.String()
}

func workspaceBaseResponse(manifest *workspace.Manifest, path string) workspaceResponse {
	return workspaceResponse{
		ProjectID:        manifest.ProjectID,
		Branch:           manifest.Branch,
		ManifestPath:     path,
		ActiveGeneration: manifest.ActiveGeneration,
	}
}

func workspaceWatchStatus(cfg APIConfig) WorkspaceWatchStatus {
	if cfg.WorkspaceWatcher == nil {
		return WorkspaceWatchStatus{}
	}
	return cfg.WorkspaceWatcher.Snapshot()
}

func pendingGCChunks(manifest *workspace.Manifest) []workspace.ChunkRecord {
	if manifest == nil {
		return nil
	}
	var out []workspace.ChunkRecord
	for genID, gen := range manifest.Generations {
		if gen.Status != workspace.GenerationGCPending {
			continue
		}
		out = append(out, manifest.ListChunks(workspace.ChunkFilter{Generation: genID})...)
	}
	return out
}

func cleanupLexicalAfterGC(cfg APIConfig, chunks []workspace.ChunkRecord, result workspace.GCResult) {
	if cfg.BM25Indexes == nil {
		return
	}
	dropped := make(map[string]struct{}, len(result.DroppedCollections))
	for _, coll := range result.DroppedCollections {
		dropped[coll] = struct{}{}
		cfg.BM25Indexes.Delete(coll)
		if cfg.BM25Store != nil {
			if err := cfg.BM25Store.Remove(coll); err != nil {
				log.Printf("[workspace] remove BM25 sidecar %q: %v", coll, err)
			}
		}
	}
	for _, rec := range chunks {
		if _, ok := dropped[rec.Collection]; ok {
			continue
		}
		if idx := cfg.BM25Indexes.Get(rec.Collection); idx != nil {
			idx.Remove(rec.VectorID)
		}
	}
}

func decodeWorkspaceArgs(args map[string]any, out any) error {
	data, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func resolveWorkspaceSearchTarget(cfg APIConfig, req workspaceSearchRequest) (workspaceSearchTarget, error) {
	branch := defaultBranch(req.Branch)
	manifest, manifestPath, err := loadWorkspaceManifest(cfg, req.ProjectID, branch)
	if err != nil {
		return workspaceSearchTarget{}, err
	}
	generation := req.Generation
	if generation == "" {
		generation = manifest.ActiveGeneration
	}
	if generation == "" {
		return workspaceSearchTarget{}, errors.New("active generation missing; run workspace_reconcile or pass generation and collection explicitly")
	}
	chunks := manifest.ListChunks(workspace.ChunkFilter{
		ProjectID:  req.ProjectID,
		Branch:     branch,
		Generation: generation,
	})
	collection := req.Collection
	if collection == "" {
		var err error
		collection, err = workspaceSearchCollection(req.ProjectID, branch, generation, chunks)
		if err != nil {
			return workspaceSearchTarget{}, err
		}
	}
	if err := refreshWorkspaceLexical(cfg, manifest, collection); err != nil {
		return workspaceSearchTarget{}, err
	}
	return workspaceSearchTarget{
		Manifest:     manifest,
		ManifestPath: manifestPath,
		Branch:       branch,
		Generation:   generation,
		Collection:   collection,
		Chunks:       chunks,
	}, nil
}

func workspaceSearchCollection(projectID, branch, generation string, chunks []workspace.ChunkRecord) (string, error) {
	collections := make(map[string]struct{})
	for _, rec := range chunks {
		if rec.Collection != "" {
			collections[rec.Collection] = struct{}{}
		}
	}
	switch len(collections) {
	case 0:
		return workspace.DefaultCollectionName(projectID, branch, generation), nil
	case 1:
		for collection := range collections {
			return collection, nil
		}
	}
	names := make([]string, 0, len(collections))
	for collection := range collections {
		names = append(names, collection)
	}
	sort.Strings(names)
	return "", fmt.Errorf("generation %q has multiple collections (%s); pass collection explicitly", generation, strings.Join(names, ", "))
}

func workspaceSearchFreshnessFor(req workspaceSearchRequest, target workspaceSearchTarget, watch WorkspaceWatchStatus) workspaceSearchFreshness {
	branchStatus := workspaceFreshnessBranchStatus(req.ProjectID, target.Branch, watch)
	f := workspaceSearchFreshness{
		ActiveGeneration:           target.Manifest.ActiveGeneration,
		RequestedGeneration:        req.Generation,
		ResolvedGeneration:         target.Generation,
		LastIndexedAt:              workspaceSearchLastIndexedAt(target.Chunks),
		ActiveChunkCount:           len(target.Chunks),
		ActivePathCount:            workspaceSearchPathCount(target.Chunks),
		WatcherEnabled:             watch.Enabled,
		WatcherPending:             watch.PendingBranches,
		WatcherLastReconcile:       watch.LastReconcileAt,
		WatcherLastError:           watch.LastError,
		WatcherBranchPending:       branchStatus.Pending,
		WatcherBranchLastScan:      branchStatus.LastScanAt,
		WatcherBranchLastChange:    branchStatus.LastChangeAt,
		WatcherBranchLastReconcile: branchStatus.LastReconcileAt,
		WatcherBranchLastError:     branchStatus.LastError,
	}
	switch {
	case target.Manifest.ActiveGeneration == "":
		f.Stale = true
		f.Reason = "no_active_generation"
	case target.Generation != target.Manifest.ActiveGeneration:
		f.Stale = true
		f.Reason = "requested_generation_is_not_active"
	}
	if branchStatus.Pending || branchStatus.LastError != "" {
		f.PotentiallyStale = true
		if f.Reason == "" && branchStatus.Pending {
			f.Reason = "watcher_branch_has_pending_reconcile"
		}
		if f.Reason == "" && branchStatus.LastError != "" {
			f.Reason = "watcher_branch_last_error"
		}
	}
	return f
}

func workspaceFreshnessBranchStatus(projectID, branch string, watch WorkspaceWatchStatus) WorkspaceBranchWatchStatus {
	if len(watch.Branches) == 0 {
		return WorkspaceBranchWatchStatus{}
	}
	keys := []workspaceWatchKey{
		{ProjectID: projectID, Branch: branch},
		{ProjectID: safeWorkspaceID(projectID), Branch: safeWorkspaceID(branch)},
	}
	for _, key := range keys {
		if status, ok := watch.Branches[workspaceWatchStatusKey(key)]; ok {
			return status
		}
	}
	return WorkspaceBranchWatchStatus{}
}

func workspaceSearchLastIndexedAt(chunks []workspace.ChunkRecord) string {
	var latest time.Time
	for _, rec := range chunks {
		if rec.UpdatedAt == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, rec.UpdatedAt)
		if err != nil {
			if parsed, perr := time.Parse(time.RFC3339, rec.UpdatedAt); perr == nil {
				t = parsed
			} else {
				continue
			}
		}
		if latest.IsZero() || t.After(latest) {
			latest = t
		}
	}
	if latest.IsZero() {
		return ""
	}
	return latest.UTC().Format(time.RFC3339Nano)
}

func workspaceSearchPathCount(chunks []workspace.ChunkRecord) int {
	paths := make(map[string]struct{})
	for _, rec := range chunks {
		if rec.Path != "" {
			paths[rec.Path] = struct{}{}
		}
	}
	return len(paths)
}

func workspaceSearchArgs(req workspaceSearchRequest, target workspaceSearchTarget) (map[string]any, error) {
	query := strings.TrimSpace(req.SearchQuery)
	if query == "" {
		query = strings.TrimSpace(req.Query)
	}
	if query == "" {
		return nil, errors.New("search_query required")
	}
	searchType := req.SearchType
	if searchType == "" {
		searchType = "HYBRID"
	}
	mode := req.Mode
	if mode == "" {
		mode = "rag"
	}
	args := map[string]any{
		"search_query": query,
		"search_type":  searchType,
		"collection":   target.Collection,
		"mode":         mode,
	}
	if req.TopK > 0 {
		args["top_k"] = req.TopK
	}
	if req.Room != "" {
		args["room"] = req.Room
	}
	if len(req.Tags) > 0 {
		tags := make([]any, 0, len(req.Tags))
		for _, tag := range req.Tags {
			if tag != "" {
				tags = append(tags, tag)
			}
		}
		args["tags"] = tags
	}
	if req.Rerank {
		args["rerank"] = true
	}
	if req.ParentChild {
		args["parent_child"] = true
	}
	if req.MultiQuery {
		args["multi_query"] = true
	}
	if req.Dedup != nil {
		args["dedup"] = *req.Dedup
	}
	if req.GraphRerank {
		args["graph_rerank"] = true
	}
	if req.VectorWeight > 0 {
		args["vector_weight"] = req.VectorWeight
	}
	if req.BM25Weight > 0 {
		args["bm25_weight"] = req.BM25Weight
	}
	return args, nil
}

func workspaceSearchResponse(req workspaceSearchRequest, target workspaceSearchTarget, freshness workspaceSearchFreshness, inner mcpToolResult) map[string]any {
	out := map[string]any{
		"project_id":            req.ProjectID,
		"branch":                target.Branch,
		"manifest_path":         target.ManifestPath,
		"active_generation":     target.Manifest.ActiveGeneration,
		"generation":            target.Generation,
		"collection":            target.Collection,
		"freshness":             freshness,
		"exact_read_required":   true,
		"exact_read_tool":       "workspace_read",
		"exact_read_reason":     "retrieval is approximate; read the exact markdown path before using a hit as source of truth",
		"answer_contract":       workspaceSearchAnswerContract(),
		"results":               []any{},
		"search_type":           req.SearchType,
		"generic_search_status": "ok",
	}
	if len(inner.Content) == 0 {
		out["generic_search_status"] = "empty_response"
		return out
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(inner.Content[0].Text), &payload); err != nil {
		out["generic_search_status"] = "non_json_response"
		out["search_message"] = inner.Content[0].Text
		if inner.IsError {
			out["generic_search_status"] = "error"
		}
		return out
	}
	for _, key := range []string{"search_type", "reranked", "routing"} {
		if value, ok := payload[key]; ok {
			out[key] = value
		}
	}
	out["results"] = workspaceSearchEnrichedResults(payload["results"], target, freshness)
	return out
}

func workspaceSearchEnrichedResults(raw any, target workspaceSearchTarget, freshness workspaceSearchFreshness) []any {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return []any{}
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		if metaText, ok := m["metadata"].(string); ok && metaText != "" {
			var meta map[string]any
			if err := json.Unmarshal([]byte(metaText), &meta); err == nil {
				for _, key := range []string{
					"text", "path", "heading_path", "project_id", "dataset_id",
					"branch", "generation", "file_digest", "chunk_id", "document_id",
				} {
					if _, exists := m[key]; !exists {
						if value, ok := meta[key]; ok {
							m[key] = value
						}
					}
				}
			}
		}
		if _, exists := m["citation"]; !exists {
			m["citation"] = workspaceCitationFromSearchResult(m, target, freshness)
		}
		out = append(out, m)
	}
	return out
}

func defaultBranch(branch string) string {
	return workspace.DefaultBranch(branch)
}

func digestText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func safeWorkspaceID(s string) string {
	return workspace.SafeID(s)
}

func (h *mcpHandler) toolWorkspaceSearch(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceSearchRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessRead); err != nil {
		return workspaceMCPError(err)
	}
	target, err := resolveWorkspaceSearchTarget(h.cfg, req)
	if err != nil {
		return workspaceMCPError(err)
	}
	searchArgs, err := workspaceSearchArgs(req, target)
	if err != nil {
		return workspaceMCPError(err)
	}
	trackWorkspaceSearchProject(ctx, req.ProjectID)
	searchCtx := context.WithValue(ctx, workspaceSearchScopeKey{}, target)
	inner := h.toolSearch(searchCtx, searchArgs)
	resp := workspaceSearchResponse(req, target, workspaceSearchFreshnessFor(req, target, workspaceWatchStatus(h.cfg)), inner)
	return workspaceMCPJSON(resp)
}

func (h *mcpHandler) toolWorkspaceIndex(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceIndexRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessWrite); err != nil {
		return workspaceMCPError(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := indexWorkspaceMarkdownAuthorized(ctx, h.cfg, req, h.MetadataActor(ctx))
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}

func (h *mcpHandler) toolWorkspaceRead(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceReadRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessRead); err != nil {
		return workspaceMCPError(err)
	}
	var resp workspaceReadResponse
	var err error
	if execution := mcp.TaskExecutionFromContext(ctx); execution != nil {
		resp, err = taskReadWorkspaceMarkdown(ctx, h.cfg, req, execution)
	} else {
		resp, err = readWorkspaceMarkdownAuthorized(ctx, h.cfg, req, h.MetadataActor(ctx))
	}
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}

func (h *mcpHandler) toolWorkspaceWrite(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceWriteRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessWrite); err != nil {
		return workspaceMCPError(err)
	}
	resp, err := writeWorkspaceMarkdownAuthorized(ctx, h.cfg, req, h.MetadataActor(ctx))
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}

func (h *mcpHandler) toolWorkspaceReindex(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceReindexRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessWrite); err != nil {
		return workspaceMCPError(err)
	}
	resp, err := reindexWorkspaceMarkdownAuthorized(ctx, h.cfg, req, h.MetadataActor(ctx))
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}

func (h *mcpHandler) toolWorkspaceReconcile(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceReconcileRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessWrite); err != nil {
		return workspaceMCPError(err)
	}
	resp, err := reconcileWorkspaceMarkdownAuthorized(ctx, h.cfg, req, h.MetadataActor(ctx))
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}

func (h *mcpHandler) toolWorkspaceIndexJobs(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceIndexJobsRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessRead); err != nil {
		return workspaceMCPError(err)
	}
	jobs, err := listWorkspaceIndexJobs(h.cfg, req)
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(fiber.Map{
		"project_id": req.ProjectID,
		"branch":     defaultBranch(req.Branch),
		"jobs":       jobs,
		"total":      len(jobs),
		"by_status":  workspaceJobStatusSummary(jobs),
	})
}

func (h *mcpHandler) toolWorkspaceEnqueueIndexJob(ctx context.Context, args map[string]any) mcpToolResult {
	var payload workspaceIndexJobPayload
	if err := decodeWorkspaceArgs(args, &payload); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, payload.ProjectID, workspaceAccessWrite); err != nil {
		return workspaceMCPError(err)
	}
	job, err := enqueueWorkspaceIndexJobFromPayloadAuthorized(ctx, h.cfg, payload, h.MetadataActor(ctx))
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(fiber.Map{"job": job})
}

func (h *mcpHandler) toolWorkspaceRetryIndexJob(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceRetryIndexJobRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessWrite); err != nil {
		return workspaceMCPError(err)
	}
	resp, err := retryWorkspaceIndexJobAuthorized(ctx, h.cfg, req, h.MetadataActor(ctx))
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}

func (h *mcpHandler) toolWorkspaceWatchStatus(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceContextRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessRead); err != nil {
		return workspaceMCPError(err)
	}
	watch := workspaceWatchStatus(h.cfg)
	if req.ProjectID != "" {
		watch = workspaceProjectWatchStatus(watch, req.ProjectID, req.Branch)
	}
	return workspaceMCPJSON(watch)
}

func (h *mcpHandler) toolWorkspaceRunStart(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceRunStartRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessWrite); err != nil {
		return workspaceMCPError(err)
	}
	resp, err := startWorkspaceRunAuthorized(ctx, h.cfg, req, h.MetadataActor(ctx))
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}

func (h *mcpHandler) toolWorkspaceRunGet(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceRunGetRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessRead); err != nil {
		return workspaceMCPError(err)
	}
	resp, err := getWorkspaceRun(h.cfg, req)
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}

func (h *mcpHandler) toolWorkspaceCommit(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceCommitRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessWrite); err != nil {
		return workspaceMCPError(err)
	}
	resp, err := commitWorkspaceAuthorized(ctx, h.cfg, req, h.MetadataActor(ctx))
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}

func (h *mcpHandler) toolWorkspaceLog(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceCommitRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessRead); err != nil {
		return workspaceMCPError(err)
	}
	resp, err := logWorkspaceCommits(h.cfg, req)
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}

func (h *mcpHandler) toolWorkspaceRevert(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceRevertRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessWrite); err != nil {
		return workspaceMCPError(err)
	}
	resp, err := revertWorkspaceAuthorized(ctx, h.cfg, req, h.MetadataActor(ctx))
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}

func (h *mcpHandler) toolWorkspaceDelete(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceDeleteRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessWrite); err != nil {
		return workspaceMCPError(err)
	}
	resp, err := deleteWorkspaceMarkdownAuthorized(ctx, h.cfg, req, h.MetadataActor(ctx))
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}

func (h *mcpHandler) toolWorkspaceGC(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceGCRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessWrite); err != nil {
		return workspaceMCPError(err)
	}
	resp, err := gcWorkspaceGenerationsAuthorized(ctx, h.cfg, req, h.MetadataActor(ctx))
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}

func (h *mcpHandler) toolWorkspaceManifest(ctx context.Context, args map[string]any) mcpToolResult {
	projectID, _ := args["project_id"].(string)
	branch, _ := args["branch"].(string)
	if err := authorizeWorkspaceMCP(ctx, h.cfg, projectID, workspaceAccessRead); err != nil {
		return workspaceMCPError(err)
	}
	manifest, path, err := loadWorkspaceManifest(h.cfg, projectID, defaultBranch(branch))
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(fiber.Map{
		"project_id":          manifest.ProjectID,
		"branch":              manifest.Branch,
		"manifest_path":       path,
		"active_generation":   manifest.ActiveGeneration,
		"generations":         manifest.Generations,
		"chunks":              manifest.ListChunks(workspace.ChunkFilter{}),
		"chunks_count":        len(manifest.Chunks),
		"workspace_manifest":  manifest,
		"manifest_version":    manifest.Version,
		"workspace_root_path": workspaceRoot(h.cfg),
	})
}

func workspaceMCPJSON(v any) mcpToolResult {
	return mcpJSONResult(v)
}

func workspaceMCPError(err error) mcpToolResult {
	return mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: err.Error()}},
		IsError: true,
	}
}

func workspaceBranchLockKey(projectID, branch string) string {
	return safeWorkspaceID(projectID) + "/" + safeWorkspaceID(defaultBranch(branch))
}

func openWorkspaceDirectory(cfg APIConfig, absolute string, create bool) (*os.Root, error) {
	relative, err := filepath.Rel(workspaceRoot(cfg), absolute)
	if err != nil {
		return nil, err
	}
	return workspace.OpenRoot(workspaceRoot(cfg), filepath.ToSlash(relative), create)
}
func openWorkspaceFileParent(cfg APIConfig, absolute string, create bool) (*os.Root, string, error) {
	root, err := openWorkspaceDirectory(cfg, filepath.Dir(absolute), create)
	return root, filepath.Base(absolute), err
}
func readWorkspaceFile(cfg APIConfig, absolute string) ([]byte, error) {
	root, name, err := openWorkspaceFileParent(cfg, absolute, false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return workspace.ReadFile(root, name)
}
func writeWorkspaceFile(ctx context.Context, cfg APIConfig, absolute string, data []byte) error {
	root, name, err := openWorkspaceFileParent(cfg, absolute, true)
	if err != nil {
		return err
	}
	defer root.Close()
	return workspace.WriteFile(ctx, root, name, data, 0644)
}
func saveWorkspaceManifest(ctx context.Context, cfg APIConfig, absolute string, manifest *workspace.Manifest) error {
	root, name, err := openWorkspaceFileParent(cfg, absolute, true)
	if err != nil {
		return err
	}
	defer root.Close()
	return manifest.SaveRoot(ctx, root, name)
}
