// api_cognify.go — Cognify pipeline trigger + status + SSE stream, split
// out of api.go (T4). Covers:
//
//	POST /cognify
//	GET  /cognify/:runId/status
//	GET  /cognify/:runId/stream
package http

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/stek0v/levara/internal/metrics"
	"github.com/stek0v/levara/pkg/orchestrator"
	"github.com/stek0v/levara/pkg/runreg"
)

// ensureCognifyDataset get-or-creates a per-(owner,collection) dataset owned
// by the caller and returns its id, so chunks/graph rows this run stamps are
// reachable through search's RBAC gate (filterByAllowedDatasets keeps a hit
// only when its dataset_id is "" or in the caller's allowed-set). An
// unregistered ephemeral runID belongs to neither, so without this every
// cognified chunk is silently dropped on read-back. Mirrors the MCP-surface
// helper ensureCognifyDatasetID (pkg/mcp/tool_cognify.go). Best-effort: any
// DB error falls back to the ephemeral id unchanged.
func ensureCognifyDataset(ctx context.Context, db *sql.DB, owner, collection, fallbackID string) string {
	name := fmt.Sprintf("__cognify__:%s:%s", owner, collection)

	var existing string
	if err := db.QueryRowContext(ctx, Q(`SELECT id FROM datasets WHERE name = $1`), name).Scan(&existing); err == nil && existing != "" {
		return existing
	}

	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx,
		Q(`INSERT INTO datasets (id, name, owner_id, created_at, updated_at)
		   VALUES ($1, $2, $3, $4, $5) ON CONFLICT (name) DO NOTHING`),
		fallbackID, name, owner, now, now); err != nil {
		return fallbackID
	}
	var resolved string
	if err := db.QueryRowContext(ctx, Q(`SELECT id FROM datasets WHERE name = $1`), name).Scan(&resolved); err == nil && resolved != "" {
		return resolved
	}
	return fallbackID
}

// cognifyHandler — POST /cognify. Kicks off an async pipeline and returns
// a run ID immediately; progress is available via /cognify/:id/status
// (polling) or /cognify/:id/stream (SSE).
//
// @Summary     Start a cognify pipeline run
// @Description Transforms text into chunks + embeddings + (optional) graph. Body may provide inline texts[] or reference datasets[] whose raw files are loaded from disk. rag mode skips graph extraction.
// @Tags        cognify
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body object true "datasets | datasetIds | texts, optional llm_model, collection, session_id"
// @Success     200 {object} map[string]string "status + pipeline_run_id"
// @Failure     400 {object} map[string]any "no texts to cognify"
// @Router      /cognify [post]
func cognifyHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		reqCtx, cancel := apiRequestContext(c)
		defer cancel()

		var req struct {
			Datasets        []string `json:"datasets"`
			DatasetIds      []string `json:"datasetIds"` // Levara frontend format
			Texts           []string `json:"texts"`
			LLMModel        string   `json:"llm_model"`
			Collection      string   `json:"collection"`
			RunInBackground bool     `json:"runInBackground"`
			SessionID       string   `json:"session_id"`
			// SkipGraph enables RAG-mode ingest: chunk → embed → HNSW only,
			// no LLM entity extraction, no graph writes. Surfaces the
			// orchestrator's existing SkipGraph flag (pipeline.go:93) so
			// callers can opt into the fastest deterministic ingest path.
			SkipGraph bool `json:"skip_graph"`
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
				rows, err := cfg.DB.QueryContext(reqCtx,
					Q(`SELECT d.raw_data_location FROM data d
					 JOIN dataset_data dd ON d.id = dd.data_id
					 WHERE dd.dataset_id = $1`), dsID)
				if err != nil {
					continue
				}
				for rows.Next() {
					var loc string
					rows.Scan(&loc)
					if data, err := loadRawDataByLocation(reqCtx, cfg, loc); err == nil {
						texts = append(texts, string(data))
					}
				}
				rows.Close()
			}
			// If no files found, check if data was stored as inline text (ingest stores to disk)
			if len(texts) == 0 {
				for _, dsID := range allDatasetIDs {
					rows, err := cfg.DB.QueryContext(reqCtx,
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
			sessionContext = GetSessionContext(cfg.DB, reqCtx, req.SessionID, 5)
		}
		userID, _ := c.Locals("user_id").(string)

		// Resolve the dataset id chunks/graph rows are stamped with. An
		// explicit dataset wins; otherwise register a per-(owner,collection)
		// cognify dataset owned by the caller so search's RBAC gate
		// (filterByAllowedDatasets) does not silently drop every chunk this
		// run produces. A bare ephemeral runID is in no caller's allowed-set.
		effectiveDatasetID := runID
		if len(allDatasetIDs) > 0 {
			effectiveDatasetID = allDatasetIDs[0]
		} else if cfg.DB != nil {
			effectiveDatasetID = ensureCognifyDataset(reqCtx, cfg.DB, userID, collection, runID)
		}

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
			EmbedClient:         cfg.EmbedClient, // T3 follow-up: reuse shared TCP pool through the pipeline
			Neo4jURL:            cfg.Neo4jCfg.Neo4jURL,
			Neo4jUser:           cfg.Neo4jCfg.Neo4jUser,
			Neo4jPassword:       cfg.Neo4jCfg.Neo4jPassword,
			Neo4jDatabase:       cfg.Neo4jCfg.Neo4jDatabase,
			Collection:          collection,
			Collections:         cfg.Collections,
			BM25Indexes:         cfg.BM25Indexes,
			BM25Store:           cfg.BM25Store,
			GenerateTriplets:    !req.SkipGraph,
			SkipGraph:           req.SkipGraph,
			SystemPrompt:        sessionContext,
			DatasetID:           effectiveDatasetID,
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
		//
		// stageSnapshot carries the most-recent stage value across the
		// goroutine boundary so panic-recover can read it without racing with
		// the progress loop that updates runStatus.Stage (C2 from the 2d15b38
		// review). The progress loop stores to both the snapshot AND the
		// runStatus field; SSE reader continues to read runStatus directly and
		// is left as a pre-existing tolerated race (tracked separately).
		var stageSnapshot atomic.Pointer[string]
		start := "starting"
		stageSnapshot.Store(&start)
		readStage := func() string {
			if p := stageSnapshot.Load(); p != nil {
				return *p
			}
			return ""
		}

		go func() {
			// Bound the detached pipeline so a stuck downstream (Neo4j, LLM,
			// embed) cannot keep the goroutine alive forever. Tunable via
			// BACKGROUND_TASK_TIMEOUT_MS; default 30 minutes.
			bgCtx, bgCancel := backgroundTaskContext()
			defer bgCancel()

			progressCh := make(chan orchestrator.Progress, 100)
			errCh := make(chan error, 1)

			defer func() {
				if r := recover(); r != nil {
					stage := readStage()
					metrics.CognifyPanics.WithLabelValues(stage).Inc()
					stack := debug.Stack()
					log.Printf("cognify outer goroutine panic run_id=%s stage=%s panic=%v\n%s",
						runID, stage, r, stack)
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
				// Inner panic recover reads stage via the atomic snapshot so
				// the closure is safe to invoke from a panic unwinding while
				// the outer goroutine may still be mutating runStatus.Stage.
				errCh <- runWithPanicGuard(runID, readStage, func() error {
					return orchestrator.Run(bgCtx, texts, pipeCfg, progressCh)
				})
			}()

			for p := range progressCh {
				stage := p.Stage
				stageSnapshot.Store(&stage)
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
			if runStatus.Status == "COMPLETED" && !pipeCfg.SkipGraph {
				rebuildVSAMemory(bgCtx, cfg, pipeCfg.DatasetID, "cognify")
			}

			recordInteraction(bgCtx, cfg, sessionID, userID, strings.Join(texts, " "),
				fmt.Sprintf("%d entities extracted", runStatus.Entities), "cognify")
		}()

		return c.JSON(fiber.Map{
			"status":          "PipelineRunStarted",
			"pipeline_run_id": runID,
		})
	}
}

// cognifyStatusHandler — GET /cognify/:runId/status (one-shot poll).
//
// @Summary     Poll the status of a cognify run
// @Tags        cognify
// @Produce     json
// @Security    BearerAuth
// @Param       runId path string true "Run ID returned by POST /cognify"
// @Success     200 {object} runreg.Status
// @Failure     404 {object} map[string]any "run not found"
// @Router      /cognify/{runId}/status [get]
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
// cognifyStreamHandler — GET /cognify/:runId/stream (SSE).
//
// @Summary     Stream cognify progress via Server-Sent Events
// @Description Emits event:progress updates every 500ms while the run is RUNNING, then event:done with the terminal payload. Client should disconnect after the done event.
// @Tags        cognify
// @Produce     text/event-stream
// @Security    BearerAuth
// @Param       runId path string true "Run ID returned by POST /cognify"
// @Success     200 {string} string "SSE stream"
// @Failure     404 {object} map[string]any "run not found"
// @Router      /cognify/{runId}/stream [get]
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

		// Capture the request context so we can notice client disconnects
		// inside the body-stream writer goroutine. fasthttp's RequestCtx
		// satisfies context.Context — Done() closes when the underlying
		// connection drops, and Err() returns non-nil at the same moment.
		// Without this check the goroutine would keep running until the
		// pipeline itself finished (1–5 min per cognify benchmark), leaking
		// one goroutine + bufio.Writer per disconnected client (C3).
		reqCtx := c.Context()

		c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
			lastStage := ""
			// flushOrExit writes its argument, flushes, and returns true if
			// the client went away (so the caller returns immediately).
			flushOrExit := func() bool {
				if err := w.Flush(); err != nil {
					return true
				}
				return reqCtx.Err() != nil
			}
			for {
				// Early exit if the client has disconnected.
				if reqCtx.Err() != nil {
					return
				}
				status, ok := cfg.Runs.Load(runID)
				if !ok {
					fmt.Fprintf(w, "event: error\ndata: {\"error\":\"run not found\"}\n\n")
					_ = w.Flush()
					return
				}

				// Send update if stage changed or terminal
				if status.Stage != lastStage || status.Status != "RUNNING" {
					data, _ := json.Marshal(status)
					fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data)
					if flushOrExit() {
						return
					}
					lastStage = status.Stage
				}

				if status.Status != "RUNNING" {
					fmt.Fprintf(w, "event: done\ndata: %s\n\n", func() string { d, _ := json.Marshal(status); return string(d) }())
					_ = w.Flush()
					return
				}

				// time.Sleep blocks for the full 500ms even if the client
				// disconnects mid-sleep. That's acceptable — one extra
				// iteration of an unused goroutine at worst.
				time.Sleep(500 * time.Millisecond)
			}
		})
		return nil
	}
}
