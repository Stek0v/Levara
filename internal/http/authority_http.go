package http

// authority_http.go — claim-time authority manifest verification (backlog B4).
// The workspace root is known at this layer, so manifest digest checks for
// task_step claims happen here before delegating to the MCP tool.

import (
	"database/sql"
	"fmt"

	"github.com/stek0v/levara/pkg/mcp"
)

// verifyTaskAuthorityAtClaim loads the authority manifest bound to the task
// (if any) and verifies its digest. A swapped or escaped manifest fails the
// claim with an explicit error. Silent no-op for tasks without a binding.
func (h *mcpHandler) verifyTaskAuthorityAtClaim(args map[string]any) error {
	action, _ := args["action"].(string)
	taskID, _ := args["task_id"].(string)
	if action != "claim" || taskID == "" || h.cfg.DB == nil {
		return nil
	}
	var authorityJSON sql.NullString
	err := h.cfg.DB.QueryRow(Q(`SELECT authority_json FROM tasks WHERE id=$1`), taskID).Scan(&authorityJSON)
	if err != nil || !authorityJSON.Valid || authorityJSON.String == "" {
		return nil // no binding — nothing to verify
	}
	if _, err := mcp.VerifyTaskManifest(workspaceRoot(h.cfg), authorityJSON.String); err != nil {
		return fmt.Errorf("authority manifest verification failed: %w", err)
	}
	return nil
}
