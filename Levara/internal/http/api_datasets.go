// api_datasets.go — Dataset CRUD + data listing/deletion endpoints, split
// out of api.go (T4). Covers:
//
//   GET    /datasets
//   POST   /datasets
//   DELETE /datasets/:id
//   GET    /datasets/:id/data
//   DELETE /datasets/:id/data/:dataId
//   GET    /datasets/:id/data/:dataId/raw
//   GET    /datasets/status
package http

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// ── U2: Datasets ──

type DatasetDTO struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   *string `json:"updated_at"`
	OwnerID     string  `json:"owner_id"`
	RecordCount int     `json:"record_count"`
}

// In-memory dataset store (fallback when no PostgreSQL)
var memDatasets = struct {
	mu   sync.Mutex
	data []DatasetDTO
}{}

// datasetsListHandler — GET /datasets.
//
// @Summary     List datasets visible to the caller
// @Description Returns datasets owned by the caller plus any explicitly shared. Superusers see all rows.
// @Tags        datasets
// @Produce     json
// @Security    BearerAuth
// @Success     200 {array}  DatasetDTO
// @Failure     500 {object} map[string]any
// @Router      /datasets [get]
func datasetsListHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			memDatasets.mu.Lock()
			ds := make([]DatasetDTO, len(memDatasets.data))
			copy(ds, memDatasets.data)
			memDatasets.mu.Unlock()
			return c.JSON(ds)
		}

		userID, _ := c.Locals("user_id").(string)

		var rows *sql.Rows
		var err error
		showAll := userID == ""
		if !showAll && userID != "" {
			// Check superuser — sees everything
			var isSuperuser bool
			cfg.DB.QueryRowContext(context.Background(), Q("SELECT COALESCE(is_superuser, false) FROM users WHERE id = $1"), userID).Scan(&isSuperuser)
			showAll = isSuperuser
		}
		if showAll {
			rows, err = cfg.DB.QueryContext(context.Background(),
				Q(`SELECT d.id, d.name, d.created_at, COALESCE(d.owner_id,''), COUNT(dd.data_id)
				 FROM datasets d LEFT JOIN dataset_data dd ON dd.dataset_id = d.id
				 GROUP BY d.id ORDER BY d.created_at DESC`))
		} else {
			dsSQL, dsArgs := QArgs(`SELECT DISTINCT d.id, d.name, d.created_at, COALESCE(d.owner_id,''), COUNT(dd.data_id)
				 FROM datasets d
				 LEFT JOIN dataset_shares s ON s.dataset_id = d.id AND s.user_id = $1
				 LEFT JOIN dataset_data dd ON dd.dataset_id = d.id
				 WHERE d.owner_id = $1 OR d.owner_id = '' OR d.owner_id IS NULL OR s.id IS NOT NULL
				 GROUP BY d.id ORDER BY d.created_at DESC`, userID)
			rows, err = cfg.DB.QueryContext(context.Background(), dsSQL, dsArgs...)
		}
		// BL-3: surface SQL errors instead of returning []. Silent empty
		// responses made bad credentials / missing tables indistinguishable
		// from an empty dataset list — the WebUI would render "no datasets"
		// forever instead of letting the user retry.
		if err != nil {
			log.Printf("[datasets] list query: %v", err)
			return c.Status(fiber.StatusInternalServerError).
				JSON(fiber.Map{"detail": "list datasets: " + err.Error()})
		}
		defer rows.Close()

		var datasets []DatasetDTO
		for rows.Next() {
			var d DatasetDTO
			var createdAt string
			if err := rows.Scan(&d.ID, &d.Name, &createdAt, &d.OwnerID, &d.RecordCount); err != nil {
				log.Printf("[datasets] scan row: %v", err)
				continue
			}
			d.CreatedAt = createdAt
			datasets = append(datasets, d)
		}
		if datasets == nil {
			datasets = []DatasetDTO{}
		}
		return c.JSON(datasets)
	}
}

// datasetCreateHandler — POST /datasets.
//
// @Summary     Create a new dataset
// @Tags        datasets
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body object{name=string} true "Dataset name"
// @Success     201 {object} DatasetDTO
// @Failure     400 {object} map[string]any "name required"
// @Router      /datasets [post]
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

		dto := DatasetDTO{
			ID: id, Name: req.Name, CreatedAt: now.Format(time.RFC3339), OwnerID: ownerID,
		}

		if cfg.DB != nil {
			cfg.DB.ExecContext(context.Background(),
				Q("INSERT INTO datasets (id, name, owner_id, created_at, updated_at) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (name) DO NOTHING"),
				id, req.Name, ownerID, now, now)
		} else {
			memDatasets.mu.Lock()
			memDatasets.data = append(memDatasets.data, dto)
			memDatasets.mu.Unlock()
		}

		return c.Status(201).JSON(dto)
	}
}

// datasetDeleteHandler — DELETE /datasets/:id. Idempotent; unknown IDs
// still return 200 {deleted:true} matching the vector-delete contract.
//
// @Summary     Delete a dataset (idempotent)
// @Tags        datasets
// @Produce     json
// @Security    BearerAuth
// @Param       id path string true "Dataset UUID"
// @Success     200 {object} map[string]bool
// @Router      /datasets/{id} [delete]
func datasetDeleteHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Params("id")
		if cfg.DB != nil {
			userID, _ := c.Locals("user_id").(string)
			if userID != "" {
				cfg.DB.ExecContext(context.Background(), Q("DELETE FROM datasets WHERE id = $1 AND (owner_id = $2 OR owner_id = '' OR owner_id IS NULL)"), id, userID)
			} else {
				cfg.DB.ExecContext(context.Background(), Q("DELETE FROM datasets WHERE id = $1"), id)
			}
		} else {
			memDatasets.mu.Lock()
			filtered := memDatasets.data[:0]
			for _, d := range memDatasets.data {
				if d.ID != id {
					filtered = append(filtered, d)
				}
			}
			memDatasets.data = filtered
			memDatasets.mu.Unlock()
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
	PipelineStatus  string `json:"pipeline_status"`
	Tags            string `json:"tags"`
	CreatedAt       string `json:"created_at"`
}

func datasetDataHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		dsID := c.Params("id")
		if cfg.DB == nil {
			return c.JSON([]DataDTO{})
		}

		rows, err := cfg.DB.QueryContext(context.Background(),
			Q(`SELECT d.id, d.name, d.extension, d.mime_type, d.raw_data_location,
			 COALESCE(d.data_size, 0), COALESCE(d.pipeline_status, '{}'), COALESCE(d.tags, '[]'), d.created_at
			 FROM data d JOIN dataset_data dd ON d.id = dd.data_id
			 WHERE dd.dataset_id = $1 ORDER BY d.created_at DESC`), dsID)
		// BL-3: same fix as datasetsListHandler — don't silently mask SQL
		// errors behind an empty array.
		if err != nil {
			log.Printf("[datasets] data query ds=%s: %v", dsID, err)
			return c.Status(fiber.StatusInternalServerError).
				JSON(fiber.Map{"detail": "load dataset data: " + err.Error()})
		}
		defer rows.Close()

		var items []DataDTO
		for rows.Next() {
			var d DataDTO
			var createdAt string
			if err := rows.Scan(&d.ID, &d.Name, &d.Extension, &d.MimeType, &d.RawDataLocation, &d.DataSize, &d.PipelineStatus, &d.Tags, &createdAt); err != nil {
				log.Printf("[datasets] data scan ds=%s: %v", dsID, err)
				continue
			}
			d.CreatedAt = createdAt
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
			cfg.DB.ExecContext(context.Background(), Q("DELETE FROM dataset_data WHERE dataset_id = $1 AND data_id = $2"), dsID, dataID)
			cfg.DB.ExecContext(context.Background(), Q("DELETE FROM data WHERE id = $1"), dataID)
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
		cfg.DB.QueryRowContext(context.Background(), Q("SELECT raw_data_location FROM data WHERE id = $1"), dataID).Scan(&location)
		if location == "" {
			return c.Status(404).JSON(fiber.Map{"detail": "not found"})
		}
		path := strings.TrimPrefix(location, "file://")
		return c.SendFile(path)
	}
}

func datasetStatusHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ready"})
	}
}
