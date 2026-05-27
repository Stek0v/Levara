// sync.go — Cross-instance data synchronization for Levara.
// Export/import memories, interactions, and graph over HTTP.
// Handles different embedding dimensions by syncing text only (vectors re-embedded on import).
package http

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/metrics"
	"github.com/stek0v/levara/pkg/embed"
)

// RegisterSyncAPI registers sync export/import endpoints.
func RegisterSyncAPI(app fiber.Router, cfg APIConfig) {
	app.Get("/sync/manifest", syncManifestHandler(cfg))

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

// ── Manifest ──

type syncManifest struct {
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

func syncManifestHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := syncRequestContext(c)
		defer cancel()

		m := syncManifest{
			EmbedModel: cfg.EmbedModel,
		}
		if cfg.Collections != nil {
			for _, meta := range cfg.Collections.ListWithMeta() {
				if m.EmbedDim == 0 {
					m.EmbedDim = meta.EmbeddingDim
				}
				m.Collections = append(m.Collections, syncCollectionInfo{
					Name: meta.Name, Records: meta.RecordCount,
					Dim: meta.EmbeddingDim, Model: meta.EmbeddingModel,
				})
			}
		}
		if cfg.DB != nil {
			cfg.DB.QueryRowContext(ctx, Q(`SELECT COUNT(*) FROM memories`)).Scan(&m.Memories.Count)
			cfg.DB.QueryRowContext(ctx, Q(`SELECT COALESCE(MAX(updated_at),'') FROM memories`)).Scan(&m.Memories.LatestUpdated)
			cfg.DB.QueryRowContext(ctx, Q(`SELECT COUNT(*) FROM interactions`)).Scan(&m.Interactions.Count)
			cfg.DB.QueryRowContext(ctx, Q(`SELECT COALESCE(MAX(created_at),'') FROM interactions`)).Scan(&m.Interactions.LatestUpdated)
			cfg.DB.QueryRowContext(ctx, Q(`SELECT COUNT(*) FROM graph_nodes`)).Scan(&m.GraphNodes.Count)
			cfg.DB.QueryRowContext(ctx, Q(`SELECT COUNT(*) FROM graph_edges`)).Scan(&m.GraphEdges.Count)
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

		if cfg.DB == nil {
			return c.JSON([]syncMemory{})
		}
		since := c.Query("since")
		var rows interface {
			Next() bool
			Scan(...any) error
			Close() error
		}
		var err error
		if since != "" {
			rows, err = cfg.DB.QueryContext(ctx,
				Q(`SELECT id, key, value, type, owner_id, collection_name,
					 COALESCE(room,''), COALESCE(hall,''), is_pinned, pin_priority,
					 created_at, updated_at
					 FROM memories WHERE updated_at > $1 ORDER BY updated_at`), since)
		} else {
			rows, err = cfg.DB.QueryContext(ctx,
				Q(`SELECT id, key, value, type, owner_id, collection_name,
					 COALESCE(room,''), COALESCE(hall,''), is_pinned, pin_priority,
					 created_at, updated_at
					 FROM memories ORDER BY updated_at`))
		}
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": err.Error()})
		}
		defer rows.Close()
		var result []syncMemory
		for rows.Next() {
			var m syncMemory
			rows.Scan(&m.ID, &m.Key, &m.Value, &m.Type, &m.OwnerID, &m.CollectionName,
				&m.Room, &m.Hall, &m.IsPinned, &m.PinPriority, &m.CreatedAt, &m.UpdatedAt)
			result = append(result, m)
		}
		if result == nil {
			result = []syncMemory{}
		}
		return c.JSON(result)
	}
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

		imported, skipped := 0, 0
		for _, m := range memories {
			// Last-writer-wins: check if existing record is newer
			var existingUpdated string
			cfg.DB.QueryRowContext(ctx,
				Q(`SELECT updated_at FROM memories WHERE key = $1 AND owner_id = $2`),
				m.Key, m.OwnerID).Scan(&existingUpdated)

			if existingUpdated != "" && existingUpdated >= m.UpdatedAt {
				skipped++
				continue
			}

			q, qargs := QArgs(`INSERT INTO memories (
					id, key, value, type, owner_id, collection_name, room, hall,
					is_pinned, pin_priority, created_at, updated_at
				)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
				 ON CONFLICT(key, owner_id) DO UPDATE SET
					value = $3, type = $4, collection_name = $6, room = $7, hall = $8,
					is_pinned = $9, pin_priority = $10, updated_at = $12`,
				m.ID, m.Key, m.Value, m.Type, m.OwnerID, m.CollectionName,
				m.Room, m.Hall, m.IsPinned, m.PinPriority, m.CreatedAt, m.UpdatedAt)
			if _, err := cfg.DB.ExecContext(ctx, q, qargs...); err == nil {
				imported++
			}
		}

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
			memoriesSnapshot := memories
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

		return c.JSON(fiber.Map{"imported": imported, "skipped": skipped, "total": len(memories), "embedding": imported > 0})
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

		if cfg.DB == nil {
			return c.JSON([]syncInteraction{})
		}
		since := c.Query("since")
		var rows interface {
			Next() bool
			Scan(...any) error
			Close() error
		}
		var err error
		if since != "" {
			rows, err = cfg.DB.QueryContext(ctx,
				Q(`SELECT id, session_id, user_id, query, response, search_type, created_at
					 FROM interactions WHERE created_at > $1 ORDER BY created_at`), since)
		} else {
			rows, err = cfg.DB.QueryContext(ctx,
				Q(`SELECT id, session_id, user_id, query, response, search_type, created_at
					 FROM interactions ORDER BY created_at`))
		}
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": err.Error()})
		}
		defer rows.Close()
		var result []syncInteraction
		for rows.Next() {
			var i syncInteraction
			rows.Scan(&i.ID, &i.SessionID, &i.UserID, &i.Query, &i.Response, &i.SearchType, &i.CreatedAt)
			result = append(result, i)
		}
		if result == nil {
			result = []syncInteraction{}
		}
		return c.JSON(result)
	}
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

		imported, skipped := 0, 0
		for _, i := range interactions {
			q, qargs := QArgs(`INSERT INTO interactions (id, session_id, user_id, query, response, search_type, created_at)
				 VALUES ($1, $2, $3, $4, $5, $6, $7)
				 ON CONFLICT(id) DO NOTHING`,
				i.ID, i.SessionID, i.UserID, i.Query, i.Response, i.SearchType, i.CreatedAt)
			res, err := cfg.DB.ExecContext(ctx, q, qargs...)
			if err == nil {
				if n, _ := res.RowsAffected(); n > 0 {
					imported++
				} else {
					skipped++
				}
			}
		}

		return c.JSON(fiber.Map{"imported": imported, "skipped": skipped, "total": len(interactions)})
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

		if cfg.DB == nil {
			return c.JSON(syncGraph{Nodes: []syncGraphNode{}, Edges: []syncGraphEdge{}})
		}
		g := syncGraph{}

		nodeRows, err := cfg.DB.QueryContext(ctx,
			Q(`SELECT id, name, type, COALESCE(description,''), COALESCE(properties,'{}'), COALESCE(dataset_id,'') FROM graph_nodes`))
		if err == nil {
			defer nodeRows.Close()
			for nodeRows.Next() {
				var n syncGraphNode
				if err := nodeRows.Scan(&n.ID, &n.Name, &n.Type, &n.Description, &n.Properties, &n.DatasetID); err != nil {
					continue
				}
				g.Nodes = append(g.Nodes, n)
			}
		}
		if g.Nodes == nil {
			g.Nodes = []syncGraphNode{}
		}

		edgeRows, err := cfg.DB.QueryContext(ctx,
			Q(`SELECT id, source_id, target_id, relationship_name, COALESCE(properties,'{}'),
				  COALESCE(valid_from,''), COALESCE(valid_until,''), COALESCE(superseded_by,''),
				  COALESCE(confidence,1.0), COALESCE(dataset_id,'')
			   FROM graph_edges`))
		if err == nil {
			defer edgeRows.Close()
			for edgeRows.Next() {
				var e syncGraphEdge
				if err := edgeRows.Scan(&e.ID, &e.SourceID, &e.TargetID, &e.RelationshipName, &e.Properties,
					&e.ValidFrom, &e.ValidUntil, &e.SupersededBy, &e.Confidence, &e.DatasetID); err != nil {
					continue
				}
				g.Edges = append(g.Edges, e)
			}
		}
		if g.Edges == nil {
			g.Edges = []syncGraphEdge{}
		}

		return c.JSON(g)
	}
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

		nodesImported, edgesImported := 0, 0

		for _, n := range g.Nodes {
			q, qargs := QArgs(`INSERT INTO graph_nodes (id, name, type, description, properties, dataset_id)
				 VALUES ($1, $2, $3, $4, $5, $6)
				 ON CONFLICT(id) DO UPDATE SET
					name = $2, type = $3, description = $4, properties = $5, dataset_id = $6`,
				n.ID, n.Name, n.Type, n.Description, n.Properties, n.DatasetID)
			if _, err := cfg.DB.ExecContext(ctx, q, qargs...); err == nil {
				nodesImported++
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
				 VALUES ($1, $2, $3, $4, $5, nullif($6,''), nullif($7,''), $8, $9, $10)
				 ON CONFLICT(id) DO UPDATE SET
					source_id = $2, target_id = $3, relationship_name = $4, properties = $5,
					valid_from = nullif($6,''), valid_until = nullif($7,''), superseded_by = $8,
					confidence = $9, dataset_id = $10`,
				e.ID, e.SourceID, e.TargetID, e.RelationshipName, e.Properties,
				e.ValidFrom, e.ValidUntil, e.SupersededBy, e.Confidence, e.DatasetID)
			if _, err := cfg.DB.ExecContext(ctx, q, qargs...); err == nil {
				edgesImported++
			}
		}

		return c.JSON(fiber.Map{
			"nodes_imported": nodesImported, "edges_imported": edgesImported,
			"nodes_total": len(g.Nodes), "edges_total": len(g.Edges),
		})
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

func (s *syncMap) Store(k string, v *syncCollectionImportStatus) { s.m.Store(k, v) }
func (s *syncMap) Load(k string) (*syncCollectionImportStatus, bool) {
	v, ok := s.m.Load(k)
	if !ok {
		return nil, false
	}
	return v.(*syncCollectionImportStatus), true
}

func syncImportCollectionHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var export syncCollectionExport
		if err := c.BodyParser(&export); err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid JSON"})
		}
		if export.Collection == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "collection name required"})
		}
		if len(export.Records) == 0 {
			return c.JSON(fiber.Map{"status": "empty", "message": "no records to import"})
		}
		if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
			return c.Status(503).JSON(fiber.Map{"detail": "embedding service or collections not configured"})
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
			}

			status.Status = "COMPLETED"
			status.ElapsedMs = time.Since(start).Milliseconds()
			status.Message = fmt.Sprintf("imported %d/%d units from %d records (%s dim=%d → %s dim=%d) in %dms",
				status.Processed, len(units), len(export.Records), export.SourceModel, export.SourceDim, cfg.EmbedModel, targetDim, status.ElapsedMs)
			log.Printf("[sync-import] %s", status.Message)
		}()

		return c.JSON(fiber.Map{
			"status":     "started",
			"run_id":     runID,
			"collection": export.Collection,
			"records":    len(export.Records),
			"source":     fmt.Sprintf("%s (dim=%d)", export.SourceModel, export.SourceDim),
			"target":     fmt.Sprintf("%s (dim=auto)", cfg.EmbedModel),
		})
	}
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

// ── Sync Pull (client-side: fetch from remote, import locally) ──

// SyncPull fetches data from a remote Levara instance and imports it locally.
// Used by the MCP sync tool and CLI.
func SyncPull(cfg APIConfig, remoteURL string, types []string, since string) map[string]any {
	results := map[string]any{}
	client := &http.Client{Timeout: 30 * time.Second}
	bgCtx, bgCancel := backgroundTaskContext()
	defer bgCancel()

	shouldSync := func(t string) bool {
		if len(types) == 0 {
			return true
		}
		for _, tt := range types {
			if tt == t {
				return true
			}
		}
		return false
	}

	if shouldSync("memories") {
		url := remoteURL + "/sync/export/memories"
		if since != "" {
			url += "?since=" + since
		}
		resp, err := client.Get(url)
		if err != nil {
			results["memories_error"] = err.Error()
		} else {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			var memories []syncMemory
			if json.Unmarshal(body, &memories) == nil && len(memories) > 0 {
				imported, skipped := 0, 0
				for _, m := range memories {
					var existingUpdated string
					cfg.DB.QueryRowContext(bgCtx,
						Q(`SELECT updated_at FROM memories WHERE key = $1 AND owner_id = $2`),
						m.Key, m.OwnerID).Scan(&existingUpdated)
					if existingUpdated != "" && existingUpdated >= m.UpdatedAt {
						skipped++
						continue
					}
					q, qargs := QArgs(`INSERT INTO memories (
								id, key, value, type, owner_id, collection_name, room, hall,
								is_pinned, pin_priority, created_at, updated_at
							)
							 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
							 ON CONFLICT(key, owner_id) DO UPDATE SET
								value = $3, type = $4, collection_name = $6, room = $7, hall = $8,
								is_pinned = $9, pin_priority = $10, updated_at = $12`,
						m.ID, m.Key, m.Value, m.Type, m.OwnerID, m.CollectionName,
						m.Room, m.Hall, m.IsPinned, m.PinPriority, m.CreatedAt, m.UpdatedAt)
					if _, err := cfg.DB.ExecContext(bgCtx, q, qargs...); err == nil {
						imported++
					}
				}
				results["memories"] = map[string]int{"imported": imported, "skipped": skipped, "total": len(memories)}
			} else {
				results["memories"] = "no data"
			}
		}
	}

	if shouldSync("interactions") {
		url := remoteURL + "/sync/export/interactions"
		if since != "" {
			url += "?since=" + since
		}
		resp, err := client.Get(url)
		if err != nil {
			results["interactions_error"] = err.Error()
		} else {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			var interactions []syncInteraction
			if json.Unmarshal(body, &interactions) == nil && len(interactions) > 0 {
				imported := 0
				for _, i := range interactions {
					q, qargs := QArgs(`INSERT INTO interactions (id, session_id, user_id, query, response, search_type, created_at)
							 VALUES ($1, $2, $3, $4, $5, $6, $7)
							 ON CONFLICT(id) DO NOTHING`,
						i.ID, i.SessionID, i.UserID, i.Query, i.Response, i.SearchType, i.CreatedAt)
					if res, err := cfg.DB.ExecContext(bgCtx, q, qargs...); err == nil {
						if n, _ := res.RowsAffected(); n > 0 {
							imported++
						}
					}
				}
				results["interactions"] = map[string]int{"imported": imported, "total": len(interactions)}
			} else {
				results["interactions"] = "no data"
			}
		}
	}

	if shouldSync("graph") {
		resp, err := client.Get(remoteURL + "/sync/export/graph")
		if err != nil {
			results["graph_error"] = err.Error()
		} else {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			var g syncGraph
			if json.Unmarshal(body, &g) == nil {
				nodesImported, edgesImported := 0, 0
				for _, n := range g.Nodes {
					q, qargs := QArgs(`INSERT INTO graph_nodes (id, name, type, description, properties, dataset_id)
							 VALUES ($1, $2, $3, $4, $5, $6)
							 ON CONFLICT(id) DO UPDATE SET
								name = $2, type = $3, description = $4, properties = $5, dataset_id = $6`,
						n.ID, n.Name, n.Type, n.Description, n.Properties, n.DatasetID)
					if _, err := cfg.DB.ExecContext(bgCtx, q, qargs...); err == nil {
						nodesImported++
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
							 VALUES ($1, $2, $3, $4, $5, nullif($6,''), nullif($7,''), $8, $9, $10)
							 ON CONFLICT(id) DO UPDATE SET
								source_id = $2, target_id = $3, relationship_name = $4, properties = $5,
								valid_from = nullif($6,''), valid_until = nullif($7,''), superseded_by = $8,
								confidence = $9, dataset_id = $10`,
						e.ID, e.SourceID, e.TargetID, e.RelationshipName, e.Properties,
						e.ValidFrom, e.ValidUntil, e.SupersededBy, e.Confidence, e.DatasetID)
					if _, err := cfg.DB.ExecContext(bgCtx, q, qargs...); err == nil {
						edgesImported++
					}
				}
				results["graph"] = map[string]int{
					"nodes_imported": nodesImported, "edges_imported": edgesImported,
					"nodes_total": len(g.Nodes), "edges_total": len(g.Edges),
				}
			}
		}
	}

	return results
}

// SyncManifestFromRemote fetches the manifest from a remote Levara instance.
func SyncManifestFromRemote(remoteURL string) (*syncManifest, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(remoteURL + "/sync/manifest")
	if err != nil {
		return nil, fmt.Errorf("failed to reach remote: %w", err)
	}
	defer resp.Body.Close()
	var m syncManifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}
	return &m, nil
}
