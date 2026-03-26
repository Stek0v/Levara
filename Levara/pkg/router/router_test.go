package router

import (
	"testing"
)

var fullCaps = Capabilities{
	HasEmbedding: true,
	HasBM25:      true,
	HasNeo4j:     true,
	HasLLM:       true,
	HasPostgres:  true,
	AllowCypher:  true,
}

var noLLMCaps = Capabilities{
	HasEmbedding: true,
	HasBM25:      true,
	HasNeo4j:     true,
	HasLLM:       false,
	HasPostgres:  true,
	AllowCypher:  false,
}

var vectorOnlyCaps = Capabilities{
	HasEmbedding: true,
}

var bm25OnlyCaps = Capabilities{
	HasBM25: true,
}

var minimalCaps = Capabilities{}

func TestRoute(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		caps     Capabilities
		wantType string
	}{
		// ── Cypher detection ──
		{"cypher match", "MATCH (n) RETURN n LIMIT 10", fullCaps, "CYPHER"},
		{"cypher optional", "optional match (n)-[r]->(m) return n,m", fullCaps, "CYPHER"},
		{"cypher no neo4j", "MATCH (n) RETURN n", Capabilities{HasEmbedding: true, HasBM25: true}, "HYBRID"},

		// ── Temporal detection ──
		{"date ISO", "что произошло 2024-03-15?", fullCaps, "TEMPORAL"},
		{"date Russian", "события 15 марта 2024", fullCaps, "TEMPORAL"},
		{"date English", "events in March 2024", fullCaps, "TEMPORAL"},
		{"date slash", "logs from 15/03/2024", fullCaps, "TEMPORAL"},
		{"date no backend", "events 2024-03-15", vectorOnlyCaps, "CHUNKS"},

		// ── Code tokens ──
		{"code func call", "parseDate()", fullCaps, "CODING_RULES"},
		{"code method", "obj.Method()", fullCaps, "CODING_RULES"},
		{"code scope", "module::item", fullCaps, "CODING_RULES"},
		{"code arrow", "ptr->field", fullCaps, "CODING_RULES"},
		{"code camelCase", "what does parseConfigFile do", fullCaps, "CODING_RULES"},
		{"code keyword", "func handleSearch", fullCaps, "CODING_RULES"},
		{"code snake_case", "test_search_results", fullCaps, "CODING_RULES"},
		{"code no graph", "parseDate()", vectorOnlyCaps, "CHUNKS"},

		// ── Relational / graph ──
		{"relational en", "how is entity A related to entity B?", fullCaps, "GRAPH_COMPLETION"},
		{"relational ru", "как модуль A связан с модулем B?", fullCaps, "GRAPH_COMPLETION"},
		{"relational depends", "what depends on auth module?", fullCaps, "GRAPH_COMPLETION"},
		{"relational no llm", "how is A related to B?", noLLMCaps, "HYBRID"},

		// ── Summary ──
		{"summary en", "summarize the project status", fullCaps, "SUMMARIES"},
		{"summary ru", "краткий обзор проекта", fullCaps, "SUMMARIES"},
		{"summary overview", "project overview", fullCaps, "SUMMARIES"},

		// ── Question → RAG ──
		{"question en", "how does the search pipeline work?", fullCaps, "RAG_COMPLETION"},
		{"question ru", "как работает поиск?", fullCaps, "RAG_COMPLETION"},
		{"question what", "what is the purpose of WAL?", fullCaps, "RAG_COMPLETION"},
		{"question mark", "vector search performance?", fullCaps, "RAG_COMPLETION"},
		{"question no llm", "how does search work?", noLLMCaps, "HYBRID"},

		// ── Short keyword ──
		{"keyword single", "authentication", fullCaps, "HYBRID"},
		{"keyword two", "search latency", fullCaps, "HYBRID"},
		{"keyword three", "vector insert speed", fullCaps, "HYBRID"},

		// ── Defaults ──
		{"default medium query", "find all records about machine learning in the database", fullCaps, "HYBRID"},
		{"default vector only", "search for documents", vectorOnlyCaps, "CHUNKS"},
		{"default bm25 only", "authentication", bm25OnlyCaps, "CHUNKS_LEXICAL"},

		// ── Edge cases ──
		{"empty query", "", fullCaps, "HYBRID"},
		{"empty no caps", "", minimalCaps, "CHUNKS"},
		{"whitespace only", "   ", fullCaps, "HYBRID"},
		{"long query no signals", "this is a very long query with many words but no special signals at all and it keeps going on and on", fullCaps, "HYBRID"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Route(tt.query, tt.caps)
			if d.SearchType != tt.wantType {
				t.Errorf("Route(%q) = %s (reason: %s, conf: %.2f), want %s",
					tt.query, d.SearchType, d.Reason, d.Confidence, tt.wantType)
			}
		})
	}
}

func TestRouteConfidence(t *testing.T) {
	d := Route("MATCH (n) RETURN n", fullCaps)
	if d.Confidence < 0.9 {
		t.Errorf("Cypher confidence = %.2f, want >= 0.9", d.Confidence)
	}

	d = Route("authentication", fullCaps)
	if d.Confidence > 0.8 {
		t.Errorf("keyword confidence = %.2f, want <= 0.8", d.Confidence)
	}
}

func TestRouteAlternatives(t *testing.T) {
	d := Route("how does parseConfig work?", fullCaps)
	if len(d.Alternatives) == 0 {
		t.Error("expected alternatives for ambiguous query, got none")
	}
	// Should have both coding_rules and question signals
	found := map[string]bool{}
	found[d.SearchType] = true
	for _, a := range d.Alternatives {
		found[a.SearchType] = true
	}
	if !found["CODING_RULES"] && !found["RAG_COMPLETION"] {
		t.Errorf("expected CODING_RULES or RAG_COMPLETION in candidates, got %v", found)
	}
}

func TestRouteCapabilityDegradation(t *testing.T) {
	// With full caps: TEMPORAL
	d1 := Route("events 2024-03-15", fullCaps)
	if d1.SearchType != "TEMPORAL" {
		t.Errorf("full caps: got %s, want TEMPORAL", d1.SearchType)
	}

	// Without Neo4j and Postgres: can't do TEMPORAL
	d2 := Route("events 2024-03-15", Capabilities{HasEmbedding: true, HasBM25: true})
	if d2.SearchType == "TEMPORAL" {
		t.Error("without graph backends: should not route to TEMPORAL")
	}
}

func TestSignalDetectors(t *testing.T) {
	t.Run("isCypher", func(t *testing.T) {
		if !isCypher("match (n) return n") {
			t.Error("should detect MATCH")
		}
		if isCypher("matching colors") {
			t.Error("should not match 'matching'")
		}
	})

	t.Run("hasCodeTokens", func(t *testing.T) {
		cases := []struct {
			q    string
			want bool
		}{
			{"parseDate()", true},
			{"obj.method()", true},
			{"std::vector", true},
			{"node->next", true},
			{"func main", true},
			{"def setup", true},
			{"class Config", true},
			{"parseConfigFile", true}, // camelCase
			{"hello world", false},
			{"simple query", false},
		}
		for _, c := range cases {
			if got := hasCodeTokens(c.q); got != c.want {
				t.Errorf("hasCodeTokens(%q) = %v, want %v", c.q, got, c.want)
			}
		}
	})

	t.Run("isQuestion", func(t *testing.T) {
		cases := []struct {
			q    string
			want bool
		}{
			{"how does it work?", true},
			{"what is this", true},
			{"когда это случилось", true},
			{"как работает поиск", true},
			{"explain the architecture", true},
			{"authentication", false},
			{"search latency", false},
		}
		for _, c := range cases {
			qLower := c.q
			words := []string{}
			if got := isQuestion(qLower, words); got != c.want {
				t.Errorf("isQuestion(%q) = %v, want %v", c.q, got, c.want)
			}
		}
	})

	t.Run("isRelational", func(t *testing.T) {
		if !isRelational("how is a related to b") {
			t.Error("should detect 'related to'")
		}
		if !isRelational("модуль связан с другим") {
			t.Error("should detect 'связан с'")
		}
		if isRelational("search for documents") {
			t.Error("should not match generic query")
		}
	})

	t.Run("wantsSummary", func(t *testing.T) {
		if !wantsSummary("summarize the project") {
			t.Error("should detect 'summarize'")
		}
		if !wantsSummary("краткий обзор") {
			t.Error("should detect 'краткий обзор'")
		}
		if wantsSummary("search for something") {
			t.Error("should not match generic query")
		}
	})

	t.Run("isKeywordOnly", func(t *testing.T) {
		if !isKeywordOnly([]string{"authentication"}) {
			t.Error("single word should be keyword")
		}
		if !isKeywordOnly([]string{"search", "latency"}) {
			t.Error("two words should be keyword")
		}
		if isKeywordOnly([]string{"how", "does", "it", "work"}) {
			t.Error("4+ words should not be keyword")
		}
		if isKeywordOnly([]string{}) {
			t.Error("empty should not be keyword")
		}
	})
}

func BenchmarkRoute(b *testing.B) {
	queries := []string{
		"authentication",
		"how does search work?",
		"events 2024-03-15",
		"MATCH (n) RETURN n LIMIT 10",
		"parseConfig()",
		"summarize the project",
		"как модуль A связан с B?",
		"find all records about machine learning",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Route(queries[i%len(queries)], fullCaps)
	}
}
