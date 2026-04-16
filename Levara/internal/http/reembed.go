// reembed.go — Re-embedding migration endpoint.
// Reads all records from source collection, re-embeds with new model, writes to target.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stek0v/cognevra/pkg/embed"
)

type reembedRequest struct {
	SourceCollection string `json:"source_collection"`
	TargetCollection string `json:"target_collection"`
	TargetModel      string `json:"target_model"`
	TargetEndpoint   string `json:"target_endpoint"` // optional, defaults to server embed endpoint
	TargetDim        int    `json:"target_dim"`       // optional, auto-detect from first embed
	BatchSize        int    `json:"batch_size"`
	DeleteSource     bool   `json:"delete_source"`
}

type reembedStatus struct {
	mu               sync.Mutex `json:"-"`
	RunID            string     `json:"run_id"`
	Status           string     `json:"status"` // RUNNING, COMPLETED, FAILED
	SourceCollection string     `json:"source_collection"`
	TargetCollection string     `json:"target_collection"`
	TargetModel      string     `json:"target_model"`
	TotalRecords     int        `json:"total_records"`
	Processed        int        `json:"processed"`
	Failed           int        `json:"failed"`
	ElapsedMs        int64      `json:"elapsed_ms"`
	Message          string     `json:"message"`
}

// reembedStatusSnapshot is a lock-free copy of reembedStatus suitable for
// JSON serialization. Decoupling it from the live status prevents the
// copylocks warning that firing from copying reembedStatus (which holds a
// sync.Mutex).
type reembedStatusSnapshot struct {
	RunID            string `json:"run_id"`
	Status           string `json:"status"`
	SourceCollection string `json:"source_collection"`
	TargetCollection string `json:"target_collection"`
	TargetModel      string `json:"target_model"`
	TotalRecords     int    `json:"total_records"`
	Processed        int    `json:"processed"`
	Failed           int    `json:"failed"`
	ElapsedMs        int64  `json:"elapsed_ms"`
	Message          string `json:"message"`
}

// snapshot returns a copy of the status safe for JSON serialization.
func (s *reembedStatus) snapshot() reembedStatusSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return reembedStatusSnapshot{
		RunID:            s.RunID,
		Status:           s.Status,
		SourceCollection: s.SourceCollection,
		TargetCollection: s.TargetCollection,
		TargetModel:      s.TargetModel,
		TotalRecords:     s.TotalRecords,
		Processed:        s.Processed,
		Failed:           s.Failed,
		ElapsedMs:        s.ElapsedMs,
		Message:          s.Message,
	}
}

var reembedRuns sync.Map

func RegisterReembedAPI(app fiber.Router, cfg APIConfig) {
	app.Post("/reembed", reembedHandler(cfg))
	app.Get("/reembed/:runId/status", reembedStatusHandler())
}

func reembedHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req reembedRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid request"})
		}
		if req.SourceCollection == "" || req.TargetCollection == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "source_collection and target_collection required"})
		}
		if req.SourceCollection == req.TargetCollection {
			return c.Status(400).JSON(fiber.Map{"detail": "source and target must be different"})
		}
		if cfg.Collections == nil {
			return c.Status(503).JSON(fiber.Map{"detail": "collections not configured"})
		}
		if !cfg.Collections.Has(req.SourceCollection) {
			return c.Status(404).JSON(fiber.Map{"detail": fmt.Sprintf("source collection %q not found", req.SourceCollection)})
		}

		// Defaults
		endpoint := req.TargetEndpoint
		if endpoint == "" {
			endpoint = cfg.EmbedEndpoint
		}
		model := req.TargetModel
		if model == "" {
			model = cfg.EmbedModel
		}
		if endpoint == "" || model == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "target_model and embedding endpoint required"})
		}
		if req.BatchSize <= 0 {
			req.BatchSize = 50
		}

		runID := uuid.New().String()
		status := &reembedStatus{
			RunID:            runID,
			Status:           "RUNNING",
			SourceCollection: req.SourceCollection,
			TargetCollection: req.TargetCollection,
			TargetModel:      model,
		}
		reembedRuns.Store(runID, status)

		go func() {
			start := time.Now()
			ctx := context.Background()

			// 1. Read all records from source
			ids, _, metas, err := cfg.Collections.AllRecords(req.SourceCollection)
			if err != nil {
				status.mu.Lock()
				status.Status = "FAILED"
				status.Message = fmt.Sprintf("read source: %v", err)
				status.mu.Unlock()
				return
			}
			status.mu.Lock()
			status.TotalRecords = len(ids)
			status.mu.Unlock()
			if len(ids) == 0 {
				status.mu.Lock()
				status.Status = "COMPLETED"
				status.Message = "source collection is empty — nothing to re-embed"
				status.mu.Unlock()
				return
			}
			log.Printf("[reembed] %s → %s: %d records, model=%s", req.SourceCollection, req.TargetCollection, len(ids), model)

			// 2. Extract text from metadata for re-embedding
			texts := make([]string, len(ids))
			for i, meta := range metas {
				var m map[string]any
				if json.Unmarshal(meta, &m) == nil {
					// Try common text fields
					for _, key := range []string{"text", "name", "description", "content"} {
						if v, ok := m[key].(string); ok && v != "" {
							texts[i] = v
							break
						}
					}
				}
				if texts[i] == "" {
					texts[i] = string(meta) // fallback: raw metadata as text
				}
			}

			// 3. Embed in batches with new model
			embedClient := embed.NewClient(endpoint, model, req.BatchSize, 3)

			// Auto-detect dimension from first batch
			targetDim := req.TargetDim
			if targetDim <= 0 {
				testVecs, err := embedClient.EmbedTexts(ctx, texts[:min(1, len(texts))])
				if err != nil || len(testVecs) == 0 {
					status.mu.Lock()
					status.Status = "FAILED"
					status.Message = fmt.Sprintf("detect dim: %v", err)
					status.mu.Unlock()
					return
				}
				targetDim = len(testVecs[0])
				log.Printf("[reembed] auto-detected dim=%d for model %s", targetDim, model)
			}

			// 4. Create target collection with correct dim
			if err := cfg.Collections.CreateWithDim(req.TargetCollection, targetDim, model, "cosine"); err != nil {
				status.mu.Lock()
				status.Status = "FAILED"
				status.Message = fmt.Sprintf("create target: %v", err)
				status.mu.Unlock()
				return
			}

			// 5. Process in batches
			for i := 0; i < len(texts); i += req.BatchSize {
				end := i + req.BatchSize
				if end > len(texts) {
					end = len(texts)
				}

				batchTexts := texts[i:end]
				batchIDs := ids[i:end]
				batchMetas := metas[i:end]

				vecs, err := embedClient.EmbedTexts(ctx, batchTexts)
				if err != nil {
					log.Printf("[reembed] batch %d-%d embed error: %v", i, end, err)
					status.mu.Lock()
					status.Failed += len(batchTexts)
					status.mu.Unlock()
					continue
				}

				for j, vec := range vecs {
					if j < len(batchIDs) {
						metaStr := string(batchMetas[j])
						status.mu.Lock()
						if err := cfg.Collections.Insert(req.TargetCollection, batchIDs[j], vec, json.RawMessage(metaStr)); err != nil {
							status.Failed++
						} else {
							status.Processed++
						}
						status.mu.Unlock()
					}
				}

				status.mu.Lock()
				status.ElapsedMs = time.Since(start).Milliseconds()
				log.Printf("[reembed] progress: %d/%d (failed: %d)", status.Processed, status.TotalRecords, status.Failed)
				status.mu.Unlock()
			}

			// 6. Delete source if requested
			status.mu.Lock()
			failed := status.Failed
			status.mu.Unlock()
			if req.DeleteSource && failed == 0 {
				if err := cfg.Collections.Drop(req.SourceCollection); err != nil {
					log.Printf("[reembed] WARNING: drop source %s failed: %v", req.SourceCollection, err)
					status.mu.Lock()
					status.Message += fmt.Sprintf(" (warning: drop source failed: %v)", err)
					status.mu.Unlock()
				} else {
					log.Printf("[reembed] deleted source collection %s", req.SourceCollection)
				}
			}

			status.mu.Lock()
			status.Status = "COMPLETED"
			status.ElapsedMs = time.Since(start).Milliseconds()
			status.Message = fmt.Sprintf("re-embedded %d/%d records (dim %d→%d, model=%s) in %dms",
				status.Processed, status.TotalRecords,
				cfg.Collections.Dim(req.SourceCollection), targetDim, model, status.ElapsedMs)
			log.Printf("[reembed] %s", status.Message)
			status.mu.Unlock()
		}()

		return c.JSON(fiber.Map{
			"status": "ReembedStarted",
			"run_id": runID,
			"source": req.SourceCollection,
			"target": req.TargetCollection,
			"model":  model,
		})
	}
}

func reembedStatusHandler() fiber.Handler {
	return func(c *fiber.Ctx) error {
		runID := c.Params("runId")
		if val, ok := reembedRuns.Load(runID); ok {
			s := val.(*reembedStatus)
			snap := s.snapshot()
			return c.JSON(&snap)
		}
		return c.Status(404).JSON(fiber.Map{"detail": "run not found"})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
