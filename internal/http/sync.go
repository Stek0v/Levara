// sync.go — Cross-instance data synchronization for Levara.
// Export/import memories, interactions, and graph over HTTP.
// Handles different embedding dimensions by syncing text only (vectors re-embedded on import).
package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/metrics"
	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/embed"
)

// RegisterSyncAPI registers sync export/import endpoints.
func RegisterSyncAPI(app fiber.Router, cfg APIConfig) {
	app.Use("/sync", func(c *fiber.Ctx) error {
		userID, _ := c.Locals("user_id").(string)
		ctx := context.WithValue(c.UserContext(), mcpUserIDKey, userID)
		if err := authorizeSync(ctx, cfg); err != nil {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": err.Error()})
		}
		c.SetUserContext(ctx)
		return c.Next()
	})
	app.Get("/sync/manifest", syncManifestHandler(cfg.Identity(), cfg.Access(), cfg.Search()))
	app.Post("/sync/run", syncRunHandler(cfg))
	app.Get("/sync/status", syncStatusHandler(cfg))

	app.Get("/sync/export/memories", syncExportMemoriesHandler(cfg))
	app.Post("/sync/import/memories", syncImportMemoriesHandler(cfg))

	app.Get("/sync/export/interactions", syncExportInteractionsHandler(cfg))
	app.Post("/sync/import/interactions", syncImportInteractionsHandler(cfg))

	app.Get("/sync/export/graph", syncExportGraphHandler(cfg))
	app.Post("/sync/import/graph", syncImportGraphHandler(cfg))

	app.Get("/sync/export/collection/:name", syncExportCollectionHandler(cfg))
	app.Post("/sync/import/collection", syncImportCollectionHandler(cfg))
	app.Get("/sync/import/collection/:runId/status", syncImportCollectionStatusHandler())
}

// Sync reads and writes instance-wide state, including other owners' records.
func authorizeSync(ctx context.Context, cfg APIConfig) error {
	userID, _ := ctx.Value(mcpUserIDKey).(string)
	if !cfg.RequireAuth && userID == "" {
		return nil
	}
	policy := access.SQLPolicy{DB: cfg.DB, Q: Q}
	super, err := policy.IsSuperuser(ctx, userID)
	if err != nil || !super {
		return fmt.Errorf("sync requires an active superuser")
	}
	active, err := policy.IsActive(ctx, userID)
	if err != nil || !active {
		return fmt.Errorf("sync requires an active superuser")
	}
	return nil
}

func validateSyncRemote(cfg APIConfig, remote string) error {
	parsed, err := url.Parse(remote)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("remote_url must be an HTTP(S) base URL without credentials, query or fragment")
	}
	if cfg.SyncToken != "" {
		trusted, err := url.Parse(cfg.SyncRemoteURL)
		if err != nil || trusted == nil || trusted.Scheme != parsed.Scheme || !strings.EqualFold(trusted.Host, parsed.Host) || strings.TrimRight(trusted.EscapedPath(), "/") != strings.TrimRight(parsed.EscapedPath(), "/") || trusted.User != nil || trusted.RawQuery != "" || trusted.Fragment != "" {
			return fmt.Errorf("remote_url must match LEVARA_SYNC_REMOTE_URL before sending the server sync token")
		}
	}
	return nil
}

type syncRunRequest struct {
	RemoteURL   string   `json:"remote_url"`
	Direction   string   `json:"direction"`
	Types       []string `json:"types,omitempty"`
	Since       string   `json:"since,omitempty"`
	Collections []string `json:"collections,omitempty"`
}

func syncRunHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := syncRequestContext(c)
		defer cancel()
		var req syncRunRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if strings.TrimSpace(req.RemoteURL) == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "remote_url required"})
		}
		direction := strings.ToLower(strings.TrimSpace(req.Direction))
		if direction == "" {
			direction = "pull"
		}
		if direction != "pull" && direction != "push" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "direction must be pull or push"})
		}
		result, manifest, err := (&mcpHandler{cfg: cfg}).DoSync(ctx, strings.TrimRight(req.RemoteURL, "/"), direction, req.Types, req.Since, req.Collections)
		if err != nil {
			(&mcpHandler{cfg: cfg}).logHeartbeatContext(ctx, "sync", map[string]any{"direction": direction, "types": req.Types, "status": "error", "error": syncErrorText(err, cfg.SyncToken)})
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": syncErrorText(err, cfg.SyncToken)})
		}
		result["remote_manifest"] = manifest
		if cfg.DB != nil {
			(&mcpHandler{cfg: cfg}).logHeartbeatContext(ctx, "sync", map[string]any{
				"direction": direction,
				"remote":    strings.TrimRight(req.RemoteURL, "/"),
				"types":     req.Types,
				"status":    result["status"],
				"result":    result,
			})
		}
		return c.JSON(result)
	}
}

func syncStatusHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		limit := c.QueryInt("limit", 10)
		result := (&mcpHandler{cfg: cfg}).toolSyncStatus(c.UserContext(), map[string]any{"limit": float64(limit)})
		if len(result.Content) == 0 {
			return c.JSON(fiber.Map{})
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "sync status response decode failed"})
		}
		if result.IsError {
			return c.Status(fiber.StatusBadRequest).JSON(payload)
		}
		return c.JSON(payload)
	}
}

// ── Manifest ──

type syncManifest struct {
	Version      string               `json:"version"`
	EmbedModel   string               `json:"embed_model"`
	EmbedDim     int                  `json:"embed_dim"`
	Memories     syncCount            `json:"memories"`
	Interactions syncCount            `json:"interactions"`
	GraphNodes   syncCount            `json:"graph_nodes"`
	GraphEdges   syncCount            `json:"graph_edges"`
	Collections  []syncCollectionInfo `json:"collections"`
}

type syncCount struct {
	Count         int    `json:"count"`
	LatestUpdated string `json:"latest_updated,omitempty"`
}

type syncCollectionInfo struct {
	Name    string `json:"name"`
	Records int    `json:"records"`
	Dim     int    `json:"dim"`
	Model   string `json:"model"`
}

func syncManifestHandler(identityCfg IdentityConfig, accessCfg AccessConfig, searchCfg SearchConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := syncRequestContext(c)
		defer cancel()

		m := syncManifest{
			Version:    identityCfg.Version,
			EmbedModel: searchCfg.EmbedModel,
		}
		if searchCfg.Collections != nil {
			for _, meta := range searchCfg.Collections.ListWithMeta() {
				if m.EmbedDim == 0 {
					m.EmbedDim = meta.EmbeddingDim
				}
				m.Collections = append(m.Collections, syncCollectionInfo{
					Name: meta.Name, Records: meta.RecordCount,
					Dim: meta.EmbeddingDim, Model: meta.EmbeddingModel,
				})
			}
		}
		if accessCfg.DB != nil {
			for _, query := range []struct {
				sql    string
				target any
			}{
				{`SELECT COUNT(*) FROM memories`, &m.Memories.Count},
				{`SELECT COALESCE(CAST(MAX(updated_at) AS TEXT),'') FROM memories`, &m.Memories.LatestUpdated},
				{`SELECT COUNT(*) FROM interactions`, &m.Interactions.Count},
				{`SELECT COALESCE(CAST(MAX(created_at) AS TEXT),'') FROM interactions`, &m.Interactions.LatestUpdated},
				{`SELECT COUNT(*) FROM graph_nodes`, &m.GraphNodes.Count},
				{`SELECT COUNT(*) FROM graph_edges`, &m.GraphEdges.Count},
			} {
				if err := accessCfg.DB.QueryRowContext(ctx, Q(query.sql)).Scan(query.target); err != nil {
					return c.Status(500).JSON(fiber.Map{"detail": "sync manifest database query failed"})
				}
			}
		}
		return c.JSON(m)
	}
}

// ── Memory Sync ──

type syncMemory struct {
	ID             string `json:"id"`
	Key            string `json:"key"`
	Value          string `json:"value"`
	Type           string `json:"type"`
	OwnerID        string `json:"owner_id"`
	CollectionName string `json:"collection_name"`
	Room           string `json:"room"`
	Hall           string `json:"hall"`
	IsPinned       bool   `json:"is_pinned"`
	PinPriority    int    `json:"pin_priority"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

func syncExportMemoriesHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := syncRequestContext(c)
		defer cancel()
		result, err := exportSyncMemories(ctx, cfg, c.Query("since"))
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": syncErrorText(err, cfg.SyncToken)})
		}
		return c.JSON(result)
	}
}

func exportSyncMemories(ctx context.Context, cfg APIConfig, since string) ([]syncMemory, error) {
	if cfg.DB == nil {
		return nil, fmt.Errorf("database not configured")
	}
	var rows *sql.Rows
	var err error
	if since != "" {
		rows, err = cfg.DB.QueryContext(ctx,
			Q(`SELECT id, key, value, type, owner_id, collection_name,
					 COALESCE(room,''), COALESCE(hall,''), is_pinned, pin_priority,
					 created_at, updated_at
					 FROM memories WHERE updated_at > $1 ORDER BY updated_at LIMIT 10000`), since)
	} else {
		rows, err = cfg.DB.QueryContext(ctx,
			Q(`SELECT id, key, value, type, owner_id, collection_name,
					 COALESCE(room,''), COALESCE(hall,''), is_pinned, pin_priority,
					 created_at, updated_at
					 FROM memories ORDER BY updated_at LIMIT 10000`))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []syncMemory
	for rows.Next() {
		var m syncMemory
		if err := rows.Scan(&m.ID, &m.Key, &m.Value, &m.Type, &m.OwnerID, &m.CollectionName,
			&m.Room, &m.Hall, &m.IsPinned, &m.PinPriority, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	if result == nil {
		result = []syncMemory{}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return result, nil
}

func syncImportMemoriesHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := syncRequestContext(c)
		defer cancel()

		if cfg.DB == nil {
			return c.Status(503).JSON(fiber.Map{"detail": "database not configured"})
		}
		var memories []syncMemory
		if err := c.BodyParser(&memories); err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid JSON array"})
		}

		counts, accepted, importErr := importSyncMemories(ctx, cfg, memories)
		imported := counts["imported"]

		metrics.SyncOperations.WithLabelValues("import", "memories", "ok").Add(float64(imported))

		// Auto re-embed imported memories into _memories vector collection.
		// A.4 (20.04 review backlog): wrap fire-and-forget goroutine in
		// recover so a single bad metadata blob can't take the goroutine
		// down silently — the original "continue on error" loop covered
		// embed failures but not panics from json.Marshal / Insert paths.
		embedded := 0
		if imported > 0 && cfg.EmbedEndpoint != "" && cfg.Collections != nil {
			embedClient := cfg.EmbedClient
			if embedClient == nil {
				embedClient = embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, 16, 3)
			}
			memoriesSnapshot := accepted
			go func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[sync] auto re-embed panic recovered: %v", r)
					}
				}()
				bgCtx, bgCancel := backgroundTaskContext()
				defer bgCancel()

				for _, m := range memoriesSnapshot {
					text := m.Key + " " + m.Value
					vec, err := embedClient.EmbedSingle(bgCtx, text)
					if err != nil {
						continue
					}
					memColl := "_memories"
					if m.CollectionName != "" {
						memColl = "_memories_" + m.CollectionName
					}
					meta, _ := json.Marshal(map[string]any{
						"key": m.Key, "value": m.Value, "type": m.Type,
						"collection": m.CollectionName, "memory_id": m.ID,
						"room": m.Room, "hall": m.Hall,
						"is_pinned": m.IsPinned, "pin_priority": m.PinPriority,
					})
					if err := cfg.Collections.Insert(memColl, m.ID, vec, meta); err == nil {
						embedded++
					}
				}
				log.Printf("[sync] auto re-embed: %d/%d memories embedded into vector index", embedded, len(memoriesSnapshot))
			}()
		}

		result := syncCountsResult(counts, importErr, cfg.SyncToken)
		result["embedding"] = imported > 0 && cfg.EmbedEndpoint != "" && cfg.Collections != nil
		return c.JSON(result)
	}
}

// ── Interaction Sync ──

type syncInteraction struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	UserID     string `json:"user_id"`
	Query      string `json:"query"`
	Response   string `json:"response"`
	SearchType string `json:"search_type"`
	CreatedAt  string `json:"created_at"`
}

func syncExportInteractionsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := syncRequestContext(c)
		defer cancel()
		result, err := exportSyncInteractions(ctx, cfg, c.Query("since"))
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": syncErrorText(err, cfg.SyncToken)})
		}
		return c.JSON(result)
	}
}

func exportSyncInteractions(ctx context.Context, cfg APIConfig, since string) ([]syncInteraction, error) {
	if cfg.DB == nil {
		return nil, fmt.Errorf("database not configured")
	}
	var rows *sql.Rows
	var err error
	if since != "" {
		rows, err = cfg.DB.QueryContext(ctx,
			Q(`SELECT id, COALESCE(session_id,''), COALESCE(user_id,''), COALESCE(query,''), COALESCE(response,''), COALESCE(search_type,''), created_at
					 FROM interactions WHERE created_at > $1 ORDER BY created_at LIMIT 10000`), since)
	} else {
		rows, err = cfg.DB.QueryContext(ctx,
			Q(`SELECT id, COALESCE(session_id,''), COALESCE(user_id,''), COALESCE(query,''), COALESCE(response,''), COALESCE(search_type,''), created_at
					 FROM interactions ORDER BY created_at LIMIT 10000`))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []syncInteraction
	for rows.Next() {
		var i syncInteraction
		if err := rows.Scan(&i.ID, &i.SessionID, &i.UserID, &i.Query, &i.Response, &i.SearchType, &i.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, i)
	}
	if result == nil {
		result = []syncInteraction{}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return result, nil
}

func syncImportInteractionsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := syncRequestContext(c)
		defer cancel()

		if cfg.DB == nil {
			return c.Status(503).JSON(fiber.Map{"detail": "database not configured"})
		}
		var interactions []syncInteraction
		if err := c.BodyParser(&interactions); err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid JSON array"})
		}

		counts, err := importSyncInteractions(ctx, cfg, interactions)
		return c.JSON(syncCountsResult(counts, err, cfg.SyncToken))
	}
}

// ── Graph Sync ──

type syncGraph struct {
	Nodes []syncGraphNode `json:"nodes"`
	Edges []syncGraphEdge `json:"edges"`
}

type syncGraphNode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Properties  string `json:"properties"` // JSON string
	DatasetID   string `json:"dataset_id"`
}

type syncGraphEdge struct {
	ID               string  `json:"id"`
	SourceID         string  `json:"source_id"`
	TargetID         string  `json:"target_id"`
	RelationshipName string  `json:"relationship_name"`
	Properties       string  `json:"properties"` // JSON string
	ValidFrom        string  `json:"valid_from,omitempty"`
	ValidUntil       string  `json:"valid_until,omitempty"`
	SupersededBy     string  `json:"superseded_by,omitempty"`
	Confidence       float64 `json:"confidence"`
	DatasetID        string  `json:"dataset_id"`
}

func syncExportGraphHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := syncRequestContext(c)
		defer cancel()
		result, err := exportSyncGraph(ctx, cfg)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": syncErrorText(err, cfg.SyncToken)})
		}
		return c.JSON(result)
	}
}
func exportSyncGraph(ctx context.Context, cfg APIConfig) (syncGraph, error) {
	if cfg.DB == nil {
		return syncGraph{}, fmt.Errorf("database not configured")
	}
	g := syncGraph{}

	nodeRows, err := cfg.DB.QueryContext(ctx,
		Q(`SELECT id, name, type, COALESCE(description,''), COALESCE(properties,'{}'), COALESCE(dataset_id,'') FROM graph_nodes LIMIT 50000`))
	if err != nil {
		return syncGraph{}, err
	}
	defer nodeRows.Close()
	for nodeRows.Next() {
		var n syncGraphNode
		if err := nodeRows.Scan(&n.ID, &n.Name, &n.Type, &n.Description, &n.Properties, &n.DatasetID); err != nil {
			return syncGraph{}, err
		}
		g.Nodes = append(g.Nodes, n)
	}
	if err := errors.Join(nodeRows.Err(), nodeRows.Close()); err != nil {
		return syncGraph{}, err
	}
	if g.Nodes == nil {
		g.Nodes = []syncGraphNode{}
	}

	edgeRows, err := cfg.DB.QueryContext(ctx,
		Q(`SELECT id, source_id, target_id, relationship_name, COALESCE(properties,'{}'),
				  COALESCE(CAST(valid_from AS TEXT),''), COALESCE(CAST(valid_until AS TEXT),''), COALESCE(superseded_by,''),
				  COALESCE(confidence,1.0), COALESCE(dataset_id,'')
			   FROM graph_edges LIMIT 50000`))
	if err != nil {
		return syncGraph{}, err
	}
	defer edgeRows.Close()
	for edgeRows.Next() {
		var e syncGraphEdge
		if err := edgeRows.Scan(&e.ID, &e.SourceID, &e.TargetID, &e.RelationshipName, &e.Properties,
			&e.ValidFrom, &e.ValidUntil, &e.SupersededBy, &e.Confidence, &e.DatasetID); err != nil {
			return syncGraph{}, err
		}
		g.Edges = append(g.Edges, e)
	}
	if err := errors.Join(edgeRows.Err(), edgeRows.Close()); err != nil {
		return syncGraph{}, err
	}
	if g.Edges == nil {
		g.Edges = []syncGraphEdge{}
	}

	return g, nil
}

func syncImportGraphHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := syncRequestContext(c)
		defer cancel()

		if cfg.DB == nil {
			return c.Status(503).JSON(fiber.Map{"detail": "database not configured"})
		}
		var g syncGraph
		if err := c.BodyParser(&g); err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid JSON"})
		}

		counts, err := importSyncGraph(ctx, cfg, g)
		return c.JSON(syncCountsResult(counts, err, cfg.SyncToken))
	}
}

// ── Collection Sync (vectors via re-embedding) ──

type syncCollectionExport struct {
	Collection  string                 `json:"collection"`
	SourceModel string                 `json:"source_model"`
	SourceDim   int                    `json:"source_dim"`
	Records     []syncCollectionRecord `json:"records"`
}

type syncCollectionRecord struct {
	ID       string          `json:"id"`
	Text     string          `json:"text"`
	Metadata json.RawMessage `json:"metadata"`
}

// textFromMetadata extracts readable text from vector metadata JSON.
// Tries common fields: text, name, description, content, value, key.
func textFromMetadata(meta []byte) string {
	var m map[string]any
	if json.Unmarshal(meta, &m) != nil {
		return string(meta)
	}
	for _, key := range []string{"text", "name", "description", "content", "value", "key"} {
		if v, ok := m[key].(string); ok && v != "" {
			return v
		}
	}
	return string(meta)
}

func syncExportCollectionHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		name := c.Params("name")
		if cfg.Collections == nil || !cfg.Collections.Has(name) {
			return c.Status(404).JSON(fiber.Map{"detail": fmt.Sprintf("collection %q not found", name)})
		}

		ids, _, metas, err := cfg.Collections.AllRecords(name)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": err.Error()})
		}

		meta := cfg.Collections.GetMeta(name)
		export := syncCollectionExport{
			Collection: name,
		}
		if meta != nil {
			export.SourceModel = meta.EmbeddingModel
			export.SourceDim = meta.EmbeddingDim
		}

		for i, id := range ids {
			text := textFromMetadata(metas[i])
			export.Records = append(export.Records, syncCollectionRecord{
				ID:       id,
				Text:     text,
				Metadata: json.RawMessage(metas[i]),
			})
		}
		if export.Records == nil {
			export.Records = []syncCollectionRecord{}
		}

		return c.JSON(export)
	}
}

type syncCollectionImportStatus struct {
	RunID      string `json:"run_id"`
	Status     string `json:"status"` // RUNNING, COMPLETED, FAILED
	Collection string `json:"collection"`
	Total      int    `json:"total"`
	Processed  int    `json:"processed"`
	Failed     int    `json:"failed"`
	Skipped    int    `json:"skipped"`
	ElapsedMs  int64  `json:"elapsed_ms"`
	Message    string `json:"message"`
}

var syncImportRuns syncMap

type syncMap struct {
	m sync.Map
}

func (s *syncMap) Store(k string, v *syncCollectionImportStatus) { s.m.Store(k, *v) }
func (s *syncMap) Load(k string) (*syncCollectionImportStatus, bool) {
	v, ok := s.m.Load(k)
	if !ok {
		return nil, false
	}
	status := v.(syncCollectionImportStatus)
	return &status, true
}

func syncImportCollectionHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var export syncCollectionExport
		if err := c.BodyParser(&export); err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid JSON"})
		}
		result, code := startSyncCollectionImport(cfg, export)
		return c.Status(code).JSON(result)
	}
}

func startSyncCollectionImport(cfg APIConfig, export syncCollectionExport) (fiber.Map, int) {
	if export.Collection == "" {
		return fiber.Map{"detail": "collection name required"}, 400
	}
	if len(export.Records) == 0 {
		return fiber.Map{"status": "empty", "message": "no records to import"}, 200
	}
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return fiber.Map{"detail": "embedding service or collections not configured"}, 503
	}

	runID := fmt.Sprintf("sync-%d", time.Now().UnixNano())
	status := &syncCollectionImportStatus{
		RunID:      runID,
		Status:     "RUNNING",
		Collection: export.Collection,
		Total:      len(export.Records),
	}
	syncImportRuns.Store(runID, status)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[sync] collection import panic recovered run=%s: %v", runID, r)
				status.Status = "FAILED"
				status.Message = fmt.Sprintf("panic: %v", r)
			}
			syncImportRuns.Store(runID, status)
		}()
		start := time.Now()
		bgCtx, bgCancel := backgroundTaskContext()
		defer bgCancel()

		// Split records that exceed the embed context into overlapping
		// chunks; records that fit pass through unchanged. Each unit is
		// embedded and stored as its own vector.
		units, skippedNoText, chunked := expandRecordsToUnits(export.Records, reembedMaxRunes, reembedMaxRunes/5)
		status.Skipped += skippedNoText
		if len(units) == 0 {
			status.Status = "COMPLETED"
			status.Message = "no embeddable text in records"
			return
		}
		status.Total = len(units)

		// Re-embed throughput is tuned to unit size. Short memory texts
		// keep the high-throughput defaults (batch 50, concurrency 3).
		// When a document was split into large chunks, a batch of 50 at
		// concurrency 3 overruns the embed client's 30s HTTP timeout on
		// modest hardware (Ollama on a Pi), so shrink the batch, drop to
		// sequential, and extend the timeout for that path only.
		// WithTimeout(0) is a no-op, leaving the default for the fast path.
		batchSize, embedConcurrency, embedTimeout := 50, 3, time.Duration(0)
		if chunked {
			batchSize, embedConcurrency, embedTimeout = 16, 1, 5*time.Minute
		}
		embedClient := embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, batchSize, embedConcurrency).
			WithTimeout(embedTimeout)

		// Auto-detect target dimension
		testVecs, err := embedClient.EmbedTexts(bgCtx, []string{units[0].text})
		if err != nil || len(testVecs) == 0 {
			status.Status = "FAILED"
			status.Message = fmt.Sprintf("embed test failed: %v", err)
			return
		}
		targetDim := len(testVecs[0])

		// Create collection if not exists
		if !cfg.Collections.Has(export.Collection) {
			if err := cfg.Collections.CreateWithDim(export.Collection, targetDim, cfg.EmbedModel, "cosine"); err != nil {
				status.Status = "FAILED"
				status.Message = fmt.Sprintf("create collection: %v", err)
				return
			}
		}

		log.Printf("[sync-import] %s: %d records → %d units, source=%s/%d → target=%s/%d",
			export.Collection, len(export.Records), len(units), export.SourceModel, export.SourceDim, cfg.EmbedModel, targetDim)

		// Process in batches
		for i := 0; i < len(units); i += batchSize {
			end := i + batchSize
			if end > len(units) {
				end = len(units)
			}
			batch := units[i:end]

			texts := make([]string, len(batch))
			for j, u := range batch {
				texts[j] = u.text
			}

			vecs, err := embedClient.EmbedTexts(bgCtx, texts)
			if err != nil {
				log.Printf("[sync-import] batch %d-%d embed error: %v", i, end, err)
				status.Failed += len(batch)
				continue
			}

			if len(vecs) < len(batch) {
				status.Failed += len(batch) - len(vecs)
			}
			for j, vec := range vecs {
				if j < len(batch) {
					if err := cfg.Collections.Insert(export.Collection, batch[j].id, vec, batch[j].meta); err != nil {
						status.Failed++
					} else {
						status.Processed++
					}
				}
			}

			status.ElapsedMs = time.Since(start).Milliseconds()
			syncImportRuns.Store(runID, status)
		}

		status.Status = "COMPLETED"
		if status.Failed > 0 {
			status.Status = "FAILED"
		}
		status.ElapsedMs = time.Since(start).Milliseconds()
		status.Message = fmt.Sprintf("imported %d/%d units from %d records (%s dim=%d → %s dim=%d) in %dms",
			status.Processed, len(units), len(export.Records), export.SourceModel, export.SourceDim, cfg.EmbedModel, targetDim, status.ElapsedMs)
		log.Printf("[sync-import] %s", status.Message)
	}()

	return fiber.Map{
		"status":     "started",
		"run_id":     runID,
		"collection": export.Collection,
		"records":    len(export.Records),
		"source":     fmt.Sprintf("%s (dim=%d)", export.SourceModel, export.SourceDim),
		"target":     fmt.Sprintf("%s (dim=auto)", cfg.EmbedModel),
	}, 200
}

func syncImportCollectionStatusHandler() fiber.Handler {
	return func(c *fiber.Ctx) error {
		runID := c.Params("runId")
		if status, ok := syncImportRuns.Load(runID); ok {
			return c.JSON(status)
		}
		return c.Status(404).JSON(fiber.Map{"detail": "run not found"})
	}
}

// ── Authenticated sync transport ──

// syncAuthGet performs a GET, attaching an Authorization: Bearer header
// when token is non-empty. Empty token preserves the original
// unauthenticated behaviour (remote with auth disabled).
func syncAuthGet(client *http.Client, url, token string) (*http.Response, error) {
	return syncAuthGetContext(context.Background(), client, url, token)
}

func syncAuthGetContext(ctx context.Context, client *http.Client, url, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	transport := *client
	transport.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return transport.Do(req)
}

// syncAuthPost performs a POST with the given body, attaching an
// Authorization: Bearer header when token is non-empty.
func syncAuthPost(client *http.Client, url, contentType, body, token string) (*http.Response, error) {
	return syncAuthPostContext(context.Background(), client, url, contentType, body, token)
}

func syncAuthPostContext(ctx context.Context, client *http.Client, url, contentType, body, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	transport := *client
	transport.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return transport.Do(req)
}

// ── Sync Pull (client-side: fetch from remote, import locally) ──

// SyncPull fetches data from a remote Levara instance and imports it locally.
// Used by the MCP sync tool and CLI.
func SyncPull(cfg APIConfig, remoteURL string, types []string, since string) map[string]any {
	ctx, cancel := backgroundTaskContext()
	defer cancel()
	return syncPullContext(ctx, cfg, remoteURL, types, since)
}

func syncPullContext(ctx context.Context, cfg APIConfig, remoteURL string, types []string, since string) map[string]any {
	results := map[string]any{}
	client := &http.Client{Timeout: 30 * time.Second}
	for _, kind := range []string{"memories", "interactions", "graph"} {
		if len(types) > 0 && !containsType(types, kind) {
			continue
		}
		if cfg.DB == nil {
			results[kind+"_error"] = "database not configured"
			continue
		}
		endpoint := remoteURL + "/sync/export/" + kind
		if since != "" && kind != "graph" {
			endpoint += "?since=" + url.QueryEscape(since)
		}
		resp, err := syncAuthGetContext(ctx, client, endpoint, cfg.SyncToken)
		if err != nil {
			results[kind+"_error"] = syncErrorText(err, cfg.SyncToken)
			continue
		}
		var counts map[string]int
		switch kind {
		case "memories":
			var records []syncMemory
			if err = decodeSyncResponse(resp, &records); err == nil {
				if len(records) == 0 {
					results[kind] = "no data"
				} else {
					counts, _, err = importSyncMemories(ctx, cfg, records)
				}
			}
		case "interactions":
			var records []syncInteraction
			if err = decodeSyncResponse(resp, &records); err == nil {
				if len(records) == 0 {
					results[kind] = "no data"
				} else {
					counts, err = importSyncInteractions(ctx, cfg, records)
				}
			}
		case "graph":
			var graph syncGraph
			if err = decodeSyncResponse(resp, &graph); err == nil {
				counts, err = importSyncGraph(ctx, cfg, graph)
			}
		}
		if counts != nil {
			results[kind] = counts
		}
		if err != nil {
			results[kind+"_error"] = syncErrorText(err, cfg.SyncToken)
		}
	}
	return results
}

// SyncManifestFromRemote preserves the standalone caller API.
func SyncManifestFromRemote(remoteURL, token string) (*syncManifest, error) {
	return syncManifestFromRemoteContext(context.Background(), remoteURL, token)
}

func syncManifestFromRemoteContext(ctx context.Context, remoteURL, token string) (*syncManifest, error) {
	resp, err := syncAuthGetContext(ctx, &http.Client{Timeout: 10 * time.Second}, remoteURL+"/sync/manifest", token)
	if err != nil {
		return nil, fmt.Errorf("failed to reach remote: %s", syncErrorText(err, token))
	}
	var manifest syncManifest
	if err := decodeSyncResponse(resp, &manifest); err != nil {
		return nil, fmt.Errorf("invalid manifest: %s", syncErrorText(err, token))
	}
	return &manifest, nil
}

// Decode only a complete successful response. Neither remote error bodies nor
// credential-bearing URLs are copied into diagnostics.
func decodeSyncResponse(resp *http.Response, out any) error {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.Join(fmt.Errorf("remote HTTP status %d", resp.StatusCode), resp.Body.Close())
	}
	body, err := io.ReadAll(resp.Body)
	err = errors.Join(err, resp.Body.Close())
	if err != nil {
		return fmt.Errorf("read remote response: %w", err)
	}
	if strings.TrimSpace(string(body)) == "null" {
		return fmt.Errorf("invalid remote JSON: null payload")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("invalid remote JSON: %w", err)
	}
	return nil
}

func syncPush(ctx context.Context, cfg APIConfig, remoteURL string, types []string, since string) map[string]any {
	results := map[string]any{}
	client := &http.Client{Timeout: 30 * time.Second}
	for _, kind := range []string{"memories", "interactions", "graph"} {
		if len(types) > 0 && !containsType(types, kind) {
			continue
		}
		var payload any
		var err error
		empty := false
		switch kind {
		case "memories":
			var records []syncMemory
			records, err = exportSyncMemories(ctx, cfg, since)
			payload = records
			empty = len(records) == 0
		case "interactions":
			var records []syncInteraction
			records, err = exportSyncInteractions(ctx, cfg, since)
			payload = records
			empty = len(records) == 0
		case "graph":
			var graph syncGraph
			graph, err = exportSyncGraph(ctx, cfg)
			payload = graph
			empty = len(graph.Nodes) == 0 && len(graph.Edges) == 0
		}
		if err != nil {
			results[kind+"_error"] = syncErrorText(err, cfg.SyncToken)
			continue
		}
		if empty {
			results[kind] = "no data to push"
			continue
		}
		body, err := json.Marshal(payload)
		if err != nil {
			results[kind+"_error"] = syncErrorText(err, cfg.SyncToken)
			continue
		}
		resp, err := syncAuthPostContext(ctx, client, remoteURL+"/sync/import/"+kind, "application/json", string(body), cfg.SyncToken)
		if err != nil {
			results[kind+"_error"] = syncErrorText(err, cfg.SyncToken)
			continue
		}
		var result map[string]any
		if err = decodeSyncResponse(resp, &result); err != nil {
			results[kind+"_error"] = syncErrorText(err, cfg.SyncToken)
			continue
		}
		if err := validateSyncImportAck(result, payload); err != nil {
			results[kind+"_error"] = err.Error()
		}
		results[kind] = result
		if failure, ok := result["error"]; ok {
			message := syncErrorText(fmt.Errorf("%v", failure), cfg.SyncToken)
			result["error"] = message
			results[kind+"_error"] = message
		}
	}
	return results
}

func containsType(types []string, t string) bool {
	for _, tt := range types {
		if tt == t {
			return true
		}
	}
	return false
}

func syncPullCollections(cfg APIConfig, remoteURL string, collections []string) map[string]any {
	return syncPullCollectionsContext(context.Background(), cfg, remoteURL, collections)
}

func syncPullCollectionsContext(ctx context.Context, cfg APIConfig, remoteURL string, collections []string) map[string]any {
	client := &http.Client{Timeout: 120 * time.Second}
	results := map[string]any{}

	for _, coll := range collections {
		resp, err := syncAuthGetContext(ctx, client, remoteURL+"/sync/export/collection/"+url.PathEscape(coll), cfg.SyncToken)
		if err != nil {
			results[coll] = map[string]string{"error": syncErrorText(err, cfg.SyncToken)}
			continue
		}
		var export syncCollectionExport
		if err = decodeSyncResponse(resp, &export); err != nil {
			results[coll] = map[string]string{"error": syncErrorText(err, cfg.SyncToken)}
			continue
		}
		if export.Collection != coll {
			results[coll] = map[string]string{"error": "remote collection name mismatch"}
			continue
		}
		result, _ := startSyncCollectionImport(cfg, export)
		results[coll] = result
	}

	return results
}

func syncPushCollections(ctx context.Context, cfg APIConfig, remoteURL string, collections []string) map[string]any {
	client := &http.Client{Timeout: 120 * time.Second}
	results := map[string]any{}

	for _, coll := range collections {
		if cfg.Collections == nil || !cfg.Collections.Has(coll) {
			results[coll] = map[string]string{"error": "collection not found locally"}
			continue
		}

		ids, _, metas, err := cfg.Collections.AllRecords(coll)
		if err != nil {
			results[coll] = map[string]string{"error": syncErrorText(err, cfg.SyncToken)}
			continue
		}

		meta := cfg.Collections.GetMeta(coll)
		export := syncCollectionExport{Collection: coll}
		if meta != nil {
			export.SourceModel = meta.EmbeddingModel
			export.SourceDim = meta.EmbeddingDim
		}
		for i, id := range ids {
			export.Records = append(export.Records, syncCollectionRecord{
				ID:       id,
				Text:     textFromMetadata(metas[i]),
				Metadata: json.RawMessage(metas[i]),
			})
		}

		body, err := json.Marshal(export)
		if err != nil {
			results[coll] = map[string]string{"error": syncErrorText(err, cfg.SyncToken)}
			continue
		}
		resp, err := syncAuthPostContext(ctx, client, remoteURL+"/sync/import/collection", "application/json", string(body), cfg.SyncToken)
		if err != nil {
			results[coll] = map[string]string{"error": syncErrorText(err, cfg.SyncToken)}
			continue
		}
		var r map[string]any
		if err := decodeSyncResponse(resp, &r); err != nil {
			results[coll] = map[string]string{"error": syncErrorText(err, cfg.SyncToken)}
			continue
		}
		if r["error"] == nil && r["detail"] == nil {
			status, _ := r["status"].(string)
			valid := false
			switch status {
			case "empty":
				valid = len(export.Records) == 0
			case "started":
				records, started := syncStartedCollection(r)
				valid = started && records == len(export.Records)
			}
			if !valid {
				results[coll] = map[string]string{"error": "invalid remote collection import acknowledgement"}
				continue
			}
		}
		for _, key := range []string{"error", "detail"} {
			if value, ok := r[key]; ok {
				r[key] = syncErrorText(fmt.Errorf("%v", value), cfg.SyncToken)
			}
		}
		results[coll] = r
	}

	return results
}

func importSyncMemories(ctx context.Context, cfg APIConfig, memories []syncMemory) (map[string]int, []syncMemory, error) {
	if cfg.DB == nil {
		return nil, nil, fmt.Errorf("database not configured")
	}
	imported, skipped, failed := 0, 0, 0
	var firstErr error
	var accepted []syncMemory
	for _, m := range memories {
		// Last-writer-wins, enforced atomically (finding H5, 2026-09-03
		// review): the conflict clause carries a WHERE guard so a
		// concurrent import that lands between our SELECT and INSERT can
		// never be overwritten by an older row. The conditional insert
		// also avoids the separate SELECT entirely.
		q, qargs := QArgs(`INSERT INTO memories (
					id, key, value, type, owner_id, collection_name, room, hall,
					is_pinned, pin_priority, created_at, updated_at
				)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
				 ON CONFLICT(key, owner_id, collection_name) DO UPDATE SET
					value = $3, type = $4, collection_name = $6, room = $7, hall = $8,
					is_pinned = $9, pin_priority = $10, updated_at = $12
				 WHERE memories.updated_at < EXCLUDED.updated_at`,
			m.ID, m.Key, m.Value, m.Type, m.OwnerID, m.CollectionName,
			m.Room, m.Hall, m.IsPinned, m.PinPriority, m.CreatedAt, m.UpdatedAt)
		res, err := cfg.DB.ExecContext(ctx, q, qargs...)
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		n, err := res.RowsAffected()
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if n > 0 {
			imported++
			accepted = append(accepted, m)
		} else {
			skipped++
		}
	}

	return map[string]int{"imported": imported, "skipped": skipped, "failed": failed, "total": len(memories)}, accepted, firstErr
}

func importSyncInteractions(ctx context.Context, cfg APIConfig, interactions []syncInteraction) (map[string]int, error) {
	if cfg.DB == nil {
		return nil, fmt.Errorf("database not configured")
	}
	imported, skipped, failed := 0, 0, 0
	var firstErr error
	for _, i := range interactions {
		q, qargs := QArgs(`INSERT INTO interactions (id, session_id, user_id, query, response, search_type, created_at)
				 VALUES ($1, $2, $3, $4, $5, $6, $7)
				 ON CONFLICT(id) DO NOTHING`,
			i.ID, i.SessionID, i.UserID, i.Query, i.Response, i.SearchType, i.CreatedAt)
		res, err := cfg.DB.ExecContext(ctx, q, qargs...)
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		n, err := res.RowsAffected()
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if n > 0 {
			imported++
		} else {
			skipped++
		}
	}

	return map[string]int{"imported": imported, "skipped": skipped, "failed": failed, "total": len(interactions)}, firstErr
}

func importSyncGraph(ctx context.Context, cfg APIConfig, g syncGraph) (map[string]int, error) {
	if cfg.DB == nil {
		return nil, fmt.Errorf("database not configured")
	}
	nodesImported, edgesImported, nodesFailed, edgesFailed := 0, 0, 0, 0
	var firstErr error

	for _, n := range g.Nodes {
		q, qargs := QArgs(`INSERT INTO graph_nodes (id, name, type, description, properties, dataset_id)
				 VALUES ($1, $2, $3, $4, $5, $6)
				 ON CONFLICT(id) DO UPDATE SET
					name = $2, type = $3, description = $4, properties = $5, dataset_id = $6`,
			n.ID, n.Name, n.Type, n.Description, n.Properties, n.DatasetID)
		if _, err := cfg.DB.ExecContext(ctx, q, qargs...); err == nil {
			nodesImported++
		} else {
			nodesFailed++
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	for _, e := range g.Edges {
		if e.Confidence == 0 {
			e.Confidence = 1.0
		}
		q, qargs := QArgs(`INSERT INTO graph_edges (
					id, source_id, target_id, relationship_name, properties,
					valid_from, valid_until, superseded_by, confidence, dataset_id
				)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
				 ON CONFLICT(id) DO UPDATE SET
					source_id = $2, target_id = $3, relationship_name = $4, properties = $5,
					valid_from = $6, valid_until = $7, superseded_by = $8,
					confidence = $9, dataset_id = $10`,
			e.ID, e.SourceID, e.TargetID, e.RelationshipName, e.Properties,
			syncOptionalTime(e.ValidFrom), syncOptionalTime(e.ValidUntil), e.SupersededBy, e.Confidence, e.DatasetID)
		if _, err := cfg.DB.ExecContext(ctx, q, qargs...); err == nil {
			edgesImported++
		} else {
			edgesFailed++
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	return map[string]int{"nodes_imported": nodesImported, "edges_imported": edgesImported, "nodes_failed": nodesFailed, "edges_failed": edgesFailed, "nodes_total": len(g.Nodes), "edges_total": len(g.Edges)}, firstErr
}

func syncErrorText(err error, token string) string {
	if err == nil {
		return ""
	}
	// Transport errors need their cause, not a URL that can carry credentials.
	var transport *url.Error
	if errors.As(err, &transport) {
		err = fmt.Errorf("%s: %w", transport.Op, transport.Err)
	}
	text := err.Error()
	if token != "" {
		text = strings.ReplaceAll(text, token, "[redacted]")
	}
	return text
}

func syncCountsResult(counts map[string]int, err error, token string) map[string]any {
	result := map[string]any{}
	for k, v := range counts {
		result[k] = v
	}
	if err != nil {
		result["error"] = syncErrorText(err, token)
	}
	return result
}

// syncResultStatus is additive: legacy per-type counts and *_error fields
// remain available, including counts from a partially imported batch.
func syncResultStatus(result map[string]any) string {
	success, failed, running := false, false, false
	for _, kind := range []string{"memories", "interactions", "graph"} {
		_, typeFailed := result[kind+"_error"]
		failed = failed || typeFailed
		if value, ok := result[kind]; ok {
			accepted, bad, pending := syncTypeOutcome(value)
			failed = failed || bad
			running = running || pending
			success = success || (accepted && (!typeFailed || syncAcceptedCount(value) > 0))
		}
	}
	if collections, ok := result["collections_sync"].(map[string]any); ok {
		for _, value := range collections {
			accepted, bad, pending := syncTypeOutcome(value)
			success = success || accepted
			failed = failed || bad
			running = running || pending
		}
	}
	if failed {
		if success || running {
			return "partial"
		}
		return "error"
	}
	if running {
		return "running"
	}
	return "ok"
}

func syncAcceptedCount(value any) int {
	count := 0
	switch data := value.(type) {
	case map[string]int:
		for _, key := range []string{"imported", "skipped", "nodes_imported", "edges_imported"} {
			count += data[key]
		}
	case map[string]any:
		for _, key := range []string{"imported", "skipped", "nodes_imported", "edges_imported"} {
			switch n := data[key].(type) {
			case int:
				count += n
			case float64:
				count += int(n)
			}
		}
	case fiber.Map:
		return syncAcceptedCount(map[string]any(data))
	}
	return count
}

func syncTypeOutcome(value any) (bool, bool, bool) {
	var fields map[string]any
	switch data := value.(type) {
	case map[string]any:
		fields = data
	case fiber.Map:
		fields = map[string]any(data)
	case map[string]string:
		fields = map[string]any{}
		for k, v := range data {
			fields[k] = v
		}
	case map[string]int:
		fields = map[string]any{}
		for k, v := range data {
			fields[k] = v
		}
	default:
		return true, false, false
	}
	failed := fields["error"] != nil || fields["detail"] != nil
	for _, key := range []string{"failed", "nodes_failed", "edges_failed"} {
		switch n := fields[key].(type) {
		case int:
			failed = failed || n > 0
		case float64:
			failed = failed || n > 0
		}
	}
	running := false
	if raw, exists := fields["status"]; exists {
		status, ok := raw.(string)
		if !ok {
			return false, true, false
		}
		switch status {
		case "started":
			_, valid := syncStartedCollection(fields)
			if !valid {
				return false, true, false
			}
			running = true
		case "running", "RUNNING":
			runID, _ := fields["run_id"].(string)
			if strings.TrimSpace(runID) == "" {
				return false, true, false
			}
			running = true
		case "empty", "ok", "COMPLETED":
		case "error", "partial", "failed", "FAILED":
			failed = true
		default:
			return false, true, false
		}
	}
	return !running && (!failed || syncAcceptedCount(value) > 0), failed, running
}

func syncOptionalTime(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// Older peers can omit explicit failure fields. A complete acknowledgement
// still accounts for every sent row as imported or skipped; missing rows must
// not turn an old peer's swallowed SQL error into a successful push.
func validateSyncImportAck(result map[string]any, payload any) error {
	type batch struct {
		prefix string
		count  int
	}
	var batches []batch
	switch records := payload.(type) {
	case []syncMemory:
		batches = []batch{{"", len(records)}}
	case []syncInteraction:
		batches = []batch{{"", len(records)}}
	case syncGraph:
		batches = []batch{{"nodes_", len(records.Nodes)}, {"edges_", len(records.Edges)}}
	}
	for _, batch := range batches {
		read := func(key string, optional bool) (int, bool) {
			value, exists := result[batch.prefix+key]
			if !exists {
				return 0, optional
			}
			n, ok := value.(float64)
			return int(n), ok && n >= 0 && n <= float64(batch.count) && n == float64(int(n))
		}
		imported, validImported := read("imported", false)
		skipped, validSkipped := read("skipped", true)
		total, validTotal := read("total", false)
		if !validImported || !validSkipped || !validTotal {
			return fmt.Errorf("invalid remote import counts")
		}
		if total != batch.count || imported+skipped != total {
			return fmt.Errorf("remote import acknowledged %d of %d %srecords", imported+skipped, batch.count, batch.prefix)
		}
	}
	return nil
}

// A collection import is asynchronous. A start acknowledgement must identify
// the job and account for the records it accepted; status alone is not proof.
func syncStartedCollection(result map[string]any) (int, bool) {
	runID, _ := result["run_id"].(string)
	if strings.TrimSpace(runID) == "" {
		return 0, false
	}
	switch n := result["records"].(type) {
	case int:
		return n, n > 0
	case float64:
		if n <= 0 || n >= float64(int(^uint(0)>>1)) {
			return 0, false
		}
		return int(n), n == float64(int(n))
	default:
		return 0, false
	}
}
