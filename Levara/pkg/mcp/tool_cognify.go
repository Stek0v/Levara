package mcp

// Cognify pipeline tools: cognify, cognify_status.
// Extracted from deps.go during F-4 wave 3j-split.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/stek0v/cognevra/pkg/orchestrator"
	"github.com/stek0v/cognevra/pkg/runreg"
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
// context.Background so an MCP client disconnect during ingestion does
// not cancel the work mid-way.
//
// Error branches that produce IsError=true:
//   - Missing 'data' arg.
//   - EmbedEndpoint not configured (registry gets FAILED state first).
//
// Successful start returns a human-readable RunID pointer; the caller
// feeds that ID back into cognify_status.
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

	status := &runreg.Status{
		RunID: runID, Status: "RUNNING", Stage: "starting", StartedAt: time.Now(),
	}
	deps.Runs().Store(runID, status)

	pipeCfg := deps.BaseCognifyConfig()
	if pipeCfg.EmbedEndpoint == "" {
		status.Status = "FAILED"
		status.Message = "Embedding service not configured (EMBED_ENDPOINT)"
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
	if di, _ := args["document_id"].(string); di != "" {
		pipeCfg.DocumentID = di
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

	go runCognifyPipeline(deps, runID, collection, texts, pipeCfg, status)

	return ToolResult{
		Content: []Content{{
			Type: "text",
			Text: fmt.Sprintf("Cognify pipeline started. Run ID: %s. Use cognify_status tool to check progress.", runID),
		}},
	}
}

// runPipelineWithStatus drives the orchestrator to completion and
// updates status fields: per-progress-event stage/message/counters,
// and terminal Status ("COMPLETED" or "FAILED" + err message). Runs
// synchronously; callers wrap in a goroutine for fire-and-forget.
// Shared between cognify (adds Persist + Heartbeat after) and
// analyze_commits (bare-bones — see tool_git.go).
func runPipelineWithStatus(deps Deps, texts []string, pipeCfg orchestrator.Config, status *runreg.Status) {
	progressCh := make(chan orchestrator.Progress, cognifyProgressBufSize)
	errCh := make(chan error, 1)

	go func() {
		errCh <- deps.RunPipeline(context.Background(), texts, pipeCfg, progressCh)
	}()

	for p := range progressCh {
		status.Stage = p.Stage
		status.Message = p.Message
		status.Chunks = p.ChunksCreated
		status.Entities = p.EntitiesExtracted
		status.Edges = p.EdgesExtracted
		status.ElapsedMs = p.ElapsedMs
	}

	if err := <-errCh; err != nil {
		status.Status = "FAILED"
		status.Message = err.Error()
	} else {
		status.Status = "COMPLETED"
	}
	status.ElapsedMs = time.Since(status.StartedAt).Milliseconds()
}

// runCognifyPipeline wraps runPipelineWithStatus with cognify-specific
// post-run bookkeeping: PersistPipelineStatus (skip-if-done) and
// heartbeat log. Analyze_commits and other pipeline-driving tools
// either call the helper directly or add their own post-run hooks.
func runCognifyPipeline(deps Deps, runID, collection string, texts []string, pipeCfg orchestrator.Config, status *runreg.Status) {
	runPipelineWithStatus(deps, texts, pipeCfg, status)

	deps.PersistPipelineStatus(runID, collection,
		status.Status, status.Chunks, status.Entities, status.Edges, status.ElapsedMs)

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

	out, _ := json.MarshalIndent(val, "", "  ")
	return ToolResult{Content: []Content{{Type: "text", Text: string(out)}}}
}
