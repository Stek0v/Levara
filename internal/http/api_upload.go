// api_upload.go — file upload + OCR endpoints, plus the cognify pipeline
// status helpers shared between cognify/search paths. Split out of api.go
// (T4). Covers:
//
//	POST /add     — multipart file upload (text, image, PDF)
//	POST /ocr     — base64 image → text via vision model
//
// PersistPipelineStatus / CheckPipelineStatus live here because addHandler
// writes pipeline_status on upload and cognifyHandler reads it via
// api_cognify.go; both import this file's symbols.
package http

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/extract"
	"github.com/stek0v/levara/pkg/fetch"
	"github.com/stek0v/levara/pkg/ingest"
)

// ── U3: File Upload (multipart) ──

func lookupUploadDatasetID(ctx context.Context, db *sql.DB, datasetName, ownerID string) string {
	if db == nil || datasetName == "" {
		return ""
	}
	var datasetID string
	if ownerID != "" {
		db.QueryRowContext(ctx,
			Q("SELECT id FROM datasets WHERE name = $1 AND owner_id = $2 LIMIT 1"), datasetName, ownerID).Scan(&datasetID)
		if datasetID != "" {
			return datasetID
		}
	}
	if ownerID != "" {
		db.QueryRowContext(ctx,
			Q("SELECT id FROM datasets WHERE name = $1 AND (owner_id = '' OR owner_id IS NULL) LIMIT 1"), datasetName).Scan(&datasetID)
		return datasetID
	}
	db.QueryRowContext(ctx,
		Q("SELECT id FROM datasets WHERE name = $1 LIMIT 1"), datasetName).Scan(&datasetID)
	return datasetID
}

func validateUploadDatasetID(ctx context.Context, db *sql.DB, datasetID, ownerID string) (bool, error) {
	return accesspkg.SQLPolicy{DB: db, Q: Q}.CanUseDatasetForUpload(ctx, datasetID, ownerID)
}

func addHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		reqCtx, cancel := apiRequestContext(c)
		defer cancel()

		datasetName := c.FormValue("datasetName")
		datasetID := c.FormValue("datasetId")
		if datasetID == "" {
			datasetID = c.FormValue("dataset_id")
		}

		form, err := c.MultipartForm()
		if err != nil {
			// Try as JSON or text body
			body := c.Body()
			if len(body) > 0 {
				bodyStr := string(body)

				// Parse JSON body for dataset_name
				var tags []string
				if c.Get("Content-Type") == "application/json" {
					var jsonBody struct {
						Data        string   `json:"data"`
						DatasetName string   `json:"dataset_name"`
						DatasetID   string   `json:"dataset_id"`
						Tags        []string `json:"tags"`
					}
					if c.BodyParser(&jsonBody) == nil {
						if jsonBody.Data != "" {
							bodyStr = jsonBody.Data
						}
						if jsonBody.DatasetName != "" {
							datasetName = jsonBody.DatasetName
						}
						if jsonBody.DatasetID != "" {
							datasetID = jsonBody.DatasetID
						}
						tags = jsonBody.Tags
					}
				}

				if datasetName == "" {
					datasetName = "default"
				}
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
				ownerID, _ := c.Locals("user_id").(string)
				if ok, err := validateUploadDatasetID(reqCtx, cfg.DB, datasetID, ownerID); err != nil {
					return c.Status(500).JSON(fiber.Map{"detail": "validate dataset: " + err.Error()})
				} else if !ok {
					return c.Status(403).JSON(fiber.Map{"detail": "dataset not accessible"})
				}
				items := []ingest.Item{{Text: bodyStr, DatasetName: datasetName, OwnerID: ownerID, Tags: tags}}
				results, err := ingest.Ingest(items, cfg.StoragePath)
				if err != nil {
					return c.Status(500).JSON(fiber.Map{"detail": err.Error()})
				}
				results, err = mirrorResultsToFileStorage(reqCtx, cfg, results)
				if err != nil {
					return c.Status(500).JSON(fiber.Map{"detail": err.Error()})
				}
				txtDsID := datasetID
				if txtDsID == "" {
					// Look up existing dataset by name before creating new
					txtDsID = lookupUploadDatasetID(reqCtx, cfg.DB, datasetName, ownerID)
					if txtDsID == "" {
						txtDsID = uuid.New().String()
					}
				}
				if cfg.DB != nil {
					mw := ingest.NewMetadataWriterFromDB(cfg.DB)
					mw.WriteMetadata(reqCtx, results, ownerID, txtDsID, datasetName)
				}
				return c.JSON(fiber.Map{"status": "ok", "items": len(results), "dataset_id": txtDsID, "dataset_name": datasetName})
			}
			return c.Status(400).JSON(fiber.Map{"detail": "no data provided"})
		}

		if datasetName == "" {
			datasetName = "default"
		}
		files := form.File["data"]
		if len(files) == 0 {
			return c.Status(400).JSON(fiber.Map{"detail": "no files uploaded"})
		}

		ownerID, _ := c.Locals("user_id").(string)
		if ok, err := validateUploadDatasetID(reqCtx, cfg.DB, datasetID, ownerID); err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "validate dataset: " + err.Error()})
		} else if !ok {
			return c.Status(403).JSON(fiber.Map{"detail": "dataset not accessible"})
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
					OwnerID:     ownerID,
				})
			} else {
				// Fallback: raw content as text
				items = append(items, ingest.Item{
					Text:        string(data),
					Filename:    file.Filename,
					DatasetName: datasetName,
					OwnerID:     ownerID,
				})
			}
		}

		results, err := ingest.Ingest(items, cfg.StoragePath)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": err.Error()})
		}
		results, err = mirrorResultsToFileStorage(reqCtx, cfg, results)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": err.Error()})
		}

		// Write metadata — reuse existing dataset by name
		dsID := datasetID
		if dsID == "" {
			dsID = lookupUploadDatasetID(reqCtx, cfg.DB, datasetName, ownerID)
			if dsID == "" {
				dsID = uuid.New().String()
			}
		}
		if cfg.DB != nil {
			ownerID, _ := c.Locals("user_id").(string)
			mw := ingest.NewMetadataWriterFromDB(cfg.DB)
			mw.WriteMetadata(reqCtx, results, ownerID, dsID, datasetName)
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
//
// Background run status lives in pkg/runreg. F-4 wave 3j-prep moved the
// package-level sync.Map + pipelineRunStatus type into that package so the
// cognify tools in pkg/mcp can share it without importing internal/http.

// PersistPipelineStatus writes pipeline completion status to the data table.
// Called after cognify finishes (success or failure) to enable skip-if-done.
// statusJSON format: {"<collection>": {"status": "COMPLETED", "chunks": N, ...}}
func PersistPipelineStatus(db *sql.DB, datasetID, collection, status string, chunks, entities, edges int, elapsedMs int64) {
	if db == nil || datasetID == "" {
		return
	}
	statusObj := map[string]any{
		"status":   status,
		"chunks":   chunks,
		"entities": entities,
		"edges":    edges,
		"elapsed":  elapsedMs,
		"at":       time.Now().UTC().Format(time.RFC3339),
	}
	statusJSON, _ := json.Marshal(map[string]any{collection: statusObj})

	// Update all data items in this dataset
	_, err := db.ExecContext(context.Background(), Q(`
		UPDATE data SET pipeline_status = $1, updated_at = NOW()
		WHERE id IN (SELECT data_id FROM dataset_data WHERE dataset_id = $2)
	`), string(statusJSON), datasetID)
	if err != nil {
		log.Printf("[pipeline] persist status error: %v", err)
	}
}

// CheckPipelineStatus returns true if ALL data items in dataset are already processed for this collection.
// Returns false if any item has empty pipeline_status or missing collection entry.
func CheckPipelineStatus(db *sql.DB, datasetID, collection string) bool {
	if db == nil || datasetID == "" {
		return false
	}
	// Count items with empty/missing pipeline_status for this collection
	var unprocessed int
	err := db.QueryRowContext(context.Background(), Q(`
		SELECT COUNT(*) FROM data
		WHERE id IN (SELECT data_id FROM dataset_data WHERE dataset_id = $1)
		AND (pipeline_status = '{}' OR pipeline_status = '' OR pipeline_status IS NULL)
	`), datasetID).Scan(&unprocessed)
	if err != nil || unprocessed > 0 {
		return false // has unprocessed items
	}
	// Check first processed item for this specific collection
	var statusStr string
	err = db.QueryRowContext(context.Background(), Q(`
		SELECT pipeline_status FROM data
		WHERE id IN (SELECT data_id FROM dataset_data WHERE dataset_id = $1)
		AND pipeline_status != '{}' LIMIT 1
	`), datasetID).Scan(&statusStr)
	if err != nil || statusStr == "" {
		return false
	}
	var statuses map[string]map[string]any
	if json.Unmarshal([]byte(statusStr), &statuses) != nil {
		return false
	}
	collStatus, ok := statuses[collection]
	if !ok {
		return false
	}
	return collStatus["status"] == "COMPLETED"
}

func ocrHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Accept JSON with base64 image or multipart file
		var imgData []byte
		var filename string

		form, err := c.MultipartForm()
		if err == nil {
			files := form.File["image"]
			if len(files) == 0 {
				files = form.File["data"]
			}
			if len(files) > 0 {
				f, err := files[0].Open()
				if err == nil {
					imgData, _ = io.ReadAll(f)
					f.Close()
					filename = files[0].Filename
				}
			}
		}

		if len(imgData) == 0 {
			var req struct {
				Image    string `json:"image"` // base64
				Filename string `json:"filename"`
			}
			if c.BodyParser(&req) == nil && req.Image != "" {
				decoded, decErr := base64Decode(req.Image)
				if decErr != nil {
					return c.Status(400).JSON(fiber.Map{"detail": "invalid base64 image"})
				}
				imgData = decoded
				filename = req.Filename
			}
		}

		if len(imgData) == 0 {
			return c.Status(400).JSON(fiber.Map{"detail": "no image provided (use 'image' field with base64 or multipart 'image'/'data')"})
		}

		result, err := extract.Extract(imgData, filename, "")
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": err.Error()})
		}

		return c.JSON(fiber.Map{
			"text":       result.Text,
			"format":     result.Format,
			"extract_ms": result.ExtractMs,
		})
	}
}

func base64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}
