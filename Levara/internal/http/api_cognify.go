// api_cognify.go — Cognify pipeline trigger + status + SSE stream, split
// out of api.go (T4). Covers:
//
//   POST /cognify
//   GET  /cognify/:runId/status
//   GET  /cognify/:runId/stream
package http

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/stek0v/cognevra/internal/metrics"
	"github.com/stek0v/cognevra/pkg/orchestrator"
	"github.com/stek0v/cognevra/pkg/runreg"
)

func cognifyHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req struct {
			Datasets        []string `json:"datasets"`
			DatasetIds      []string `json:"datasetIds"` // Cognee frontend format
			Texts           []string `json:"texts"`
			LLMModel        string   `json:"llm_model"`
			Collection      string   `json:"collection"`
			RunInBackground bool     `json:"runInBackground"`
			SessionID       string   `json:"session_id"`
		}
		c.BodyParser(&req)

		// Merge datasets and datasetIds
		allDatasetIDs := append(req.Datasets, req.DatasetIds...)

		// Collect texts: either from request body or from dataset files
		var texts []string
		if len(req.Texts) > 0 {
			texts = req.Texts
		} else if cfg.DB != nil && len(allDatasetIDs) > 0 {
			for _, dsID := range allDatasetIDs {
				rows, err := cfg.DB.QueryContext(context.Background(),
					Q(`SELECT d.raw_data_location FROM data d
					 JOIN dataset_data dd ON d.id = dd.data_id
					 WHERE dd.dataset_id = $1`), dsID)
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
					rows, err := cfg.DB.QueryContext(context.Background(),
						Q(`SELECT d.name FROM data d
						 JOIN dataset_data dd ON d.id = dd.data_id
						 WHERE dd.dataset_id = $1`), dsID)
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

		// Skip if already processed (check pipeline_status in data table)
		if len(allDatasetIDs) > 0 && CheckPipelineStatus(cfg.DB, allDatasetIDs[0], collection) {
			return c.JSON(fiber.Map{
				"status":  "already_processed",
				"message": fmt.Sprintf("Dataset already cognified for collection %q. Delete pipeline_status to re-process.", collection),
			})
		}

		runStatus := &runreg.Status{
			RunID:     runID,
			Status:    "RUNNING",
			Stage:     "starting",
			StartedAt: time.Now(),
		}
		cfg.Runs.Store(runID, runStatus)

		// P2.1: Load session context if session_id provided
		var sessionContext string
		if req.SessionID != "" && cfg.DB != nil {
			sessionContext = GetSessionContext(cfg.DB, context.Background(), req.SessionID, 5)
		}
		userID, _ := c.Locals("user_id").(string)

		// Build orchestrator config from server config + request overrides
		pipeCfg := orchestrator.Config{
			ChunkStrategy:       "merged",
			MinChunkChars:       50,
			MaxChunkChars:       2000,
			LLMEndpoint:         os.Getenv("LLM_ENDPOINT"),
			LLMModel:            os.Getenv("LLM_MODEL"),
			LLMConcurrency:      1,
			EmbedEndpoint:       cfg.EmbedEndpoint,
			EmbedModel:          cfg.EmbedModel,
			Neo4jURL:            cfg.Neo4jCfg.Neo4jURL,
			Neo4jUser:           cfg.Neo4jCfg.Neo4jUser,
			Neo4jPassword:       cfg.Neo4jCfg.Neo4jPassword,
			Neo4jDatabase:       cfg.Neo4jCfg.Neo4jDatabase,
			Collection:          collection,
			Collections:         cfg.Collections,
			BM25Indexes:         cfg.BM25Indexes,
			GenerateTriplets:    true,
			SystemPrompt:        sessionContext,
			DatasetID: func() string {
				if len(allDatasetIDs) > 0 {
					return allDatasetIDs[0]
				}
				return runID
			}(),
			DB:                  cfg.DB,
			LLMCache:            cfg.LLMCache,
			LLMProvider:         cfg.LLMProvider,
			UseStructuredOutput: func() *bool { b := true; return &b }(),
		}
		if req.LLMModel != "" {
			pipeCfg.LLMModel = req.LLMModel
		}

		// Capture for background goroutine
		sessionID := req.SessionID

		// Run pipeline in background. Both goroutines guard against panic (T15):
		// a panic in orchestrator.Run (inner) is forwarded to errCh via
		// runWithPanicGuard so the outer goroutine can mark the run FAILED and
		// persist state — otherwise the run stays in RUNNING forever. The outer
		// goroutine also recovers against panics in the progress loop or
		// persistence path.
		go func() {
			progressCh := make(chan orchestrator.Progress, 100)
			errCh := make(chan error, 1)

			defer func() {
				if r := recover(); r != nil {
					metrics.CognifyPanics.WithLabelValues(runStatus.Stage).Inc()
					stack := debug.Stack()
					log.Printf("cognify outer goroutine panic run_id=%s stage=%s panic=%v\n%s",
						runID, runStatus.Stage, r, stack)
					runStatus.Status = "FAILED"
					runStatus.Message = fmt.Sprintf("panic: %v", r)
					runStatus.ElapsedMs = time.Since(runStatus.StartedAt).Milliseconds()
					// Best-effort persistence; swallow further panics to avoid crash loops.
					func() {
						defer func() { _ = recover() }()
						PersistPipelineStatus(cfg.DB, pipeCfg.DatasetID, collection,
							runStatus.Status, runStatus.Chunks, runStatus.Entities, runStatus.Edges, runStatus.ElapsedMs)
					}()
				}
			}()

			go func() {
				errCh <- runWithPanicGuard(runID, func() string { return runStatus.Stage }, func() error {
					return orchestrator.Run(context.Background(), texts, pipeCfg, progressCh)
				})
			}()

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

			// Persist pipeline status to data table
			PersistPipelineStatus(cfg.DB, pipeCfg.DatasetID, collection,
				runStatus.Status, runStatus.Chunks, runStatus.Entities, runStatus.Edges, runStatus.ElapsedMs)

			recordInteraction(cfg, sessionID, userID, strings.Join(texts, " "),
				fmt.Sprintf("%d entities extracted", runStatus.Entities), "cognify")
		}()

		return c.JSON(fiber.Map{
			"status":          "PipelineRunStarted",
			"pipeline_run_id": runID,
		})
	}
}

func cognifyStatusHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		runID := c.Params("runId")
		if val, ok := cfg.Runs.Load(runID); ok {
			return c.JSON(val)
		}
		return c.Status(404).JSON(fiber.Map{"detail": "run not found"})
	}
}

// cognifyStreamHandler streams pipeline progress via Server-Sent Events (SSE).
// GET /cognify/:runId/stream
// React frontend: const es = new EventSource("/api/v1/cognify/{runId}/stream")
func cognifyStreamHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		runID := c.Params("runId")
		if _, ok := cfg.Runs.Load(runID); !ok {
			return c.Status(404).JSON(fiber.Map{"detail": "run not found"})
		}

		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache")
		c.Set("Connection", "keep-alive")
		c.Set("X-Accel-Buffering", "no")

		c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
			lastStage := ""
			for {
				status, ok := cfg.Runs.Load(runID)
				if !ok {
					fmt.Fprintf(w, "event: error\ndata: {\"error\":\"run not found\"}\n\n")
					w.Flush()
					return
				}

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
