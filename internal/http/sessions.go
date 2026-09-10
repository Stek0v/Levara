// sessions.go — Scoped conversation history and server-recorded provenance.
package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	accesspkg "github.com/stek0v/levara/pkg/access"
)

var (
	errSessionForbidden         = errors.New("session access denied")
	errSessionConflict          = errors.New("session ownership conflict")
	errSessionInvalidProvenance = errors.New("session provenance invalid")
)

type sessionTurn struct {
	ID, SessionID, Query, Response, SearchType, CreatedAt, Kind string
	Sources                                                     []searchDocumentSource
	RequiresAdmin                                               bool
}

func sessionActor(ctx context.Context, cfg APIConfig, action string) (accesspkg.Actor, error) {
	actor, _ := ctx.Value(searchActorKey{}).(accesspkg.Actor)
	if !accesspkg.APIKeyAllows(actor.APIKeyPermissions, action) {
		return actor, errSessionForbidden
	}
	if actor.UserID == "" {
		if cfg.RequireAuth {
			return actor, errSessionForbidden
		}
		return actor, nil // explicit no-auth standalone behavior
	}
	if cfg.DB == nil {
		return actor, fiber.NewError(503, "session storage unavailable")
	}
	p := accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}
	active, err := p.IsActive(ctx, actor.UserID)
	if err != nil {
		return actor, err
	}
	if !active {
		return actor, errSessionForbidden
	}
	if actor.TenantID != "" {
		member, err := p.IsTenantMember(ctx, actor.UserID, actor.TenantID)
		if err != nil {
			return actor, err
		}
		if !member {
			return actor, errSessionForbidden
		}
	}
	return actor, nil
}

// RecordSessionInteraction derives owner and trusted sources solely from the
// authenticated search context. Recording is a side effect of an authorized
// read; explicit client submissions separately require write permission.
func RecordSessionInteraction(ctx context.Context, cfg APIConfig, sessionID, query, answer, searchType string) (string, error) {
	if sessionID == "" || answer == "" {
		return "", nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg.DB == nil {
		actor, _ := ctx.Value(searchActorKey{}).(accesspkg.Actor)
		if cfg.RequireAuth || actor.UserID != "" {
			return "", fiber.NewError(503, "session storage unavailable")
		}
		return "", nil
	}
	return recordSessionTurn(ctx, cfg, sessionID, query, answer, searchType, "server")
}

func recordSessionTurn(ctx context.Context, cfg APIConfig, sessionID, query, answer, searchType, kind string) (string, error) {
	action := accesspkg.ActionRead
	if kind == "client" {
		action = accesspkg.ActionWrite
	}
	actor, err := sessionActor(ctx, cfg, action)
	if err != nil {
		return "", err
	}
	sources := []searchDocumentSource{}
	if kind == "server" {
		sources = append(sources, searchSources(ctx)...)
		for _, source := range sources {
			allowed, err := searchDocumentAllowed(ctx, cfg, actor, source)
			if err != nil {
				return "", err
			}
			if !allowed {
				return "", errSessionForbidden
			}
		}
	}
	raw, err := json.Marshal(sources)
	if err != nil {
		return "", err
	}
	requiresAdmin := 0
	if kind == "server" && searchEvidenceRequiresAdmin(ctx) {
		requiresAdmin = 1
	}
	tx, err := cfg.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var owner, tenant string
	err = tx.QueryRowContext(ctx, Q("SELECT owner_id,tenant_id FROM interaction_provenance WHERE session_id=$1 AND kind='session'"), sessionID).Scan(&owner, &tenant)
	if errors.Is(err, sql.ErrNoRows) {
		// Legacy history has no trustworthy tenant binding. Do not adopt a
		// pre-existing session merely because a client knows its identifier.
		var count int
		if err := tx.QueryRowContext(ctx, Q("SELECT COUNT(*) FROM interactions WHERE session_id=$1"), sessionID).Scan(&count); err != nil {
			return "", err
		}
		if count > 0 {
			return "", errSessionConflict
		}
		_, err = tx.ExecContext(ctx, Q("INSERT INTO interaction_provenance(id,session_id,owner_id,tenant_id,kind,sources,created_at) VALUES($1,$2,$3,$4,'session','[]',$5) ON CONFLICT DO NOTHING"), uuid.NewString(), sessionID, actor.UserID, actor.TenantID, time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z"))
		if err != nil {
			return "", err
		}
		err = tx.QueryRowContext(ctx, Q("SELECT owner_id,tenant_id FROM interaction_provenance WHERE session_id=$1 AND kind='session'"), sessionID).Scan(&owner, &tenant)
	}
	if err != nil {
		return "", err
	}
	if owner != actor.UserID || tenant != actor.TenantID {
		return "", errSessionConflict
	}
	id := uuid.NewString()
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, Q("INSERT INTO interactions(id,session_id,user_id,query,response,search_type,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)"), id, sessionID, actor.UserID, query, answer, searchType, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, Q("INSERT INTO interaction_provenance(id,interaction_id,session_id,owner_id,tenant_id,kind,sources,created_at,requires_admin) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)"), uuid.NewString(), id, sessionID, actor.UserID, actor.TenantID, kind, string(raw), now.Format("2006-01-02T15:04:05.000000000Z"), requiresAdmin); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

// GetScopedSessionContext revalidates every source before reading turn content
// and propagates inherited evidence to subsequent server-generated answers.
// Unknown legacy answers are hidden for every authenticated actor.
func GetScopedSessionContext(ctx context.Context, cfg APIConfig, sessionID string, limit int) (string, error) {
	if sessionID == "" {
		return "", nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	turns, err := loadSessionTurns(ctx, cfg, sessionID, limit)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(turns))
	for i := len(turns) - 1; i >= 0; i-- {
		turn := turns[i]
		if turn.Kind == "client" {
			parts = append(parts, fmt.Sprintf("User-supplied conversation text:\n%s\nAdditional user-supplied text:\n%s", turn.Query, turn.Response))
		} else {
			parts = append(parts, fmt.Sprintf("User: %s\nAssistant: %s", turn.Query, turn.Response))
		}
		for _, source := range turn.Sources {
			trackSearchSource(ctx, source)
		}
	}
	if len(parts) == 0 {
		return "", nil
	}
	return "Previous conversation context:\n" + strings.Join(parts, "\n---\n"), nil
}

func loadSessionTurns(ctx context.Context, cfg APIConfig, sessionID string, limit int) ([]sessionTurn, error) {
	return loadSessionTurnsMatching(ctx, cfg, sessionID, limit, "")
}

func loadSessionTurnsMatching(ctx context.Context, cfg APIConfig, sessionID string, limit int, match string) ([]sessionTurn, error) {
	actor, err := sessionActor(ctx, cfg, accesspkg.ActionRead)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 100 {
		limit = 100
	}
	if cfg.DB == nil {
		return []sessionTurn{}, nil
	}
	if actor.UserID == "" && !cfg.RequireAuth {
		turns, err := loadLegacySessionTurns(ctx, cfg.DB, sessionID, limit)
		if match == "" || err != nil {
			return turns, err
		}
		filtered := []sessionTurn{}
		for _, turn := range turns {
			if strings.Contains(turn.Query, match) || strings.Contains(turn.Response, match) {
				filtered = append(filtered, turn)
			}
		}
		return filtered, nil
	}
	if sessionID != "" {
		var owner, tenant string
		err := cfg.DB.QueryRowContext(ctx, Q("SELECT owner_id,tenant_id FROM interaction_provenance WHERE session_id=$1 AND kind='session'"), sessionID).Scan(&owner, &tenant)
		if errors.Is(err, sql.ErrNoRows) {
			return []sessionTurn{}, nil
		}
		if err != nil {
			return nil, err
		}
		if owner != actor.UserID || tenant != actor.TenantID {
			return nil, errSessionForbidden
		}
	}
	query := `SELECT p.interaction_id,p.session_id,p.kind,p.sources,p.created_at,p.id,p.requires_admin FROM interaction_provenance p
  JOIN interaction_provenance a ON a.session_id=p.session_id AND a.kind='session' AND a.owner_id=p.owner_id AND a.tenant_id=p.tenant_id
  WHERE p.owner_id=$1 AND p.tenant_id=$2 AND p.kind IN ('server','client')`
	args := []any{actor.UserID, actor.TenantID}
	if sessionID != "" {
		query += " AND p.session_id=$3"
		args = append(args, sessionID)
	}
	if match != "" {
		query += fmt.Sprintf(` AND EXISTS (SELECT 1 FROM interactions i WHERE i.id=p.interaction_id AND (i.query LIKE $%d OR i.response LIKE $%d))`, len(args)+1, len(args)+2)
		args = append(args, "%"+match+"%", "%"+match+"%")
	}
	turns := []sessionTurn{}
	cursorAt, cursorID := "", ""
	// ponytail: bounded keyset scan, add a scoped text index when histories grow.
	for len(turns) < limit {
		pageSQL := query
		pageArgs := append([]any(nil), args...)
		if cursorID != "" {
			pageSQL += fmt.Sprintf(" AND (p.created_at,p.id)<($%d,$%d)", len(pageArgs)+1, len(pageArgs)+2)
			pageArgs = append(pageArgs, cursorAt, cursorID)
		}
		pageSQL += " ORDER BY p.created_at DESC,p.id DESC LIMIT 128"
		rows, err := cfg.DB.QueryContext(ctx, Q(pageSQL), pageArgs...)
		if err != nil {
			return nil, err
		}
		metadata := []sessionTurn{}
		for rows.Next() {
			var turn sessionTurn
			var raw string
			var requiresAdmin int
			if err := rows.Scan(&turn.ID, &turn.SessionID, &turn.Kind, &raw, &turn.CreatedAt, &cursorID, &requiresAdmin); err != nil {
				rows.Close()
				return nil, err
			}
			cursorAt = turn.CreatedAt
			turn.RequiresAdmin = requiresAdmin != 0
			if err := json.Unmarshal([]byte(raw), &turn.Sources); err != nil || turn.Sources == nil || (turn.Kind == "client" && (len(turn.Sources) > 0 || turn.RequiresAdmin)) {
				rows.Close()
				return nil, errSessionInvalidProvenance
			}
			metadata = append(metadata, turn)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		for _, turn := range metadata {
			allowed := true
			if turn.RequiresAdmin || (turn.Kind == "server" && len(turn.Sources) == 0) {
				admin, err := (accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}).IsSuperuser(ctx, actor.UserID)
				if err != nil {
					return nil, err
				}
				allowed = admin && actor.TenantID == ""
				if allowed {
					requireAdminSearchEvidence(ctx)
				}
			}
			for _, source := range turn.Sources {
				ok, err := searchDocumentAllowed(ctx, cfg, actor, source)
				if err != nil {
					return nil, err
				}
				if !ok {
					allowed = false
					break
				}
			}
			if !allowed {
				continue
			}
			err := cfg.DB.QueryRowContext(ctx, Q("SELECT query,response,search_type FROM interactions WHERE id=$1 AND session_id=$2 AND user_id=$3"), turn.ID, turn.SessionID, actor.UserID).Scan(&turn.Query, &turn.Response, &turn.SearchType)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			for _, source := range turn.Sources {
				trackSearchSource(ctx, source)
			}
			turns = append(turns, turn)
			if len(turns) == limit {
				break
			}
		}
		if len(metadata) < 128 {
			break
		}
	}
	return turns, nil
}

func loadLegacySessionTurns(ctx context.Context, db *sql.DB, sessionID string, limit int) ([]sessionTurn, error) {
	query := "SELECT id,session_id,query,response,search_type,created_at FROM interactions WHERE user_id=''"
	args := []any{limit}
	if sessionID != "" {
		query = "SELECT id,session_id,query,response,search_type,created_at FROM interactions WHERE session_id=$1"
		args = []any{sessionID, limit}
	}
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))
	rows, err := db.QueryContext(ctx, Q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	turns := []sessionTurn{}
	for rows.Next() {
		var turn sessionTurn
		if err := rows.Scan(&turn.ID, &turn.SessionID, &turn.Query, &turn.Response, &turn.SearchType, &turn.CreatedAt); err != nil {
			return nil, err
		}
		turns = append(turns, turn)
	}
	return turns, rows.Err()
}

// GetSessionContext is retained only for explicit no-auth legacy callers. All
// request handlers must use GetScopedSessionContext with authenticated context.
func GetSessionContext(db *sql.DB, ctx context.Context, sessionID string, limit int) string {
	if ctx == nil {
		ctx = context.Background()
	}
	if actor, _ := ctx.Value(searchActorKey{}).(accesspkg.Actor); actor.UserID != "" {
		return ""
	}
	text, _ := GetScopedSessionContext(ctx, APIConfig{DB: db}, sessionID, limit)
	return text
}

func prependSessionContext(ctx context.Context, cfg APIConfig, sessionID, prompt string) string {
	text, err := GetScopedSessionContext(ctx, cfg, sessionID, 5)
	if err != nil {
		log.Print("[sessions] history unavailable")
		return prompt
	}
	if text == "" {
		return prompt
	}
	return text + "\n\n" + prompt
}

// userID is a legacy call-site parameter, never authorization. New callers use
// RecordSessionInteraction and handle its persistence error explicitly.
func recordInteraction(ctx context.Context, cfg APIConfig, sessionID, userID, query, answer, searchType string) error {
	_, err := RecordSessionInteraction(ctx, cfg, sessionID, query, answer, searchType)
	if err != nil {
		log.Print("[sessions] interaction was not persisted")
	}
	return err
}

func RegisterSessionAPI(app fiber.Router, cfg APIConfig) {
	app.Post("/interactions", saveInteractionHandler(cfg))
	app.Get("/interactions", listInteractionsHandler(cfg))
	app.Get("/interactions/:sessionId", getSessionHandler(cfg))
}

func sessionHTTPError(err error) error {
	if errors.Is(err, errSessionForbidden) {
		return fiber.NewError(403, "session access denied")
	}
	if errors.Is(err, errSessionConflict) {
		return fiber.NewError(409, "session ownership conflict")
	}
	var fe *fiber.Error
	if errors.As(err, &fe) && fe.Code == 503 {
		return fe
	}
	return fiber.NewError(500, "session operation failed")
}

func saveInteractionHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		ctx = context.WithValue(ctx, searchActorKey{}, workspaceActorFromFiber(c))
		if cfg.DB == nil {
			return fiber.NewError(503, "session storage unavailable")
		}
		var req struct {
			SessionID  string `json:"session_id"`
			Query      string `json:"query"`
			Response   string `json:"response"`
			SearchType string `json:"search_type"`
		}
		if err := c.BodyParser(&req); err != nil || req.Query == "" {
			return fiber.NewError(400, "valid query required")
		}
		if req.SessionID == "" {
			req.SessionID = uuid.NewString()
		}
		id, err := recordSessionTurn(ctx, cfg, req.SessionID, req.Query, req.Response, req.SearchType, "client")
		if err != nil {
			return sessionHTTPError(err)
		}
		return c.Status(201).JSON(fiber.Map{"id": id, "session_id": req.SessionID, "saved": true})
	}
}

func sessionTurnJSON(turn sessionTurn) fiber.Map {
	return fiber.Map{"id": turn.ID, "session_id": turn.SessionID, "query": turn.Query, "response": turn.Response, "search_type": turn.SearchType, "created_at": turn.CreatedAt, "source_type": turn.Kind}
}

func listInteractionsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := searchRequestContext(c)
		defer cancel()
		ctx = searchEgressContext(c, cfg, ctx)
		c.SetUserContext(ctx)
		turns, err := loadSessionTurns(ctx, cfg, "", 50)
		if err != nil {
			return sessionHTTPError(err)
		}
		items := make([]fiber.Map, 0, len(turns))
		for _, turn := range turns {
			items = append(items, sessionTurnJSON(turn))
		}
		c.Set("Cache-Control", "private, no-store")
		if err := c.JSON(items); err != nil {
			return err
		}
		return sendProtectedResponseWithFence(c, ctx)
	}
}

func getSessionHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := searchRequestContext(c)
		defer cancel()
		ctx = searchEgressContext(c, cfg, ctx)
		c.SetUserContext(ctx)
		turns, err := loadSessionTurns(ctx, cfg, c.Params("sessionId"), 10)
		if err != nil {
			return sessionHTTPError(err)
		}
		items := make([]fiber.Map, 0, len(turns))
		for i := len(turns) - 1; i >= 0; i-- {
			items = append(items, sessionTurnJSON(turns[i]))
		}
		c.Set("Cache-Control", "private, no-store")
		if err := c.JSON(items); err != nil {
			return err
		}
		return sendProtectedResponseWithFence(c, ctx)
	}
}
