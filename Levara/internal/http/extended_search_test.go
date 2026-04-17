// extended_search_test.go — Wave C coverage for the "extended" search
// handlers on top of vector + graph:
//
//   contextExtensionSearch     (GRAPH_COMPLETION_CONTEXT_EXTENSION, 2-hop)
//   cotSearch                  (GRAPH_COMPLETION_COT, chain-of-thought)
//   communityLocalSearch       (COMMUNITY_LOCAL)
//   communityGlobalSearch      (COMMUNITY_GLOBAL, map-reduce)
//   parseJSONStringArray       (pure helper used by cotSearch)
//
// All four handlers have cheap, deterministic fallback paths that dominate
// real-world traffic (any time embed/DB/LLM is missing). Those are the
// branches we cover here — the full happy path for every handler requires
// a live Neo4j and a capable LLM, both of which are out of scope for unit
// tests.
package http

import (
	"reflect"
	"strings"
	"testing"
)

// ── contextExtensionSearch ──

func TestContextExtensionSearch_EmptyConfig(t *testing.T) {
	env := newSearchTestEnv(t)
	env.cfg.EmbedEndpoint = ""
	env.cfg.Collections = nil
	env.start()

	status, body := env.postSearch(map[string]any{
		"query_text": "q",
		"query_type": "GRAPH_COMPLETION_CONTEXT_EXTENSION",
	})
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["search_type"] != "GRAPH_COMPLETION_CONTEXT_EXTENSION" {
		t.Errorf("search_type = %v", body["search_type"])
	}
}

// Postgres path populates hop1 context via graphContextFromPostgres, but
// the postgres helper does NOT return target names, so hop2 stays empty.
// We verify that explicitly — the 2-hop feature only works against Neo4j.
func TestContextExtensionSearch_PostgresHop1Only(t *testing.T) {
	env := newSearchTestEnv(t)
	t.Setenv("LLM_ENDPOINT", "")
	t.Setenv("LLM_MODEL", "")
	env.start()

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"name": "Alice"})
	env.insertNode("n1", "Alice", "Person", "")
	env.insertNode("n2", "Bob", "Person", "")
	env.insertEdge("r1", "n1", "n2", "KNOWS")

	_, body := env.postSearch(map[string]any{
		"query_text": "alice",
		"query_type": "GRAPH_COMPLETION_CONTEXT_EXTENSION",
		"collection": "entities",
	})

	hop1, _ := body["context_hop1"].([]any)
	hop2, _ := body["context_hop2"].([]any)
	if len(hop1) != 1 {
		t.Fatalf("hop1 = %v (len=%d), want 1", hop1, len(hop1))
	}
	if len(hop2) != 0 {
		t.Errorf("hop2 = %v, want empty (postgres backend has no targets)", hop2)
	}
	if body["hops"] != float64(2) {
		t.Errorf("hops = %v, want 2", body["hops"])
	}
}

// With LLM provider wired, contextExtension must call it with a prompt
// that labels the 1-hop vs 2-hop context sections.
func TestContextExtensionSearch_CallsLLMWithLabelledContext(t *testing.T) {
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
	env.insertEdge("r1", "n1", "n2", "KNOWS")

	_, body := env.postSearch(map[string]any{
		"query_text": "who does alice know",
		"query_type": "GRAPH_COMPLETION_CONTEXT_EXTENSION",
		"collection": "entities",
	})
	if body["answer"] != "Alice knows Bob." {
		t.Errorf("answer = %v", body["answer"])
	}
	prompts := llm.promptsSnapshot()
	if len(prompts) != 1 {
		t.Fatalf("captured %d prompts, want 1", len(prompts))
	}
	if !strings.Contains(prompts[0], "Direct relationships (1-hop)") {
		t.Errorf("prompt missing hop1 label:\n%s", prompts[0])
	}
	// No hop2 content via postgres, so the "Extended relationships (2-hop)"
	// header must NOT appear when hop2 is empty.
	if strings.Contains(prompts[0], "Extended relationships (2-hop)") {
		t.Errorf("prompt has hop2 section with no hop2 data:\n%s", prompts[0])
	}
}

// ── cotSearch ──

// No LLM env → falls back to graphCompletionSearch (single-step).
func TestCoTSearch_NoLLMFallsBack(t *testing.T) {
	env := newSearchTestEnv(t)
	t.Setenv("LLM_ENDPOINT", "")
	t.Setenv("LLM_MODEL", "")
	env.start()

	_, body := env.postSearch(map[string]any{
		"query_text": "q",
		"query_type": "GRAPH_COMPLETION_COT",
	})
	if body["search_type"] != "GRAPH_COMPLETION" {
		t.Errorf("search_type = %v, want GRAPH_COMPLETION (fallback)", body["search_type"])
	}
}

// LLM env set but no collections → skeleton response with COT search_type.
func TestCoTSearch_NoCollectionsSkeleton(t *testing.T) {
	env := newSearchTestEnv(t)
	t.Setenv("LLM_ENDPOINT", "http://unused.test")
	t.Setenv("LLM_MODEL", "unused")
	env.cfg.LLMProvider = &recordingLLM{}
	env.cfg.EmbedEndpoint = ""
	env.cfg.Collections = nil
	env.start()

	_, body := env.postSearch(map[string]any{
		"query_text": "q",
		"query_type": "GRAPH_COMPLETION_COT",
	})
	if body["search_type"] != "GRAPH_COMPLETION_COT" {
		t.Errorf("search_type = %v", body["search_type"])
	}
	if body["answer"] != "" {
		t.Errorf("answer = %v, want empty", body["answer"])
	}
}

// Happy path: LLM decomposes into sub-questions, sub-queries find entities
// and graph context, synthesis prompt called. We script two LLM responses
// — decomposition (JSON array) and synthesis — and verify the prompt
// shape captured.
func TestCoTSearch_DecomposesAndSynthesises(t *testing.T) {
	env := newSearchTestEnv(t)
	llm := &recordingLLM{
		responses: []string{
			`["who is alice", "who does alice know"]`, // decompose
			"Alice knows Bob.",                        // synthesise
		},
	}
	env.cfg.LLMProvider = llm
	t.Setenv("LLM_ENDPOINT", "http://unused.test")
	t.Setenv("LLM_MODEL", "unused")
	env.start()

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"name": "Alice"})
	env.insertNode("n1", "Alice", "Person", "")
	env.insertNode("n2", "Bob", "Person", "")
	env.insertEdge("r1", "n1", "n2", "KNOWS")

	_, body := env.postSearch(map[string]any{
		"query_text": "tell me about alice",
		"query_type": "GRAPH_COMPLETION_COT",
		"collection": "entities",
	})
	if body["answer"] != "Alice knows Bob." {
		t.Errorf("answer = %v, want scripted reply", body["answer"])
	}
	steps, _ := body["reasoning_steps"].([]any)
	if len(steps) != 2 {
		t.Fatalf("reasoning_steps len=%d, want 2", len(steps))
	}
	prompts := llm.promptsSnapshot()
	if len(prompts) != 2 {
		t.Fatalf("captured %d prompts, want 2 (decompose + synthesise)", len(prompts))
	}
	if !strings.Contains(prompts[0], "sub-questions") {
		t.Errorf("first prompt should be the decomposition prompt, got:\n%s", prompts[0])
	}
	if !strings.Contains(prompts[1], "multi-step research") {
		t.Errorf("second prompt should be the synthesis prompt, got:\n%s", prompts[1])
	}
}

// Decomposition fails to produce a JSON array → handler uses the original
// query as the sole sub-question. We detect this by checking the LLM saw
// exactly one decompose call and one step surfaced in the response.
func TestCoTSearch_BadDecompositionFallsBackToSingleStep(t *testing.T) {
	env := newSearchTestEnv(t)
	llm := &recordingLLM{
		responses: []string{
			"not a json array", // bad decompose
			"answer about alice",
		},
	}
	env.cfg.LLMProvider = llm
	t.Setenv("LLM_ENDPOINT", "http://unused.test")
	t.Setenv("LLM_MODEL", "unused")
	env.start()

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"name": "Alice"})
	env.insertNode("n1", "Alice", "Person", "")
	env.insertNode("n2", "Bob", "Person", "")
	env.insertEdge("r1", "n1", "n2", "KNOWS")

	_, body := env.postSearch(map[string]any{
		"query_text": "tell me about alice",
		"query_type": "GRAPH_COMPLETION_COT",
		"collection": "entities",
	})
	steps, _ := body["reasoning_steps"].([]any)
	if len(steps) != 1 {
		t.Fatalf("reasoning_steps len=%d, want 1 (single-step fallback)", len(steps))
	}
	step0, _ := steps[0].(map[string]any)
	if sub, _ := step0["sub_question"].(string); sub != "tell me about alice" {
		t.Errorf("sub_question = %q, want original query", sub)
	}
}

// ── parseJSONStringArray ──

func TestParseJSONStringArray(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"plain array", `["a", "b", "c"]`, []string{"a", "b", "c"}},
		{"markdown wrapped", "```json\n[\"a\", \"b\"]\n```", []string{"a", "b"}},
		{"markdown generic", "```\n[\"a\"]\n```", []string{"a"}},
		{"with preamble", `Here are the questions: ["a", "b"]`, []string{"a", "b"}},
		{"empty array", `[]`, nil}, // Go's json decodes [] into a nil slice for our initial nil var
		{"invalid", "not json", nil},
		{"object not array", `{"a": 1}`, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseJSONStringArray(tc.in)
			if tc.name == "empty array" {
				// json.Unmarshal into a nil slice leaves it nil; accept [] too.
				if len(got) != 0 {
					t.Errorf("parseJSONStringArray(%q) = %v, want empty", tc.in, got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseJSONStringArray(%q)\n  got  %v\n  want %v", tc.in, got, tc.want)
			}
		})
	}
}

// ── communityLocalSearch ──

func TestCommunityLocalSearch_EmptyConfig(t *testing.T) {
	env := newSearchTestEnv(t)
	env.cfg.EmbedEndpoint = ""
	env.cfg.Collections = nil
	env.start()

	_, body := env.postSearch(map[string]any{
		"query_text": "q",
		"query_type": "COMMUNITY_LOCAL",
	})
	if body["search_type"] != "COMMUNITY_LOCAL" {
		t.Errorf("search_type = %v", body["search_type"])
	}
}

// Vector hit but no matching community → fall back to graphCompletion.
func TestCommunityLocalSearch_NoCommunityFallback(t *testing.T) {
	env := newSearchTestEnv(t)
	t.Setenv("LLM_ENDPOINT", "")
	t.Setenv("LLM_MODEL", "")
	env.start()

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"name": "Alice"})
	env.insertNode("n1", "Alice", "Person", "")
	// No community inserted → LookupCommunities returns empty → fallback.

	_, body := env.postSearch(map[string]any{
		"query_text": "alice",
		"query_type": "COMMUNITY_LOCAL",
		"collection": "entities",
	})
	if body["search_type"] != "GRAPH_COMPLETION" {
		t.Errorf("search_type = %v, want GRAPH_COMPLETION (fallback)", body["search_type"])
	}
}

// Happy path: entity has a community, LLM wired → community-based answer.
func TestCommunityLocalSearch_UsesCommunityContext(t *testing.T) {
	env := newSearchTestEnv(t)
	llm := &recordingLLM{responses: []string{"The auth community covers users and roles."}}
	env.cfg.LLMProvider = llm
	t.Setenv("LLM_ENDPOINT", "http://unused.test")
	t.Setenv("LLM_MODEL", "unused")
	env.start()

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"name": "Alice"})
	env.insertNode("n1", "Alice", "Person", "")
	env.insertNode("n2", "Bob", "Person", "")
	env.insertEdge("r1", "n1", "n2", "KNOWS")
	env.insertCommunity("c-auth", 0, "Auth subsystem: users, roles, permissions", []string{"n1", "n2"})

	_, body := env.postSearch(map[string]any{
		"query_text": "who is alice",
		"query_type": "COMMUNITY_LOCAL",
		"collection": "entities",
	})
	if body["search_type"] != "COMMUNITY_LOCAL" {
		t.Errorf("search_type = %v, want COMMUNITY_LOCAL", body["search_type"])
	}
	if body["answer"] != "The auth community covers users and roles." {
		t.Errorf("answer = %v", body["answer"])
	}
	commsUsed, _ := body["communities_used"].([]any)
	if len(commsUsed) != 1 {
		t.Fatalf("communities_used len=%d, want 1", len(commsUsed))
	}
	prompts := llm.promptsSnapshot()
	if len(prompts) != 1 {
		t.Fatalf("captured %d prompts, want 1", len(prompts))
	}
	if !strings.Contains(prompts[0], "Community summary: Auth subsystem") {
		t.Errorf("prompt missing community summary:\n%s", prompts[0])
	}
}

// ── communityGlobalSearch ──

func TestCommunityGlobalSearch_EmptyConfig(t *testing.T) {
	env := newSearchTestEnv(t)
	env.cfg.EmbedEndpoint = ""
	env.cfg.Collections = nil
	env.start()

	_, body := env.postSearch(map[string]any{
		"query_text": "q",
		"query_type": "COMMUNITY_GLOBAL",
	})
	if body["search_type"] != "COMMUNITY_GLOBAL" {
		t.Errorf("search_type = %v", body["search_type"])
	}
}

// No LLM → falls back to plain chunksSearch (documented behaviour:
// map-reduce is pointless without an LLM to map/reduce with).
func TestCommunityGlobalSearch_NoLLMFallsBackToChunks(t *testing.T) {
	env := newSearchTestEnv(t)
	t.Setenv("LLM_ENDPOINT", "")
	t.Setenv("LLM_MODEL", "")
	env.start()

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"text": "hello"})

	status, body := env.postSearch(map[string]any{
		"query_text": "q",
		"query_type": "COMMUNITY_GLOBAL",
		"collection": "entities",
	})
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	// chunksSearch returns a raw array, so the parsed map will be nil and the
	// raw body comes back as a JSON array. postSearch decodes into map[string]
	// any — a JSON array decodes to nil map. Confirm via absence of the
	// search_type field.
	if _, ok := body["search_type"]; ok {
		t.Errorf("expected raw-array chunks response (no search_type), got %v", body)
	}
}

// No _community_summaries collection → falls back to graphCompletion.
func TestCommunityGlobalSearch_NoSummariesFallback(t *testing.T) {
	env := newSearchTestEnv(t)
	llm := &recordingLLM{responses: []string{"fallback answer"}}
	env.cfg.LLMProvider = llm
	t.Setenv("LLM_ENDPOINT", "http://unused.test")
	t.Setenv("LLM_MODEL", "unused")
	env.start()

	// Only a regular collection, no _community_summaries.
	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"name": "Alice"})
	env.insertNode("n1", "Alice", "Person", "")
	env.insertNode("n2", "Bob", "Person", "")
	env.insertEdge("r1", "n1", "n2", "KNOWS")

	_, body := env.postSearch(map[string]any{
		"query_text": "alice",
		"query_type": "COMMUNITY_GLOBAL",
		"collection": "entities",
	})
	if body["search_type"] != "GRAPH_COMPLETION" {
		t.Errorf("search_type = %v, want GRAPH_COMPLETION (fallback)", body["search_type"])
	}
}

// Summary collection exists → map step runs concurrently, reduce step
// synthesises. We script N map-phase responses + 1 synthesis response and
// confirm all were called.
func TestCommunityGlobalSearch_MapReduceSynthesises(t *testing.T) {
	env := newSearchTestEnv(t)
	llm := &recordingLLM{
		responses: []string{
			// Order of map-phase responses is non-deterministic (concurrent).
			// All of them return the same string so ordering doesn't matter;
			// the synthesis response is the last in the script.
			"Auth covers login.",
			"Ingest covers pipelines.",
			"Final synthesised answer.",
		},
	}
	env.cfg.LLMProvider = llm
	t.Setenv("LLM_ENDPOINT", "http://unused.test")
	t.Setenv("LLM_MODEL", "unused")
	env.start()

	// Seed _community_summaries collection. The request collection must NOT
	// be set to only this one, since resolveCollections with req.Collection
	// would restrict the search to a different collection. Leave it empty so
	// ListByDomain / List returns all collections, which includes the
	// _community_summaries one.
	vec := []float32{1, 0, 0, 0}
	env.insertVector("_community_summaries", "c1", vec, map[string]any{
		"community_id": "c-auth", "text": "Auth: users, roles", "member_count": float64(5), "level": float64(0),
	})
	env.insertVector("_community_summaries", "c2", vec, map[string]any{
		"community_id": "c-ingest", "text": "Ingest: pipelines", "member_count": float64(3), "level": float64(0),
	})

	_, body := env.postSearch(map[string]any{
		"query_text": "what subsystems exist",
		"query_type": "COMMUNITY_GLOBAL",
	})
	if body["search_type"] != "COMMUNITY_GLOBAL" {
		t.Errorf("search_type = %v", body["search_type"])
	}
	if body["total_communities_searched"] != float64(2) {
		t.Errorf("total_communities_searched = %v, want 2", body["total_communities_searched"])
	}
	if body["answer"] != "Final synthesised answer." {
		t.Errorf("answer = %v, want synthesised reply", body["answer"])
	}
	prompts := llm.promptsSnapshot()
	if len(prompts) != 3 {
		t.Fatalf("captured %d prompts, want 3 (2 map + 1 reduce)", len(prompts))
	}
}
