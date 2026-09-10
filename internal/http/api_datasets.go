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
	TotalSize   int64   `json:"total_size"`
	GitHubRepo  string  `json:"github_repo"`
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
		if !accesspkg.APIKeyAllows(workspaceActorFromFiber(c).APIKeyPermissions, accesspkg.ActionRead) {
			return fiber.NewError(403, "dataset access denied")
		}
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
			count, size, err := documentDatasetStats(ctx, c, cfg, d.ID)
			if err != nil {
				return documentHTTPError(err)
			}
			datasets = append(datasets, DatasetDTO{
				ID:          d.ID,
				Name:        d.Name,
				CreatedAt:   d.CreatedAt,
				OwnerID:     d.OwnerID,
				RecordCount: count,
				TotalSize:   size,
				GitHubRepo:  d.GitHubRepo,
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
		cleanupPending := false
		if cfg.DB != nil {
			if err := documentSQLPolicy(cfg).DeleteDatasetWithDocuments(ctx, workspaceActorFromFiber(c), id); err != nil {
				if !errors.Is(err, accesspkg.ErrDocumentNotFound) {
					return documentHTTPError(err)
				}
			}
			cleanupPending = cleanupRetiredStructuredArtifacts(ctx, cfg, "")
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
		return c.JSON(fiber.Map{"deleted": true, "artifact_cleanup_pending": cleanupPending})
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
	SourceRevision  int64  `json:"source_revision"`
	RawContentHash  string `json:"raw_content_hash"`
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
			 COALESCE(d.data_size, 0), COALESCE(d.pipeline_status, '{}'), COALESCE(d.tags, '[]'),
			 d.source_revision,LOWER(d.raw_content_hash),d.created_at
			 FROM data d JOIN dataset_data dd ON d.id = dd.data_id
			 WHERE dd.dataset_id = $1 ORDER BY d.created_at DESC`), dsID)
		// BL-3: same fix as datasetsListHandler — don't silently mask SQL
		// errors behind an empty array.
		if err != nil {
			log.Printf("[datasets] data query ds=%s: %v", dsID, err)
			return c.Status(fiber.StatusInternalServerError).
				JSON(fiber.Map{"detail": "load dataset data: " + err.Error()})
		}
		var items []DataDTO
		for rows.Next() {
			var d DataDTO
			var createdAt string
			if err := rows.Scan(&d.ID, &d.Name, &d.Extension, &d.MimeType, &d.RawDataLocation, &d.DataSize, &d.PipelineStatus, &d.Tags, &d.SourceRevision, &d.RawContentHash, &createdAt); err != nil {
				rows.Close()
				return documentHTTPError(err)
			}
			d.CreatedAt = createdAt
			items = append(items, d)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return documentHTTPError(err)
		}
		for i := range items {
			items[i].PipelineStatus, err = pipelineStatusForDocument(ctx, cfg.DB, dsID, items[i].ID, items[i].PipelineStatus)
			if err != nil {
				return documentHTTPError(err)
			}
		}
		filtered := make([]DataDTO, 0, len(items))
		for _, d := range items {
			ref := accesspkg.DocumentRef{DatasetID: dsID, DataID: d.ID}
			decision, err := documentSQLPolicy(cfg).AuthorizeDocument(ctx, workspaceActorFromFiber(c), ref, accesspkg.ActionRead)
			if err != nil {
				return documentHTTPError(err)
			}
			if decision.Allowed {
				d.RawDataLocation = documentRawProxy(c, ref)
				filtered = append(filtered, d)
			}
		}
		return c.JSON(filtered)
	}
}

func datasetDataDeleteHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()

		cleanupPending := false
		if cfg.DB != nil {
			ref := documentRefFromFiber(c)
			r, err := documentRegistration(c, cfg, ref)
			if err != nil {
				return err
			}
			var acl, content int64
			if r != nil {
				if err := authorizeDocumentFiber(c, cfg, ref, accesspkg.ActionDelete); err != nil {
					return err
				}
				acl, content, err = documentExpectedVersion(c)
				if err != nil {
					return err
				}
			}
			if err := documentSQLPolicy(cfg).DeleteDocumentAssociation(ctx, workspaceActorFromFiber(c), ref, acl, content); err != nil {
				return documentHTTPError(err)
			}
			cleanupPending = cleanupRetiredStructuredArtifacts(ctx, cfg, ref.DataID)
		}
		return c.JSON(fiber.Map{"deleted": true, "artifact_cleanup_pending": cleanupPending})
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
		if err := authorizeDocumentFiber(c, cfg, documentRefFromFiber(c), accesspkg.ActionRead); err != nil {
			return err
		}

		ctx, accessErr := documentReadContext(c, cfg, ctx, documentRefFromFiber(c))
		if accessErr != nil {
			return accessErr
		}

		var location, originalLocation string
		err := cfg.DB.QueryRowContext(ctx, Q(`SELECT d.raw_data_location, COALESCE(d.original_data_location, '') FROM data d
			JOIN dataset_data dd ON dd.data_id = d.id
			WHERE d.id = $1 AND dd.dataset_id = $2`), dataID, datasetID).Scan(&location, &originalLocation)
		if c.QueryBool("original") {
			location = originalLocation
		}
		if errors.Is(err, sql.ErrNoRows) {
			return c.Status(404).JSON(fiber.Map{"detail": "not found"})
		}
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "load raw data failed"})
		}
		if location == "" {
			return c.Status(404).JSON(fiber.Map{"detail": "not found"})
		}
		raw, err := loadRawDataByLocation(ctx, cfg, location)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) || os.IsNotExist(err) {
				return c.Status(404).JSON(fiber.Map{"detail": "not found"})
			}
			return c.Status(500).JSON(fiber.Map{"detail": "load raw data failed"})
		}
		if err := authorizeDocumentFiber(c, cfg, documentRefFromFiber(c), accesspkg.ActionRead); err != nil {
			return err
		}
		r, err := documentRegistration(c, cfg, documentRefFromFiber(c))
		if err != nil {
			return err
		}
		if r != nil {
			c.Set("ETag", documentETag(*r))
		}
		c.Set("Cache-Control", "private, no-store")
		// Download semantics (finding L7, 2026-09-03 review): opaque
		// octet-stream plus no-sniff prevents browser content sniffing of
		// attacker-supplied bytes.
		c.Set("Content-Type", "application/octet-stream")
		c.Set("X-Content-Type-Options", "nosniff")
		if err := c.Send(raw); err != nil {
			return err
		}
		return sendProtectedResponseWithFence(c, ctx)
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
		if err := authorizeDocumentFiber(c, cfg, documentRefFromFiber(c), accesspkg.ActionRead); err != nil {
			return err
		}

		var location string
		err := cfg.DB.QueryRowContext(ctx, Q(`SELECT d.raw_data_location FROM data d
			JOIN dataset_data dd ON dd.data_id = d.id
			WHERE d.id = $1 AND dd.dataset_id = $2`), dataID, datasetID).Scan(&location)
		if errors.Is(err, sql.ErrNoRows) {
			return c.Status(404).JSON(fiber.Map{"detail": "not found"})
		}
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "load raw data failed"})
		}
		if location == "" {
			return c.Status(404).JSON(fiber.Map{"detail": "not found"})
		}
		r, err := documentRegistration(c, cfg, documentRefFromFiber(c))
		if err != nil {
			return err
		}
		if r != nil {
			// This response mints no storage capability that could bypass the
			// proxy's authorization checks on a later download request.
			c.Set("ETag", documentETag(*r))
			c.Set("Cache-Control", "private, no-store")
			return c.JSON(fiber.Map{"url": documentRawProxy(c, documentRefFromFiber(c)), "expires_in": 0, "location": "", "presigned": false})
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
