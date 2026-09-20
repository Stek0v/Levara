// chat_import.go — REST surface for imported chat transcripts (E2b).
// Writes go through pkg/chatimport so DB-level idempotency and the run
// ledger behave identically for REST and any future MCP exposure.
package http

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/chatimport"
)

// Deployments that predate the chat_import tables reach this handler before
// a schema-migrating restart; ensure once per process instead.
var chatImportSchemaOnce sync.Once

func ensureChatImportSchema(cfg APIConfig) error {
	if cfg.DB == nil {
		return fiber.NewError(503, "chat import storage unavailable")
	}
	var err error
	chatImportSchemaOnce.Do(func() {
		err = chatimport.EnsureSchema(context.Background(), cfg.DB, Q)
	})
	return err
}

func RegisterChatImportAPI(app fiber.Router, cfg APIConfig) {
	app.Post("/chats/import", chatImportPostHandler(cfg))
	app.Get("/chats/import/runs", chatImportRunsHandler(cfg))
	app.Get("/chats/import/sessions", chatImportSessionsHandler(cfg))
	app.Get("/chats/import/sessions/:platform/:sessionId", chatImportSessionHandler(cfg))
}

type chatImportRunRequest struct {
	RunID        string                   `json:"run_id"`
	SourcePath   string                   `json:"source_path"`
	SourceSHA256 string                   `json:"source_sha256"`
	Skipped      int                      `json:"skipped"`
	Finish       bool                     `json:"finish"`
	Conversation *chatimport.Conversation `json:"conversation"`
}

func chatImportPostHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		ctx = context.WithValue(ctx, searchActorKey{}, workspaceActorFromFiber(c))
		if _, err := sessionActor(ctx, cfg, access.ActionWrite); err != nil {
			return sessionHTTPError(err)
		}
		if err := ensureChatImportSchema(cfg); err != nil {
			return err
		}
		var req chatImportRunRequest
		if err := c.BodyParser(&req); err != nil {
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
		startedAt := time.Now().UTC().Format(time.RFC3339)
		if err := chatimport.StartRun(ctx, cfg.DB, Q, chatimport.RunInfo{
			ID: req.RunID, Platform: conv.Platform,
			SourcePath: req.SourcePath, SourceSHA256: req.SourceSHA256,
			StartedAt: startedAt,
		}); err != nil {
			return chatImportStorageError(err)
		}
		var warnings []string
		inserted, err := chatimport.InsertConversation(ctx, cfg.DB, Q, req.RunID, conv, &warnings, time.Now())
		if err != nil {
			_ = chatimport.FinishRun(ctx, cfg.DB, Q, req.RunID, "failed", inserted, req.Skipped, len(warnings), warnings, time.Now().UTC().Format(time.RFC3339))
			return chatImportStorageError(err)
		}
		status := "running"
		if req.Finish {
			status = "ok"
			if err := chatimport.FinishRun(ctx, cfg.DB, Q, req.RunID, status, inserted, req.Skipped, len(warnings), warnings, time.Now().UTC().Format(time.RFC3339)); err != nil {
				return chatImportStorageError(err)
			}
		}
		c.Set("Cache-Control", "private, no-store")
		return c.Status(http.StatusCreated).JSON(fiber.Map{
			"run_id":       req.RunID,
			"inserted":     inserted,
			"messages":     len(conv.Messages),
			"status":       status,
			"warned_count": len(warnings),
			"warnings":     warnings,
			"session_id":   conv.SessionID,
		})
	}
}

func chatImportRunsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		ctx = context.WithValue(ctx, searchActorKey{}, workspaceActorFromFiber(c))
		if _, err := sessionActor(ctx, cfg, access.ActionRead); err != nil {
			return sessionHTTPError(err)
		}
		if err := ensureChatImportSchema(cfg); err != nil {
			return err
		}
		limit := chatImportLimit(c, 100, 500)
		query := `SELECT id, platform, source_path, status, imported_count, skipped_count, warned_count, started_at, finished_at FROM chat_import_runs`
		args := []any{}
		if p := c.Query("platform"); p != "" {
			query += ` WHERE platform = $1`
			args = append(args, p)
		}
		query += fmt.Sprintf(` ORDER BY started_at DESC LIMIT $%d`, len(args)+1)
		args = append(args, limit)
		rows, err := cfg.DB.QueryContext(ctx, Q(query), args...)
		if err != nil {
			return chatImportStorageError(err)
		}
		defer rows.Close()
		runs := []fiber.Map{}
		for rows.Next() {
			var id, platform, sourcePath, status, startedAt, finishedAt string
			var imported, skipped, warned int
			if err := rows.Scan(&id, &platform, &sourcePath, &status, &imported, &skipped, &warned, &startedAt, &finishedAt); err != nil {
				return chatImportStorageError(err)
			}
			runs = append(runs, fiber.Map{
				"id": id, "platform": platform, "source_path": sourcePath, "status": status,
				"imported_count": imported, "skipped_count": skipped, "warned_count": warned,
				"started_at": startedAt, "finished_at": finishedAt,
			})
		}
		c.Set("Cache-Control", "private, no-store")
		return c.JSON(fiber.Map{"runs": runs})
	}
}

func chatImportSessionsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		ctx = context.WithValue(ctx, searchActorKey{}, workspaceActorFromFiber(c))
		if _, err := sessionActor(ctx, cfg, access.ActionRead); err != nil {
			return sessionHTTPError(err)
		}
		if err := ensureChatImportSchema(cfg); err != nil {
			return err
		}
		limit := chatImportLimit(c, 100, 1000)
		query := `SELECT platform, session_id, MAX(session_title), COUNT(*), MIN(source_created_at), MAX(source_created_at) FROM chat_import_messages`
		args := []any{}
		if p := c.Query("platform"); p != "" {
			query += ` WHERE platform = $1`
			args = append(args, p)
		}
		query += fmt.Sprintf(` GROUP BY platform, session_id ORDER BY MIN(source_created_at) DESC LIMIT $%d`, len(args)+1)
		args = append(args, limit)
		rows, err := cfg.DB.QueryContext(ctx, Q(query), args...)
		if err != nil {
			return chatImportStorageError(err)
		}
		defer rows.Close()
		sessions := []fiber.Map{}
		for rows.Next() {
			var platform, sessionID, title, first, last string
			var count int
			if err := rows.Scan(&platform, &sessionID, &title, &count, &first, &last); err != nil {
				return chatImportStorageError(err)
			}
			sessions = append(sessions, fiber.Map{
				"platform": platform, "session_id": sessionID, "title": title,
				"message_count": count, "first_message_at": first, "last_message_at": last,
			})
		}
		c.Set("Cache-Control", "private, no-store")
		return c.JSON(fiber.Map{"sessions": sessions})
	}
}

func chatImportSessionHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		ctx = context.WithValue(ctx, searchActorKey{}, workspaceActorFromFiber(c))
		if _, err := sessionActor(ctx, cfg, access.ActionRead); err != nil {
			return sessionHTTPError(err)
		}
		if err := ensureChatImportSchema(cfg); err != nil {
			return err
		}
		platform := c.Params("platform")
		switch chatimport.Platform(platform) {
		case chatimport.PlatformCodex, chatimport.PlatformClaudeCode, chatimport.PlatformCursor:
		default:
			return fiber.NewError(400, fmt.Sprintf("unknown platform %q", platform))
		}
		sessionID := c.Params("sessionId")
		limit := chatImportLimit(c, 1000, 5000)
		rows, err := cfg.DB.QueryContext(ctx, Q(`
			SELECT external_id, ordinal, role, kind, model, content, source_created_at, metadata
			FROM chat_import_messages
			WHERE platform = $1 AND session_id = $2
			ORDER BY ordinal, source_created_at, external_id LIMIT $3
		`), platform, sessionID, limit)
		if err != nil {
			return chatImportStorageError(err)
		}
		defer rows.Close()
		messages := []fiber.Map{}
		for rows.Next() {
			var externalID, role, kind, model, content, createdAt, metadata string
			var ordinal int
			if err := rows.Scan(&externalID, &ordinal, &role, &kind, &model, &content, &createdAt, &metadata); err != nil {
				return chatImportStorageError(err)
			}
			messages = append(messages, fiber.Map{
				"external_id": externalID, "ordinal": ordinal, "role": role, "kind": kind,
				"model": model, "content": content, "created_at": createdAt, "metadata": metadata,
			})
		}
		if len(messages) == 0 {
			return fiber.NewError(404, "imported session not found")
		}
		c.Set("Cache-Control", "private, no-store")
		return c.JSON(fiber.Map{"platform": platform, "session_id": sessionID, "messages": messages})
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
	return fiber.NewError(500, fmt.Sprintf("chat import storage: %v", err))
}
