package http

import (
	"bufio"
	"context"
	"database/sql"
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
	"github.com/stek0v/levara/internal/metrics"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/audit"
)

type workspaceAccessCheckRequest struct {
	ProjectID string `json:"project_id"`
	Access    string `json:"access,omitempty"`
}

type workspaceAccessCheckResponse struct {
	ProjectID     string `json:"project_id"`
	UserID        string `json:"user_id,omitempty"`
	Access        string `json:"access"`
	Allowed       bool   `json:"allowed"`
	Role          string `json:"role,omitempty"`
	Reason        string `json:"reason,omitempty"`
	DevMode       bool   `json:"dev_mode,omitempty"`
	Authenticated bool   `json:"authenticated"`
	APIKeyAllowed bool   `json:"api_key_allowed"`
}

type workspaceAuditListRequest struct {
	ProjectID string `json:"project_id"`
	Branch    string `json:"branch,omitempty"`
	Operation string `json:"operation,omitempty"`
	Result    string `json:"result,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type workspaceAuditEvent struct {
	ID        string         `json:"id"`
	At        string         `json:"at"`
	Source    string         `json:"source"`
	Operation string         `json:"operation"`
	ProjectID string         `json:"project_id"`
	Branch    string         `json:"branch,omitempty"`
	UserID    string         `json:"user_id,omitempty"`
	Access    string         `json:"access,omitempty"`
	Result    string         `json:"result"`
	Status    int            `json:"status,omitempty"`
	Error     string         `json:"error,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type workspaceAuditLogResponse struct {
	ProjectID string                `json:"project_id"`
	Branch    string                `json:"branch,omitempty"`
	Events    []workspaceAuditEvent `json:"events"`
	Total     int                   `json:"total"`
	Limit     int                   `json:"limit"`
}

func workspaceAuditMiddleware(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		projectID, branch, metadata := workspaceAuditRequestSnapshot(c)
		if projectID == "" {
			return err
		}
		operation := workspaceAuditOperationFromPath(c.Path())
		if operation == "" {
			return err
		}
		status := c.Response().StatusCode()
		if ferr, ok := err.(*fiber.Error); ok {
			status = ferr.Code
		}
		result := workspaceAuditResult(status, err)
		metadata["method"] = c.Method()
		metadata["duration_ms"] = time.Since(start).Milliseconds()
		userID, _ := c.Locals("user_id").(string)
		_ = recordWorkspaceAuditEvent(cfg, workspaceAuditEvent{
			ID:        uuid.NewString(),
			At:        time.Now().UTC().Format(time.RFC3339Nano),
			Source:    "rest",
			Operation: operation,
			ProjectID: projectID,
			Branch:    defaultBranch(branch),
			UserID:    userID,
			Access:    string(workspaceAuditOperationAccess(operation)),
			Result:    result,
			Status:    status,
			Error:     workspaceAuditErrorCode(status, err),
			Metadata:  metadata,
		})
		return err
	}
}

func workspaceAccessCheckHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req workspaceAccessCheckRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		access, err := workspaceAccessLevelFromString(req.Access)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		userID, _ := c.Locals("user_id").(string)
		apiKeyPerms, _ := c.Locals("api_key_permissions").(string)
		resp, err := workspaceAccessCheck(c.UserContext(), cfg.DB, userID, req.ProjectID, access, apiKeyPerms)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "workspace access check failed"})
		}
		return c.JSON(resp)
	}
}

func workspaceAuditLogHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		req := workspaceAuditListRequest{
			ProjectID: c.Query("project_id"),
			Branch:    c.Query("branch"),
			Operation: c.Query("operation"),
			Result:    c.Query("result"),
			Limit:     c.QueryInt("limit", 100),
		}
		if err := authorizeWorkspaceFiber(c, cfg, req.ProjectID, workspaceAccessRead); err != nil {
			return err
		}
		resp, err := listWorkspaceAuditEvents(cfg, req)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(resp)
	}
}

func workspaceAccessCheck(ctx context.Context, db *sql.DB, userID, projectID string, access workspaceAccessLevel, apiKeyPerms string) (workspaceAccessCheckResponse, error) {
	decision, err := workspaceAccessDecision(ctx, db, userID, projectID, access, apiKeyPerms)
	if err != nil {
		return workspaceAccessCheckResponse{}, err
	}
	return workspaceAccessCheckResponse{
		ProjectID:     projectID,
		UserID:        userID,
		Access:        string(access),
		Allowed:       decision.Allowed,
		Role:          decision.Role,
		Reason:        decision.Reason,
		DevMode:       decision.DevMode,
		Authenticated: decision.Authenticated,
		APIKeyAllowed: decision.APIKeyAllowed,
	}, nil
}

type accessDB = *sql.DB

func workspaceAccessDecision(ctx context.Context, db accessDB, userID, projectID string, access workspaceAccessLevel, apiKeyPerms string) (accesspkg.Decision, error) {
	if access == "" {
		access = workspaceAccessRead
	}
	return accesspkg.SQLPolicy{DB: db, Q: Q}.AuthorizeWorkspace(ctx, accesspkg.WorkspaceRequest{
		UserID:            userID,
		ProjectID:         projectID,
		Action:            string(access),
		APIKeyPermissions: apiKeyPerms,
	})
}

func workspaceAccessLevelFromString(access string) (workspaceAccessLevel, error) {
	switch strings.ToLower(strings.TrimSpace(access)) {
	case "", string(workspaceAccessRead):
		return workspaceAccessRead, nil
	case string(workspaceAccessWrite):
		return workspaceAccessWrite, nil
	default:
		return "", fmt.Errorf("access must be %q or %q", workspaceAccessRead, workspaceAccessWrite)
	}
}

func recordWorkspaceAuditEvent(cfg APIConfig, event workspaceAuditEvent) error {
	if event.ProjectID == "" || event.Operation == "" {
		return nil
	}
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.At == "" {
		event.At = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if event.Branch == "" {
		event.Branch = "main"
	}
	event.ProjectID = safeWorkspaceID(event.ProjectID)
	event.Branch = defaultBranch(event.Branch)
	path := workspaceAuditPath(cfg, event.ProjectID, time.Now().UTC())
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	metrics.WorkspaceAuditEventsTotal.WithLabelValues(event.Source, event.Operation, event.Result).Inc()
	if cfg.WorkspaceAuditSink != nil {
		cfg.WorkspaceAuditSink.LogEvent(audit.Event{
			TS:      event.At,
			Source:  "workspace." + event.Source,
			Type:    event.Operation,
			Subject: event.ProjectID,
			ActorID: event.UserID,
			Outcome: event.Result,
			Metadata: map[string]any{
				"branch": event.Branch,
				"access": event.Access,
				"status": event.Status,
				"error":  event.Error,
			},
		})
	}
	return nil
}

func listWorkspaceAuditEvents(cfg APIConfig, req workspaceAuditListRequest) (workspaceAuditLogResponse, error) {
	if req.ProjectID == "" {
		return workspaceAuditLogResponse{}, errors.New("project_id required")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	dir := workspaceAuditDir(cfg, req.ProjectID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return workspaceAuditLogResponse{ProjectID: req.ProjectID, Branch: req.Branch, Events: []workspaceAuditEvent{}, Limit: limit}, nil
		}
		return workspaceAuditLogResponse{}, err
	}
	var events []workspaceAuditEvent
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		fileEvents, err := readWorkspaceAuditFile(filepath.Join(dir, entry.Name()), req)
		if err != nil {
			return workspaceAuditLogResponse{}, err
		}
		events = append(events, fileEvents...)
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].At == events[j].At {
			return events[i].ID > events[j].ID
		}
		return events[i].At > events[j].At
	})
	total := len(events)
	if len(events) > limit {
		events = events[:limit]
	}
	return workspaceAuditLogResponse{
		ProjectID: req.ProjectID,
		Branch:    req.Branch,
		Events:    events,
		Total:     total,
		Limit:     limit,
	}, nil
}

func readWorkspaceAuditFile(path string, req workspaceAuditListRequest) ([]workspaceAuditEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var events []workspaceAuditEvent
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var event workspaceAuditEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if req.Branch != "" && event.Branch != defaultBranch(req.Branch) {
			continue
		}
		if req.Operation != "" && event.Operation != req.Operation {
			continue
		}
		if req.Result != "" && event.Result != req.Result {
			continue
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}

func workspaceAuditDir(cfg APIConfig, projectID string) string {
	return filepath.Join(workspaceRoot(cfg), ".kb", "audit", safeWorkspaceID(projectID))
}

func workspaceAuditPath(cfg APIConfig, projectID string, at time.Time) string {
	return filepath.Join(workspaceAuditDir(cfg, projectID), "audit-"+at.Format("2006-01")+".jsonl")
}

func workspaceAuditRequestSnapshot(c *fiber.Ctx) (string, string, map[string]any) {
	metadata := map[string]any{}
	projectID := c.Query("project_id")
	branch := c.Query("branch")
	var raw map[string]any
	if len(c.Body()) > 0 {
		_ = json.Unmarshal(c.Body(), &raw)
	}
	if projectID == "" {
		projectID = workspaceStringFromMap(raw, "project_id")
	}
	if branch == "" {
		branch = workspaceStringFromMap(raw, "branch")
	}
	for _, key := range []string{"generation", "collection", "operation", "commit_id", "job_id"} {
		if v := workspaceStringFromMap(raw, key); v != "" {
			metadata[key] = v
		} else if v := c.Query(key); v != "" {
			metadata[key] = v
		}
	}
	if _, ok := raw["path"]; ok {
		metadata["path_count"] = 1
	}
	if paths, ok := raw["paths"].([]any); ok {
		metadata["path_count"] = len(paths)
	}
	if _, ok := raw["text"]; ok {
		metadata["text_provided"] = true
	}
	if _, ok := raw["query"]; ok {
		metadata["query_provided"] = true
	}
	if _, ok := raw["search_query"]; ok {
		metadata["query_provided"] = true
	}
	return projectID, defaultBranch(branch), metadata
}

func workspaceStringFromMap(raw map[string]any, key string) string {
	if raw == nil {
		return ""
	}
	if v, ok := raw[key].(string); ok {
		return v
	}
	return ""
}

func workspaceAuditOperationFromPath(path string) string {
	i := strings.Index(path, "/workspace/")
	if i < 0 {
		return ""
	}
	rest := strings.Trim(path[i+len("/workspace/"):], "/")
	switch rest {
	case "access/check":
		return "access_check"
	case "context/artifacts":
		return "context_artifacts"
	case "context/artifacts/reindex":
		return "reindex_artifacts"
	case "conflicts":
		return "conflicts"
	case "ops/status":
		return "ops_status"
	case "jobs":
		return "index_jobs"
	case "jobs/enqueue":
		return "enqueue_index_job"
	case "jobs/retry":
		return "retry_index_job"
	case "watch/status":
		return "watch_status"
	case "runs/start":
		return "run_start"
	case "runs/get":
		return "run_get"
	default:
		return strings.ReplaceAll(rest, "/", "_")
	}
}

func workspaceAuditOperationAccess(operation string) workspaceAccessLevel {
	switch operation {
	case "access_check", "read", "manifest", "search", "index_jobs", "watch_status", "run_get", "log", "audit", "audit_log", "ops_status", "context_artifacts", "conflicts":
		return workspaceAccessRead
	default:
		return workspaceAccessWrite
	}
}

func workspaceAuditResult(status int, err error) string {
	if err != nil {
		if ferr, ok := err.(*fiber.Error); ok && ferr.Code == fiber.StatusForbidden {
			return "denied"
		}
		return "failure"
	}
	if status == fiber.StatusForbidden || status == fiber.StatusUnauthorized {
		return "denied"
	}
	if status >= 400 {
		return "failure"
	}
	return "success"
}

func workspaceAuditErrorCode(status int, err error) string {
	if err == nil && status < 400 {
		return ""
	}
	if status == 0 {
		status = fiber.StatusInternalServerError
	}
	return fmt.Sprintf("http_%d", status)
}

func (h *mcpHandler) auditWorkspaceTool(ctx context.Context, name string, args map[string]any, result mcpToolResult) {
	if !strings.HasPrefix(name, "workspace_") {
		return
	}
	projectID, _ := args["project_id"].(string)
	if projectID == "" {
		return
	}
	branch, _ := args["branch"].(string)
	metadata := workspaceAuditMetadataFromArgs(args)
	userID, _ := ctx.Value(mcpUserIDKey).(string)
	auditResult := "success"
	if result.IsError {
		auditResult = "failure"
		if len(result.Content) > 0 && strings.Contains(strings.ToLower(result.Content[0].Text), "access denied") {
			auditResult = "denied"
		}
	}
	_ = recordWorkspaceAuditEvent(h.cfg, workspaceAuditEvent{
		ID:        uuid.NewString(),
		At:        time.Now().UTC().Format(time.RFC3339Nano),
		Source:    "mcp",
		Operation: strings.TrimPrefix(name, "workspace_"),
		ProjectID: projectID,
		Branch:    defaultBranch(branch),
		UserID:    userID,
		Access:    string(workspaceAuditOperationAccess(strings.TrimPrefix(name, "workspace_"))),
		Result:    auditResult,
		Error:     workspaceMCPAuditErrorCode(result),
		Metadata:  metadata,
	})
}

func workspaceAuditMetadataFromArgs(args map[string]any) map[string]any {
	metadata := map[string]any{}
	for _, key := range []string{"generation", "collection", "operation", "commit_id", "job_id"} {
		if v, ok := args[key].(string); ok && v != "" {
			metadata[key] = v
		}
	}
	if _, ok := args["path"]; ok {
		metadata["path_count"] = 1
	}
	if paths, ok := args["paths"].([]any); ok {
		metadata["path_count"] = len(paths)
	} else if paths, ok := args["paths"].([]string); ok {
		metadata["path_count"] = len(paths)
	}
	if _, ok := args["text"]; ok {
		metadata["text_provided"] = true
	}
	if _, ok := args["query"]; ok {
		metadata["query_provided"] = true
	}
	if _, ok := args["search_query"]; ok {
		metadata["query_provided"] = true
	}
	return metadata
}

func workspaceMCPAuditErrorCode(result mcpToolResult) string {
	if !result.IsError {
		return ""
	}
	if len(result.Content) == 0 {
		return "mcp_error"
	}
	if strings.Contains(strings.ToLower(result.Content[0].Text), "access denied") {
		return "mcp_access_denied"
	}
	return "mcp_error"
}

func (h *mcpHandler) toolWorkspaceAccessCheck(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceAccessCheckRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	access, err := workspaceAccessLevelFromString(req.Access)
	if err != nil {
		return workspaceMCPError(err)
	}
	userID, _ := ctx.Value(mcpUserIDKey).(string)
	resp, err := workspaceAccessCheck(ctx, h.cfg.DB, userID, req.ProjectID, access, "")
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}

func (h *mcpHandler) toolWorkspaceAuditLog(ctx context.Context, args map[string]any) mcpToolResult {
	var req workspaceAuditListRequest
	if err := decodeWorkspaceArgs(args, &req); err != nil {
		return workspaceMCPError(err)
	}
	if err := authorizeWorkspaceMCP(ctx, h.cfg, req.ProjectID, workspaceAccessRead); err != nil {
		return workspaceMCPError(err)
	}
	resp, err := listWorkspaceAuditEvents(h.cfg, req)
	if err != nil {
		return workspaceMCPError(err)
	}
	return workspaceMCPJSON(resp)
}
