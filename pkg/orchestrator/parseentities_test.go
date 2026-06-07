package orchestrator

import (
	"strings"
	"testing"
)

// Golden tests for the pure-function layer of cognify pipeline:
// parseEntities(LLM_text) → nodes, edges
// extractJSON(string)     → isolated JSON object

func TestExtractJSON_BareJSON(t *testing.T) {
	in := `{"nodes":[],"edges":[]}`
	got := extractJSON(in)
	if got != in {
		t.Errorf("want %q, got %q", in, got)
	}
}

func TestExtractJSON_WrappedInMarkdown(t *testing.T) {
	in := "Here is the graph:\n```json\n{\"nodes\":[{\"name\":\"X\"}]}\n```\nEnd."
	got := extractJSON(in)
	want := `{"nodes":[{"name":"X"}]}`
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

func TestExtractJSON_NestedBraces(t *testing.T) {
	// Properties object has its own braces — bracket matching must handle depth.
	in := `prefix {"nodes":[{"name":"X","properties":{"k":"v"}}],"edges":[]} suffix`
	got := extractJSON(in)
	want := `{"nodes":[{"name":"X","properties":{"k":"v"}}],"edges":[]}`
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

func TestExtractJSON_NoBrace(t *testing.T) {
	if got := extractJSON("just text no json"); got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

func TestExtractJSON_UnbalancedBrace(t *testing.T) {
	// Opening brace with no close — must not return partial string.
	if got := extractJSON(`{"nodes":[`); got != "" {
		t.Errorf("want empty on unbalanced, got %q", got)
	}
}

func TestParseEntities_SimpleGraph(t *testing.T) {
	llmOut := `{
		"nodes": [
			{"id":"n1","name":"Alice","type":"Person","description":"engineer"},
			{"id":"n2","name":"Acme","type":"Org","description":""}
		],
		"edges": [
			{"source":"n1","target":"n2","relationship":"works_at","edge_text":"Alice works at Acme"}
		]
	}`
	nodes, edges, err := parseEntities(llmOut)
	if err != nil {
		t.Fatalf("parseEntities: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(nodes))
	}
	if nodes[0].Name != "Alice" || nodes[0].Type != "Person" || nodes[0].ID != "n1" {
		t.Errorf("node[0] wrong: %+v", nodes[0])
	}
	if nodes[0].Confidence != 1.0 {
		t.Errorf("want default Confidence=1.0, got %v", nodes[0].Confidence)
	}
	if nodes[0].ExtractedAt == "" {
		t.Error("want ExtractedAt populated")
	}
	if len(edges) != 1 {
		t.Fatalf("want 1 edge, got %d", len(edges))
	}
	if edges[0].RelationshipName != "works_at" {
		t.Errorf("edge relationship wrong: %q", edges[0].RelationshipName)
	}
	if edges[0].EdgeText != "Alice works at Acme" {
		t.Errorf("edge_text wrong: %q", edges[0].EdgeText)
	}
}

func TestParseEntities_MissingIDGenerated(t *testing.T) {
	// When LLM omits `id`, parseEntities must derive a stable id from name.
	llmOut := `{"nodes":[{"name":"Alice","type":"Person"}],"edges":[]}`
	nodes, _, err := parseEntities(llmOut)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("want 1 node, got %d", len(nodes))
	}
	if nodes[0].ID == "" {
		t.Error("want generated ID for node missing id field")
	}
}

func TestParseEntities_RelationshipNameFallback(t *testing.T) {
	// Some LLMs emit `relationship_name` instead of `relationship`. Accept both.
	llmOut := `{
		"nodes":[{"id":"a","name":"A"},{"id":"b","name":"B"}],
		"edges":[{"source":"a","target":"b","relationship_name":"knows","edge_text":"A knows B"}]
	}`
	_, edges, err := parseEntities(llmOut)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 || edges[0].RelationshipName != "knows" {
		t.Errorf("relationship_name fallback failed: %+v", edges)
	}
}

func TestParseEntities_WrappedInMarkdownFromLLM(t *testing.T) {
	// Models often wrap JSON in ```json blocks — full pipeline handles it.
	llmOut := "Here is the extracted graph:\n```json\n" +
		`{"nodes":[{"id":"1","name":"Go"}],"edges":[]}` +
		"\n```\nThat's all."
	nodes, _, err := parseEntities(llmOut)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Name != "Go" {
		t.Errorf("markdown-wrapped parse failed: %+v", nodes)
	}
}

func TestParseEntities_NoJSON(t *testing.T) {
	_, _, err := parseEntities("sorry I cannot help with that")
	if err == nil {
		t.Fatal("want error on non-JSON input")
	}
	if !strings.Contains(err.Error(), "no JSON") {
		t.Errorf("want 'no JSON' in error, got: %v", err)
	}
}

func TestParseEntities_InvalidJSON(t *testing.T) {
	_, _, err := parseEntities(`{"nodes": [bogus}`)
	if err == nil {
		t.Fatal("want error on malformed JSON")
	}
	if !strings.Contains(err.Error(), "parse entities JSON") {
		t.Errorf("want 'parse entities JSON' in error, got: %v", err)
	}
}

// Regression for #57: LLMs typically reference entities in edges by *name*
// rather than by the node id field. parseEntities must rewrite those refs
// to the node's UUID so that graph_edges rows align with the UUID-keyed
// schema query_entity reads through.
func TestParseEntities_EdgeRefsResolvedByName(t *testing.T) {
	llmOut := `{
		"nodes": [
			{"id":"uuid-a","name":"Alice","type":"Person"},
			{"id":"uuid-b","name":"Acme","type":"Org"}
		],
		"edges": [
			{"source":"Alice","target":"Acme","relationship":"works_at","edge_text":"Alice works at Acme"}
		]
	}`
	_, edges, err := parseEntities(llmOut)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Fatalf("want 1 edge, got %d", len(edges))
	}
	if edges[0].SourceID != "uuid-a" {
		t.Errorf("SourceID = %q, want uuid-a (name→ID rewrite failed)", edges[0].SourceID)
	}
	if edges[0].TargetID != "uuid-b" {
		t.Errorf("TargetID = %q, want uuid-b (name→ID rewrite failed)", edges[0].TargetID)
	}
}

// Regression for #57: edges that reference an entity not present in the
// nodes list ("loose ref") must pass through unchanged rather than being
// dropped — the dedup stage handles them downstream.
func TestParseEntities_UnknownEdgeRefFallsThrough(t *testing.T) {
	llmOut := `{
		"nodes": [{"id":"uuid-a","name":"Alice"}],
		"edges": [{"source":"Alice","target":"board","relationship":"reports_to","edge_text":"Alice reports to board"}]
	}`
	_, edges, err := parseEntities(llmOut)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Fatalf("want 1 edge, got %d", len(edges))
	}
	if edges[0].SourceID != "uuid-a" {
		t.Errorf("SourceID = %q, want uuid-a", edges[0].SourceID)
	}
	if edges[0].TargetID != "board" {
		t.Errorf("TargetID = %q, want raw 'board' fallback", edges[0].TargetID)
	}
}

func TestParseEntities_EmptyGraph(t *testing.T) {
	// Empty but valid JSON must succeed — downstream treats zero entities as "nothing to write".
	nodes, edges, err := parseEntities(`{"nodes":[],"edges":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 || len(edges) != 0 {
		t.Errorf("want empty slices, got %d nodes / %d edges", len(nodes), len(edges))
	}
}
