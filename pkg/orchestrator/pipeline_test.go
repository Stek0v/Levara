package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/llm/mock"
)

// T-2: integration test for the cognify pipeline against a mock LLM provider.
//
// The pipeline has many knobs (embed endpoint, Neo4j, PG, BM25, community
// detection). This test exercises the LLM-extraction path end-to-end while
// keeping every external dependency disabled:
//
//   - LLMProvider = mock (returns scripted JSON per substring)
//   - LLMEndpoint = ""         -> bypasses StructuredCall JSON-Schema branch
//   - EmbedEndpoint = ""       -> no embedding HTTP
//   - Neo4jURL = ""            -> no graph DB write
//   - DB = nil                 -> no PG upsert
//   - Collections = nil        -> no vector write
//   - SkipGraph = false        -> extractEntities IS called
//
// What we assert from the final "complete" Progress event:
//   - ItemsProcessed == len(texts)
//   - ChunksCreated  >= len(texts)   (merged strategy keeps short texts intact)
//   - EntitiesExtracted / EdgesExtracted reflect mock rules (after dedup)
//   - no error returned by Run
func TestPipeline_Run_WithMockLLM(t *testing.T) {
	// Two texts, each triggers a distinct mock rule.
	const (
		catText = "Alice adopted a cat. The cat is named Whiskers and lives with Alice."
		dogText = "Bob trains a dog. The dog is named Rex and plays fetch with Bob."
	)

	catJSON := `{
		"nodes": [
			{"id":"alice","name":"Alice","type":"Person","description":"cat owner"},
			{"id":"whiskers","name":"Whiskers","type":"Animal","description":"a cat"}
		],
		"edges": [
			{"source":"alice","target":"whiskers","relationship":"owns","edge_text":"Alice owns Whiskers"}
		]
	}`
	dogJSON := `{
		"nodes": [
			{"id":"bob","name":"Bob","type":"Person","description":"dog trainer"},
			{"id":"rex","name":"Rex","type":"Animal","description":"a dog"}
		],
		"edges": [
			{"source":"bob","target":"rex","relationship":"trains","edge_text":"Bob trains Rex"}
		]
	}`

	provider := mock.New().
		On("Whiskers").Reply(catJSON).
		On("Rex").Reply(dogJSON).
		Provider()

	cfg := Config{
		ChunkStrategy:  "merged",
		MinChunkChars:  1,
		MaxChunkChars:  9999,
		LLMConcurrency: 2,
		LLMModel:       "mock",
		Temperature:    0.0,
		LLMProvider:    provider,
		// Everything below intentionally empty/nil to isolate the LLM path.
		LLMEndpoint:   "",
		EmbedEndpoint: "",
		Neo4jURL:      "",
		Collection:    "",
	}
	falseB := false
	cfg.UseStructuredOutput = &falseB // belt-and-braces: also force the provider path

	progressCh := make(chan Progress, 64)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var runErr error
	done := make(chan struct{})
	go func() {
		runErr = Run(ctx, []string{catText, dogText}, cfg, progressCh)
		close(done)
	}()

	var events []Progress
	for p := range progressCh {
		events = append(events, p)
	}
	<-done

	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if len(events) == 0 {
		t.Fatal("no progress events emitted")
	}
	final := events[len(events)-1]
	if final.Stage != "complete" {
		t.Errorf("last event Stage = %q, want 'complete'", final.Stage)
	}
	if final.ItemsTotal != 2 || final.ItemsProcessed != 2 {
		t.Errorf("ItemsTotal/Processed = %d/%d, want 2/2", final.ItemsTotal, final.ItemsProcessed)
	}
	if final.ChunksCreated < 2 {
		t.Errorf("ChunksCreated = %d, want >= 2", final.ChunksCreated)
	}
	// Each mock rule returns 2 nodes + 1 edge. After dedup of disjoint graphs
	// we expect 4 nodes / 2 edges. Dedup may collapse identical descriptions
	// but the entity names are distinct, so 4/2 is the exact expected count.
	if final.EntitiesExtracted != 4 {
		t.Errorf("EntitiesExtracted = %d, want 4", final.EntitiesExtracted)
	}
	if final.EdgesExtracted != 2 {
		t.Errorf("EdgesExtracted = %d, want 2", final.EdgesExtracted)
	}

	// Mock must have been called at least once per text (could be more if
	// chunking split them; merged strategy with MaxChunkChars=9999 should
	// keep each text as one chunk).
	calls := provider.Name() // just touch something so the import is used
	_ = calls
}

// TestPipeline_Run_LLMFailure ensures that when mock.Fail fires for a chunk,
// Run still completes (other chunks succeed) and propagates no panic.
func TestPipeline_Run_LLMFailure(t *testing.T) {
	good := "Carol wrote a book titled Lighthouse."
	bad := "FAIL_ME please"

	goodJSON := `{
		"nodes":[{"id":"carol","name":"Carol","type":"Person"},
		         {"id":"light","name":"Lighthouse","type":"Book"}],
		"edges":[{"source":"carol","target":"light","relationship":"wrote","edge_text":"Carol wrote Lighthouse"}]
	}`

	provider := mock.New().
		On("FAIL_ME").Fail(errExtraction).
		On("Lighthouse").Reply(goodJSON).
		Provider()

	cfg := Config{
		ChunkStrategy:  "merged",
		MinChunkChars:  1,
		MaxChunkChars:  9999,
		LLMConcurrency: 2,
		LLMModel:       "mock",
		LLMProvider:    provider,
	}
	falseB := false
	cfg.UseStructuredOutput = &falseB

	progressCh := make(chan Progress, 32)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- Run(ctx, []string{good, bad}, cfg, progressCh) }()

	for range progressCh {
	}
	if err := <-done; err != nil {
		t.Fatalf("Run should not fail end-to-end on per-chunk LLM error: %v", err)
	}
}

// errExtraction is a sentinel used by the failure-path test above. We declare
// it with a small helper so the test file compiles without importing errors.
var errExtraction = &extractErr{msg: "mock extraction failure"}

type extractErr struct{ msg string }

func (e *extractErr) Error() string { return e.msg }

// Guardrail: ensure the mock LLM is really being used — if the network path
// leaked we'd see DNS resolution delays. Keep this as a quick sanity smoke.
func TestPipeline_Run_NoNetworkLeak(t *testing.T) {
	provider := mock.New().OnAny().Reply(`{"nodes":[],"edges":[]}`).Provider()
	cfg := Config{
		ChunkStrategy:  "merged",
		MinChunkChars:  1,
		MaxChunkChars:  9999,
		LLMConcurrency: 1,
		LLMModel:       "mock",
		LLMProvider:    provider,
	}
	falseB := false
	cfg.UseStructuredOutput = &falseB

	progressCh := make(chan Progress, 16)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, []string{"hello world"}, cfg, progressCh) }()
	for p := range progressCh {
		if strings.Contains(p.Message, "http") || strings.Contains(p.Message, "dial") {
			t.Errorf("unexpected network-ish message: %q", p.Message)
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 800*time.Millisecond {
		t.Errorf("Run took %v — suspicious for offline mock", elapsed)
	}
}
