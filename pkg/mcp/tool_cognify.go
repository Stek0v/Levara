package mcp

// Cognify pipeline tools: cognify, cognify_status.
// Extracted from deps.go during F-4 wave 3j-split.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/orchestrator"
	"github.com/stek0v/levara/pkg/runreg"
)

// cognifyProgressBufSize matches the pre-refactor channel capacity on
// internal/http/mcp.go. 100 is enough slack that orchestrator stages
// never block emitting progress while the tool goroutine's reader loop
// is running a map update.
const cognifyProgressBufSize = 100

// cognifyDefaultCollection is the collection name used when the caller
// does not supply one. Must match the REST default in api.go so both
// paths converge on the same vector store.
const cognifyDefaultCollection = "default"

// ToolCognify starts a background cognify pipeline run.
//
// Returns immediately with a RUNNING status entry in the registry; the
// caller polls via cognify_status (or subscribes to the REST SSE stream)
// to observe progress. The pipeline goroutine runs under
// a detached, bounded context retaining the verified caller, so a client
// disconnect does not erase identity or cancel ingestion mid-way.
//
// Error branches that produce IsError=true:
//   - Missing 'data' arg.
//   - EmbedEndpoint not configured.
//
// Successful start returns the initial structured run status; the caller
// feeds pipeline_run_id back into cognify_status.
func ToolCognify(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	data, _ := args["data"].(string)
	if data == "" {
		return ToolResult{Content: []Content{{Type: "text", Text: "Error: 'data' parameter required"}}, IsError: true}
	}

	runID := uuid.New().String()
	collection, _ := args["collection"].(string)
	if collection == "" {
		collection = cognifyDefaultCollection
	}

	tenantID, _ := ctx.Value(TenantIDKey).(string)
	status := &runreg.Status{
		OwnerID: extractOwnerID(ctx), TenantID: tenantID, RunID: runID, Status: "RUNNING", Stage: "starting", StartedAt: time.Now(),
	}

	pipeCfg := deps.BaseCognifyConfig()
	if pipeCfg.EmbedEndpoint == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: embedding service not configured"}},
			IsError: true,
		}
	}

	pipeCfg.Collection = collection
	pipeCfg.DatasetID = runID
	pipeCfg.GenerateTriplets = true
	trueVal := true
	pipeCfg.UseStructuredOutput = &trueVal

	// RAG mode: skip graph extraction (chunk+embed only, no LLM needed)
	if mode, _ := args["mode"].(string); mode == "rag" {
		pipeCfg.SkipGraph = true
		pipeCfg.GenerateTriplets = false
	}
	if cs, _ := args["chunk_strategy"].(string); cs != "" {
		pipeCfg.ChunkStrategy = cs
	}
	if oc, ok := args["overlap_chars"].(float64); ok && oc > 0 {
		pipeCfg.OverlapChars = int(oc)
	}
	if snap, ok := args["snap_to_sentence"].(bool); ok {
		pipeCfg.SnapToSentence = &snap
	}
	if pc, ok := args["parent_child"].(bool); ok && pc {
		pipeCfg.ParentChild = true
	}
	if dt, _ := args["document_title"].(string); dt != "" {
		pipeCfg.DocumentTitle = dt
	}
	if cr, ok := args["community_resolution"].(float64); ok && cr > 0 {
		pipeCfg.CommunityResolution = cr
	}
	if dt, ok := args["dedup_threshold"].(float64); ok && dt > 0 {
		pipeCfg.DedupThreshold = dt
	}
	if minC, ok := args["min_chunk_chars"].(float64); ok && minC > 0 {
		pipeCfg.MinChunkChars = int(minC)
	}
	if maxC, ok := args["max_chunk_chars"].(float64); ok && maxC > 0 {
		pipeCfg.MaxChunkChars = int(maxC)
	}
	if cp, _ := args["custom_prompt"].(string); cp != "" {
		pipeCfg.SystemPrompt = cp
	}
	if room, _ := args["room"].(string); room != "" {
		pipeCfg.Room = room
	}
	if rawTags, ok := args["tags"].([]any); ok {
		for _, t := range rawTags {
			if s, ok := t.(string); ok && s != "" {
				pipeCfg.Tags = append(pipeCfg.Tags, s)
			}
		}
	}
	if suffix := deps.OntologyPromptSuffix(collection); suffix != "" {
		pipeCfg.SystemPrompt += suffix
	}

	texts := []string{data}
	var err error
	pipeCfg, err = deps.PrepareCognify(ctx, texts, pipeCfg)
	if err != nil {
		return toolError("cognify source unavailable")
	}
	status.DatasetID = pipeCfg.DatasetID
	status.SourcesJSON = cognifySourcesJSON(pipeCfg)
	pipeCfg.AttemptID = runID
	if err := deps.ClaimPipelineAttempt(ctx, pipeCfg.DatasetID, pipeCfg.DocumentID, collection, runID, pipeCfg.SourceRevision, pipeCfg.RawContentHash); err != nil {
		return toolError("cognify source unavailable")
	}

	deps.Runs().Store(runID, status)
	go runCognifyPipeline(ctx, deps, runID, collection, texts, pipeCfg, status)

	return jsonResult(map[string]any{
		"pipeline_run_id": runID,
		"status":          "RUNNING",
		"stage":           "starting",
		"message":         "Cognify pipeline started; use cognify_status to check progress.",
	})
}

func cognifySourcesJSON(cfg orchestrator.Config) string {
	if cfg.DocumentID == "" {
		return ""
	}
	proof, _ := json.Marshal([]map[string]any{{
		"dataset_id": cfg.DatasetID, "document_id": cfg.DocumentID,
		"content_revision": cfg.ContentRevision, "source_revision": cfg.SourceRevision,
		"raw_content_hash": cfg.RawContentHash,
	}})
	return string(proof)
}

func pipelineBackgroundContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := 30 * time.Minute
	if ms, err := strconv.Atoi(os.Getenv("BACKGROUND_TASK_TIMEOUT_MS")); err == nil && ms > 0 {
		timeout = time.Duration(ms) * time.Millisecond
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

// runPipelineWithStatus drives the orchestrator to completion and
// updates status fields: per-progress-event stage/message/counters,
// and terminal Status ("COMPLETED" or "FAILED" + err message). Runs
// synchronously; callers wrap in a goroutine for fire-and-forget.
// Shared between cognify (adds Persist + Heartbeat after) and
// analyze_commits (bare-bones — see tool_git.go).
func runPipelineWithStatus(parent context.Context, deps Deps, texts []string, pipeCfg orchestrator.Config, status *runreg.Status) {
	ctx, cancel := pipelineBackgroundContext(parent)
	defer cancel()
	progressCh := make(chan orchestrator.Progress, cognifyProgressBufSize)
	errCh := make(chan error, 1)

	go func() {
		errCh <- deps.RunPipeline(ctx, texts, pipeCfg, progressCh)
	}()

	// T9: emit one event per stage transition so MCP clients polling
	// cognify_status (or REST SSE) can reconstruct per-stage timing without
	// a long-lived stream. Initial Stage is "starting" (set in ToolCognify),
	// so the first different Stage value triggers the first append.
	lastStage := status.Stage
	for p := range progressCh {
		status.Stage = p.Stage
		status.Message = p.Message
		status.Chunks = p.ChunksCreated
		status.Entities = p.EntitiesExtracted
		status.Edges = p.EdgesExtracted
		status.ElapsedMs = p.ElapsedMs
		if p.Stage != lastStage {
			status.AppendEvent(runreg.StageEvent{
				Stage:     p.Stage,
				Message:   p.Message,
				At:        time.Now(),
				ElapsedMs: p.ElapsedMs,
				Chunks:    p.ChunksCreated,
				Entities:  p.EntitiesExtracted,
				Edges:     p.EdgesExtracted,
			})
			lastStage = p.Stage
		}
		deps.Runs().Store(status.RunID, status)
	}

	if err := <-errCh; err != nil {
		status.Status = "FAILED"
		status.Message = err.Error()
	} else {
		status.Status = "COMPLETED"
	}
	status.ElapsedMs = time.Since(status.StartedAt).Milliseconds()
	status.AppendEvent(runreg.StageEvent{
		Stage:     status.Status,
		Message:   status.Message,
		At:        time.Now(),
		ElapsedMs: status.ElapsedMs,
		Chunks:    status.Chunks,
		Entities:  status.Entities,
		Edges:     status.Edges,
		Terminal:  true,
	})
	deps.Runs().Store(status.RunID, status)
}

// runCognifyPipeline wraps runPipelineWithStatus with cognify-specific
// post-run bookkeeping: PersistPipelineStatus (skip-if-done) and
// heartbeat log. Analyze_commits and other pipeline-driving tools
// either call the helper directly or add their own post-run hooks.
func runCognifyPipeline(ctx context.Context, deps Deps, runID, collection string, texts []string, pipeCfg orchestrator.Config, status *runreg.Status) {
	runPipelineWithStatus(ctx, deps, texts, pipeCfg, status)

	if status.Status != "COMPLETED" || !deps.PipelineFinalizesStatus() {
		if err := deps.PersistPipelineStatus(pipeCfg.DatasetID, pipeCfg.DocumentID, collection,
			status.Status, pipeCfg.SourceRevision, pipeCfg.RawContentHash, status.Chunks, status.Entities, status.Edges, status.ElapsedMs, pipeCfg.AttemptID); err != nil {
			status.Message = "pipeline status persistence failed"
			deps.Runs().Store(runID, status)
		}
	}

	deps.LogHeartbeat("cognify", map[string]any{
		"run_id":     runID,
		"collection": collection,
		"status":     status.Status,
		"chunks":     status.Chunks,
		"entities":   status.Entities,
		"elapsed_ms": status.ElapsedMs,
	})
}

// ToolCognifyStatus returns the current state of a pipeline run as
// pretty-printed JSON. IsError=true when run_id is missing or unknown.
// Successful lookup returns the Status struct JSON so the caller can see
// stage, progress counters, and message fields.
func ToolCognifyStatus(deps Deps, args map[string]any) ToolResult {
	runID, _ := args["run_id"].(string)
	if runID == "" {
		return ToolResult{Content: []Content{{Type: "text", Text: "Error: 'run_id' required"}}, IsError: true}
	}

	val, ok := deps.Runs().Load(runID)
	if !ok {
		return ToolResult{Content: []Content{{Type: "text", Text: fmt.Sprintf("Run %s not found.", runID)}}, IsError: true}
	}

	return jsonResult(val)
}
