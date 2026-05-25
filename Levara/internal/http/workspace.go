package http

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/bm25"
	"github.com/stek0v/levara/pkg/embed"
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
	ProjectID          string `json:"project_id"`
	Branch             string `json:"branch"`
	CommitID           string `json:"commit_id"`
	Reindex            bool   `json:"reindex,omitempty"`
	Generation         string `json:"generation,omitempty"`
	Collection         string `json:"collection,omitempty"`
	ChunkStrategy      string `json:"chunk_strategy,omitempty"`
	MinChunkChars      int    `json:"min_chunk_chars,omitempty"`
	MaxChunkChars      int    `json:"max_chunk_chars,omitempty"`
	OverlapChars       int    `json:"overlap_chars,omitempty"`
	SnapToSentence     *bool  `json:"snap_to_sentence,omitempty"`
	ActivateGeneration *bool  `json:"activate_generation,omitempty"`
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
	ProjectID string                    `json:"project_id"`
	Branch    string                    `json:"branch"`
	Path      string                    `json:"path"`
	Text      string                    `json:"text"`
	Citation  workspaceSourceCitation   `json:"citation"`
	Citations []workspaceSourceCitation `json:"citations,omitempty"`
	Chunks    []workspace.ChunkRecord   `json:"chunks,omitempty"`
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
		resp, err := indexWorkspaceMarkdown(c.UserContext(), cfg, req)
		if err != nil {
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
		resp, err := deleteWorkspaceMarkdown(cfg, req)
		if err != nil {
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
		resp, err := gcWorkspaceGenerations(cfg, req)
		if err != nil {
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
		return c.JSON(fiber.Map{
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
		})
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
		resp, err := readWorkspaceMarkdown(cfg, req)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(resp)
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
		resp, err := writeWorkspaceMarkdown(c.UserContext(), cfg, req)
		if err != nil {
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
		resp, err := reindexWorkspaceMarkdown(c.UserContext(), cfg, req)
		if err != nil {
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
		resp, err := reconcileWorkspaceMarkdown(c.UserContext(), cfg, req)
		if err != nil {
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
		return c.JSON(fiber.Map{
			"project_id": req.ProjectID,
			"branch":     defaultBranch(req.Branch),
			"jobs":       jobs,
			"total":      len(jobs),
			"by_status":  workspaceJobStatusSummary(jobs),
		})
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
		job, err := enqueueWorkspaceIndexJobFromPayload(cfg, payload)
		if err != nil {
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
		resp, err := retryWorkspaceIndexJob(c.UserContext(), cfg, req)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(resp)
	}
}

func workspaceWatchStatusHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		return c.JSON(workspaceWatchStatus(cfg))
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
		resp, err := startWorkspaceRun(cfg, req)
		if err != nil {
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
		return c.JSON(resp)
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
		resp, err := commitWorkspace(cfg, req)
		if err != nil {
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
		return c.JSON(resp)
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
		resp, err := revertWorkspace(c.UserContext(), cfg, req)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(resp)
	}
}

func authorizeWorkspaceFiber(c *fiber.Ctx, cfg APIConfig, projectID string, access workspaceAccessLevel) error {
	if projectID == "" {
		return nil
	}
	if perms, _ := c.Locals("api_key_permissions").(string); !workspaceAPIKeyAllows(perms, access) {
		return fiber.NewError(fiber.StatusForbidden, errWorkspaceAccessDenied.Error())
	}
	userID, _ := c.Locals("user_id").(string)
	allowed, err := checkWorkspaceAccess(c.UserContext(), cfg.DB, userID, projectID, access)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "workspace access check failed")
	}
	if !allowed {
		return fiber.NewError(fiber.StatusForbidden, errWorkspaceAccessDenied.Error())
	}
	return nil
}

func authorizeWorkspaceMCP(ctx context.Context, cfg APIConfig, projectID string, access workspaceAccessLevel) error {
	if projectID == "" {
		return nil
	}
	userID, _ := ctx.Value(mcpUserIDKey).(string)
	allowed, err := checkWorkspaceAccess(ctx, cfg.DB, userID, projectID, access)
	if err != nil {
		return err
	}
	if !allowed {
		return errWorkspaceAccessDenied
	}
	return nil
}

func workspaceAPIKeyAllows(perms string, access workspaceAccessLevel) bool {
	if perms == "" {
		return true
	}
	perms = strings.ToLower(perms)
	if access == workspaceAccessRead {
		return strings.Contains(perms, "read") || strings.Contains(perms, "write") || strings.Contains(perms, "admin")
	}
	return strings.Contains(perms, "write") || strings.Contains(perms, "admin")
}

func checkWorkspaceAccess(ctx context.Context, db *sql.DB, userID, projectID string, access workspaceAccessLevel) (bool, error) {
	if db == nil || userID == "" || projectID == "" {
		return true, nil
	}

	var isSuperuser bool
	_ = db.QueryRowContext(ctx, Q("SELECT COALESCE(is_superuser, false) FROM users WHERE id = $1"), userID).Scan(&isSuperuser)
	if isSuperuser {
		return true, nil
	}

	var ownerID string
	err := db.QueryRowContext(ctx, Q("SELECT COALESCE(owner_id, '') FROM datasets WHERE id = $1"), projectID).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if ownerID == "" || ownerID == userID {
		return true, nil
	}

	var role string
	err = db.QueryRowContext(ctx, Q("SELECT role FROM dataset_shares WHERE dataset_id = $1 AND user_id = $2"), projectID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return workspaceRoleAllows(role, access), nil
}

func workspaceRoleAllows(role string, access workspaceAccessLevel) bool {
	switch strings.ToLower(role) {
	case RoleAdmin:
		return true
	case RoleEditor:
		return access == workspaceAccessRead || access == workspaceAccessWrite
	case RoleViewer:
		return access == workspaceAccessRead
	default:
		return false
	}
}

func indexWorkspaceMarkdown(ctx context.Context, cfg APIConfig, req workspaceIndexRequest) (workspaceIndexResponse, error) {
	if strings.TrimSpace(req.Text) == "" {
		return workspaceIndexResponse{}, errors.New("text required")
	}
	manifest, path, err := loadWorkspaceManifest(cfg, req.ProjectID, defaultBranch(req.Branch))
	if err != nil {
		return workspaceIndexResponse{}, err
	}
	if req.FileDigest == "" {
		req.FileDigest = digestText(req.Text)
	}
	if req.Collection == "" {
		req.Collection = workspace.DefaultCollectionName(req.ProjectID, defaultBranch(req.Branch), req.Generation)
	}
	indexer, err := newWorkspaceIndexer(cfg, manifest, req.Collection)
	if err != nil {
		return workspaceIndexResponse{}, err
	}
	result, indexErr := indexer.IndexMarkdown(ctx, workspace.MarkdownFile{
		Path:       req.Path,
		Text:       req.Text,
		FileDigest: req.FileDigest,
		DocumentID: req.DocumentID,
		Title:      req.Title,
		Room:       req.Room,
		Tags:       req.Tags,
	}, workspace.IndexOptions{
		ProjectID:          req.ProjectID,
		Branch:             defaultBranch(req.Branch),
		Generation:         req.Generation,
		Collection:         req.Collection,
		CommitHash:         req.CommitHash,
		ChunkStrategy:      req.ChunkStrategy,
		MinChunkChars:      req.MinChunkChars,
		MaxChunkChars:      req.MaxChunkChars,
		OverlapChars:       req.OverlapChars,
		SnapToSentence:     req.SnapToSentence,
		ActivateGeneration: req.ActivateGeneration,
	})
	saveErr := manifest.Save(path)
	if indexErr != nil {
		if saveErr != nil {
			return workspaceIndexResponse{}, fmt.Errorf("%w (also failed to save manifest: %v)", indexErr, saveErr)
		}
		return workspaceIndexResponse{}, indexErr
	}
	if saveErr != nil {
		return workspaceIndexResponse{}, saveErr
	}
	return workspaceIndexResponse{
		workspaceResponse: workspaceBaseResponse(manifest, path),
		Result:            result,
	}, nil
}

func readWorkspaceMarkdown(cfg APIConfig, req workspaceReadRequest) (workspaceReadResponse, error) {
	branch := defaultBranch(req.Branch)
	filePath, relPath, err := workspaceFilePath(cfg, req.ProjectID, branch, req.Path)
	if err != nil {
		return workspaceReadResponse{}, err
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return workspaceReadResponse{}, err
	}
	manifest, _, err := loadWorkspaceManifest(cfg, req.ProjectID, branch)
	chunks := []workspace.ChunkRecord(nil)
	if err == nil {
		chunks = manifest.ListChunks(workspace.ChunkFilter{
			ProjectID: req.ProjectID,
			Branch:    branch,
			Path:      relPath,
		})
	}
	return workspaceReadResponse{
		ProjectID: req.ProjectID,
		Branch:    branch,
		Path:      relPath,
		Text:      string(data),
		Citation:  workspaceFileCitation(req.ProjectID, branch, relPath),
		Citations: workspaceCitationsFromChunks(req.ProjectID, branch, relPath, chunks),
		Chunks:    chunks,
	}, nil
}

func writeWorkspaceMarkdown(ctx context.Context, cfg APIConfig, req workspaceWriteRequest) (workspaceWriteResponse, error) {
	branch := defaultBranch(req.Branch)
	filePath, relPath, err := workspaceFilePath(cfg, req.ProjectID, branch, req.Path)
	if err != nil {
		return workspaceWriteResponse{}, err
	}
	if req.ExpectedFileDigest != nil {
		currentDigest := ""
		if current, err := os.ReadFile(filePath); err == nil {
			currentDigest = digestBytes(current)
		} else if !errors.Is(err, os.ErrNotExist) {
			return workspaceWriteResponse{}, err
		}
		if currentDigest != *req.ExpectedFileDigest {
			return workspaceWriteResponse{}, fmt.Errorf("workspace write conflict: current file digest %q does not match expected_file_digest %q", currentDigest, *req.ExpectedFileDigest)
		}
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return workspaceWriteResponse{}, err
	}
	if err := os.WriteFile(filePath, []byte(req.Text), 0644); err != nil {
		return workspaceWriteResponse{}, err
	}
	resp := workspaceWriteResponse{
		ProjectID: req.ProjectID,
		Branch:    branch,
		Path:      relPath,
		Bytes:     len([]byte(req.Text)),
	}
	shouldIndex := req.Generation != ""
	if req.Index != nil {
		shouldIndex = *req.Index
	}
	if shouldIndex {
		indexResp, err := indexWorkspaceMarkdown(ctx, cfg, workspaceIndexRequest{
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

func reindexWorkspaceMarkdown(ctx context.Context, cfg APIConfig, req workspaceReindexRequest) (workspaceReindexResponse, error) {
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
	job, err := beginWorkspaceIndexJob(cfg, workspaceIndexJobPayloadFromReindex("reindex", req, false))
	if err != nil {
		return workspaceReindexResponse{}, err
	}
	resp, runErr := reindexWorkspaceMarkdownDirect(ctx, cfg, req)
	if _, finishErr := finishWorkspaceIndexJob(cfg, job, runErr); finishErr != nil {
		return workspaceReindexResponse{}, finishErr
	}
	return resp, nil
}

func reindexWorkspaceMarkdownDirect(ctx context.Context, cfg APIConfig, req workspaceReindexRequest) (workspaceReindexResponse, error) {
	branch := defaultBranch(req.Branch)
	if req.ProjectID == "" {
		return workspaceReindexResponse{}, workspace.ErrMissingProjectID
	}
	if req.Generation == "" {
		return workspaceReindexResponse{}, workspace.ErrMissingGeneration
	}
	if len(req.Paths) == 0 {
		return workspaceReindexResponse{}, errors.New("paths required")
	}
	var results []workspace.IndexResult
	var manifestPath string
	var active string
	for _, path := range req.Paths {
		filePath, relPath, err := workspaceFilePath(cfg, req.ProjectID, branch, path)
		if err != nil {
			return workspaceReindexResponse{}, err
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			return workspaceReindexResponse{}, err
		}
		indexResp, err := indexWorkspaceMarkdown(ctx, cfg, workspaceIndexRequest{
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
			Text:               string(data),
			Title:              filepath.Base(relPath),
			Room:               req.Room,
			Tags:               req.Tags,
		})
		if err != nil {
			return workspaceReindexResponse{}, err
		}
		results = append(results, indexResp.Result)
		manifestPath = indexResp.ManifestPath
		active = indexResp.ActiveGeneration
	}
	return workspaceReindexResponse{
		workspaceResponse: workspaceResponse{
			ProjectID:        req.ProjectID,
			Branch:           branch,
			ManifestPath:     manifestPath,
			ActiveGeneration: active,
		},
		Results: results,
	}, nil
}

func reconcileWorkspaceMarkdown(ctx context.Context, cfg APIConfig, req workspaceReconcileRequest) (workspaceReconcileResponse, error) {
	branch := defaultBranch(req.Branch)
	req.Branch = branch
	if req.ProjectID == "" {
		return workspaceReconcileResponse{}, workspace.ErrMissingProjectID
	}
	if req.Generation == "" {
		return workspaceReconcileResponse{}, workspace.ErrMissingGeneration
	}
	payload := workspaceIndexJobPayloadFromReindex("reconcile", req.workspaceReindexRequest, req.DeleteMissing)
	job, err := beginWorkspaceIndexJob(cfg, payload)
	if err != nil {
		return workspaceReconcileResponse{}, err
	}
	resp, runErr := reconcileWorkspaceMarkdownDirect(ctx, cfg, req)
	if _, finishErr := finishWorkspaceIndexJob(cfg, job, runErr); finishErr != nil {
		return workspaceReconcileResponse{}, finishErr
	}
	return resp, nil
}

func reconcileWorkspaceMarkdownDirect(ctx context.Context, cfg APIConfig, req workspaceReconcileRequest) (workspaceReconcileResponse, error) {
	branch := defaultBranch(req.Branch)
	if req.ProjectID == "" {
		return workspaceReconcileResponse{}, workspace.ErrMissingProjectID
	}
	if req.Generation == "" {
		return workspaceReconcileResponse{}, workspace.ErrMissingGeneration
	}
	paths := append([]string(nil), req.Paths...)
	if len(paths) == 0 {
		var err error
		paths, err = listWorkspaceMarkdownPaths(workspaceProjectRoot(cfg, req.ProjectID, branch))
		if err != nil {
			return workspaceReconcileResponse{}, err
		}
	}
	req.Branch = branch
	req.Paths = paths
	if len(paths) == 0 {
		manifest, manifestPath, err := loadWorkspaceManifest(cfg, req.ProjectID, branch)
		if err != nil {
			return workspaceReconcileResponse{}, err
		}
		if req.ActivateGeneration {
			if err := manifest.ActivateGeneration(req.Generation); err != nil {
				return workspaceReconcileResponse{}, err
			}
			if err := manifest.Save(manifestPath); err != nil {
				return workspaceReconcileResponse{}, err
			}
		}
		return workspaceReconcileResponse{
			workspaceResponse: workspaceBaseResponse(manifest, manifestPath),
			Paths:             []string{},
			Results:           []workspace.IndexResult{},
		}, nil
	}
	reindexed, err := reindexWorkspaceMarkdownDirect(ctx, cfg, req.workspaceReindexRequest)
	if err != nil {
		return workspaceReconcileResponse{}, err
	}
	return workspaceReconcileResponse{
		workspaceResponse: reindexed.workspaceResponse,
		Paths:             paths,
		Results:           reindexed.Results,
	}, nil
}

func startWorkspaceRun(cfg APIConfig, req workspaceRunStartRequest) (workspaceRunResponse, error) {
	branch := defaultBranch(req.Branch)
	if req.ProjectID == "" {
		return workspaceRunResponse{}, workspace.ErrMissingProjectID
	}
	runID := req.RunID
	if runID == "" {
		runID = uuid.New().String()
	}
	runDir, err := workspaceRunDir(cfg, req.ProjectID, branch, runID)
	if err != nil {
		return workspaceRunResponse{}, err
	}
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return workspaceRunResponse{}, err
	}
	files := map[string]string{
		"metadata.md": runMetadataMarkdown(req, runID),
	}
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
		if err := os.WriteFile(filepath.Join(runDir, name), []byte(content), 0644); err != nil {
			return workspaceRunResponse{}, err
		}
	}
	return workspaceRunResponse{
		ProjectID: req.ProjectID,
		Branch:    branch,
		RunID:     runID,
		Path:      runDir,
		Files:     files,
	}, nil
}

func getWorkspaceRun(cfg APIConfig, req workspaceRunGetRequest) (workspaceRunResponse, error) {
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
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return workspaceRunResponse{}, err
	}
	files := make(map[string]string)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(runDir, entry.Name()))
		if err != nil {
			return workspaceRunResponse{}, err
		}
		files[entry.Name()] = string(data)
	}
	return workspaceRunResponse{
		ProjectID: req.ProjectID,
		Branch:    branch,
		RunID:     req.RunID,
		Path:      runDir,
		Files:     files,
	}, nil
}

func commitWorkspace(cfg APIConfig, req workspaceCommitRequest) (workspaceCommitRecord, error) {
	branch := defaultBranch(req.Branch)
	if req.ProjectID == "" {
		return workspaceCommitRecord{}, workspace.ErrMissingProjectID
	}
	commitID := uuid.New().String()
	commitDir, err := workspaceCommitDir(cfg, req.ProjectID, branch, commitID)
	if err != nil {
		return workspaceCommitRecord{}, err
	}
	filesDir := filepath.Join(commitDir, "files")
	if err := os.MkdirAll(filesDir, 0755); err != nil {
		return workspaceCommitRecord{}, err
	}
	root := workspaceProjectRoot(cfg, req.ProjectID, branch)
	files, err := snapshotWorkspaceFiles(root, filesDir)
	if err != nil {
		return workspaceCommitRecord{}, err
	}
	record := workspaceCommitRecord{
		ProjectID: req.ProjectID,
		Branch:    branch,
		CommitID:  commitID,
		Message:   req.Message,
		Author:    req.Author,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Path:      commitDir,
		Files:     files,
	}
	if err := saveWorkspaceCommitRecord(commitDir, record); err != nil {
		return workspaceCommitRecord{}, err
	}
	return record, nil
}

func logWorkspaceCommits(cfg APIConfig, req workspaceCommitRequest) (workspaceLogResponse, error) {
	branch := defaultBranch(req.Branch)
	if req.ProjectID == "" {
		return workspaceLogResponse{}, workspace.ErrMissingProjectID
	}
	dir := workspaceCommitsRoot(cfg, req.ProjectID, branch)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return workspaceLogResponse{ProjectID: req.ProjectID, Branch: branch, Commits: []workspaceCommitRecord{}}, nil
		}
		return workspaceLogResponse{}, err
	}
	commits := make([]workspaceCommitRecord, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		record, err := loadWorkspaceCommitRecord(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		commits = append(commits, record)
	}
	sort.Slice(commits, func(i, j int) bool {
		if commits[i].CreatedAt == commits[j].CreatedAt {
			return commits[i].CommitID > commits[j].CommitID
		}
		return commits[i].CreatedAt > commits[j].CreatedAt
	})
	return workspaceLogResponse{ProjectID: req.ProjectID, Branch: branch, Commits: commits}, nil
}

func revertWorkspace(ctx context.Context, cfg APIConfig, req workspaceRevertRequest) (workspaceRevertResponse, error) {
	branch := defaultBranch(req.Branch)
	if req.ProjectID == "" {
		return workspaceRevertResponse{}, workspace.ErrMissingProjectID
	}
	if req.CommitID == "" {
		return workspaceRevertResponse{}, errors.New("commit_id required")
	}
	commitDir, err := workspaceCommitDir(cfg, req.ProjectID, branch, req.CommitID)
	if err != nil {
		return workspaceRevertResponse{}, err
	}
	record, err := loadWorkspaceCommitRecord(commitDir)
	if err != nil {
		return workspaceRevertResponse{}, err
	}
	root := workspaceProjectRoot(cfg, req.ProjectID, branch)
	if err := os.RemoveAll(root); err != nil {
		return workspaceRevertResponse{}, err
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return workspaceRevertResponse{}, err
	}
	for _, file := range record.Files {
		src := filepath.Join(commitDir, "files", filepath.FromSlash(file.Path))
		dst := filepath.Join(root, filepath.FromSlash(file.Path))
		if err := copyWorkspaceFile(src, dst); err != nil {
			return workspaceRevertResponse{}, err
		}
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
	indexed, err := reconcileWorkspaceMarkdown(ctx, cfg, workspaceReconcileRequest{
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
	})
	if err != nil {
		return workspaceRevertResponse{}, err
	}
	resp.Indexed = &indexed
	return resp, nil
}

func deleteWorkspaceMarkdown(cfg APIConfig, req workspaceDeleteRequest) (workspaceDeleteResponse, error) {
	manifest, path, err := loadWorkspaceManifest(cfg, req.ProjectID, defaultBranch(req.Branch))
	if err != nil {
		return workspaceDeleteResponse{}, err
	}
	if req.Collection == "" {
		req.Collection = workspace.DefaultCollectionName(req.ProjectID, defaultBranch(req.Branch), req.Generation)
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
	ids, err := indexer.DeleteMarkdown(req.Path, workspace.IndexOptions{
		ProjectID:  req.ProjectID,
		Branch:     defaultBranch(req.Branch),
		Generation: req.Generation,
		Collection: req.Collection,
	})
	if err != nil {
		return workspaceDeleteResponse{}, err
	}
	if err := manifest.Save(path); err != nil {
		return workspaceDeleteResponse{}, err
	}
	return workspaceDeleteResponse{
		workspaceResponse: workspaceBaseResponse(manifest, path),
		DeletedVectorIDs:  ids,
	}, nil
}

func gcWorkspaceGenerations(cfg APIConfig, req workspaceGCRequest) (workspaceGCResponse, error) {
	manifest, path, err := loadWorkspaceManifest(cfg, req.ProjectID, defaultBranch(req.Branch))
	if err != nil {
		return workspaceGCResponse{}, err
	}
	pendingChunks := pendingGCChunks(manifest)
	if req.DryRun {
		result, err := workspace.PlanGCGenerations(manifest)
		if err != nil {
			return workspaceGCResponse{}, err
		}
		return workspaceGCResponse{
			workspaceResponse: workspaceBaseResponse(manifest, path),
			Result:            result,
		}, nil
	}
	store, err := workspaceVectorStore(cfg)
	if err != nil {
		return workspaceGCResponse{}, err
	}
	result, err := workspace.GCGenerations(manifest, store)
	if err != nil {
		return workspaceGCResponse{}, err
	}
	cleanupLexicalAfterGC(cfg, pendingChunks, result)
	if err := manifest.Save(path); err != nil {
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
	if idx := cfg.BM25Indexes[collection]; idx != nil {
		return idx
	}
	idx := bm25.NewIndex()
	cfg.BM25Indexes[collection] = idx
	return idx
}

func loadWorkspaceManifest(cfg APIConfig, projectID, branch string) (*workspace.Manifest, string, error) {
	if projectID == "" {
		return nil, "", workspace.ErrMissingProjectID
	}
	if branch == "" {
		branch = "main"
	}
	path := workspaceManifestPath(cfg, projectID, branch)
	manifest, err := workspace.LoadManifest(path)
	if err == nil {
		return manifest, path, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	return workspace.NewManifest(projectID, branch), path, nil
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

func listWorkspaceMarkdownPaths(root string) ([]string, error) {
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, err
	}
	var paths []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || strings.ToLower(filepath.Ext(path)) != ".md" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
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

func snapshotWorkspaceFiles(root, dstRoot string) ([]workspaceCommitFile, error) {
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []workspaceCommitFile{}, nil
		}
		return nil, err
	}
	var files []workspaceCommitFile
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		digest, err := copyWorkspaceFileWithDigest(path, filepath.Join(dstRoot, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		files = append(files, workspaceCommitFile{Path: rel, Digest: digest, Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func saveWorkspaceCommitRecord(commitDir string, record workspaceCommitRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(commitDir, "commit.json"), data, 0644)
}

func loadWorkspaceCommitRecord(commitDir string) (workspaceCommitRecord, error) {
	data, err := os.ReadFile(filepath.Join(commitDir, "commit.json"))
	if err != nil {
		return workspaceCommitRecord{}, err
	}
	var record workspaceCommitRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return workspaceCommitRecord{}, err
	}
	record.Path = commitDir
	if record.Files == nil {
		record.Files = []workspaceCommitFile{}
	}
	return record, nil
}

func copyWorkspaceFile(src, dst string) error {
	_, err := copyWorkspaceFileWithDigest(src, dst)
	return err
}

func copyWorkspaceFileWithDigest(src, dst string) (string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
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
		delete(cfg.BM25Indexes, coll)
	}
	for _, rec := range chunks {
		if _, ok := dropped[rec.Collection]; ok {
			continue
		}
		if idx := cfg.BM25Indexes[rec.Collection]; idx != nil {
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
	inner := h.toolSearch(ctx, searchArgs)
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
	resp, err := indexWorkspaceMarkdown(ctx, h.cfg, req)
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
	resp, err := readWorkspaceMarkdown(h.cfg, req)
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
	resp, err := writeWorkspaceMarkdown(ctx, h.cfg, req)
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
	resp, err := reindexWorkspaceMarkdown(ctx, h.cfg, req)
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
	resp, err := reconcileWorkspaceMarkdown(ctx, h.cfg, req)
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
	job, err := enqueueWorkspaceIndexJobFromPayload(h.cfg, payload)
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
	resp, err := retryWorkspaceIndexJob(ctx, h.cfg, req)
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}

func (h *mcpHandler) toolWorkspaceWatchStatus(_ context.Context, _ map[string]any) mcpToolResult {
	return workspaceMCPJSON(workspaceWatchStatus(h.cfg))
}

func (h *mcpHandler) toolWorkspaceRunStart(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceRunStartRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessWrite); err != nil {
		return workspaceMCPError(err)
	}
	resp, err := startWorkspaceRun(h.cfg, req)
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
	resp, err := commitWorkspace(h.cfg, req)
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
	resp, err := revertWorkspace(ctx, h.cfg, req)
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
	resp, err := deleteWorkspaceMarkdown(h.cfg, req)
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
	resp, err := gcWorkspaceGenerations(h.cfg, req)
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
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return workspaceMCPError(err)
	}
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(data)}}}
}

func workspaceMCPError(err error) mcpToolResult {
	return mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: err.Error()}},
		IsError: true,
	}
}
