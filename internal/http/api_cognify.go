// api_cognify.go — Cognify pipeline trigger + status + SSE stream, split
// out of api.go (T4). Covers:
//
//	POST /cognify
//	GET  /cognify/:runId/status
//	GET  /cognify/:runId/stream
package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/stek0v/levara/internal/metrics"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/ingest"
	"github.com/stek0v/levara/pkg/orchestrator"
	"github.com/stek0v/levara/pkg/runreg"
)

// resolveCognifyDataset reuses only the caller's own internal dataset. A new
// dataset is returned as a target and created later by IngestAuthorized in the
// same authorized transaction as the immutable source.
func resolveCognifyDataset(ctx context.Context, db *sql.DB, owner, collection, fallbackID string) (string, string, error) {
	name := fmt.Sprintf("__cognify__:%s:%s", owner, collection)
	var id, actualOwner string
	err := db.QueryRowContext(ctx, Q(`SELECT id,COALESCE(owner_id,'') FROM datasets WHERE name = $1`), name).Scan(&id, &actualOwner)
	if errors.Is(err, sql.ErrNoRows) {
		return fallbackID, name, nil
	}
	if err != nil {
		return "", "", err
	}
	if id == "" || actualOwner != owner {
		return "", "", accesspkg.ErrDocumentForbidden
	}
	return id, name, nil
}

func cognifySkipGraphFromMode(mode string, skipGraph bool) bool {
	return skipGraph || strings.EqualFold(mode, "rag")
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
		reqCtx, cancel := searchRequestContext(c)
		defer cancel()
		reqCtx = searchEgressContext(c, cfg, reqCtx)
		c.SetUserContext(reqCtx)

		var req struct {
			Datasets  []string `json:"datasets"`
			Documents []struct {
				DatasetID  string `json:"dataset_id"`
				DocumentID string `json:"document_id"`
			} `json:"documents"`
			DatasetIds      []string `json:"datasetIds"` // Levara frontend format
			Texts           []string `json:"texts"`
			LLMModel        string   `json:"llm_model"`
			Collection      string   `json:"collection"`
			Mode            string   `json:"mode"`
			RunInBackground bool     `json:"runInBackground"`
			SessionID       string   `json:"session_id"`
			// SkipGraph enables RAG-mode ingest: chunk → embed → HNSW only,
			// no LLM entity extraction, no graph writes. Surfaces the
			// orchestrator's existing SkipGraph flag (pipeline.go:93) so
			// callers can opt into the fastest deterministic ingest path.
			SkipGraph bool `json:"skip_graph"`
		}
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid request"})
		}
		req.SkipGraph = cognifySkipGraphFromMode(req.Mode, req.SkipGraph)

		collection := req.Collection
		if collection == "" {
			collection = "default"
		}
		runID := uuid.New().String()
		userID, _ := c.Locals("user_id").(string)
		userID = strings.Clone(userID)

		// Check every source before loading any bytes. Cognify also writes derived
		// records for each dataset, so read-only shares cannot start this mutation.
		var allDatasetIDs []string
		seen := map[string]bool{}
		for _, id := range append(req.Datasets, req.DatasetIds...) {
			if seen[id] {
				continue
			}
			if err := authorizeDatasetFiber(c, cfg, id, accesspkg.ActionWrite); err != nil {
				return err
			}
			seen[id] = true
			allDatasetIDs = append(allDatasetIDs, id)
		}
		if len(req.Documents) > 0 && (len(allDatasetIDs) > 0 || len(req.Texts) > 0) {
			return fiber.NewError(400, "documents cannot be mixed with datasets or inline texts")
		}
		if len(req.Texts) > 0 && len(allDatasetIDs) > 1 {
			return c.Status(400).JSON(fiber.Map{"detail": "inline texts require at most one dataset"})
		}

		var sources []cognifySource
		var texts []string
		if len(req.Texts) > 0 {
			datasetID := runID
			datasetName := ""
			if len(allDatasetIDs) > 0 {
				datasetID = allDatasetIDs[0]
			} else if cfg.DB != nil {
				var err error
				datasetID, datasetName, err = resolveCognifyDataset(reqCtx, cfg.DB, userID, collection, runID)
				if err != nil {
					return documentHTTPError(err)
				}
			}
			var err error
			sources, err = ingestCognifySources(reqCtx, cfg, uploadMetadataActor(c, cfg, reqCtx), datasetID, datasetName, "", req.Texts)
			if err != nil {
				return documentHTTPError(err)
			}
			texts = req.Texts
		} else if cfg.DB != nil {
			type locationSource struct {
				source   cognifySource
				location string
			}
			var pending []locationSource
			for _, datasetID := range allDatasetIDs {
				rows, err := cfg.DB.QueryContext(reqCtx, Q(`SELECT d.id, d.name, d.raw_data_location FROM data d
                    JOIN dataset_data dd ON d.id=dd.data_id WHERE dd.dataset_id=$1 ORDER BY d.id`), datasetID)
				if err != nil {
					return fiber.NewError(503, "dataset source lookup failed")
				}
				for rows.Next() {
					item := locationSource{source: cognifySource{datasetID: datasetID}}
					if err := rows.Scan(&item.source.documentID, &item.source.title, &item.location); err != nil {
						rows.Close()
						return fiber.NewError(503, "dataset source lookup failed")
					}
					pending = append(pending, item)
				}
				readErr, closeErr := rows.Err(), rows.Close()
				if readErr != nil || closeErr != nil {
					return fiber.NewError(503, "dataset source lookup failed")
				}
			}
			for _, doc := range req.Documents {
				if doc.DatasetID == "" || doc.DocumentID == "" {
					return fiber.NewError(400, "document requires dataset_id and document_id")
				}
				item := locationSource{source: cognifySource{datasetID: doc.DatasetID, documentID: doc.DocumentID}}
				err := cfg.DB.QueryRowContext(reqCtx, Q(`SELECT d.name,d.raw_data_location FROM data d JOIN dataset_data dd ON dd.data_id=d.id
                    WHERE dd.dataset_id=$1 AND dd.data_id=$2`), doc.DatasetID, doc.DocumentID).Scan(&item.source.title, &item.location)
				if errors.Is(err, sql.ErrNoRows) {
					return fiber.NewError(404, "document not found")
				}
				if err != nil {
					return fiber.NewError(503, "document source lookup failed")
				}
				pending = append(pending, item)
			}
			// Buffer IDs and release every cursor before ACL queries or I/O.
			// Authorize every source before loading the first document.
			for i := range pending {
				item := &pending[i]
				ref := accesspkg.DocumentRef{DatasetID: item.source.datasetID, DataID: item.source.documentID}
				d, err := documentSQLPolicy(cfg).AuthorizeDocument(reqCtx, workspaceActorFromFiber(c), ref, accesspkg.ActionWrite)
				if err != nil {
					return documentHTTPError(err)
				}
				if !d.Allowed {
					return fiber.NewError(403, "document processing denied")
				}
				resource, err := documentSQLPolicy(cfg).GetDocumentResource(reqCtx, ref)
				if err != nil && !errors.Is(err, accesspkg.ErrDocumentNotFound) {
					return documentHTTPError(err)
				}
				item.source.contentRevision = resource.ContentRevision
				item.source.sourceRevision, item.source.rawContentHash, err = documentSQLPolicy(cfg).SourceVersion(reqCtx, ref)
				if err != nil {
					return fiber.NewError(409, "source version unavailable; reimport the document")
				}
			}
			for _, item := range pending {
				raw, err := loadRawDataByLocation(reqCtx, cfg, item.location)
				if err != nil {
					return fiber.NewError(422, "unable to read document "+item.source.documentID)
				}
				if fmt.Sprintf("%x", sha256.Sum256(raw)) != item.source.rawContentHash {
					return fiber.NewError(409, "document bytes changed; reimport the document")
				}
				item.source.texts = []string{string(raw)}
				sources = append(sources, item.source)
				texts = append(texts, string(raw))
			}
		}
		if len(texts) == 0 {
			return c.Status(400).JSON(fiber.Map{"detail": "no texts to cognify (provide texts[] or datasets[])"})
		}

		var sessionContext string
		if req.SessionID != "" && cfg.DB != nil {
			var err error
			sessionContext, err = GetScopedSessionContext(reqCtx, cfg, req.SessionID, 5)
			if err != nil {
				return sessionHTTPError(err)
			}
		}

		actor, _ := reqCtx.Value(searchActorKey{}).(accesspkg.Actor)
		proofs := make([]searchDocumentSource, 0, len(sources))
		for _, source := range sources {
			proofs = append(proofs, source.proof())
		}
		proofs = append(proofs, searchSources(reqCtx)...)
		rawProofs, _ := json.Marshal(proofs)
		runStatus := &runreg.Status{RequiresAdmin: searchEvidenceRequiresAdmin(reqCtx), OwnerID: actor.UserID, TenantID: actor.TenantID, SourcesJSON: string(rawProofs), RunID: runID, Status: "RUNNING", Stage: "starting", StartedAt: time.Now()}

		pipeCfg := baseCognifyConfig(cfg)
		pipeCfg.Collection = collection
		pipeCfg.GenerateTriplets, pipeCfg.SkipGraph = !req.SkipGraph, req.SkipGraph
		pipeCfg.SystemPrompt, pipeCfg.DatasetID = sessionContext, sources[0].datasetID
		if req.LLMModel != "" {
			pipeCfg.LLMModel = req.LLMModel
		}
		pipeCfg.AttemptID = runID
		if cfg.DB != nil {
			claims := make([]pipelineAttemptSource, 0, len(sources))
			for _, source := range sources {
				claims = append(claims, pipelineAttemptSource{datasetID: source.datasetID, dataID: source.documentID, sourceRevision: source.sourceRevision, rawContentHash: source.rawContentHash})
			}
			if err := claimPipelineAttempts(reqCtx, cfg.DB, claims, collection, runID); err != nil {
				return documentHTTPError(err)
			}
		}
		startCognifyRun(reqCtx, cfg, runStatus, pipeCfg, sources, texts, req.SessionID, userID)

		return c.JSON(fiber.Map{
			"status":          "PipelineRunStarted",
			"pipeline_run_id": runID,
		})
	}
}

func baseCognifyConfig(cfg APIConfig) orchestrator.Config {
	return orchestrator.Config{
		ChunkStrategy:  "merged",
		MinChunkChars:  50,
		MaxChunkChars:  2000,
		LLMEndpoint:    os.Getenv("LLM_ENDPOINT"),
		LLMModel:       os.Getenv("LLM_MODEL"),
		LLMConcurrency: 1,
		// M8: opt-in chunk batching. LEVARA_LLM_EXTRACT_BATCH_SIZE=4
		// coalesces 4 chunks per extraction call — fewer LLM requests
		// for large cognify runs. Default 1 keeps legacy behavior.
		LLMBatchSize:        batchSizeFromEnv(),
		EmbedEndpoint:       cfg.EmbedEndpoint,
		EmbedModel:          cfg.EmbedModel,
		EmbedClient:         cfg.EmbedClient, // T3 follow-up: reuse shared TCP pool through the pipeline
		Neo4jURL:            cfg.Neo4jCfg.Neo4jURL,
		Neo4jUser:           cfg.Neo4jCfg.Neo4jUser,
		Neo4jPassword:       cfg.Neo4jCfg.Neo4jPassword,
		Neo4jDatabase:       cfg.Neo4jCfg.Neo4jDatabase,
		Collections:         cfg.Collections,
		BM25Indexes:         cfg.BM25Indexes,
		BM25Store:           cfg.BM25Store,
		DB:                  cfg.DB,
		LLMCache:            cfg.LLMCache,
		LLMProvider:         cfg.LLMProvider,
		UseStructuredOutput: func() *bool { b := true; return &b }(),
	}
}

func startCognifyRun(reqCtx context.Context, cfg APIConfig, runStatus *runreg.Status, pipeCfg orchestrator.Config, sources []cognifySource, texts []string, sessionID, userID string) {
	runID, collection := runStatus.RunID, pipeCfg.Collection
	cfg.Runs.Store(runID, runStatus)

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
	// runStatus field; readers receive immutable registry snapshots.
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
		bgCtx, bgCancel := context.WithTimeout(context.WithoutCancel(reqCtx), timeoutFromEnvMs("BACKGROUND_TASK_TIMEOUT_MS", defaultBackgroundTaskTimeout))
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
				publishCognifyFailure(cfg.DB, cfg.Runs, sources, collection, runID, runStatus)
			}
		}()

		go func() {
			// Inner panic recover reads stage via the atomic snapshot so
			// the closure is safe to invoke from a panic unwinding while
			// the outer goroutine may still be mutating runStatus.Stage.
			errCh <- runWithPanicGuard(runID, readStage, func() error {
				return runCognifySources(bgCtx, sources, cfg, pipeCfg, progressCh)
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
			cfg.Runs.Store(runID, runStatus)
		}

		if err := <-errCh; err != nil {
			runStatus.Status = "FAILED"
			runStatus.Message = err.Error()
		} else {
			runStatus.Status = "COMPLETED"
		}
		runStatus.ElapsedMs = time.Since(runStatus.StartedAt).Milliseconds()
		if runStatus.Status != "COMPLETED" {
			if !publishCognifyFailure(cfg.DB, cfg.Runs, sources, collection, runID, runStatus) {
				return
			}
		} else {
			cfg.Runs.Store(runID, runStatus)
		}

		// VSA remains dataset-scoped.
		rebuilt := map[string]bool{}
		for _, source := range sources {
			if runStatus.Status == "COMPLETED" && !pipeCfg.SkipGraph && !rebuilt[source.datasetID] {
				rebuilt[source.datasetID] = true
				rebuildVSAMemory(bgCtx, cfg, source.datasetID, "cognify")
			}
		}

		recordInteraction(bgCtx, cfg, sessionID, userID, strings.Join(texts, " "),
			fmt.Sprintf("%d entities extracted", runStatus.Entities), "cognify")
	}()
}

func publishCognifyFailure(db *sql.DB, runs *runreg.Registry, sources []cognifySource, collection, runID string, status *runreg.Status) bool {
	if err := finalizeCognifyFailure(db, sources, collection, runID, status); err != nil {
		log.Printf("cognify status finalization pending run_id=%s: %v", runID, err)
		pending := *status
		pending.Status = "RUNNING"
		pending.Stage = "finalizing"
		pending.Message = "pipeline failed; exact source status finalization pending"
		runs.Store(runID, &pending)
		return false
	}
	runs.Store(runID, status)
	return true
}

func finalizeCognifyFailure(db *sql.DB, sources []cognifySource, collection, runID string, status *runreg.Status) error {
	if db == nil {
		return nil
	}
	if status == nil || len(sources) == 0 || collection == "" || runID == "" {
		return accesspkg.ErrDocumentInvalid
	}
	for _, source := range sources {
		if source.datasetID == "" || source.documentID == "" || source.sourceRevision <= 0 || len(source.rawContentHash) != 64 {
			return accesspkg.ErrDocumentInvalid
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	statusJSON := pipelineStatusJSON("FAILED", status.Chunks, status.Entities, status.Edges, status.ElapsedMs)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		lastErr = finalizeCognifyFailureAttempt(ctx, db, sources, collection, runID, statusJSON)
		if lastErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
	return lastErr
}

func finalizeCognifyFailureAttempt(ctx context.Context, db *sql.DB, sources []cognifySource, collection, runID, statusJSON string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	updated := make([]cognifySource, 0, len(sources))
	for _, source := range sources {
		query, args := QArgs(`UPDATE document_pipeline_statuses SET
			pipeline_state='FAILED',status_json=$1,updated_at=CURRENT_TIMESTAMP
			WHERE dataset_id=$2 AND data_id=$3 AND collection_name=$4
			AND source_revision=$5 AND LOWER(raw_content_hash)=$6 AND attempt_id=$7
			AND pipeline_state<>'COMPLETED'
			AND EXISTS (SELECT 1 FROM data d JOIN dataset_data dd ON dd.data_id=d.id
				WHERE d.id=$3 AND dd.dataset_id=$2 AND d.source_revision=$5 AND LOWER(d.raw_content_hash)=$6)`,
			statusJSON, source.datasetID, source.documentID, collection, source.sourceRevision, source.rawContentHash, runID)
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 1 {
			updated = append(updated, source)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, source := range updated {
		mirrorSingleDatasetPipelineStatus(ctx, db, source.datasetID, source.documentID, collection, []byte(statusJSON), source.sourceRevision, source.rawContentHash)
	}
	return nil
}

// A source owns its dataset and document attribution throughout the pipeline.
type cognifySource struct {
	datasetID       string
	documentID      string
	contentRevision int64
	sourceRevision  int64
	rawContentHash  string
	title           string
	texts           []string
}

func (s cognifySource) proof() searchDocumentSource {
	return searchDocumentSource{DatasetID: s.datasetID, DocumentID: s.documentID, ContentRevision: s.contentRevision, SourceRevision: s.sourceRevision, RawContentHash: s.rawContentHash}
}

// Inline input becomes an ordinary immutable source before any model call.
func ingestCognifySources(ctx context.Context, cfg APIConfig, actor accesspkg.MetadataActor, datasetID, datasetName, title string, texts []string) ([]cognifySource, error) {
	if cfg.DB == nil {
		if cfg.RequireAuth {
			return nil, accesspkg.ErrDocumentForbidden
		}
		return []cognifySource{{datasetID: datasetID, title: title, texts: texts}}, nil
	}
	if datasetName == "" {
		if err := cfg.DB.QueryRowContext(ctx, Q("SELECT name FROM datasets WHERE id=$1"), datasetID).Scan(&datasetName); err != nil {
			return nil, err
		}
	}
	items := make([]ingest.Item, len(texts))
	for i, text := range texts {
		if strings.TrimSpace(text) == "" {
			return nil, accesspkg.ErrDocumentInvalid
		}
		items[i] = ingest.Item{Text: text, DatasetName: datasetName}
	}
	w, err := ingest.NewMetadataWriterForStorage(cfg.DB, cfg.FileStorage)
	if err != nil {
		return nil, err
	}
	results, _, err := w.IngestAuthorized(ctx, items, nil, cfg.StoragePath, cfg.FileStorage, actor, datasetID, datasetName)
	if err != nil {
		return nil, err
	}
	sources := make([]cognifySource, 0, len(results))
	seen := map[string]bool{}
	for i, result := range results {
		if seen[result.ID] {
			continue
		}
		seen[result.ID] = true
		ref := accesspkg.DocumentRef{DatasetID: datasetID, DataID: result.ID}
		version, hash, err := documentSQLPolicy(cfg).SourceVersion(ctx, ref)
		if err != nil {
			return nil, err
		}
		if hash != result.ContentHash {
			return nil, accesspkg.ErrDocumentVersionConflict
		}
		r, err := documentSQLPolicy(cfg).GetDocumentResource(ctx, ref)
		if err != nil && !errors.Is(err, accesspkg.ErrDocumentNotFound) {
			return nil, err
		}
		sources = append(sources, cognifySource{datasetID: datasetID, documentID: result.ID, contentRevision: r.ContentRevision, sourceRevision: version, rawContentHash: hash, title: title, texts: []string{texts[i]}})
	}
	return sources, nil
}

func runCognifySources(ctx context.Context, sources []cognifySource, apiCfg APIConfig, cfg orchestrator.Config, progress chan<- orchestrator.Progress) error {
	defer close(progress)
	var totals orchestrator.Progress
	inheritedSources := append([]searchDocumentSource(nil), searchSources(ctx)...)
	for _, source := range sources {
		sourceCfg := cfg
		sourceCfg.DatasetID, sourceCfg.DocumentID, sourceCfg.DocumentTitle = source.datasetID, source.documentID, source.title
		sourceCfg.ContentRevision = source.contentRevision
		if apiCfg.DB != nil {
			if source.documentID == "" || source.sourceRevision <= 0 || len(source.rawContentHash) != 64 || len(source.texts) != 1 || fmt.Sprintf("%x", sha256.Sum256([]byte(source.texts[0]))) != source.rawContentHash {
				return accesspkg.ErrDocumentVersionConflict
			}
			sourceCfg.Generation = uuid.NewString()
		}
		evidence := &searchEvidence{requiresAdmin: searchEvidenceRequiresAdmin(ctx), sources: make(map[searchDocumentSource]struct{})}
		for _, inherited := range inheritedSources {
			evidence.sources[inherited] = struct{}{}
		}
		sourceCtx := context.WithValue(ctx, searchEvidenceKey{}, evidence)
		trackSearchSource(sourceCtx, source.proof())
		sourceCfg.GuardTransfer = func(ctx context.Context) (func(), error) { return beginCognifyTransfer(ctx, apiCfg, source) }
		sourceCfg.CheckWrite = func(ctx context.Context) error {
			release, err := sourceCfg.GuardTransfer(ctx)
			if err != nil {
				return err
			}
			release()
			return nil
		}
		updates := make(chan orchestrator.Progress, 100)
		done := make(chan error, 1)
		go func() {
			done <- runWithPanicGuard(source.datasetID, func() string { return "cognify" }, func() error { return orchestrator.Run(sourceCtx, source.texts, sourceCfg, updates) })
		}()
		var last orchestrator.Progress
		for update := range updates {
			last = update
			update.ChunksCreated += totals.ChunksCreated
			update.EntitiesExtracted += totals.EntitiesExtracted
			update.EdgesExtracted += totals.EdgesExtracted
			update.ElapsedMs += totals.ElapsedMs
			progress <- update
		}
		if err := <-done; err != nil {
			return err
		}
		if apiCfg.DB != nil {
			fenced, release, err := beginSearchReadFence(sourceCtx)
			if err != nil {
				return err
			}
			p, ok := fenced.Value(searchReadPolicyKey{}).(accesspkg.SQLPolicy)
			if !ok {
				release()
				return fiber.NewError(503, "document publication authorization unavailable")
			}
			actor, _ := fenced.Value(searchActorKey{}).(accesspkg.Actor)
			lineage, marshalErr := json.Marshal(searchSources(fenced))
			if marshalErr != nil {
				release()
				return marshalErr
			}
			err = p.CommitDocumentIndexVersioned(fenced, actor, accesspkg.DocumentRef{DatasetID: source.datasetID, DataID: source.documentID}, source.contentRevision, sourceCfg.Collection, sourceCfg.Generation, accesspkg.DocumentPublicationLineage{
				SourcesJSON: string(lineage), RequiresAdmin: searchEvidenceRequiresAdmin(fenced),
				SourceRevision: source.sourceRevision, RawContentHash: source.rawContentHash,
				AttemptID:          sourceCfg.AttemptID,
				PipelineStatusJSON: pipelineStatusJSON("COMPLETED", last.ChunksCreated, last.EntitiesExtracted, last.EdgesExtracted, last.ElapsedMs),
			})
			release()
			if err != nil {
				return err
			}
		}
		for _, used := range searchSources(sourceCtx) {
			trackSearchSource(ctx, used)
		}
		totals.ChunksCreated += last.ChunksCreated
		totals.EntitiesExtracted += last.EntitiesExtracted
		totals.EdgesExtracted += last.EdgesExtracted
		totals.ElapsedMs += last.ElapsedMs
	}
	return nil
}

// Each outgoing model request holds the same read fence as search and also
// requires processing permission; an editor revoked after enqueue cannot run.
func beginCognifyTransfer(ctx context.Context, cfg APIConfig, source cognifySource) (func(), error) {
	fenced, release, err := beginSearchReadFence(ctx)
	if err != nil {
		return nil, err
	}
	actor, _ := ctx.Value(searchActorKey{}).(accesspkg.Actor)
	if cfg.DB == nil && !cfg.RequireAuth {
		return release, nil
	}
	p := documentSQLPolicy(cfg)
	if locked, ok := fenced.Value(searchReadPolicyKey{}).(accesspkg.SQLPolicy); ok {
		p = locked
	}
	var d accesspkg.Decision
	if source.documentID != "" {
		d, err = p.AuthorizeDocument(fenced, actor, accesspkg.DocumentRef{DatasetID: source.datasetID, DataID: source.documentID}, accesspkg.ActionWrite)
	} else {
		d, err = p.AuthorizeDataset(fenced, actor, source.datasetID, accesspkg.ActionWrite)
	}
	if err != nil || !d.Allowed {
		release()
		return nil, fiber.NewError(403, "document processing revoked")
	}
	version, hash, err := p.SourceVersion(fenced, accesspkg.DocumentRef{DatasetID: source.datasetID, DataID: source.documentID})
	if err != nil || version != source.sourceRevision || hash != source.rawContentHash {
		release()
		return nil, accesspkg.ErrDocumentVersionConflict
	}
	return release, nil
}

// The binding is server-created and omitted from public run JSON. Legacy runs
// with no owner remain visible only to an active instance administrator.
func authorizeRunStatus(ctx context.Context, cfg APIConfig, status *runreg.Status) error {
	if status == nil {
		return errSessionForbidden
	}
	actor, err := sessionActor(ctx, cfg, accesspkg.ActionRead)
	if err != nil {
		return err
	}
	if actor.UserID == "" && !cfg.RequireAuth {
		return nil
	}
	if status.OwnerID == "" || status.RequiresAdmin {
		if !globalSearchGraphAllowed(ctx, cfg) {
			return errSessionForbidden
		}
		requireAdminSearchEvidence(ctx)
	}
	if status.OwnerID != "" && (status.OwnerID != actor.UserID || status.TenantID != actor.TenantID) {
		return errSessionForbidden
	}
	var sources []searchDocumentSource
	if status.SourcesJSON != "" {
		if err := json.Unmarshal([]byte(status.SourcesJSON), &sources); err != nil {
			return errSessionInvalidProvenance
		}
	} else if status.DatasetID != "" {
		sources = []searchDocumentSource{{DatasetID: status.DatasetID}}
	}
	for _, source := range sources {
		allowed, err := searchDocumentAllowed(ctx, cfg, actor, source)
		if err != nil {
			return err
		}
		if !allowed {
			return errSessionForbidden
		}
		trackSearchSource(ctx, source)
	}
	return nil
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
		ctx, cancel := searchRequestContext(c)
		defer cancel()
		ctx = searchEgressContext(c, cfg, ctx)
		c.SetUserContext(ctx)
		runID := strings.Clone(c.Params("runId"))
		if val, ok := cfg.Runs.Load(runID); ok && authorizeRunStatus(ctx, cfg, val) == nil {
			if err := c.JSON(val); err != nil {
				return err
			}
			return sendProtectedResponseWithFence(c, ctx)
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
		ctx, cancel := searchRequestContext(c)
		defer cancel()
		ctx = searchEgressContext(c, cfg, ctx)
		runID := strings.Clone(c.Params("runId"))
		status, ok := cfg.Runs.Load(runID)
		if !ok || authorizeRunStatus(ctx, cfg, status) != nil {
			return fiber.NewError(404, "run not found")
		}
		deadline, _ := ctx.Deadline()
		streamCtx, streamCancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
		stream := &runProgressStream{ctx: streamCtx, cancel: streamCancel, cfg: cfg, runID: runID}
		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "private, no-store")
		c.Set("X-Accel-Buffering", "no")
		if err := c.SendStream(stream); err != nil {
			stream.Close()
			return err
		}
		return nil
	}
}

// Each frame rechecks the run's sources and credential. Close cancels pending
// SQL/poll work; no Fiber context survives the handler's return.
type runProgressStream struct {
	ctx              context.Context
	cancel           context.CancelFunc
	cfg              APIConfig
	runID, lastStage string
	frame            *bytes.Reader
	release          func()
	terminal         bool
	mu               sync.Mutex
}

func (s *runProgressStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if err := s.ctx.Err(); err != nil {
			return 0, err
		}
		if s.frame != nil && s.frame.Len() > 0 {
			return s.frame.Read(p)
		}
		if s.release != nil {
			s.release()
			s.release = nil
		}
		if s.terminal {
			return 0, io.EOF
		}
		status, ok := s.cfg.Runs.Load(s.runID)
		if !ok {
			return 0, io.EOF
		}
		if err := authorizeRunStatus(s.ctx, s.cfg, status); err != nil {
			return 0, err
		}
		if status.Stage == s.lastStage && status.Status == "RUNNING" {
			timer := time.NewTimer(500 * time.Millisecond)
			select {
			case <-s.ctx.Done():
				timer.Stop()
				return 0, s.ctx.Err()
			case <-timer.C:
			}
			continue
		}
		_, release, err := beginSearchReadFence(s.ctx)
		if err != nil {
			return 0, err
		}
		s.release = release
		data, err := json.Marshal(status)
		if err != nil {
			release()
			s.release = nil
			return 0, err
		}
		frame := fmt.Sprintf("event: progress\ndata: %s\n\n", data)
		s.lastStage = status.Stage
		if status.Status != "RUNNING" {
			s.terminal = true
			frame += fmt.Sprintf("event: done\ndata: %s\n\n", data)
		}
		s.frame = bytes.NewReader([]byte(frame))
	}
}
func (s *runProgressStream) Close() error {
	s.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release != nil {
		s.release()
		s.release = nil
	}
	return nil
}

// batchSizeFromEnv reads LEVARA_LLM_EXTRACT_BATCH_SIZE (M8); values < 1
// keep the legacy per-chunk extraction.
func batchSizeFromEnv() int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("LEVARA_LLM_EXTRACT_BATCH_SIZE")))
	if err != nil || n < 1 {
		return 1
	}
	return n
}
