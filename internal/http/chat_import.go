// chat_import.go — authorized imported transcripts and import ledgers.
package http

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/chatimport"
)

func ensureChatImportSchema(ctx context.Context, cfg APIConfig) error {
	if cfg.DB == nil {
		return fiber.NewError(503, "chat import storage unavailable")
	}
	return chatimport.EnsureSchema(ctx, cfg.DB, Q)
}

func RegisterChatImportAPI(app fiber.Router, cfg APIConfig) {
	app.Post("/chats/import", chatImportPostHandler(cfg))
	app.Get("/chats/import/runs", chatImportRunsHandler(cfg))
	app.Get("/chats/import/sessions", chatImportSessionsHandler(cfg))
	app.Get("/chats/import/sessions/:platform/:sessionId", chatImportSessionHandler(cfg))
	app.Post("/chats/import/sessions/:platform/:sessionId/project", chatImportProjectHandler(cfg, false))
	app.Delete("/chats/import/sessions/:platform/:sessionId/project", chatImportProjectHandler(cfg, true))
}

type chatImportRunRequest struct {
	RunID        string                   `json:"run_id"`
	SourcePath   string                   `json:"source_path"`
	SourceSHA256 string                   `json:"source_sha256"`
	Skipped      int                      `json:"skipped"`
	Finish       bool                     `json:"finish"`
	Conversation *chatimport.Conversation `json:"conversation"`
}

// The explicit local path also supports standalone import databases without ACL tables.
func chatImportFence(ctx context.Context, cfg APIConfig, actor access.MetadataActor, write bool) (*sql.Tx, access.SQLPolicy, func(), error) {
	policy := access.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}
	var tx *sql.Tx
	var release func()
	var err error
	local := actor.TrustedLocal && actor.UserID == "" && actor.TenantID == ""
	if local {
		tx, err = cfg.DB.BeginTx(ctx, nil)
		if err == nil {
			policy = policy.WithReadTransaction(tx)
			release = func() { _ = tx.Rollback() }
		}
	} else if write {
		tx, policy, err = policy.BeginMetadataWrite(ctx, actor, GetDBProvider() == DBSQLite)
		if err == nil {
			release = func() { _ = tx.Rollback() }
		}
	} else {
		tx, policy, release, err = policy.BeginTransferFenceTx(ctx, GetDBProvider() == DBSQLite)
	}
	if err != nil {
		return nil, policy, nil, err
	}
	action := access.ActionRead
	if write {
		action = access.ActionWrite
	}
	if err = chatimport.LockChatRegistry(ctx, tx, GetDBProvider() == DBSQLite, write); err == nil {
		err = chatimport.RecheckChatActor(ctx, policy, actor, action)
	}
	if err != nil {
		release()
		return nil, policy, nil, err
	}
	return tx, policy, release, nil
}

func chatImportPostHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		actor := uploadMetadataActor(c, cfg, ctx)
		if err := ensureChatImportSchema(ctx, cfg); err != nil {
			return chatImportStorageError(err)
		}
		var req chatImportRunRequest
		if c.BodyParser(&req) != nil {
			return fiber.NewError(400, "valid JSON body required")
		}
		conv := req.Conversation
		if conv == nil || conv.SessionID == "" || conv.Platform == "" {
			return fiber.NewError(400, "conversation with platform and session_id required")
		}
		switch conv.Platform {
		case chatimport.PlatformCodex, chatimport.PlatformClaudeCode, chatimport.PlatformCursor:
		default:
			return fiber.NewError(400, fmt.Sprintf("unknown platform %q", conv.Platform))
		}
		if req.RunID == "" {
			req.RunID = uuid.NewString()
		}
		tx, policy, release, err := chatImportFence(ctx, cfg, actor, true)
		if err != nil {
			return chatImportStorageError(err)
		}
		defer release()
		scope := chatimport.ImportScope{OwnerID: actor.UserID, TenantID: actor.TenantID, TrustedLocal: actor.TrustedLocal}
		runID, err := chatimport.StartScopedRunTx(ctx, tx, Q, scope, chatimport.RunInfo{ID: req.RunID, Platform: conv.Platform, SourcePath: req.SourcePath, SourceSHA256: req.SourceSHA256, StartedAt: time.Now().UTC().Format(time.RFC3339)})
		if err != nil {
			return chatImportStorageError(err)
		}
		warnings := []string{}
		chat, inserted, err := chatimport.InsertScopedConversationTx(ctx, tx, Q, scope, runID, conv, &warnings, time.Now())
		if err != nil {
			return chatImportStorageError(err)
		}
		status := "running"
		if req.Finish {
			status = "ok"
			if err := chatimport.FinishRunTx(ctx, tx, Q, runID, status, inserted, req.Skipped, len(warnings), warnings, time.Now().UTC().Format(time.RFC3339)); err != nil {
				return chatImportStorageError(err)
			}
		}
		if err := chatimport.RecheckChatActor(ctx, policy, actor, access.ActionWrite); err != nil {
			return chatImportStorageError(err)
		}
		if err := tx.Commit(); err != nil {
			return chatImportStorageError(err)
		}
		c.Set("Cache-Control", "private, no-store")
		return c.Status(http.StatusCreated).JSON(fiber.Map{"run_id": runID, "chat_id": chat.ID, "platform": string(chat.Platform), "session_id": chat.SourceSessionID, "inserted": inserted, "messages": len(conv.Messages), "status": status, "warned_count": len(warnings), "warnings": warnings})
	}
}

func chatImportRunsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		actor := uploadMetadataActor(c, cfg, ctx)
		if err := ensureChatImportSchema(ctx, cfg); err != nil {
			return chatImportStorageError(err)
		}
		tx, policy, release, err := chatImportFence(ctx, cfg, actor, false)
		if err != nil {
			return chatImportStorageError(err)
		}
		defer release()
		query := `SELECT r.id,r.platform,r.source_path,r.status,r.imported_count,r.skipped_count,r.warned_count,r.started_at,r.finished_at FROM chat_import_runs r LEFT JOIN chat_import_run_scopes s ON s.run_id=r.id WHERE `
		args := []any{}
		if actor.UserID == "" {
			query += `(s.run_id IS NULL OR (s.owner_id='' AND s.tenant_id=''))`
		} else {
			query += `s.owner_id=$1 AND s.tenant_id=$2`
			args = append(args, actor.UserID, actor.TenantID)
		}
		if p := c.Query("platform"); p != "" {
			query += fmt.Sprintf(" AND r.platform=$%d", len(args)+1)
			args = append(args, p)
		}
		query += fmt.Sprintf(" ORDER BY r.started_at DESC LIMIT $%d", len(args)+1)
		args = append(args, chatImportLimit(c, 100, 500))
		rows, err := tx.QueryContext(ctx, Q(query), args...)
		if err != nil {
			return chatImportStorageError(err)
		}
		defer rows.Close()
		runs := []fiber.Map{}
		for rows.Next() {
			var id, platform, path, status, started, finished string
			var imported, skipped, warned int
			if err := rows.Scan(&id, &platform, &path, &status, &imported, &skipped, &warned, &started, &finished); err != nil {
				return chatImportStorageError(err)
			}
			runs = append(runs, fiber.Map{"id": id, "platform": platform, "source_path": path, "status": status, "imported_count": imported, "skipped_count": skipped, "warned_count": warned, "started_at": started, "finished_at": finished})
		}
		if err := rows.Err(); err != nil {
			return chatImportStorageError(err)
		}
		if err := rows.Close(); err != nil {
			return chatImportStorageError(err)
		}
		if err := chatimport.RecheckChatActor(ctx, policy, actor, access.ActionRead); err != nil {
			return chatImportStorageError(err)
		}
		c.Set("Cache-Control", "private, no-store")
		return c.JSON(fiber.Map{"runs": runs})
	}
}

func chatImportSessionsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		actor := uploadMetadataActor(c, cfg, ctx)
		if err := ensureChatImportSchema(ctx, cfg); err != nil {
			return chatImportStorageError(err)
		}
		tx, policy, release, err := chatImportFence(ctx, cfg, actor, false)
		if err != nil {
			return chatImportStorageError(err)
		}
		defer release()
		query := `SELECT m.platform,m.session_id,COALESCE(s.owner_id,''),COALESCE(s.tenant_id,''),COALESCE(s.source_session_id,m.session_id),COALESCE(s.project_id,''),MAX(m.session_title),COUNT(*),MIN(m.source_created_at),MAX(m.source_created_at) FROM chat_import_messages m LEFT JOIN chat_import_sessions s ON s.id=m.session_id AND s.platform=m.platform`
		args := []any{}
		if p := c.Query("platform"); p != "" {
			query += " WHERE m.platform=$1"
			args = append(args, p)
		}
		query += ` GROUP BY m.platform,m.session_id,s.owner_id,s.tenant_id,s.source_session_id,s.project_id ORDER BY MIN(m.source_created_at) DESC`
		rows, err := tx.QueryContext(ctx, Q(query), args...)
		if err != nil {
			return chatImportStorageError(err)
		}
		defer rows.Close()
		type entry struct {
			chat               chatimport.ChatIdentity
			title, first, last string
			count              int
		}
		entries := []entry{}
		for rows.Next() {
			var e entry
			if err := rows.Scan(&e.chat.Platform, &e.chat.ID, &e.chat.OwnerID, &e.chat.TenantID, &e.chat.SourceSessionID, &e.chat.ProjectID, &e.title, &e.count, &e.first, &e.last); err != nil {
				return chatImportStorageError(err)
			}
			entries = append(entries, e)
		}
		if err := rows.Err(); err != nil {
			return chatImportStorageError(err)
		}
		if err := rows.Close(); err != nil {
			return chatImportStorageError(err)
		}
		sessions := []fiber.Map{}
		limit := chatImportLimit(c, 100, 1000)
		for _, e := range entries {
			allowed, err := chatimport.CanReadChat(ctx, tx, Q, policy, actor, e.chat)
			if err != nil {
				return chatImportStorageError(err)
			}
			if !allowed {
				continue
			}
			if len(sessions) < limit {
				sessions = append(sessions, fiber.Map{"chat_id": e.chat.ID, "platform": string(e.chat.Platform), "session_id": e.chat.SourceSessionID, "project_id": e.chat.ProjectID, "title": e.title, "message_count": e.count, "first_message_at": e.first, "last_message_at": e.last})
			}
		}
		if err := chatimport.RecheckChatActor(ctx, policy, actor, access.ActionRead); err != nil {
			return chatImportStorageError(err)
		}
		c.Set("Cache-Control", "private, no-store")
		return c.JSON(fiber.Map{"sessions": sessions})
	}
}

func chatImportSessionHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		actor := uploadMetadataActor(c, cfg, ctx)
		if err := ensureChatImportSchema(ctx, cfg); err != nil {
			return chatImportStorageError(err)
		}
		platform := c.Params("platform")
		switch chatimport.Platform(platform) {
		case chatimport.PlatformCodex, chatimport.PlatformClaudeCode, chatimport.PlatformCursor:
		default:
			return fiber.NewError(400, fmt.Sprintf("unknown platform %q", platform))
		}
		tx, policy, release, err := chatImportFence(ctx, cfg, actor, false)
		if err != nil {
			return chatImportStorageError(err)
		}
		defer release()
		chat, err := chatimport.ResolveChat(ctx, tx, Q, policy, actor, c.Query("chat_id"), platform, c.Params("sessionId"))
		if err != nil {
			return chatImportStorageError(err)
		}
		rows, err := tx.QueryContext(ctx, Q(`SELECT external_id,ordinal,role,kind,model,content,source_created_at,metadata FROM chat_import_messages WHERE platform=$1 AND session_id=$2 ORDER BY ordinal,source_created_at,external_id LIMIT $3`), platform, chat.ID, chatImportLimit(c, 1000, 5000))
		if err != nil {
			return chatImportStorageError(err)
		}
		defer rows.Close()
		messages := []fiber.Map{}
		for rows.Next() {
			var externalID, role, kind, model, content, created, metadata string
			var ordinal int
			if err := rows.Scan(&externalID, &ordinal, &role, &kind, &model, &content, &created, &metadata); err != nil {
				return chatImportStorageError(err)
			}
			messages = append(messages, fiber.Map{"external_id": externalID, "ordinal": ordinal, "role": role, "kind": kind, "model": model, "content": content, "created_at": created, "metadata": metadata})
		}
		if err := rows.Err(); err != nil {
			return chatImportStorageError(err)
		}
		if err := rows.Close(); err != nil {
			return chatImportStorageError(err)
		}
		if len(messages) == 0 {
			return fiber.NewError(404, "imported session not found")
		}
		if allowed, err := chatimport.CanReadChat(ctx, tx, Q, policy, actor, chat); err != nil {
			return chatImportStorageError(err)
		} else if !allowed {
			return chatImportStorageError(chatimport.ErrChatForbidden)
		}
		c.Set("Cache-Control", "private, no-store")
		return c.JSON(fiber.Map{"chat_id": chat.ID, "platform": platform, "session_id": chat.SourceSessionID, "project_id": chat.ProjectID, "messages": messages})
	}
}

func chatImportProjectHandler(cfg APIConfig, detach bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		actor := uploadMetadataActor(c, cfg, ctx)
		if err := ensureChatImportSchema(ctx, cfg); err != nil {
			return chatImportStorageError(err)
		}
		project := ""
		if !detach {
			var req struct {
				ProjectID string `json:"project_id"`
			}
			if c.BodyParser(&req) != nil || req.ProjectID == "" {
				return fiber.NewError(400, "project_id required")
			}
			project = req.ProjectID
		}
		tx, policy, release, err := chatImportFence(ctx, cfg, actor, true)
		if err != nil {
			return chatImportStorageError(err)
		}
		defer release()
		chat, err := chatimport.ResolveChat(ctx, tx, Q, policy, actor, c.Query("chat_id"), c.Params("platform"), c.Params("sessionId"))
		if err != nil {
			return chatImportStorageError(err)
		}
		if err := chatimport.SetChatProject(ctx, tx, Q, policy, actor, chat, project); err != nil {
			return chatImportStorageError(err)
		}
		if err := tx.Commit(); err != nil {
			return chatImportStorageError(err)
		}
		c.Set("Cache-Control", "private, no-store")
		return c.JSON(fiber.Map{"chat_id": chat.ID, "platform": string(chat.Platform), "session_id": chat.SourceSessionID, "project_id": project})
	}
}

func chatImportLimit(c *fiber.Ctx, def, max int) int {
	n, err := strconv.Atoi(c.Query("limit"))
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}
func chatImportStorageError(err error) error {
	if errors.Is(err, chatimport.ErrChatAmbiguous) {
		return fiber.NewError(409, err.Error())
	}
	if errors.Is(err, chatimport.ErrChatForbidden) || errors.Is(err, access.ErrDocumentForbidden) || errors.Is(err, access.ErrRevokedCredential) {
		return fiber.NewError(403, "imported chat access denied")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fiber.NewError(408, "chat import request canceled")
	}
	return fiber.NewError(500, fmt.Sprintf("chat import storage: %v", err))
}
