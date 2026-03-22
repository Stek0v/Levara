// api.go — Cognee-compatible REST API endpoints for React frontend.
// Implements: health, datasets CRUD, file upload, cognify trigger, search.
package http

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stek0v/cognevra/internal/store"
	"github.com/stek0v/cognevra/pkg/bm25"
	"github.com/stek0v/cognevra/pkg/embed"
	"github.com/stek0v/cognevra/pkg/fetch"
	"github.com/stek0v/cognevra/pkg/graphdb"
	"github.com/stek0v/cognevra/pkg/extract"
	"github.com/stek0v/cognevra/pkg/ingest"
	"github.com/stek0v/cognevra/pkg/llm"
	"github.com/stek0v/cognevra/pkg/llmcache"
	"github.com/stek0v/cognevra/pkg/orchestrator"
	"github.com/stek0v/cognevra/pkg/temporal"
	"github.com/stek0v/cognevra/pipeline"
)

// APIConfig holds configuration for Cognee-compatible API endpoints.
type APIConfig struct {
	PostgresDSN   string
	StoragePath   string
	EmbedEndpoint string
	EmbedModel    string
	Collections   *store.CollectionManager
	Neo4jCfg      GraphVisualizationConfig
	DB            *sql.DB // shared connection pool (nil if no PostgresDSN)
	BM25Indexes   map[string]*bm25.Index // shared BM25 indexes (same as gRPC service)
	LLMCache      llmcache.LLMCacher // shared LLM response cache (nil = no caching)
	LLMProvider   llm.Provider // multi-provider LLM abstraction (nil = legacy raw HTTP)
}

// RegisterCogneeAPI registers all Cognee-compatible endpoints on the Fiber app.
func RegisterCogneeAPI(app fiber.Router, cfg APIConfig) {
	if cfg.StoragePath == "" {
		cfg.StoragePath = "data/uploads"
	}
	os.MkdirAll(cfg.StoragePath, 0755)

	// U1: Health is registered as public route in main.go (before JWT middleware)

	// U2: Datasets CRUD
	app.Get("/datasets", datasetsListHandler(cfg))
	app.Post("/datasets", datasetCreateHandler(cfg))
	app.Delete("/datasets/:id", datasetDeleteHandler(cfg))
	app.Get("/datasets/:id/data", datasetDataHandler(cfg))
	app.Delete("/datasets/:id/data/:dataId", datasetDataDeleteHandler(cfg))
	app.Get("/datasets/:id/data/:dataId/raw", datasetDataRawHandler(cfg))
	app.Get("/datasets/status", datasetStatusHandler(cfg))

	// U3: File upload (multipart)
	app.Post("/add", addHandler(cfg))

	// U4: Cognify trigger + status + SSE stream
	app.Post("/cognify", cognifyHandler(cfg))
	app.Get("/cognify/:runId/status", cognifyStatusHandler())
	app.Get("/cognify/:runId/stream", cognifyStreamHandler())

	// U6: Memify — post-cognify graph enrichment + SSE stream
	app.Post("/memify", memifyHandler(cfg))
	app.Get("/memify/:runId/status", memifyStatusHandler())
	app.Get("/memify/:runId/stream", memifyStreamHandler())

	// U7: User management (protected)
	app.Get("/users/me", userMeHandler(cfg))
	app.Put("/users/me", userUpdateHandler(cfg))
	app.Put("/users/me/password", userChangePasswordHandler(cfg))

	// U8: Settings API (protected)
	app.Get("/settings", settingsGetHandler(cfg))
	app.Put("/settings", settingsPutHandler(cfg))

	// U11: Collections metadata
	app.Get("/collections", collectionsListHandler(cfg))
	app.Post("/collections", collectionCreateHandler(cfg))
	app.Delete("/collections/:name", collectionDeleteHandler(cfg))
	app.Get("/collections/:name/meta", collectionMetaHandler(cfg))
	app.Put("/collections/:name/meta", collectionMetaUpdateHandler(cfg))

	// U12: Re-embedding migration
	RegisterReembedAPI(app, cfg)

	// U13: Dual-search across collections with different models/dims
	RegisterDualSearchAPI(app, cfg)

	// U14: Prune endpoints (cleanup)
	app.Post("/prune/data", pruneDataHandler(cfg))
	app.Post("/prune/system", pruneSystemHandler(cfg))

	// U15: Update data endpoint
	app.Patch("/datasets/:id/data/:dataId", updateDataHandler(cfg))

	// U16: Tenant management + ACL
	RegisterTenantAPI(app, cfg)

	// U17: Session/interaction tracking
	RegisterSessionAPI(app, cfg)

	// U18: Ontology management
	app.Post("/ontologies", ontologyUploadHandler(cfg))
	app.Get("/ontologies", ontologyListHandler(cfg))
	app.Delete("/ontologies/:id", ontologyDeleteHandler(cfg))

	// U10: RBAC — dataset sharing + permissions
	RegisterRBACAPI(app, cfg)

	// U9: Notebooks CRUD + cell execution
	app.Get("/notebooks", notebooksListHandler(cfg))
	app.Post("/notebooks", notebookCreateHandler(cfg))
	app.Get("/notebooks/:id", notebookGetHandler(cfg))
	app.Put("/notebooks/:id", notebookUpdateHandler(cfg))
	app.Delete("/notebooks/:id", notebookDeleteHandler(cfg))
	app.Post("/notebooks/:id/cells", cellAddHandler(cfg))
	app.Put("/notebooks/:id/cells/:cellId", cellUpdateHandler(cfg))
	app.Delete("/notebooks/:id/cells/:cellId", cellDeleteHandler(cfg))
	app.Post("/notebooks/:id/cells/:cellId/run", cellRunHandler(cfg))

	// U5: Cognee-compatible search (separate from legacy vector /search)
	app.Post("/search/text", searchHandler(cfg))
}

// ── U1: Health ──
// Already inline above.

// ── U2: Datasets ──

type DatasetDTO struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt *string `json:"updated_at"`
	OwnerID   string  `json:"owner_id"`
}

func datasetsListHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.JSON([]DatasetDTO{})
		}

		userID, _ := c.Locals("user_id").(string)

		var rows *sql.Rows
		var err error
		showAll := userID == ""
		if !showAll && userID != "" {
			// Check superuser — sees everything
			var isSuperuser bool
			cfg.DB.QueryRowContext(c.Context(), "SELECT COALESCE(is_superuser, false) FROM users WHERE id = $1", userID).Scan(&isSuperuser)
			showAll = isSuperuser
		}
		if showAll {
			rows, err = cfg.DB.QueryContext(c.Context(),
				"SELECT id, name, created_at, COALESCE(owner_id,'') FROM datasets ORDER BY created_at DESC")
		} else {
			rows, err = cfg.DB.QueryContext(c.Context(),
				`SELECT DISTINCT d.id, d.name, d.created_at, COALESCE(d.owner_id,'')
				 FROM datasets d
				 LEFT JOIN dataset_shares s ON s.dataset_id = d.id AND s.user_id = $1
				 WHERE d.owner_id = $1 OR d.owner_id = '' OR d.owner_id IS NULL OR s.id IS NOT NULL
				 ORDER BY d.created_at DESC`, userID)
		}
		if err != nil {
			return c.JSON([]DatasetDTO{})
		}
		defer rows.Close()

		var datasets []DatasetDTO
		for rows.Next() {
			var d DatasetDTO
			var createdAt time.Time
			rows.Scan(&d.ID, &d.Name, &createdAt, &d.OwnerID)
			d.CreatedAt = createdAt.Format(time.RFC3339)
			datasets = append(datasets, d)
		}
		if datasets == nil {
			datasets = []DatasetDTO{}
		}
		return c.JSON(datasets)
	}
}

func datasetCreateHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req struct {
			Name string `json:"name"`
		}
		if err := c.BodyParser(&req); err != nil || req.Name == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "name required"})
		}

		id := uuid.New().String()
		now := time.Now().UTC()
		ownerID, _ := c.Locals("user_id").(string)

		if cfg.DB != nil {
			cfg.DB.ExecContext(c.Context(),
				"INSERT INTO datasets (id, name, owner_id, created_at, updated_at) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (name) DO NOTHING",
				id, req.Name, ownerID, now, now)
		}

		return c.Status(201).JSON(DatasetDTO{
			ID: id, Name: req.Name, CreatedAt: now.Format(time.RFC3339), OwnerID: ownerID,
		})
	}
}

func datasetDeleteHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Params("id")
		if cfg.DB != nil {
			userID, _ := c.Locals("user_id").(string)
			if userID != "" {
				cfg.DB.ExecContext(c.Context(), "DELETE FROM datasets WHERE id = $1 AND (owner_id = $2 OR owner_id = '' OR owner_id IS NULL)", id, userID)
			} else {
				cfg.DB.ExecContext(c.Context(), "DELETE FROM datasets WHERE id = $1", id)
			}
		}
		return c.JSON(fiber.Map{"deleted": true})
	}
}

type DataDTO struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Extension       string `json:"extension"`
	MimeType        string `json:"mime_type"`
	RawDataLocation string `json:"raw_data_location"`
	DataSize        int64  `json:"data_size"`
	CreatedAt       string `json:"created_at"`
}

func datasetDataHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		dsID := c.Params("id")
		if cfg.DB == nil {
			return c.JSON([]DataDTO{})
		}

		rows, err := cfg.DB.QueryContext(c.Context(),
			`SELECT d.id, d.name, d.extension, d.mime_type, d.raw_data_location,
			 COALESCE(d.data_size, 0), d.created_at
			 FROM data d JOIN dataset_data dd ON d.id = dd.data_id
			 WHERE dd.dataset_id = $1 ORDER BY d.created_at DESC`, dsID)
		if err != nil {
			return c.JSON([]DataDTO{})
		}
		defer rows.Close()

		var items []DataDTO
		for rows.Next() {
			var d DataDTO
			var createdAt time.Time
			rows.Scan(&d.ID, &d.Name, &d.Extension, &d.MimeType, &d.RawDataLocation, &d.DataSize, &createdAt)
			d.CreatedAt = createdAt.Format(time.RFC3339)
			items = append(items, d)
		}
		if items == nil {
			items = []DataDTO{}
		}
		return c.JSON(items)
	}
}

func datasetDataDeleteHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		dataID := c.Params("dataId")
		dsID := c.Params("id")
		if cfg.DB != nil {
			cfg.DB.ExecContext(c.Context(), "DELETE FROM dataset_data WHERE dataset_id = $1 AND data_id = $2", dsID, dataID)
			cfg.DB.ExecContext(c.Context(), "DELETE FROM data WHERE id = $1", dataID)
		}
		return c.JSON(fiber.Map{"deleted": true})
	}
}

func datasetDataRawHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		dataID := c.Params("dataId")
		if cfg.DB == nil {
			return c.Status(404).JSON(fiber.Map{"detail": "not found"})
		}

		var location string
		cfg.DB.QueryRowContext(c.Context(), "SELECT raw_data_location FROM data WHERE id = $1", dataID).Scan(&location)
		if location == "" {
			return c.Status(404).JSON(fiber.Map{"detail": "not found"})
		}
		// Convert file:// URI to path
		path := strings.TrimPrefix(location, "file://")
		return c.SendFile(path)
	}
}

func datasetStatusHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ready"})
	}
}

// ── U3: File Upload (multipart) ──

func addHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		datasetName := c.FormValue("datasetName")
		datasetID := c.FormValue("datasetId")
		if datasetName == "" {
			datasetName = "default"
		}

		form, err := c.MultipartForm()
		if err != nil {
			// Try as text body or URL
			body := c.Body()
			if len(body) > 0 {
				bodyStr := string(body)
				// URL detection: fetch content from URL
				if fetch.IsURL(strings.TrimSpace(bodyStr)) {
					var fetchedText string
					var fetchErr error
					if fetch.IsGitHubURL(bodyStr) {
						fetchedText, fetchErr = fetch.FetchGitHub(strings.TrimSpace(bodyStr))
					} else {
						fetchedText, fetchErr = fetch.FetchURL(strings.TrimSpace(bodyStr))
					}
					if fetchErr == nil && fetchedText != "" {
						bodyStr = fetchedText
					}
				}
				items := []ingest.Item{{Text: bodyStr, DatasetName: datasetName}}
				results, err := ingest.Ingest(items, cfg.StoragePath)
				if err != nil {
					return c.Status(500).JSON(fiber.Map{"detail": err.Error()})
				}
				txtDsID := datasetID
				if txtDsID == "" {
					txtDsID = uuid.New().String()
				}
				if cfg.DB != nil {
					ownerID, _ := c.Locals("user_id").(string)
					mw := ingest.NewMetadataWriterFromDB(cfg.DB)
					mw.WriteMetadata(context.Background(), results, ownerID, txtDsID, datasetName)
				}
				return c.JSON(fiber.Map{"status": "ok", "items": len(results), "dataset_id": txtDsID, "dataset_name": datasetName})
			}
			return c.Status(400).JSON(fiber.Map{"detail": "no data provided"})
		}

		files := form.File["data"]
		if len(files) == 0 {
			return c.Status(400).JSON(fiber.Map{"detail": "no files uploaded"})
		}

		var items []ingest.Item
		for _, file := range files {
			f, err := file.Open()
			if err != nil {
				continue
			}
			data, _ := io.ReadAll(f)
			f.Close()

			// Extract text if needed
			result, err := extract.Extract(data, file.Filename, file.Header.Get("Content-Type"))
			if err == nil && result.Text != "" {
				items = append(items, ingest.Item{
					Text:        result.Text,
					Filename:    file.Filename,
					DatasetName: datasetName,
				})
			} else {
				// Fallback: raw content as text
				items = append(items, ingest.Item{
					Text:        string(data),
					Filename:    file.Filename,
					DatasetName: datasetName,
				})
			}
		}

		results, err := ingest.Ingest(items, cfg.StoragePath)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": err.Error()})
		}

		// Write metadata to PostgreSQL if configured
		dsID := datasetID
		if dsID == "" {
			dsID = uuid.New().String()
		}
		if cfg.DB != nil {
			ownerID, _ := c.Locals("user_id").(string)
			mw := ingest.NewMetadataWriterFromDB(cfg.DB)
			mw.WriteMetadata(context.Background(), results, ownerID, dsID, datasetName)
		}

		return c.JSON(fiber.Map{
			"status":       "ok",
			"items":        len(results),
			"files":        len(files),
			"dataset_id":   dsID,
			"dataset_name": datasetName,
		})
	}
}

// ── U4: Cognify ──

// pipelineRuns tracks background cognify pipeline statuses.
var pipelineRuns sync.Map // runID → *pipelineRunStatus

type pipelineRunStatus struct {
	RunID     string    `json:"pipeline_run_id"`
	Status    string    `json:"status"` // RUNNING, COMPLETED, FAILED
	Stage     string    `json:"stage"`
	Message   string    `json:"message"`
	Chunks    int       `json:"chunks_created"`
	Entities  int       `json:"entities_extracted"`
	Edges     int       `json:"edges_extracted"`
	ElapsedMs int64     `json:"elapsed_ms"`
	StartedAt time.Time `json:"started_at"`
}

func cognifyHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req struct {
			Datasets        []string `json:"datasets"`
			DatasetIds      []string `json:"datasetIds"`      // Cognee frontend format
			Texts           []string `json:"texts"`
			LLMModel        string   `json:"llm_model"`
			Collection      string   `json:"collection"`
			RunInBackground bool     `json:"runInBackground"`
		}
		c.BodyParser(&req)

		// Merge datasets and datasetIds
		allDatasetIDs := append(req.Datasets, req.DatasetIds...)

		// Collect texts: either from request body or from dataset files
		var texts []string
		if len(req.Texts) > 0 {
			texts = req.Texts
		} else if cfg.DB != nil && len(allDatasetIDs) > 0 {
			// Load text from dataset files
			for _, dsID := range allDatasetIDs {
				rows, err := cfg.DB.QueryContext(c.Context(),
					`SELECT d.raw_data_location FROM data d
					 JOIN dataset_data dd ON d.id = dd.data_id
					 WHERE dd.dataset_id = $1`, dsID)
				if err != nil {
					continue
				}
				for rows.Next() {
					var loc string
					rows.Scan(&loc)
					loc = strings.TrimPrefix(loc, "file://")
					if data, err := os.ReadFile(loc); err == nil {
						texts = append(texts, string(data))
					}
				}
				rows.Close()
			}
			// If no files found, check if data was stored as inline text (ingest stores to disk)
			if len(texts) == 0 {
				for _, dsID := range allDatasetIDs {
					rows, err := cfg.DB.QueryContext(c.Context(),
						`SELECT d.name FROM data d
						 JOIN dataset_data dd ON d.id = dd.data_id
						 WHERE dd.dataset_id = $1`, dsID)
					if err != nil {
						continue
					}
					for rows.Next() {
						var name string
						rows.Scan(&name)
						if name != "" {
							texts = append(texts, name)
						}
					}
					rows.Close()
				}
			}
		}

		if len(texts) == 0 {
			return c.Status(400).JSON(fiber.Map{"detail": "no texts to cognify (provide texts[] or datasets[])"})
		}

		runID := uuid.New().String()
		collection := req.Collection
		if collection == "" {
			collection = "default"
		}

		runStatus := &pipelineRunStatus{
			RunID:     runID,
			Status:    "RUNNING",
			Stage:     "starting",
			StartedAt: time.Now(),
		}
		pipelineRuns.Store(runID, runStatus)

		// Build orchestrator config from server config + request overrides
		pipeCfg := orchestrator.Config{
			ChunkStrategy:   "merged",
			MinChunkChars:   50,
			MaxChunkChars:   2000,
			LLMEndpoint:     os.Getenv("LLM_ENDPOINT"),
			LLMModel:        os.Getenv("LLM_MODEL"),
			LLMConcurrency:  1,
			EmbedEndpoint:   cfg.EmbedEndpoint,
			EmbedModel:      cfg.EmbedModel,
			Neo4jURL:        cfg.Neo4jCfg.Neo4jURL,
			Neo4jUser:       cfg.Neo4jCfg.Neo4jUser,
			Neo4jPassword:   cfg.Neo4jCfg.Neo4jPassword,
			Neo4jDatabase:   cfg.Neo4jCfg.Neo4jDatabase,
			Collection:      collection,
			Collections:     cfg.Collections,
			GenerateTriplets: true,
			DatasetID:       func() string { if len(allDatasetIDs) > 0 { return allDatasetIDs[0] }; return runID }(),
			DB:              cfg.DB,
			LLMCache:            cfg.LLMCache,
			LLMProvider:         cfg.LLMProvider,
			UseStructuredOutput: func() *bool { b := true; return &b }(),
		}
		if req.LLMModel != "" {
			pipeCfg.LLMModel = req.LLMModel
		}

		// Run pipeline in background
		go func() {
			progressCh := make(chan orchestrator.Progress, 100)
			errCh := make(chan error, 1)

			go func() {
				errCh <- orchestrator.Run(context.Background(), texts, pipeCfg, progressCh)
			}()

			// Track progress
			for p := range progressCh {
				runStatus.Stage = p.Stage
				runStatus.Message = p.Message
				runStatus.Chunks = p.ChunksCreated
				runStatus.Entities = p.EntitiesExtracted
				runStatus.Edges = p.EdgesExtracted
				runStatus.ElapsedMs = p.ElapsedMs
			}

			if err := <-errCh; err != nil {
				runStatus.Status = "FAILED"
				runStatus.Message = err.Error()
			} else {
				runStatus.Status = "COMPLETED"
			}
			runStatus.ElapsedMs = time.Since(runStatus.StartedAt).Milliseconds()
		}()

		return c.JSON(fiber.Map{
			"status":          "PipelineRunStarted",
			"pipeline_run_id": runID,
		})
	}
}

func cognifyStatusHandler() fiber.Handler {
	return func(c *fiber.Ctx) error {
		runID := c.Params("runId")
		if val, ok := pipelineRuns.Load(runID); ok {
			return c.JSON(val)
		}
		return c.Status(404).JSON(fiber.Map{"detail": "run not found"})
	}
}

// cognifyStreamHandler streams pipeline progress via Server-Sent Events (SSE).
// GET /cognify/:runId/stream
// React frontend: const es = new EventSource("/api/v1/cognify/{runId}/stream")
func cognifyStreamHandler() fiber.Handler {
	return func(c *fiber.Ctx) error {
		runID := c.Params("runId")
		if _, ok := pipelineRuns.Load(runID); !ok {
			return c.Status(404).JSON(fiber.Map{"detail": "run not found"})
		}

		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache")
		c.Set("Connection", "keep-alive")
		c.Set("X-Accel-Buffering", "no")

		c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
			lastStage := ""
			for {
				val, ok := pipelineRuns.Load(runID)
				if !ok {
					fmt.Fprintf(w, "event: error\ndata: {\"error\":\"run not found\"}\n\n")
					w.Flush()
					return
				}
				status := val.(*pipelineRunStatus)

				// Send update if stage changed or terminal
				if status.Stage != lastStage || status.Status != "RUNNING" {
					data, _ := json.Marshal(status)
					fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data)
					w.Flush()
					lastStage = status.Stage
				}

				if status.Status != "RUNNING" {
					fmt.Fprintf(w, "event: done\ndata: %s\n\n", func() string { d, _ := json.Marshal(status); return string(d) }())
					w.Flush()
					return
				}

				time.Sleep(500 * time.Millisecond)
			}
		})
		return nil
	}
}

// ── U5: Cognee-compatible Search ──

type CogneeSearchRequest struct {
	QueryText         string   `json:"query_text"`
	QueryType         string   `json:"query_type"` // CHUNKS, GRAPH_COMPLETION, etc.
	TopK              int      `json:"top_k"`
	CypherQuery       string   `json:"cypher_query"` // Raw Cypher for CYPHER search type
	AllowedDatasetIDs []string `json:"-"`             // RBAC: nil = no filtering (dev mode)
}

// filterByAllowedDatasets post-filters search results by allowed dataset IDs.
// If allowedIDs is nil, no filtering is applied (dev mode / backward compat).
func filterByAllowedDatasets(results []fiber.Map, allowedIDs []string) []fiber.Map {
	if allowedIDs == nil {
		return results
	}
	allowed := make(map[string]bool, len(allowedIDs))
	for _, id := range allowedIDs {
		allowed[id] = true
	}
	var filtered []fiber.Map
	for _, r := range results {
		dsID := extractDatasetID(r)
		if dsID == "" || allowed[dsID] {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// extractDatasetID extracts dataset_id from a result's metadata field.
func extractDatasetID(r fiber.Map) string {
	meta, ok := r["metadata"]
	if !ok {
		return ""
	}
	var m map[string]any
	switch v := meta.(type) {
	case json.RawMessage:
		json.Unmarshal(v, &m)
	case []byte:
		json.Unmarshal(v, &m)
	case string:
		json.Unmarshal([]byte(v), &m)
	case map[string]any:
		m = v
	}
	dsID, _ := m["dataset_id"].(string)
	return dsID
}

func searchHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req CogneeSearchRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid request"})
		}
		if req.QueryText == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "query_text required"})
		}
		if req.TopK <= 0 {
			req.TopK = 10
		}

		// RBAC: resolve allowed dataset IDs for this user
		userID, _ := c.Locals("user_id").(string)
		req.AllowedDatasetIDs = GetAllowedDatasetIDs(cfg.DB, c.Context(), userID)

		queryType := strings.ToUpper(req.QueryType)
		if queryType == "" {
			queryType = "CHUNKS"
		}

		switch queryType {
		case "CHUNKS":
			return chunksSearch(c, cfg, req)
		case "RAG_COMPLETION":
			return ragCompletionSearch(c, cfg, req)
		case "SUMMARIES":
			return summariesSearch(c, cfg, req)
		case "CHUNKS_LEXICAL":
			return bm25Search(c, cfg, req)
		case "HYBRID", "WEIGHTED_HYBRID":
			return hybridSearch(c, cfg, req)
		case "TEMPORAL":
			return temporalSearch(c, cfg, req)
		case "GRAPH_COMPLETION", "GRAPH_SUMMARY_COMPLETION",
			"GRAPH_COMPLETION_COT", "GRAPH_COMPLETION_CONTEXT_EXTENSION":
			return graphCompletionSearch(c, cfg, req)
		case "TRIPLET_COMPLETION":
			return tripletCompletionSearch(c, cfg, req)
		case "NATURAL_LANGUAGE":
			return naturalLanguageSearch(c, cfg, req)
		case "CYPHER":
			return cypherSearch(c, cfg, req)
		case "CODE", "CODING_RULES":
			return chunksSearch(c, cfg, req) // code → vector fallback
		case "FEELING_LUCKY":
			return hybridSearch(c, cfg, req) // auto-select → hybrid is best general
		default:
			return chunksSearch(c, cfg, req)
		}
	}
}

func chunksSearch(c *fiber.Ctx, cfg APIConfig, req CogneeSearchRequest) error {
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return c.JSON([]any{})
	}

	embedClient := embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, 16, 1)
	sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections)

	// Search across all collections
	colls := cfg.Collections.List()
	var allResults []fiber.Map

	for _, coll := range colls {
		results, err := sp.SearchByText(context.Background(), coll, req.QueryText, req.TopK)
		if err != nil {
			continue
		}
		for _, r := range results {
			meta := string(r.Metadata)
			allResults = append(allResults, fiber.Map{
				"id":         r.ID,
				"score":      r.Score,
				"collection": coll,
				"metadata":   json.RawMessage(meta),
			})
		}
	}

	// RBAC post-filter by allowed datasets
	allResults = filterByAllowedDatasets(allResults, req.AllowedDatasetIDs)

	// Sort by score (lower = better for cosine distance)
	if len(allResults) > req.TopK {
		allResults = allResults[:req.TopK]
	}

	return c.JSON(allResults)
}

func bm25Search(c *fiber.Ctx, cfg APIConfig, req CogneeSearchRequest) error {
	if cfg.BM25Indexes == nil {
		return c.JSON([]any{})
	}

	var allResults []fiber.Map
	for collection, idx := range cfg.BM25Indexes {
		results := idx.Search(req.QueryText, req.TopK)
		for _, r := range results {
			allResults = append(allResults, fiber.Map{
				"id":         r.ID,
				"score":      r.Score,
				"collection": collection,
				"metadata":   json.RawMessage(r.Metadata),
			})
		}
	}

	// RBAC post-filter
	allResults = filterByAllowedDatasets(allResults, req.AllowedDatasetIDs)

	if len(allResults) > req.TopK {
		allResults = allResults[:req.TopK]
	}
	return c.JSON(allResults)
}

func hybridSearch(c *fiber.Ctx, cfg APIConfig, req CogneeSearchRequest) error {
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return c.JSON([]any{})
	}

	embedClient := embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, 16, 1)
	sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections)

	colls := cfg.Collections.List()
	var allResults []fiber.Map

	for _, coll := range colls {
		// Vector search
		vectorResults, err := sp.SearchByText(context.Background(), coll, req.QueryText, req.TopK*2)
		if err != nil {
			continue
		}
		var vr []bm25.VectorResult
		for _, r := range vectorResults {
			vr = append(vr, bm25.VectorResult{
				ID: r.ID, Score: r.Score, Metadata: string(r.Metadata),
			})
		}

		// BM25 search
		var br []bm25.Result
		if cfg.BM25Indexes != nil {
			if idx, ok := cfg.BM25Indexes[coll]; ok {
				br = idx.Search(req.QueryText, req.TopK*2)
			}
		}

		// Fuse with RRF
		hybrid := bm25.HybridSearch(vr, br, req.TopK, 1.0, 1.0)
		for _, h := range hybrid {
			allResults = append(allResults, fiber.Map{
				"id":           h.ID,
				"fused_score":  h.FusedScore,
				"vector_score": h.VectorScore,
				"bm25_score":   h.BM25Score,
				"collection":   coll,
				"metadata":     json.RawMessage(h.Metadata),
			})
		}
	}

	// RBAC post-filter
	allResults = filterByAllowedDatasets(allResults, req.AllowedDatasetIDs)

	if len(allResults) > req.TopK {
		allResults = allResults[:req.TopK]
	}
	return c.JSON(allResults)
}

func temporalSearch(c *fiber.Ctx, cfg APIConfig, req CogneeSearchRequest) error {
	// Step 1: Extract dates from query text
	events := temporal.ExtractTimestamps(req.QueryText, time.Now())

	var temporalResults []fiber.Map

	if len(events) > 0 {
		from, to, ok := temporal.DateRangeFromEvents(events)
		if ok {
			// Step 2: Try Neo4j temporal query
			if cfg.Neo4jCfg.Neo4jURL != "" {
				temporalResults = temporalSearchNeo4j(c.Context(), cfg, from, to, req.TopK)
			}

			// Step 3: Fallback to PostgreSQL if Neo4j returned nothing
			if len(temporalResults) == 0 && cfg.DB != nil {
				temporalResults = temporalSearchPostgres(c.Context(), cfg, from, to, req.TopK)
			}
		}
	}

	// Step 4: Also do vector search for temporal context if we have embed
	var vectorResults []fiber.Map
	if cfg.EmbedEndpoint != "" && cfg.Collections != nil {
		embedClient := embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, 16, 1)
		sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections)

		colls := cfg.Collections.List()
		for _, coll := range colls {
			results, err := sp.SearchByText(context.Background(), coll, req.QueryText, req.TopK)
			if err != nil {
				continue
			}
			for _, r := range results {
				vectorResults = append(vectorResults, fiber.Map{
					"id":         r.ID,
					"score":      r.Score,
					"collection": coll,
					"metadata":   json.RawMessage(r.Metadata),
					"source":     "vector",
				})
			}
		}
	}

	// RBAC post-filter
	vectorResults = filterByAllowedDatasets(vectorResults, req.AllowedDatasetIDs)

	// Combine: temporal results first, then vector results
	combined := make([]fiber.Map, 0, len(temporalResults)+len(vectorResults))
	combined = append(combined, temporalResults...)
	combined = append(combined, vectorResults...)

	if len(combined) > req.TopK {
		combined = combined[:req.TopK]
	}

	// Include extracted dates for transparency
	extractedDates := make([]fiber.Map, 0, len(events))
	for _, e := range events {
		extractedDates = append(extractedDates, fiber.Map{
			"date":       e.Date.Format(time.RFC3339),
			"date_str":   e.DateStr,
			"confidence": e.Confidence,
		})
	}

	return c.JSON(fiber.Map{
		"results":         combined,
		"extracted_dates": extractedDates,
		"search_type":     "TEMPORAL",
	})
}

// temporalSearchNeo4j queries Neo4j for entities linked to TemporalEvent nodes in a date range.
func temporalSearchNeo4j(ctx context.Context, cfg APIConfig, from, to time.Time, limit int) []fiber.Map {
	// Use a timeout context for Neo4j query (5 seconds max)
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	writer, err := graphdb.NewWriter(queryCtx, cfg.Neo4jCfg.Neo4jURL, cfg.Neo4jCfg.Neo4jUser,
		cfg.Neo4jCfg.Neo4jPassword, cfg.Neo4jCfg.Neo4jDatabase)
	if err != nil {
		return nil
	}
	defer writer.Close(queryCtx)

	cypher := `MATCH (e:` + "`__Node__`" + `)-[:HAPPENED_AT]->(t:` + "`__Node__`" + `)
		WHERE t.type = 'TemporalEvent'
		AND t.date >= $from AND t.date <= $to
		RETURN e.id AS entity_id, e.name AS entity_name, e.type AS entity_type,
		       e.description AS entity_desc, t.date AS date, t.name AS date_str
		LIMIT $limit`

	rows, err := writer.Query(queryCtx, cypher, map[string]any{
		"from":  from.Format("2006-01-02"),
		"to":    to.Format("2006-01-02"),
		"limit": int64(limit),
	})
	if err != nil {
		return nil
	}

	var results []fiber.Map
	for _, row := range rows {
		name, _ := row["entity_name"].(string)
		typ, _ := row["entity_type"].(string)
		desc, _ := row["entity_desc"].(string)
		date, _ := row["date"].(string)
		dateStr, _ := row["date_str"].(string)
		entityID, _ := row["entity_id"].(string)

		results = append(results, fiber.Map{
			"id":         entityID,
			"name":       name,
			"type":       typ,
			"description": desc,
			"date":       date,
			"date_str":   dateStr,
			"source":     "neo4j_temporal",
		})
	}
	return results
}

// temporalSearchPostgres queries PostgreSQL for TemporalEvent nodes in a date range.
func temporalSearchPostgres(ctx context.Context, cfg APIConfig, from, to time.Time, limit int) []fiber.Map {
	if cfg.DB == nil {
		return nil
	}

	// Query temporal nodes and their connected entities via edges
	query := `
		SELECT gn.id, gn.name, gn.type, gn.description,
		       gn.properties::jsonb->>'date' AS date,
		       ge.source_id AS entity_id,
		       en.name AS entity_name, en.type AS entity_type, en.description AS entity_desc
		FROM graph_nodes gn
		LEFT JOIN graph_edges ge ON ge.target_id = gn.id AND ge.relationship_name = 'HAPPENED_AT'
		LEFT JOIN graph_nodes en ON en.id = ge.source_id
		WHERE gn.type = 'TemporalEvent'
		AND gn.properties::jsonb->>'date' >= $1
		AND gn.properties::jsonb->>'date' <= $2
		LIMIT $3`

	rows, err := cfg.DB.QueryContext(ctx, query, from.Format("2006-01-02"), to.Format("2006-01-02"), limit)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []fiber.Map
	for rows.Next() {
		var id, name, typ, desc string
		var date, entityID, entityName, entityType, entityDesc sql.NullString
		rows.Scan(&id, &name, &typ, &desc, &date, &entityID, &entityName, &entityType, &entityDesc)

		if entityID.Valid && entityName.Valid {
			results = append(results, fiber.Map{
				"id":          entityID.String,
				"name":        entityName.String,
				"type":        entityType.String,
				"description": entityDesc.String,
				"date":        date.String,
				"date_str":    name,
				"source":      "postgres_temporal",
			})
		} else {
			// No linked entity, return the temporal node itself
			results = append(results, fiber.Map{
				"id":          id,
				"name":        name,
				"type":        typ,
				"description": desc,
				"date":        date.String,
				"source":      "postgres_temporal",
			})
		}
	}
	return results
}

// ragCompletionSearch does vector search + LLM completion over results.
// Returns both raw chunks and an LLM-generated answer.
func ragCompletionSearch(c *fiber.Ctx, cfg APIConfig, req CogneeSearchRequest) error {
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return c.JSON(fiber.Map{"chunks": []any{}, "answer": ""})
	}

	// Step 1: vector search (same as chunksSearch)
	embedClient := embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, 16, 1)
	sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections)

	colls := cfg.Collections.List()
	var chunks []fiber.Map

	for _, coll := range colls {
		results, err := sp.SearchByText(context.Background(), coll, req.QueryText, req.TopK)
		if err != nil {
			continue
		}
		for _, r := range results {
			chunks = append(chunks, fiber.Map{
				"id":         r.ID,
				"score":      r.Score,
				"collection": coll,
				"metadata":   json.RawMessage(r.Metadata),
			})
		}
	}
	// RBAC post-filter
	chunks = filterByAllowedDatasets(chunks, req.AllowedDatasetIDs)

	if len(chunks) > req.TopK {
		chunks = chunks[:req.TopK]
	}

	// Step 2: LLM completion using retrieved chunks as context
	llmEndpoint := os.Getenv("LLM_ENDPOINT")
	llmModel := os.Getenv("LLM_MODEL")
	answer := ""

	if llmEndpoint != "" && llmModel != "" && len(chunks) > 0 {
		// Build context from chunk metadata
		var contextParts []string
		for i, chunk := range chunks {
			meta := ""
			if raw, ok := chunk["metadata"].(json.RawMessage); ok {
				meta = string(raw)
			}
			contextParts = append(contextParts, fmt.Sprintf("[%d] %s", i+1, meta))
			if i >= 9 {
				break
			}
		}

		prompt := fmt.Sprintf("Based on the following context, answer the question.\n\nContext:\n%s\n\nQuestion: %s\n\nAnswer:",
			strings.Join(contextParts, "\n"), req.QueryText)

		answer = callLLMFromAPI(llmEndpoint, llmModel, prompt, cfg.LLMProvider)
	}

	return c.JSON(fiber.Map{
		"chunks": chunks,
		"answer": answer,
	})
}

// summariesSearch searches only in summary collections (TextSummary nodes from memify).
func summariesSearch(c *fiber.Ctx, cfg APIConfig, req CogneeSearchRequest) error {
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return c.JSON([]any{})
	}

	embedClient := embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, 16, 1)
	sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections)

	// Search only in summary/triplet collections
	colls := cfg.Collections.List()
	var allResults []fiber.Map

	for _, coll := range colls {
		// Filter: only collections that contain summaries or triplets
		lower := strings.ToLower(coll)
		if !strings.Contains(lower, "summary") && !strings.Contains(lower, "triplet") && !strings.Contains(lower, "memify") {
			continue
		}

		results, err := sp.SearchByText(context.Background(), coll, req.QueryText, req.TopK)
		if err != nil {
			continue
		}
		for _, r := range results {
			allResults = append(allResults, fiber.Map{
				"id":         r.ID,
				"score":      r.Score,
				"collection": coll,
				"metadata":   json.RawMessage(r.Metadata),
			})
		}
	}

	// Also check PostgreSQL graph_nodes for TextSummary type
	if cfg.DB != nil {
		var sqlQuery string
		var sqlArgs []any
		if req.AllowedDatasetIDs != nil {
			// Build dataset_id filter placeholders starting at $3
			dsPlaceholders := make([]string, len(req.AllowedDatasetIDs))
			sqlArgs = append(sqlArgs, req.QueryText, req.TopK)
			for i, id := range req.AllowedDatasetIDs {
				dsPlaceholders[i] = fmt.Sprintf("$%d", i+3)
				sqlArgs = append(sqlArgs, id)
			}
			sqlQuery = fmt.Sprintf(`SELECT id, name, description FROM graph_nodes
			 WHERE type = 'TextSummary' AND (
				 name ILIKE '%%' || $1 || '%%' OR description ILIKE '%%' || $1 || '%%'
			 ) AND (dataset_id IS NULL OR dataset_id = '' OR dataset_id IN (%s))
			 LIMIT $2`, strings.Join(dsPlaceholders, ","))
		} else {
			sqlQuery = `SELECT id, name, description FROM graph_nodes
			 WHERE type = 'TextSummary' AND (
				 name ILIKE '%' || $1 || '%' OR description ILIKE '%' || $1 || '%'
			 ) LIMIT $2`
			sqlArgs = []any{req.QueryText, req.TopK}
		}
		rows, err := cfg.DB.QueryContext(c.Context(), sqlQuery, sqlArgs...)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id, name, desc string
				rows.Scan(&id, &name, &desc)
				allResults = append(allResults, fiber.Map{
					"id":         id,
					"score":      0.5, // SQL match, no vector score
					"collection": "graph_nodes",
					"metadata":   json.RawMessage(fmt.Sprintf(`{"name":%q,"description":%q,"type":"TextSummary"}`, name, desc)),
				})
			}
		}
	}

	// RBAC post-filter
	allResults = filterByAllowedDatasets(allResults, req.AllowedDatasetIDs)

	if len(allResults) > req.TopK {
		allResults = allResults[:req.TopK]
	}

	return c.JSON(allResults)
}

// callLLMFromAPI is a standalone LLM call helper for search handlers.
// If provider is non-nil, uses the provider abstraction (supports Anthropic, etc.).
// Otherwise falls back to raw HTTP POST to OpenAI-compatible endpoint.
func callLLMFromAPI(endpoint, model, prompt string, provider ...llm.Provider) string {
	// Provider path: use abstraction if available.
	if len(provider) > 0 && provider[0] != nil {
		resp, err := provider[0].ChatCompletion(context.Background(), llm.CompletionRequest{
			Model:       model,
			Messages:    []llm.Message{{Role: "user", Content: prompt}},
			Temperature: 0.3,
			MaxTokens:   2000,
		})
		if err != nil {
			return ""
		}
		return strings.TrimSpace(resp.Content)
	}

	// Legacy raw HTTP path.
	body := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.3,
		"max_tokens":  2000,
	}

	jsonBody, _ := json.Marshal(body)
	resp, err := http.Post(endpoint+"/chat/completions", "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(respBody, &result) == nil && len(result.Choices) > 0 {
		return strings.TrimSpace(result.Choices[0].Message.Content)
	}
	return ""
}

// ── Helpers ──

// ── Prune endpoints ──

func pruneDataHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB != nil {
			cfg.DB.ExecContext(c.Context(), "DELETE FROM dataset_data")
			cfg.DB.ExecContext(c.Context(), "DELETE FROM data")
			cfg.DB.ExecContext(c.Context(), "DELETE FROM datasets")
		}
		return c.JSON(fiber.Map{"status": "ok", "pruned": "data"})
	}
}

func pruneSystemHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB != nil {
			cfg.DB.ExecContext(c.Context(), "DELETE FROM graph_nodes")
			cfg.DB.ExecContext(c.Context(), "DELETE FROM graph_edges")
			cfg.DB.ExecContext(c.Context(), "DELETE FROM dataset_data")
			cfg.DB.ExecContext(c.Context(), "DELETE FROM data")
			cfg.DB.ExecContext(c.Context(), "DELETE FROM datasets")
		}
		return c.JSON(fiber.Map{"status": "ok", "pruned": "system"})
	}
}

// ── Update data endpoint ──

func updateDataHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		dataID := c.Params("dataId")
		if cfg.DB == nil {
			return c.Status(503).JSON(fiber.Map{"detail": "database required"})
		}
		// Accept new content via body
		body := c.Body()
		if len(body) == 0 {
			return c.Status(400).JSON(fiber.Map{"detail": "content required"})
		}
		// Update name and content in data table
		_, err := cfg.DB.ExecContext(c.Context(),
			"UPDATE data SET name = $1, updated_at = NOW() WHERE id = $2",
			string(body), dataID)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": err.Error()})
		}
		return c.JSON(fiber.Map{"id": dataID, "updated": true})
	}
}

func absPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}
