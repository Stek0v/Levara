// api_datasets.go — Dataset CRUD + data listing/deletion endpoints, split
// out of api.go (T4). Covers:
//
//	GET    /datasets
//	POST   /datasets
//	DELETE /datasets/:id
//	GET    /datasets/:id/data
//	DELETE /datasets/:id/data/:dataId
//	GET    /datasets/:id/data/:dataId/raw
//	GET    /datasets/status
package http

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	accesspkg "github.com/stek0v/levara/pkg/access"
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

func authorizeDatasetFiber(c *fiber.Ctx, cfg APIConfig, datasetID, action string) error {
	decision, err := (accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}).Authorize(
		c.UserContext(),
		workspaceActorFromFiber(c),
		accesspkg.Resource{Kind: accesspkg.ResourceDataset, ID: datasetID},
		action,
	)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "dataset access check failed")
	}
	if !decision.Allowed {
		return fiber.NewError(fiber.StatusForbidden, "dataset access denied")
	}
	return nil
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
		ctx, cancel := apiRequestContext(c)
		defer cancel()

		if cfg.DB == nil {
			memDatasets.mu.Lock()
			ds := make([]DatasetDTO, len(memDatasets.data))
			copy(ds, memDatasets.data)
			memDatasets.mu.Unlock()
			return c.JSON(ds)
		}

		userID, _ := c.Locals("user_id").(string)
		visible, err := accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}.ListVisibleDatasets(ctx, userID)
		// BL-3: surface SQL errors instead of returning []. Silent empty
		// responses made bad credentials / missing tables indistinguishable
		// from an empty dataset list — the WebUI would render "no datasets"
		// forever instead of letting the user retry.
		if err != nil {
			log.Printf("[datasets] list query: %v", err)
			return c.Status(fiber.StatusInternalServerError).
				JSON(fiber.Map{"detail": "list datasets: " + err.Error()})
		}

		datasets := make([]DatasetDTO, 0, len(visible))
		for _, d := range visible {
			datasets = append(datasets, DatasetDTO{
				ID:          d.ID,
				Name:        d.Name,
				CreatedAt:   d.CreatedAt,
				OwnerID:     d.OwnerID,
				RecordCount: d.RecordCount,
			})
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
		ctx, cancel := apiRequestContext(c)
		defer cancel()

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
			result, err := cfg.DB.ExecContext(ctx,
				Q("INSERT INTO datasets (id, name, owner_id, created_at, updated_at) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (name) DO NOTHING"),
				id, req.Name, ownerID, now, now)
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "create dataset failed"})
			}
			if inserted, err := result.RowsAffected(); err == nil && inserted == 0 {
				return c.Status(fiber.StatusConflict).JSON(fiber.Map{"detail": "dataset name already exists"})
			}
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
		ctx, cancel := apiRequestContext(c)
		defer cancel()

		id := c.Params("id")
		if cfg.DB != nil {
			if err := authorizeDatasetFiber(c, cfg, id, accesspkg.ActionWrite); err != nil {
				return err
			}
			if _, err := cfg.DB.ExecContext(ctx, Q("DELETE FROM datasets WHERE id = $1"), id); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "delete dataset failed"})
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
		ctx, cancel := apiRequestContext(c)
		defer cancel()

		dsID := c.Params("id")
		if cfg.DB == nil {
			return c.JSON([]DataDTO{})
		}
		if err := authorizeDatasetFiber(c, cfg, dsID, accesspkg.ActionRead); err != nil {
			return err
		}

		rows, err := cfg.DB.QueryContext(ctx,
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
		ctx, cancel := apiRequestContext(c)
		defer cancel()

		dataID := c.Params("dataId")
		dsID := c.Params("id")
		if cfg.DB != nil {
			if err := authorizeDatasetFiber(c, cfg, dsID, accesspkg.ActionWrite); err != nil {
				return err
			}
			tx, err := cfg.DB.BeginTx(ctx, nil)
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "delete data failed"})
			}
			defer tx.Rollback()
			result, err := tx.ExecContext(ctx, Q("DELETE FROM dataset_data WHERE dataset_id = $1 AND data_id = $2"), dsID, dataID)
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "delete data failed"})
			}
			if deleted, err := result.RowsAffected(); err == nil && deleted == 0 {
				return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"detail": "not found"})
			}
			query, args := QArgs(`DELETE FROM data WHERE id = $1
				AND NOT EXISTS (SELECT 1 FROM dataset_data WHERE data_id = $1)`, dataID)
			if _, err := tx.ExecContext(ctx, query, args...); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "delete data failed"})
			}
			if err := tx.Commit(); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "delete data failed"})
			}
		}
		return c.JSON(fiber.Map{"deleted": true})
	}
}

func datasetDataRawHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()

		dataID := c.Params("dataId")
		datasetID := c.Params("id")
		if cfg.DB == nil {
			return c.Status(404).JSON(fiber.Map{"detail": "not found"})
		}
		if err := authorizeDatasetFiber(c, cfg, datasetID, accesspkg.ActionRead); err != nil {
			return err
		}

		var location string
		err := cfg.DB.QueryRowContext(ctx, Q(`SELECT d.raw_data_location FROM data d
			JOIN dataset_data dd ON dd.data_id = d.id
			WHERE d.id = $1 AND dd.dataset_id = $2`), dataID, datasetID).Scan(&location)
		if errors.Is(err, sql.ErrNoRows) || location == "" {
			return c.Status(404).JSON(fiber.Map{"detail": "not found"})
		}
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "load raw data failed"})
		}
		raw, err := loadRawDataByLocation(ctx, cfg, location)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) || os.IsNotExist(err) {
				return c.Status(404).JSON(fiber.Map{"detail": "not found"})
			}
			return c.Status(500).JSON(fiber.Map{"detail": "load raw data: " + err.Error()})
		}
		return c.Send(raw)
	}
}

func datasetDataRawURLHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()

		dataID := c.Params("dataId")
		datasetID := c.Params("id")
		if cfg.DB == nil {
			return c.Status(404).JSON(fiber.Map{"detail": "not found"})
		}
		if err := authorizeDatasetFiber(c, cfg, datasetID, accesspkg.ActionRead); err != nil {
			return err
		}

		var location string
		err := cfg.DB.QueryRowContext(ctx, Q(`SELECT d.raw_data_location FROM data d
			JOIN dataset_data dd ON dd.data_id = d.id
			WHERE d.id = $1 AND dd.dataset_id = $2`), dataID, datasetID).Scan(&location)
		if errors.Is(err, sql.ErrNoRows) || location == "" {
			return c.Status(404).JSON(fiber.Map{"detail": "not found"})
		}
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "load raw data failed"})
		}

		ttlSec := c.QueryInt("ttl_seconds", 900)
		if ttlSec <= 0 {
			ttlSec = 900
		}
		if ttlSec > 7*24*60*60 {
			ttlSec = 7 * 24 * 60 * 60
		}
		ttl := time.Duration(ttlSec) * time.Second

		url, presigned, err := presignRawLocation(ctx, cfg, location, ttl)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "presign raw data: " + err.Error()})
		}
		if presigned {
			return c.JSON(fiber.Map{
				"url":        url,
				"expires_in": ttlSec,
				"location":   location,
				"presigned":  true,
			})
		}

		// Fallback for local backends: return API URL that proxies bytes.
		proxyURL := fmt.Sprintf("%s/api/v1/datasets/%s/data/%s/raw", c.BaseURL(), datasetID, dataID)
		return c.JSON(fiber.Map{
			"url":        proxyURL,
			"expires_in": 0,
			"location":   location,
			"presigned":  false,
		})
	}
}

func datasetStatusHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ready"})
	}
}
