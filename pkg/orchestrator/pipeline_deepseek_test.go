package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/llm"
)

// Integration test against the real DeepSeek Chat API (OpenAI-compatible).
//
// Complements pipeline_test.go's mock-based tests — the mocks verify wiring,
// this verifies that real LLM output flowing through parseEntities / dedup /
// Progress plumbing still yields a sensible graph shape.
//
// Opt-in: reads the key from env var DEEPSEEK_API_KEY. Skipped in normal CI
// runs. Never commit the key. Run locally with:
//
//	DEEPSEEK_API_KEY=sk-... go test -run TestPipeline_DeepSeek -v \
//	                                 ./pkg/orchestrator/
//
// Cost: 1-2 chat completions per run, ~$0.0001. Latency: ~2-10s.
const (
	deepseekEndpoint = "https://api.deepseek.com/v1"
	deepseekModel    = "deepseek-chat"
	// defaultExtractionPrompt already lives in pipeline.go — we intentionally
	// pass SystemPrompt="" to exercise that default path.
)

func deepseekProvider(t *testing.T) llm.Provider {
	t.Helper()
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		t.Skip("DEEPSEEK_API_KEY not set — skipping live API integration test")
	}
	return llm.NewOpenAIProvider(deepseekEndpoint, key)
}

// TestPipeline_DeepSeek_ExtractsGraph runs the cognify pipeline against
// DeepSeek with a compact factual text. Loose assertions (ranges, not exact
// counts) because LLM output varies run-to-run.
func TestPipeline_DeepSeek_ExtractsGraph(t *testing.T) {
	provider := deepseekProvider(t)

	// Short factual passage — low tokens, stable entities. Easy for the LLM
	// to extract Person/Thing/Relation without ambiguity.
	text := `Marie Curie was a Polish-French physicist. She was born in Warsaw in 1867.
She discovered the elements polonium and radium together with her husband Pierre Curie.
In 1903 Marie Curie won the Nobel Prize in Physics.`

	cfg := Config{
		ChunkStrategy:  "merged",
		MinChunkChars:  1,
		MaxChunkChars:  9999,
		LLMConcurrency: 1,
		LLMModel:       deepseekModel,
		Temperature:    0.0,
		LLMProvider:    provider,
		// Keep integrations off so failure is localised to the LLM path.
		LLMEndpoint:   "", // bypass StructuredCall JSON-Schema branch, use provider fallback
		EmbedEndpoint: "",
	}
	falseB := false
	cfg.UseStructuredOutput = &falseB

	progressCh := make(chan Progress, 32)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var runErr error
	done := make(chan struct{})
	go func() {
		runErr = Run(ctx, []string{text}, cfg, progressCh)
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
	final := events[len(events)-1]
	if final.Stage != "complete" {
		t.Fatalf("final stage = %q, want complete; events: %+v", final.Stage, events)
	}
	if final.ItemsProcessed != 1 {
		t.Errorf("ItemsProcessed = %d, want 1", final.ItemsProcessed)
	}
	// Loose shape check: DeepSeek should find at least 2 entities and at least
	// 1 edge for this text. Exact counts vary.
	if final.EntitiesExtracted < 2 {
		t.Errorf("EntitiesExtracted = %d, want >= 2", final.EntitiesExtracted)
	}
	if final.EdgesExtracted < 1 {
		t.Errorf("EdgesExtracted = %d, want >= 1", final.EdgesExtracted)
	}

	t.Logf("DeepSeek produced: %d entities, %d edges, %d chunks, elapsed=%dms",
		final.EntitiesExtracted, final.EdgesExtracted, final.ChunksCreated, final.ElapsedMs)
}

// TestPipeline_DeepSeek_StructuredOutput exercises the StructuredCall
// JSON-Schema branch end-to-end. DeepSeek supports response_format=json_object
// but not full json_schema — the code path tries schema mode, may fall back,
// and should still succeed. We assert success, not which path was taken.
func TestPipeline_DeepSeek_StructuredOutput(t *testing.T) {
	provider := deepseekProvider(t)

	text := "Ada Lovelace wrote the first computer algorithm in 1843."

	cfg := Config{
		ChunkStrategy:  "merged",
		MinChunkChars:  1,
		MaxChunkChars:  9999,
		LLMConcurrency: 1,
		LLMModel:       deepseekModel,
		Temperature:    0.0,
		LLMProvider:    provider,
		LLMEndpoint:    deepseekEndpoint, // enable StructuredCall branch
		// UseStructuredOutput left nil => defaults to true
	}

	progressCh := make(chan Progress, 32)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- Run(ctx, []string{text}, cfg, progressCh) }()

	var final Progress
	for p := range progressCh {
		final = p
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final.Stage != "complete" {
		t.Fatalf("final stage = %q, want complete", final.Stage)
	}
	// At least 1 entity expected — even conservative models find "Ada Lovelace".
	if final.EntitiesExtracted < 1 {
		t.Errorf("EntitiesExtracted = %d, want >= 1", final.EntitiesExtracted)
	}
}

// Sanity smoke: direct provider call, bypassing the pipeline. Useful to
// localise failure when the pipeline test above breaks — is it the API, or
// is it the orchestrator?
func TestDeepSeek_ProviderSmoke(t *testing.T) {
	provider := deepseekProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := provider.ChatCompletion(ctx, llm.CompletionRequest{
		Model: deepseekModel,
		Messages: []llm.Message{
			{Role: "system", Content: "Reply with the single word OK."},
			{Role: "user", Content: "Ping."},
		},
		Temperature: 0.0,
		MaxTokens:   10,
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.Content == "" {
		t.Error("empty content from DeepSeek")
	}
	if !strings.Contains(strings.ToUpper(resp.Content), "OK") {
		t.Logf("response did not contain OK (model drift?): %q", resp.Content)
	}
	t.Logf("DeepSeek smoke: model=%s, tokens=%d/%d, content=%q",
		resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Content)
}
