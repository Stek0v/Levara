package http

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stek0v/levara/pkg/llm"
)

// This opt-in records real local answers through native RAG. Retrieval is a
// controlled vector fixture; the separate frozen corpus run measures embeddings.
func TestT11NativeRAGLocalQuality(t *testing.T) {
	if os.Getenv("LEVARA_T11_LOCAL_MODEL") != "gemma4:e2b" {
		t.Skip("explicit installed local model required")
	}
	var fixture struct {
		Facts []struct{ Key, Value string }
		Cases []struct {
			ID, Query    string
			ExpectedKeys []string `json:"expected_keys"`
		}
	}
	raw, err := os.ReadFile("../../benchmark/factual_quality_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	facts := map[string]string{}
	for _, f := range fixture.Facts {
		facts[f.Key] = f.Value
	}
	selected := map[string]bool{"q001": true, "q032": true, "q033": true, "q037": true, "q038": true, "q039": true}
	executed := 0
	for _, c := range fixture.Cases {
		if !selected[c.ID] {
			continue
		}
		executed++
		t.Run(c.ID, func(t *testing.T) {
			env := newSearchTestEnv(t)
			env.cfg.DB = nil
			env.cfg.LLMProvider = llm.NewOpenAIProvider("http://127.0.0.1:11434/v1", "")
			t.Setenv("LLM_ENDPOINT", "http://127.0.0.1:11434/v1")
			t.Setenv("LLM_MODEL", "gemma4:e2b")
			t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD", "0")
			keys := c.ExpectedKeys
			if len(keys) == 0 {
				keys = []string{"f001"}
			}
			for _, key := range keys {
				env.insertVector("entities", key, []float32{1, 0, 0, 0}, map[string]any{"text": facts[key]})
			}
			env.start()
			status, body := env.postSearch(map[string]any{"query_text": c.Query, "query_type": "RAG_COMPLETION", "collection": "entities", "strict_grounded": true})
			ids, _ := body["evidence_ids"].([]any)
			if status != 200 || body["answer"] == "" || body["abstained"] != false || len(ids) != len(keys) {
				t.Fatalf("native model execution failed: status=%d body=%v", status, body)
			}
			b, err := json.Marshal(map[string]any{"case_id": c.ID, "query": c.Query, "source_keys": keys, "response": body})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("NATIVE_QUALITY %s", b)
		})
	}
	if executed != len(selected) {
		t.Fatalf("incomplete native quality coverage: %d/%d", executed, len(selected))
	}
}
