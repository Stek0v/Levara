package http

import (
	"testing"
)

func TestMCPSearchCapabilitiesMatchVectorAvailability(t *testing.T) {
	env := newSearchTestEnv(t)
	h := &mcpHandler{cfg: env.cfg}
	if !h.SearchCapabilities("").HasEmbedding {
		t.Fatal("configured vector pipeline was not advertised")
	}
	h.cfg.EmbedClient = nil
	if h.NewSearchPipeline(false) != nil || h.SearchCapabilities("").HasEmbedding {
		t.Fatal("missing embedding client advertised an unusable vector pipeline")
	}
}

func TestRAGSearchUnavailableBackendReportsStrategy(t *testing.T) {
	env := newSearchTestEnv(t)
	env.cfg.EmbedEndpoint = ""
	model := &recordingLLM{}
	env.cfg.LLMProvider = model
	env.start()
	status, raw := postSearchAny(t, env, map[string]any{
		"query_text": "what is the source?", "query_type": "RAG_COMPLETION",
	})
	result, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("unexpected response: %T %v", raw, raw)
	}
	if status != 200 || result["search_type"] != "RAG_COMPLETION" || result["abstained"] != true || result["answer"] != "" {
		t.Fatalf("unavailable backend must report its abstaining strategy: HTTP=%d result=%v", status, result)
	}
	if len(model.promptsSnapshot()) != 0 {
		t.Fatal("unavailable retrieval backend reached the model")
	}
}
