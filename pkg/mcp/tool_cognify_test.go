package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/orchestrator"
	"github.com/stek0v/levara/pkg/runreg"
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

// extractRunID pulls the run ID out of the structured success payload.
func extractRunID(t *testing.T, res ToolResult) string {
	t.Helper()
	if res.IsError {
		t.Fatalf("ToolCognify returned IsError=true; content=%q", contentText(res))
	}
	var started struct {
		RunID  string `json:"pipeline_run_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(contentText(res)), &started); err != nil || started.RunID == "" || started.Status != "RUNNING" {
		t.Fatalf("invalid start payload: %q err=%v", contentText(res), err)
	}
	return started.RunID
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
	if len(deps.Runs().Snapshot()) != 0 {
		t.Fatal("configuration failure left a source-less run")
	}
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
	deps.persistFn = func(datasetID, _, collection, status string, _ int64, _ string, chunks, entities, edges int, elapsedMs int64) {
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
	deps.persistFn = func(datasetID, _, _, status string, _ int64, _ string, _, _, _ int, _ int64) {
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
	deps.persistFn = func(datasetID, _, _, _ string, _ int64, _ string, chunks, entities, edges int, _ int64) {
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
	deps.persistFn = func(string, string, string, string, int64, string, int, int, int, int64) { close(done) }
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
	deps.persistFn = func(string, string, string, string, int64, string, int, int, int, int64) { close(done) }
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
	deps.persistFn = func(string, string, string, string, int64, string, int, int, int, int64) { close(done) }
	snap := false
	ToolCognify(context.Background(), deps, map[string]any{
		"data":                 "x",
		"chunk_strategy":       "sentence",
		"overlap_chars":        float64(32),
		"snap_to_sentence":     snap,
		"parent_child":         true,
		"document_title":       "My Doc",
		"document_id":          "doc-42",
		"min_chunk_chars":      float64(10),
		"max_chunk_chars":      float64(500),
		"dedup_threshold":      float64(0.8),
		"community_resolution": float64(1.5),
		"room":                 "auth",
		"tags":                 []any{"security", ""},
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
	if captured.DocumentTitle != "My Doc" || captured.DocumentID != "" {
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
	deps.persistFn = func(string, string, string, string, int64, string, int, int, int, int64) { close(done) }
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

// ctxWithUser tags a context with the MCP user id the same way the HTTP
// handler does on auth, so extractOwnerID(ctx) resolves to owner.
func ctxWithUser(owner string) context.Context {
	return context.WithValue(context.Background(), UserIDKey, owner)
}

func TestCognifyPreparesServerSourceBeforeRunVisibility(t *testing.T) {
	done := make(chan struct{})
	deps := &fakeDeps{baseCfg: orchestrator.Config{EmbedEndpoint: "http://embed"}}
	deps.prepareFn = func(_ context.Context, texts []string, cfg orchestrator.Config) (orchestrator.Config, error) {
		if len(deps.Runs().Snapshot()) != 0 {
			t.Fatal("run became visible before source preparation")
		}
		if len(texts) != 1 || texts[0] != "private" {
			t.Fatalf("texts=%v", texts)
		}
		cfg.DatasetID, cfg.DocumentID = "server-dataset", "server-document"
		cfg.ContentRevision, cfg.SourceRevision = 1, 7
		cfg.RawContentHash = strings.Repeat("a", 64)
		return cfg, nil
	}
	deps.persistFn = func(string, string, string, string, int64, string, int, int, int, int64) { close(done) }
	result := ToolCognify(ctxWithUser("alice"), deps, map[string]any{"data": "private", "document_id": "forged"})
	if result.IsError {
		t.Fatal(result)
	}
	snapshot := deps.Runs().Snapshot()
	if len(snapshot) != 1 || snapshot[0].DatasetID != "server-dataset" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	var proof []map[string]any
	if err := json.Unmarshal([]byte(snapshot[0].SourcesJSON), &proof); err != nil || len(proof) != 1 || proof[0]["document_id"] != "server-document" {
		t.Fatalf("proof=%v err=%v", proof, err)
	}
	waitDone(t, done, 2*time.Second)
}

func TestCognifyPreparationFailureLeavesNoRun(t *testing.T) {
	deps := &fakeDeps{baseCfg: orchestrator.Config{EmbedEndpoint: "http://embed"}}
	deps.prepareFn = func(context.Context, []string, orchestrator.Config) (orchestrator.Config, error) {
		return orchestrator.Config{}, errors.New("source write failed")
	}
	if result := ToolCognify(ctxWithUser("alice"), deps, map[string]any{"data": "private"}); !result.IsError {
		t.Fatalf("result=%+v", result)
	}
	if len(deps.Runs().Snapshot()) != 0 {
		t.Fatal("failed source preparation left a visible run")
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

// T9: stage transitions are recorded in Status.Events plus a synthesized
// terminal entry. Repeated progress events for the same stage must not
// double-emit — callers iterate Events to render a stage-by-stage timeline
// and would render duplicates if the dedup logic regressed.
func TestToolCognify_StageTransitionsRecorded(t *testing.T) {
	done := make(chan struct{})
	var gotRunID string
	deps := &fakeDeps{
		baseCfg: orchestrator.Config{EmbedEndpoint: "http://embed"},
		pipelineFn: func(ctx context.Context, texts []string, cfg orchestrator.Config, progress chan<- orchestrator.Progress) error {
			progress <- orchestrator.Progress{Stage: "chunk", ChunksCreated: 2}
			progress <- orchestrator.Progress{Stage: "chunk", ChunksCreated: 4} // dup stage
			progress <- orchestrator.Progress{Stage: "embed", ChunksCreated: 4}
			progress <- orchestrator.Progress{Stage: "write", ChunksCreated: 4, EntitiesExtracted: 3}
			close(progress)
			return nil
		},
	}
	deps.persistFn = func(datasetID, _, _, _ string, _ int64, _ string, _, _, _ int, _ int64) {
		gotRunID = datasetID
	}
	deps.heartbeatFn = func(string, any) { close(done) }

	ToolCognify(context.Background(), deps, map[string]any{"data": "x"})
	waitDone(t, done, 2*time.Second)

	s, ok := deps.Runs().Load(gotRunID)
	if !ok {
		t.Fatal("run not registered")
	}
	// Expect: chunk, embed, write, COMPLETED (terminal). Duplicate "chunk"
	// progress event must not produce a second chunk entry.
	wantStages := []string{"chunk", "embed", "write", "COMPLETED"}
	if len(s.Events) != len(wantStages) {
		t.Fatalf("Events len=%d, want %d (%v)", len(s.Events), len(wantStages), s.Events)
	}
	for i, w := range wantStages {
		if s.Events[i].Stage != w {
			t.Errorf("Events[%d].Stage=%q, want %q", i, s.Events[i].Stage, w)
		}
	}
	if !s.Events[len(s.Events)-1].Terminal {
		t.Error("last event should have Terminal=true")
	}
	if s.Events[2].Chunks != 4 {
		t.Errorf("write event Chunks=%d, want 4", s.Events[2].Chunks)
	}
}

func TestToolCognify_TerminalEventOnFailure(t *testing.T) {
	done := make(chan struct{})
	var gotRunID string
	deps := &fakeDeps{
		baseCfg: orchestrator.Config{EmbedEndpoint: "http://embed"},
		pipelineFn: func(ctx context.Context, texts []string, cfg orchestrator.Config, progress chan<- orchestrator.Progress) error {
			progress <- orchestrator.Progress{Stage: "chunk"}
			close(progress)
			return errors.New("boom")
		},
	}
	deps.persistFn = func(datasetID, _, _, _ string, _ int64, _ string, _, _, _ int, _ int64) { gotRunID = datasetID }
	deps.heartbeatFn = func(string, any) { close(done) }

	ToolCognify(context.Background(), deps, map[string]any{"data": "x"})
	waitDone(t, done, 2*time.Second)

	s, _ := deps.Runs().Load(gotRunID)
	if s == nil || len(s.Events) == 0 {
		t.Fatalf("expected events, got %v", s)
	}
	last := s.Events[len(s.Events)-1]
	if last.Stage != "FAILED" || !last.Terminal {
		t.Errorf("terminal event=%+v, want stage=FAILED terminal=true", last)
	}
	if last.Message != "boom" {
		t.Errorf("terminal Message=%q, want boom", last.Message)
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

func TestCognifyBackgroundRetainsCallerAndHasDeadline(t *testing.T) {
	t.Setenv("BACKGROUND_TASK_TIMEOUT_MS", "1000")
	done := make(chan struct{})
	deps := &fakeDeps{baseCfg: orchestrator.Config{EmbedEndpoint: "http://embed"}}
	deps.pipelineFn = func(ctx context.Context, texts []string, cfg orchestrator.Config, progress chan<- orchestrator.Progress) error {
		defer close(progress)
		if extractOwnerID(ctx) != "alice" || ctx.Value(TenantIDKey) != "tenant-a" {
			t.Error("background lost verified scope")
		}
		if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > time.Second {
			t.Error("background is unbounded")
		}
		if ctx.Err() != nil {
			t.Error("request cancellation killed detached pipeline")
		}
		return nil
	}
	deps.heartbeatFn = func(string, any) { close(done) }
	ctx, cancel := context.WithCancel(context.WithValue(ctxWithUser("alice"), TenantIDKey, "tenant-a"))
	cancel()
	if result := ToolCognify(ctx, deps, map[string]any{"data": "private"}); result.IsError {
		t.Fatal(result)
	}
	waitDone(t, done, 2*time.Second)
}
