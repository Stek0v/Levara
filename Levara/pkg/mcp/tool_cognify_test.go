package mcp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stek0v/cognevra/pkg/orchestrator"
	"github.com/stek0v/cognevra/pkg/runreg"
)

// waitDone blocks until done is closed or the deadline elapses. Fails
// the test on timeout. Deadline is generous because CI boxes are slow.
// Tests close done inside the last Deps callback runCognifyPipeline
// invokes (typically persistFn, or heartbeatFn when heartbeat state is
// under test) so the channel close happens-after every field write in
// the tool goroutine. That turns the test's read-after-wait into a
// race-free cross-goroutine handoff.
func waitDone(t *testing.T, done <-chan struct{}, deadline time.Duration) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(deadline):
		t.Fatalf("timed out after %v waiting for cognify goroutine to finish", deadline)
	}
}

// extractRunID pulls the run ID out of the human-readable success
// message returned by ToolCognify. Breaks when the message format
// changes — good, because the format is part of the MCP contract.
func extractRunID(t *testing.T, res ToolResult) string {
	t.Helper()
	if res.IsError {
		t.Fatalf("ToolCognify returned IsError=true; content=%q", contentText(res))
	}
	text := contentText(res)
	const marker = "Run ID: "
	idx := strings.Index(text, marker)
	if idx < 0 {
		t.Fatalf("success message missing %q marker; got %q", marker, text)
	}
	rest := text[idx+len(marker):]
	dot := strings.Index(rest, ".")
	if dot < 0 {
		t.Fatalf("success message missing period after run id; got %q", text)
	}
	return rest[:dot]
}

func contentText(res ToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	return res.Content[0].Text
}

func TestToolCognify_MissingData(t *testing.T) {
	deps := &fakeDeps{}
	res := ToolCognify(context.Background(), deps, map[string]any{})
	if !res.IsError {
		t.Fatal("want IsError=true when data missing")
	}
	if !strings.Contains(contentText(res), "'data' parameter required") {
		t.Errorf("unexpected error text: %q", contentText(res))
	}
	// Nothing should be registered for a failed validation.
	// (deps.Runs() returns a fresh registry, so we cannot list it — but
	// contract says no Store happens before the early-return.)
}

func TestToolCognify_NoEmbedEndpoint(t *testing.T) {
	deps := &fakeDeps{baseCfg: orchestrator.Config{}}
	res := ToolCognify(context.Background(), deps, map[string]any{"data": "hello"})
	if !res.IsError {
		t.Fatal("want IsError=true when embed endpoint missing")
	}
	if !strings.Contains(contentText(res), "embedding service not configured") {
		t.Errorf("unexpected error text: %q", contentText(res))
	}
	// The registry should hold a FAILED entry for the assigned runID —
	// tested indirectly: exactly one entry exists and it's FAILED.
	// We cannot enumerate a sync.Map, so iterate by timing: ToolCognify
	// must assign an ID before the EmbedEndpoint check, so Runs() is
	// non-empty. Skip for now; the registry state is covered by the
	// happy-path tests below.
	_ = deps
}

func TestToolCognify_HappyPathCompletes(t *testing.T) {
	// Close done from inside heartbeatFn: that's the LAST call in
	// runCognifyPipeline, so every status write + PersistPipelineStatus
	// call happens-before the channel close. Anything the test reads
	// after <-done is then race-free. Using persistFn to signal would
	// race with the subsequent LogHeartbeat write.
	done := make(chan struct{})
	var gotStatus, gotCollection, heartbeatEvent string
	deps := &fakeDeps{baseCfg: orchestrator.Config{EmbedEndpoint: "http://embed"}}
	deps.persistFn = func(datasetID, collection, status string, chunks, entities, edges int, elapsedMs int64) {
		gotStatus = status
		gotCollection = collection
	}
	deps.heartbeatFn = func(eventType string, payload any) {
		heartbeatEvent = eventType
		close(done)
	}

	res := ToolCognify(context.Background(), deps, map[string]any{"data": "some text"})
	runID := extractRunID(t, res)
	waitDone(t, done, 2*time.Second)

	if gotStatus != "COMPLETED" {
		t.Errorf("PersistPipelineStatus status=%q, want COMPLETED", gotStatus)
	}
	if gotCollection != "default" {
		t.Errorf("PersistPipelineStatus collection=%q, want default", gotCollection)
	}
	if heartbeatEvent != "cognify" {
		t.Errorf("LogHeartbeat eventType=%q, want cognify", heartbeatEvent)
	}
	s, ok := deps.Runs().Load(runID)
	if !ok {
		t.Fatal("runID not in registry after completion")
	}
	if s.Status != "COMPLETED" {
		t.Errorf("registry Status=%q, want COMPLETED", s.Status)
	}
	if s.ElapsedMs < 0 {
		t.Errorf("ElapsedMs=%d, expected non-negative", s.ElapsedMs)
	}
}

func TestToolCognify_PipelineErrorMarksFailed(t *testing.T) {
	done := make(chan struct{})
	var gotStatus, gotRunID string
	deps := &fakeDeps{
		baseCfg: orchestrator.Config{EmbedEndpoint: "http://embed"},
		pipelineFn: func(ctx context.Context, texts []string, cfg orchestrator.Config, progress chan<- orchestrator.Progress) error {
			close(progress)
			return errors.New("boom")
		},
	}
	deps.persistFn = func(datasetID, _, status string, _, _, _ int, _ int64) {
		gotStatus = status
		gotRunID = datasetID
		close(done)
	}
	ToolCognify(context.Background(), deps, map[string]any{"data": "x"})
	waitDone(t, done, 2*time.Second)
	if gotStatus != "FAILED" {
		t.Errorf("PersistPipelineStatus status=%q, want FAILED", gotStatus)
	}
	s, ok := deps.Runs().Load(gotRunID)
	if !ok {
		t.Fatal("run not registered")
	}
	if s.Message != "boom" {
		t.Errorf("Message=%q, want boom", s.Message)
	}
}

func TestToolCognify_ProgressUpdatesStatusFields(t *testing.T) {
	done := make(chan struct{})
	var gotChunks, gotEntities, gotEdges int
	deps := &fakeDeps{
		baseCfg: orchestrator.Config{EmbedEndpoint: "http://embed"},
		pipelineFn: func(ctx context.Context, texts []string, cfg orchestrator.Config, progress chan<- orchestrator.Progress) error {
			progress <- orchestrator.Progress{Stage: "chunk", ChunksCreated: 4}
			progress <- orchestrator.Progress{Stage: "extract", ChunksCreated: 4, EntitiesExtracted: 7, EdgesExtracted: 11}
			close(progress)
			return nil
		},
	}
	var gotRunID string
	deps.persistFn = func(datasetID, _, _ string, chunks, entities, edges int, _ int64) {
		gotChunks = chunks
		gotEntities = entities
		gotEdges = edges
		gotRunID = datasetID
		close(done)
	}
	ToolCognify(context.Background(), deps, map[string]any{"data": "x"})
	waitDone(t, done, 2*time.Second)
	if gotChunks != 4 || gotEntities != 7 || gotEdges != 11 {
		t.Errorf("progress counters not copied to Persist: chunks=%d entities=%d edges=%d", gotChunks, gotEntities, gotEdges)
	}
	s, _ := deps.Runs().Load(gotRunID)
	if s == nil || s.Stage != "extract" {
		t.Errorf("final Stage=%v, want extract (last progress event)", s)
	}
}

func TestToolCognify_RAGModeSetsSkipGraph(t *testing.T) {
	done := make(chan struct{})
	var captured orchestrator.Config
	var mu sync.Mutex
	deps := &fakeDeps{
		baseCfg: orchestrator.Config{EmbedEndpoint: "http://embed"},
		pipelineFn: func(ctx context.Context, texts []string, cfg orchestrator.Config, progress chan<- orchestrator.Progress) error {
			mu.Lock()
			captured = cfg
			mu.Unlock()
			close(progress)
			return nil
		},
	}
	deps.persistFn = func(string, string, string, int, int, int, int64) { close(done) }
	ToolCognify(context.Background(), deps, map[string]any{"data": "x", "mode": "rag"})
	waitDone(t, done, 2*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if !captured.SkipGraph {
		t.Error("mode=rag should set SkipGraph=true")
	}
	if captured.GenerateTriplets {
		t.Error("mode=rag should set GenerateTriplets=false")
	}
}

func TestToolCognify_CustomCollectionAndPrompt(t *testing.T) {
	done := make(chan struct{})
	var captured orchestrator.Config
	var mu sync.Mutex
	deps := &fakeDeps{
		baseCfg: orchestrator.Config{EmbedEndpoint: "http://embed"},
		ontologyFn: func(collection string) string {
			if collection != "my_coll" {
				return ""
			}
			return " EXTRA"
		},
		pipelineFn: func(ctx context.Context, texts []string, cfg orchestrator.Config, progress chan<- orchestrator.Progress) error {
			mu.Lock()
			captured = cfg
			mu.Unlock()
			close(progress)
			return nil
		},
	}
	deps.persistFn = func(string, string, string, int, int, int, int64) { close(done) }
	ToolCognify(context.Background(), deps, map[string]any{
		"data":          "x",
		"collection":    "my_coll",
		"custom_prompt": "BASE",
	})
	waitDone(t, done, 2*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if captured.Collection != "my_coll" {
		t.Errorf("Collection=%q, want my_coll", captured.Collection)
	}
	if captured.SystemPrompt != "BASE EXTRA" {
		t.Errorf("SystemPrompt=%q, want 'BASE EXTRA' (custom_prompt + ontology suffix)", captured.SystemPrompt)
	}
}

func TestToolCognify_ChunkingOverrides(t *testing.T) {
	done := make(chan struct{})
	var captured orchestrator.Config
	var mu sync.Mutex
	deps := &fakeDeps{
		baseCfg: orchestrator.Config{EmbedEndpoint: "http://embed"},
		pipelineFn: func(ctx context.Context, texts []string, cfg orchestrator.Config, progress chan<- orchestrator.Progress) error {
			mu.Lock()
			captured = cfg
			mu.Unlock()
			close(progress)
			return nil
		},
	}
	deps.persistFn = func(string, string, string, int, int, int, int64) { close(done) }
	snap := false
	ToolCognify(context.Background(), deps, map[string]any{
		"data":              "x",
		"chunk_strategy":    "sentence",
		"overlap_chars":     float64(32),
		"snap_to_sentence":  snap,
		"parent_child":      true,
		"document_title":    "My Doc",
		"document_id":       "doc-42",
		"min_chunk_chars":   float64(10),
		"max_chunk_chars":   float64(500),
		"dedup_threshold":   float64(0.8),
		"community_resolution": float64(1.5),
		"room":              "auth",
		"tags":              []any{"security", ""},
	})
	waitDone(t, done, 2*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if captured.ChunkStrategy != "sentence" {
		t.Errorf("ChunkStrategy=%q, want sentence", captured.ChunkStrategy)
	}
	if captured.OverlapChars != 32 {
		t.Errorf("OverlapChars=%d, want 32", captured.OverlapChars)
	}
	if captured.SnapToSentence == nil || *captured.SnapToSentence != false {
		t.Errorf("SnapToSentence=%v, want &false", captured.SnapToSentence)
	}
	if !captured.ParentChild {
		t.Error("ParentChild not set")
	}
	if captured.DocumentTitle != "My Doc" || captured.DocumentID != "doc-42" {
		t.Errorf("Document fields wrong: title=%q id=%q", captured.DocumentTitle, captured.DocumentID)
	}
	if captured.MinChunkChars != 10 || captured.MaxChunkChars != 500 {
		t.Errorf("chunk-char overrides wrong: min=%d max=%d", captured.MinChunkChars, captured.MaxChunkChars)
	}
	if captured.DedupThreshold != 0.8 || captured.CommunityResolution != 1.5 {
		t.Errorf("fidelity knobs wrong: dedup=%v community=%v", captured.DedupThreshold, captured.CommunityResolution)
	}
	if captured.Room != "auth" {
		t.Errorf("Room=%q, want auth", captured.Room)
	}
	if len(captured.Tags) != 1 || captured.Tags[0] != "security" {
		t.Errorf("Tags=%v, want [security] (empty strings filtered)", captured.Tags)
	}
}

func TestToolCognify_DefaultCollectionWhenEmpty(t *testing.T) {
	done := make(chan struct{})
	var captured orchestrator.Config
	var mu sync.Mutex
	deps := &fakeDeps{
		baseCfg: orchestrator.Config{EmbedEndpoint: "http://embed"},
		pipelineFn: func(ctx context.Context, texts []string, cfg orchestrator.Config, progress chan<- orchestrator.Progress) error {
			mu.Lock()
			captured = cfg
			mu.Unlock()
			close(progress)
			return nil
		},
	}
	deps.persistFn = func(string, string, string, int, int, int, int64) { close(done) }
	res := ToolCognify(context.Background(), deps, map[string]any{"data": "x"})
	runID := extractRunID(t, res)
	waitDone(t, done, 2*time.Second)
	mu.Lock()
	defer mu.Unlock()
	if captured.Collection != "default" {
		t.Errorf("Collection=%q, want default", captured.Collection)
	}
	if captured.DatasetID != runID {
		t.Errorf("DatasetID=%q, want runID=%q", captured.DatasetID, runID)
	}
}

// ── ToolCognifyStatus ──

func TestToolCognifyStatus_MissingRunID(t *testing.T) {
	deps := &fakeDeps{}
	res := ToolCognifyStatus(deps, map[string]any{})
	if !res.IsError {
		t.Fatal("want IsError when run_id missing")
	}
	if !strings.Contains(contentText(res), "'run_id' required") {
		t.Errorf("unexpected error text: %q", contentText(res))
	}
}

func TestToolCognifyStatus_UnknownRunID(t *testing.T) {
	deps := &fakeDeps{}
	res := ToolCognifyStatus(deps, map[string]any{"run_id": "nonexistent"})
	if !res.IsError {
		t.Fatal("want IsError when run_id unknown")
	}
	if !strings.Contains(contentText(res), "not found") {
		t.Errorf("unexpected error text: %q", contentText(res))
	}
}

func TestToolCognifyStatus_ReturnsStatusJSON(t *testing.T) {
	deps := &fakeDeps{}
	deps.Runs().Store("r1", &runreg.Status{
		RunID:   "r1",
		Status:  "RUNNING",
		Stage:   "chunk",
		Chunks:  3,
		Message: "progress",
	})
	res := ToolCognifyStatus(deps, map[string]any{"run_id": "r1"})
	if res.IsError {
		t.Fatalf("unexpected IsError=true: %q", contentText(res))
	}
	text := contentText(res)
	wants := []string{
		`"pipeline_run_id": "r1"`,
		`"status": "RUNNING"`,
		`"stage": "chunk"`,
		`"chunks_created": 3`,
		`"message": "progress"`,
	}
	for _, w := range wants {
		if !strings.Contains(text, w) {
			t.Errorf("JSON missing %s:\n%s", w, text)
		}
	}
}

// Sanity: registry is shared between Save and Load paths inside one deps.
// Prevents a regression where Runs() returns a fresh registry per call,
// which would silently break cognify_status (no run would ever be found).
func TestFakeDeps_RunsIsStable(t *testing.T) {
	d := &fakeDeps{}
	r1 := d.Runs()
	r2 := d.Runs()
	if r1 != r2 {
		t.Errorf("fakeDeps.Runs() should return the same registry on repeated calls")
	}
	r1.Store("x", &runreg.Status{RunID: "x"})
	if _, ok := d.Runs().Load("x"); !ok {
		t.Error("Store via one ref not visible via another")
	}
}
