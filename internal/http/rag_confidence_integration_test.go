package http

import (
	"testing"
)

func TestRAGCompletionSearch_ResponseIncludesConfidenceAndDebug(t *testing.T) {
	env := newSearchTestEnv(t)
	llm := &recordingLLM{responses: []string{"Alice writes code."}}
	env.cfg.LLMProvider = llm
	t.Setenv("LLM_ENDPOINT", "http://unused.test")
	t.Setenv("LLM_MODEL", "unused-model")
	t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD", "0")
	env.start()

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{
		"name": "Alice", "text": "Alice writes code in Go",
	})

	status, body := env.postSearch(map[string]any{
		"query_text": "who writes code",
		"query_type": "RAG_COMPLETION",
		"collection": "entities",
	})
	if status != 200 {
		t.Fatalf("status=%d, want 200", status)
	}
	if body["answer"] != "Alice writes code." {
		t.Fatalf("answer=%v, want scripted response", body["answer"])
	}
	if body["abstained"] != false {
		t.Fatalf("abstained=%v, want false", body["abstained"])
	}
	if conf, ok := body["confidence"].(float64); !ok || conf <= 0 {
		t.Fatalf("confidence=%v, want positive float64", body["confidence"])
	}
	if _, ok := body["confidence_breakdown"].(map[string]any); !ok {
		t.Fatalf("confidence_breakdown missing or wrong type: %#v", body["confidence_breakdown"])
	}
	if ids, ok := body["evidence_ids"].([]any); !ok || len(ids) == 0 {
		t.Fatalf("evidence_ids missing/empty: %#v", body["evidence_ids"])
	}

	debug, ok := body["debug"].(map[string]any)
	if !ok {
		t.Fatalf("debug block missing: %#v", body["debug"])
	}
	if debug["source"] != "explicit" {
		t.Fatalf("debug.source=%v, want explicit", debug["source"])
	}
}

func TestRAGCompletionSearch_EmptyConfigIncludesAbstainReasonAndDebug(t *testing.T) {
	env := newSearchTestEnv(t)
	env.cfg.EmbedEndpoint = ""
	env.cfg.Collections = nil
	env.start()

	status, body := env.postSearch(map[string]any{
		"query_text": "anything",
		"query_type": "RAG_COMPLETION",
	})
	if status != 200 {
		t.Fatalf("status=%d, want 200", status)
	}
	if body["abstained"] != true {
		t.Fatalf("abstained=%v, want true", body["abstained"])
	}
	if body["abstain_reason"] != "embedding backend unavailable" {
		t.Fatalf("abstain_reason=%v", body["abstain_reason"])
	}
	if body["confidence"] != float64(0) {
		t.Fatalf("confidence=%v, want 0", body["confidence"])
	}
	debug, ok := body["debug"].(map[string]any)
	if !ok || debug["source"] != "explicit" {
		t.Fatalf("debug=%v, want source=explicit", body["debug"])
	}
}

func TestRAGCompletionSearch_AbstainsAtHighThreshold(t *testing.T) {
	env := newSearchTestEnv(t)
	llm := &recordingLLM{responses: []string{"should not be used"}}
	env.cfg.LLMProvider = llm
	t.Setenv("LLM_ENDPOINT", "http://unused.test")
	t.Setenv("LLM_MODEL", "unused-model")
	t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD", "0.95")
	env.start()

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{
		"name": "Alice", "text": "Alice writes code in Go",
	})

	_, body := env.postSearch(map[string]any{
		"query_text": "who writes code",
		"query_type": "RAG_COMPLETION",
		"collection": "entities",
	})
	if body["abstained"] != true {
		t.Fatalf("abstained=%v, want true", body["abstained"])
	}
	if body["answer"] != defaultAbstainMessage {
		t.Fatalf("answer=%v, want abstain message", body["answer"])
	}
	if got := len(llm.promptsSnapshot()); got != 0 {
		t.Fatalf("llm prompts=%d, want 0 when abstained", got)
	}
}

func TestRAGCompletionSearch_UsesPerTypeThresholdOverride(t *testing.T) {
	env := newSearchTestEnv(t)
	llm := &recordingLLM{responses: []string{"should not be used"}}
	env.cfg.LLMProvider = llm
	t.Setenv("LLM_ENDPOINT", "http://unused.test")
	t.Setenv("LLM_MODEL", "unused-model")
	t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD", "0.10")
	t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD_RAG_COMPLETION", "0.95")
	env.start()

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"text": "Go is a programming language"})

	_, body := env.postSearch(map[string]any{
		"query_text": "what is go language?",
		"query_type": "RAG_COMPLETION",
		"collection": "entities",
	})
	if body["threshold"] != 0.95 {
		t.Fatalf("threshold=%v, want 0.95 (per-type override)", body["threshold"])
	}
	if body["abstained"] != true {
		t.Fatalf("abstained=%v, want true", body["abstained"])
	}
}

func TestRAGCompletionSearch_StrictGroundedAbstainsWithoutEvidence(t *testing.T) {
	env := newSearchTestEnv(t)
	llm := &recordingLLM{responses: []string{"should not be used"}}
	env.cfg.LLMProvider = llm
	t.Setenv("LLM_ENDPOINT", "http://unused.test")
	t.Setenv("LLM_MODEL", "unused-model")
	t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD", "0")
	env.start()

	_, body := env.postSearch(map[string]any{
		"query_text":      "what is go language?",
		"query_type":      "RAG_COMPLETION",
		"collection":      "entities",
		"strict_grounded": true,
	})
	if body["abstained"] != true {
		t.Fatalf("abstained=%v, want true", body["abstained"])
	}
	if body["abstain_reason"] != "strict_grounded_no_evidence" {
		t.Fatalf("abstain_reason=%v, want strict_grounded_no_evidence", body["abstain_reason"])
	}
	if got := len(llm.promptsSnapshot()); got != 0 {
		t.Fatalf("llm prompts=%d, want 0 when strict grounded abstains", got)
	}
}

func TestRAGCompletionSearch_VerificationFiltersLowScore(t *testing.T) {
	env := newSearchTestEnv(t)
	llm := &recordingLLM{responses: []string{"verified answer"}}
	env.cfg.LLMProvider = llm
	t.Setenv("LLM_ENDPOINT", "http://unused.test")
	t.Setenv("LLM_MODEL", "unused-model")
	t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD", "0")
	env.start()

	env.insertVector("entities", "high", []float32{1, 0, 0, 0}, map[string]any{"text": "high relevance"})
	env.insertVector("entities", "low", []float32{0, 1, 0, 0}, map[string]any{"text": "low relevance"})

	_, body := env.postSearch(map[string]any{
		"query_text":     "relevance",
		"query_type":     "RAG_COMPLETION",
		"collection":     "entities",
		"top_k":          10,
		"verify_results": true,
		"min_score":      0.5,
	})

	ids, _ := body["evidence_ids"].([]any)
	if len(ids) != 1 {
		t.Fatalf("evidence_ids len=%d, want 1 after min_score filter", len(ids))
	}
	verif, ok := body["verification"].(map[string]any)
	if !ok {
		t.Fatalf("verification missing: %#v", body["verification"])
	}
	if verif["dropped_low_score"] == float64(0) {
		t.Fatalf("verification.dropped_low_score=%v, want >0", verif["dropped_low_score"])
	}
}

func TestRAGCompletionSearch_RoutedDebugMetadata(t *testing.T) {
	env := newSearchTestEnv(t)
	llm := &recordingLLM{responses: []string{"Go is a language."}}
	env.cfg.LLMProvider = llm
	t.Setenv("LLM_ENDPOINT", "http://unused.test")
	t.Setenv("LLM_MODEL", "unused-model")
	t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD", "0")
	env.start()

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"text": "Go is a programming language"})

	_, body := env.postSearch(map[string]any{
		"query_text": "what is go language?",
		"query_type": "AUTO",
		"collection": "entities",
	})
	if body["search_type"] != "RAG_COMPLETION" {
		t.Fatalf("search_type=%v, want RAG_COMPLETION", body["search_type"])
	}
	debug, ok := body["debug"].(map[string]any)
	if !ok {
		t.Fatalf("debug block missing")
	}
	if debug["source"] != "routed" {
		t.Fatalf("debug.source=%v, want routed", debug["source"])
	}
	if debug["strategy"] != "RAG_COMPLETION" {
		t.Fatalf("debug.strategy=%v, want RAG_COMPLETION", debug["strategy"])
	}
	if reason, _ := debug["reason"].(string); reason == "" {
		t.Fatalf("debug.reason empty, want non-empty")
	}
}

func TestGraphCompletionSearch_RoutedDebugMetadata(t *testing.T) {
	env := newSearchTestEnv(t)
	llm := &recordingLLM{responses: []string{"Alice knows Bob."}}
	env.cfg.LLMProvider = llm
	t.Setenv("LLM_ENDPOINT", "http://unused.test")
	t.Setenv("LLM_MODEL", "unused-model")
	env.start()

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"name": "Alice"})
	env.insertNode("n1", "Alice", "Person", "")
	env.insertNode("n2", "Bob", "Person", "")
	env.insertEdge("rel1", "n1", "n2", "KNOWS")

	_, body := env.postSearch(map[string]any{
		"query_text": "what is related to alice",
		"query_type": "AUTO",
		"collection": "entities",
	})

	if body["search_type"] != "GRAPH_COMPLETION" {
		t.Fatalf("search_type=%v, want GRAPH_COMPLETION", body["search_type"])
	}
	debug, ok := body["debug"].(map[string]any)
	if !ok {
		t.Fatalf("debug block missing: %#v", body["debug"])
	}
	if debug["source"] != "routed" {
		t.Fatalf("debug.source=%v, want routed", debug["source"])
	}
	if debug["strategy"] != "GRAPH_COMPLETION" {
		t.Fatalf("debug.strategy=%v, want GRAPH_COMPLETION", debug["strategy"])
	}
	if _, ok := debug["alternatives"].([]any); !ok {
		t.Fatalf("debug.alternatives missing or wrong type: %#v", debug["alternatives"])
	}
	if reason, _ := debug["reason"].(string); reason == "" {
		t.Fatalf("debug.reason empty, want non-empty")
	}
}

func TestGraphCompletionSearch_StrictGroundedAbstainsWithoutEvidence(t *testing.T) {
	env := newSearchTestEnv(t)
	llm := &recordingLLM{responses: []string{"should not be used"}}
	env.cfg.LLMProvider = llm
	t.Setenv("LLM_ENDPOINT", "http://unused.test")
	t.Setenv("LLM_MODEL", "unused-model")
	t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD", "0")
	env.start()

	_, body := env.postSearch(map[string]any{
		"query_text":      "what is related to alice",
		"query_type":      "GRAPH_COMPLETION",
		"collection":      "entities",
		"strict_grounded": true,
	})
	if body["abstained"] != true {
		t.Fatalf("abstained=%v, want true", body["abstained"])
	}
	if body["abstain_reason"] != "strict_grounded_no_evidence" {
		t.Fatalf("abstain_reason=%v, want strict_grounded_no_evidence", body["abstain_reason"])
	}
	if got := len(llm.promptsSnapshot()); got != 0 {
		t.Fatalf("llm prompts=%d, want 0 when strict grounded abstains", got)
	}
}
