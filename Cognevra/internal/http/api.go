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
	"github.com/stek0v/cognevra/pkg/extract"
	"github.com/stek0v/cognevra/pkg/ingest"
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
		if userID != "" {
			rows, err = cfg.DB.QueryContext(c.Context(),
				"SELECT id, name, created_at, COALESCE(owner_id,'') FROM datasets WHERE owner_id = $1 OR owner_id = '' OR owner_id IS NULL ORDER BY created_at DESC", userID)
		} else {
			rows, err = cfg.DB.QueryContext(c.Context(),
				"SELECT id, name, created_at, COALESCE(owner_id,'') FROM datasets ORDER BY created_at DESC")
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
			return c.Status(400).JSON(fiber.Map{"error": "name required"})
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
			return c.Status(404).JSON(fiber.Map{"error": "not found"})
		}

		var location string
		cfg.DB.QueryRowContext(c.Context(), "SELECT raw_data_location FROM data WHERE id = $1", dataID).Scan(&location)
		if location == "" {
			return c.Status(404).JSON(fiber.Map{"error": "not found"})
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
		if datasetName == "" {
			datasetName = "default"
		}

		form, err := c.MultipartForm()
		if err != nil {
			// Try as text body
			body := c.Body()
			if len(body) > 0 {
				items := []ingest.Item{{Text: string(body), DatasetName: datasetName}}
				results, err := ingest.Ingest(items, cfg.StoragePath)
				if err != nil {
					return c.Status(500).JSON(fiber.Map{"error": err.Error()})
				}
				return c.JSON(fiber.Map{"status": "ok", "items": len(results)})
			}
			return c.Status(400).JSON(fiber.Map{"error": "no data provided"})
		}

		files := form.File["data"]
		if len(files) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "no files uploaded"})
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
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}

		// Write metadata to PostgreSQL if configured
		if cfg.DB != nil {
			dsID := uuid.New().String()
			ownerID, _ := c.Locals("user_id").(string)
			mw := ingest.NewMetadataWriterFromDB(cfg.DB)
			mw.WriteMetadata(context.Background(), results, ownerID, dsID, datasetName)
		}

		return c.JSON(fiber.Map{
			"status": "ok",
			"items":  len(results),
			"files":  len(files),
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
			Datasets  []string `json:"datasets"`
			Texts     []string `json:"texts"`      // direct text input
			LLMModel  string   `json:"llm_model"`   // optional LLM model override
			Collection string  `json:"collection"`  // target collection
		}
		c.BodyParser(&req)

		// Collect texts: either from request body or from dataset files
		var texts []string
		if len(req.Texts) > 0 {
			texts = req.Texts
		} else if cfg.DB != nil && len(req.Datasets) > 0 {
			// Load text from dataset files
			for _, dsID := range req.Datasets {
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
		}

		if len(texts) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "no texts to cognify (provide texts[] or datasets[])"})
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
			MinChunkChars:   200,
			MaxChunkChars:   2000,
			LLMEndpoint:     os.Getenv("LLM_ENDPOINT"),
			LLMModel:        os.Getenv("LLM_MODEL"),
			LLMConcurrency:  4,
			EmbedEndpoint:   cfg.EmbedEndpoint,
			EmbedModel:      cfg.EmbedModel,
			Neo4jURL:        cfg.Neo4jCfg.Neo4jURL,
			Neo4jUser:       cfg.Neo4jCfg.Neo4jUser,
			Neo4jPassword:   cfg.Neo4jCfg.Neo4jPassword,
			Neo4jDatabase:   cfg.Neo4jCfg.Neo4jDatabase,
			Collection:      collection,
			Collections:     cfg.Collections,
			GenerateTriplets: true,
			DB:              cfg.DB,
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
		return c.Status(404).JSON(fiber.Map{"error": "run not found"})
	}
}

// cognifyStreamHandler streams pipeline progress via Server-Sent Events (SSE).
// GET /cognify/:runId/stream
// React frontend: const es = new EventSource("/api/v1/cognify/{runId}/stream")
func cognifyStreamHandler() fiber.Handler {
	return func(c *fiber.Ctx) error {
		runID := c.Params("runId")
		if _, ok := pipelineRuns.Load(runID); !ok {
			return c.Status(404).JSON(fiber.Map{"error": "run not found"})
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
	QueryText string `json:"query_text"`
	QueryType string `json:"query_type"` // CHUNKS, GRAPH_COMPLETION, etc.
	TopK      int    `json:"top_k"`
}

func searchHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req CogneeSearchRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "invalid request"})
		}
		if req.QueryText == "" {
			return c.Status(400).JSON(fiber.Map{"error": "query_text required"})
		}
		if req.TopK <= 0 {
			req.TopK = 10
		}

		queryType := strings.ToUpper(req.QueryType)
		if queryType == "" {
			queryType = "CHUNKS"
		}

		switch queryType {
		case "CHUNKS", "GRAPH_COMPLETION":
			return chunksSearch(c, cfg, req)
		case "RAG_COMPLETION":
			return ragCompletionSearch(c, cfg, req)
		case "SUMMARIES":
			return summariesSearch(c, cfg, req)
		case "CHUNKS_LEXICAL":
			return bm25Search(c, cfg, req)
		case "HYBRID":
			return hybridSearch(c, cfg, req)
		case "TEMPORAL":
			return temporalSearch(c, cfg, req)
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

	if len(allResults) > req.TopK {
		allResults = allResults[:req.TopK]
	}
	return c.JSON(allResults)
}

func temporalSearch(c *fiber.Ctx, cfg APIConfig, req CogneeSearchRequest) error {
	// Extract timestamps from query text
	events := temporal.ExtractTimestamps(req.QueryText, time.Now())
	results := make([]fiber.Map, 0, len(events))
	for _, e := range events {
		results = append(results, fiber.Map{
			"date":       e.Date.Format(time.RFC3339),
			"date_str":   e.DateStr,
			"text":       e.Text,
			"confidence": e.Confidence,
			"node_id":    e.NodeID,
		})
	}
	if len(results) > req.TopK {
		results = results[:req.TopK]
	}
	return c.JSON(results)
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

		answer = callLLMFromAPI(llmEndpoint, llmModel, prompt)
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
		rows, err := cfg.DB.QueryContext(c.Context(),
			`SELECT id, name, description FROM graph_nodes
			 WHERE type = 'TextSummary' AND (
				 name ILIKE '%' || $1 || '%' OR description ILIKE '%' || $1 || '%'
			 ) LIMIT $2`, req.QueryText, req.TopK)
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

	if len(allResults) > req.TopK {
		allResults = allResults[:req.TopK]
	}

	return c.JSON(allResults)
}

// callLLMFromAPI is a standalone LLM call helper for search handlers.
func callLLMFromAPI(endpoint, model, prompt string) string {
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

func absPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}
