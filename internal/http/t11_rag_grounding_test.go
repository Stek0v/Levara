package http

import (
	"errors"
	"strings"
	"testing"
)

func TestT11RAGGroundingUsesActualContext(t *testing.T) {
	for _, tc := range []struct {
		name   string
		meta   map[string]any
		strict bool
		usable bool
	}{
		{"missing_text", map[string]any{}, true, false},
		{"wrong_text_type", map[string]any{"text": 42}, true, false},
		{"blank_text", map[string]any{"text": "   "}, true, false},
		{"default_empty_context", map[string]any{}, false, false},
		{"short_fact", map[string]any{"text": "Port is 42."}, true, true},
		{"entity_description", map[string]any{"name": "Lira", "description": "A passenger ferry departs at 22:44."}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newSearchTestEnv(t)
			env.cfg.DB = nil
			model := &recordingLLM{responses: []string{"supported answer"}}
			env.cfg.LLMProvider = model
			t.Setenv("LLM_ENDPOINT", "http://unused.test")
			t.Setenv("LLM_MODEL", "fixture")
			t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD", "0")
			env.insertVector("entities", "source", []float32{1, 0, 0, 0}, tc.meta)
			env.start()
			status, body := env.postSearch(map[string]any{"query_text": "fact?", "query_type": "RAG_COMPLETION", "collection": "entities", "strict_grounded": tc.strict})
			if status != 200 {
				t.Fatalf("status=%d body=%v", status, body)
			}
			ids, _ := body["evidence_ids"].([]any)
			prompts := model.promptsSnapshot()
			if !tc.usable {
				if body["abstained"] != true || len(ids) != 0 || len(prompts) != 0 {
					t.Fatalf("unusable context advertised/generated: %v prompts=%v", body, prompts)
				}
			} else if body["abstained"] != false || len(ids) != 1 || len(prompts) != 1 || !strings.Contains(prompts[0], "[1]") || strings.Contains(prompts[0], "Context:\n\n") {
				t.Fatalf("usable fact missing from grounded prompt: %v prompts=%v", body, prompts)
			} else if ids[0] != "source" {
				t.Fatalf("wrong evidence source: %v", ids)
			} else if text, ok := tc.meta["text"].(string); ok && !strings.Contains(prompts[0], text) {
				t.Fatalf("source text missing from actual prompt: %v", prompts)
			}
		})
	}
}

func TestT11RAGEvidenceIDsExcludeUnusedSources(t *testing.T) {
	env := newSearchTestEnv(t)
	env.cfg.DB = nil
	model := &recordingLLM{responses: []string{"Port is 42. [1]"}}
	env.cfg.LLMProvider = model
	t.Setenv("LLM_ENDPOINT", "http://unused.test")
	t.Setenv("LLM_MODEL", "fixture")
	t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD", "0")
	env.insertVector("entities", "unused", []float32{1, 0, 0, 0}, map[string]any{})
	env.insertVector("entities", "used", []float32{1, 0, 0, 0}, map[string]any{"text": "Port is 42."})
	env.start()
	_, body := env.postSearch(map[string]any{"query_text": "port?", "query_type": "RAG_COMPLETION", "collection": "entities", "strict_grounded": true})
	ids, _ := body["evidence_ids"].([]any)
	if len(ids) != 1 || ids[0] != "used" {
		t.Fatalf("citations include a source omitted from generation: %v", body)
	}
	prompts := model.promptsSnapshot()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "[1] Port is 42.") || strings.Contains(prompts[0], "[2]") {
		t.Fatalf("prompt and citation inventory disagree: %v", prompts)
	}
}

func TestT11RAGGenerationFailureIsNotSuccessfulAnswer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		answer string
	}{
		{"provider_error", errors.New("provider unavailable"), ""},
		{"empty_response", nil, ""},
		{"positive", nil, "Port is 42. [1]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newSearchTestEnv(t)
			env.cfg.DB = nil
			model := &recordingLLM{err: tc.err, responses: []string{tc.answer}}
			env.cfg.LLMProvider = model
			t.Setenv("LLM_ENDPOINT", "http://unused.test")
			t.Setenv("LLM_MODEL", "fixture")
			t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD", "0")
			env.insertVector("entities", "source", []float32{1, 0, 0, 0}, map[string]any{"text": "The fictional port number is 42."})
			env.start()
			status, body := env.postSearch(map[string]any{"query_text": "port?", "query_type": "RAG_COMPLETION", "collection": "entities", "strict_grounded": true})
			if status != 200 {
				t.Fatalf("status=%d body=%v", status, body)
			}
			if tc.answer == "" {
				if body["abstained"] != true || body["abstain_reason"] != "generation_unavailable" {
					t.Fatalf("failed generation reported success: %v", body)
				}
			} else if body["abstained"] != false || body["answer"] != tc.answer {
				t.Fatalf("positive answer lost: %v", body)
			}
		})
	}
}
