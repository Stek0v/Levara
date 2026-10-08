package http

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/workspace"
)

// NewTaskExecutor supplies the server's real, constrained MCP dispatcher.
// Only bounded workspace reads and non-indexing writes are currently supported.
func NewTaskExecutor(cfg APIConfig) mcp.TaskStepExecutor {
	h := &mcpHandler{cfg: cfg, sessions: mcp.NewSessionStore()}
	return mcp.NewTaskExecutor(h, func(ctx context.Context, e *mcp.TaskExecution) (mcp.ToolResult, error) {
		if err := validateTaskWorkspaceAction(e); err != nil {
			return mcp.ToolResult{}, err
		}
		session := &mcp.Session{}
		session.SetUserID(e.OwnerID)
		return h.executeTool(ctx, session, e.Action.Name, e.Action.Arguments), nil
	})
}

func validateTaskWorkspaceAction(e *mcp.TaskExecution) error {
	if e.Action.Name != "workspace_read" && e.Action.Name != "workspace_write" {
		return errors.New("task executor supports only workspace_read and workspace_write; network and shell actions are unsupported")
	}
	allowed := map[string]bool{"project_id": true, "branch": true, "path": true}
	if e.Action.Name == "workspace_write" {
		allowed["text"] = true
		allowed["index"] = true
		allowed["expected_file_digest"] = true
	}
	for k, v := range e.Action.Arguments {
		if !allowed[k] {
			return fmt.Errorf("unsupported task workspace argument: %s", k)
		}
		if k == "index" {
			if v != false {
				return errors.New("task workspace indexing is unsupported")
			}
			continue
		}
		if _, ok := v.(string); !ok {
			return fmt.Errorf("task workspace argument must be a string: %s", k)
		}
	}
	for _, k := range []string{"project_id", "path"} {
		v, _ := e.Action.Arguments[k].(string)
		if v == "" {
			return fmt.Errorf("task workspace %s required", k)
		}
	}
	if e.Action.Name == "workspace_write" {
		if _, ok := e.Action.Arguments["text"]; !ok {
			return errors.New("task workspace text required")
		}
	}
	return nil
}

func taskWorkspacePath(projectID, branch, rel string) (string, error) {
	if projectID == "" || strings.ContainsRune(projectID, 0) || workspace.SafeID(branch) != branch {
		return "", errors.New("task workspace requires a project ID and canonical branch")
	}
	if path.IsAbs(rel) || rel == "" || rel == "." || path.Clean(rel) != rel || strings.Contains(rel, "\\") || strings.ContainsRune(rel, 0) || strings.HasPrefix(rel, "../") {
		return "", errors.New("task workspace path must be canonical and relative")
	}
	return path.Join("projects", workspace.SafeID(projectID), branch, rel), nil
}

func taskWorkspaceFence(ctx context.Context, cfg APIConfig, e *mcp.TaskExecution, projectID, action string, fn func() error) error {
	return e.Fence(ctx, func(tx *sql.Tx) error {
		policy := accesspkg.SQLPolicy{DB: cfg.DB, Q: Q}
		decision, err := policy.AuthorizeWorkspaceFenced(ctx, tx, GetDBProvider() == DBSQLite, accesspkg.WorkspaceRequest{UserID: e.OwnerID, ProjectID: projectID, Action: action})
		if err != nil {
			return err
		}
		if !decision.Allowed || decision.DevMode {
			return errors.New("task workspace permission denied")
		}
		if !mcp.ToolAllowedForMode(os.Getenv("LEVARA_MCP_TOOLSET"), e.Action.Name) || !mcp.ToolAllowedForMode(os.Getenv("LEVARA_MCP_TOOLSET"), "task_step") {
			return errors.New("task tool disabled by active profile or feature flag")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return fn()
	})
}

func taskReadWorkspaceMarkdown(ctx context.Context, cfg APIConfig, req workspaceReadRequest, e *mcp.TaskExecution) (workspaceReadResponse, error) {
	branch := defaultBranch(req.Branch)
	rel, err := taskWorkspacePath(req.ProjectID, branch, req.Path)
	if err != nil {
		return workspaceReadResponse{}, err
	}
	var data []byte
	err = taskWorkspaceFence(ctx, cfg, e, req.ProjectID, accesspkg.ActionRead, func() error {
		release, err := workspace.LockProject(ctx, workspaceRoot(cfg), req.ProjectID)
		if err != nil {
			return err
		}
		defer release()
		if err := recoverWorkspaceRestores(ctx, cfg, req.ProjectID); err != nil {
			return err
		}
		data, err = taskWorkspaceFile(ctx, workspaceRoot(cfg), e, rel, nil, nil)
		return err
	})
	if err != nil {
		return workspaceReadResponse{}, err
	}
	if !utf8.Valid(data) {
		return workspaceReadResponse{}, errors.New("workspace text must be valid UTF-8")
	}
	return workspaceReadResponse{ProjectID: req.ProjectID, Branch: branch, Path: req.Path, Text: string(data), FileDigest: digestBytes(data), Citation: workspaceFileCitation(req.ProjectID, branch, req.Path)}, nil
}

func taskWriteWorkspaceMarkdown(ctx context.Context, cfg APIConfig, req workspaceWriteRequest, e *mcp.TaskExecution) (workspaceWriteResponse, error) {
	if !utf8.ValidString(req.Text) {
		return workspaceWriteResponse{}, errors.New("workspace text must be valid UTF-8")
	}
	if req.Generation != "" || (req.Index != nil && *req.Index) {
		return workspaceWriteResponse{}, errors.New("task workspace indexing is unsupported")
	}
	branch := defaultBranch(req.Branch)
	rel, err := taskWorkspacePath(req.ProjectID, branch, req.Path)
	if err != nil {
		return workspaceWriteResponse{}, err
	}
	unlock := workspaceManifestLocks.lock(workspaceBranchLockKey(req.ProjectID, branch))
	defer unlock()
	err = taskWorkspaceFence(ctx, cfg, e, req.ProjectID, accesspkg.ActionWrite, func() error {
		release, err := workspace.LockProject(ctx, workspaceRoot(cfg), req.ProjectID)
		if err != nil {
			return err
		}
		defer release()
		if err := recoverWorkspaceRestores(ctx, cfg, req.ProjectID); err != nil {
			return err
		}
		_, err = taskWorkspaceFile(ctx, workspaceRoot(cfg), e, rel, []byte(req.Text), req.ExpectedFileDigest)
		return err
	})
	if err != nil {
		return workspaceWriteResponse{}, err
	}
	return workspaceWriteResponse{ProjectID: req.ProjectID, Branch: branch, Path: req.Path, Bytes: len([]byte(req.Text))}, nil
}
