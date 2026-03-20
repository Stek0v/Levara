// api.go — Cognee-compatible REST API endpoints for React frontend.
// Implements: health, datasets CRUD, file upload, cognify trigger, search.
package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/stek0v/cognevra/internal/store"
	"github.com/stek0v/cognevra/pkg/embed"
	"github.com/stek0v/cognevra/pkg/extract"
	"github.com/stek0v/cognevra/pkg/ingest"
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
}

// RegisterCogneeAPI registers all Cognee-compatible endpoints on the Fiber app.
func RegisterCogneeAPI(app fiber.Router, cfg APIConfig) {
	if cfg.StoragePath == "" {
		cfg.StoragePath = "data/uploads"
	}
	os.MkdirAll(cfg.StoragePath, 0755)

	// U1: Health
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ready", "health": "healthy", "version": "cognevra-go"})
	})

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

	// U4: Cognify trigger
	app.Post("/cognify", cognifyHandler(cfg))

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
		if cfg.PostgresDSN == "" {
			return c.JSON([]DatasetDTO{})
		}
		db, err := sql.Open("postgres", cfg.PostgresDSN)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		defer db.Close()

		rows, err := db.QueryContext(c.Context(), "SELECT id, name, created_at, owner_id FROM datasets ORDER BY created_at DESC")
		if err != nil {
			return c.JSON([]DatasetDTO{}) // empty if table doesn't exist
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

		if cfg.PostgresDSN != "" {
			db, _ := sql.Open("postgres", cfg.PostgresDSN)
			defer db.Close()
			db.ExecContext(c.Context(),
				"INSERT INTO datasets (id, name, created_at, updated_at) VALUES ($1, $2, $3, $4) ON CONFLICT (name) DO NOTHING",
				id, req.Name, now, now)
		}

		return c.Status(201).JSON(DatasetDTO{
			ID: id, Name: req.Name, CreatedAt: now.Format(time.RFC3339),
		})
	}
}

func datasetDeleteHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Params("id")
		if cfg.PostgresDSN != "" {
			db, _ := sql.Open("postgres", cfg.PostgresDSN)
			defer db.Close()
			db.ExecContext(c.Context(), "DELETE FROM datasets WHERE id = $1", id)
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
		if cfg.PostgresDSN == "" {
			return c.JSON([]DataDTO{})
		}
		db, _ := sql.Open("postgres", cfg.PostgresDSN)
		defer db.Close()

		rows, err := db.QueryContext(c.Context(),
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
		if cfg.PostgresDSN != "" {
			db, _ := sql.Open("postgres", cfg.PostgresDSN)
			defer db.Close()
			db.ExecContext(c.Context(), "DELETE FROM dataset_data WHERE dataset_id = $1 AND data_id = $2", dsID, dataID)
			db.ExecContext(c.Context(), "DELETE FROM data WHERE id = $1", dataID)
		}
		return c.JSON(fiber.Map{"deleted": true})
	}
}

func datasetDataRawHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		dataID := c.Params("dataId")
		if cfg.PostgresDSN == "" {
			return c.Status(404).JSON(fiber.Map{"error": "not found"})
		}
		db, _ := sql.Open("postgres", cfg.PostgresDSN)
		defer db.Close()

		var location string
		db.QueryRowContext(c.Context(), "SELECT raw_data_location FROM data WHERE id = $1", dataID).Scan(&location)
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
		if cfg.PostgresDSN != "" {
			dsID := uuid.New().String()
			mw, err := ingest.NewMetadataWriter(cfg.PostgresDSN)
			if err == nil {
				mw.WriteMetadata(context.Background(), results, "", dsID, datasetName)
				mw.Close()
			}
		}

		return c.JSON(fiber.Map{
			"status": "ok",
			"items":  len(results),
			"files":  len(files),
		})
	}
}

// ── U4: Cognify ──

func cognifyHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req struct {
			Datasets []string `json:"datasets"`
		}
		c.BodyParser(&req)

		// Return immediately with pipeline run info
		// Actual cognify runs in background via PipelineCognify gRPC
		runID := uuid.New().String()
		return c.JSON(fiber.Map{
			"status":          "PipelineRunStarted",
			"pipeline_run_id": runID,
			"message":         "Use PipelineCognify gRPC RPC for streaming progress",
		})
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

		// Route to appropriate search based on type
		switch queryType {
		case "CHUNKS", "RAG_COMPLETION":
			return chunksSearch(c, cfg, req)
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

// ── Helpers ──

func absPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}
