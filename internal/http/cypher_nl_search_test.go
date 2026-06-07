// cypher_nl_search_test.go — Wave B coverage for Cypher-gated paths.
//
// Covers:
//   - cypherSearch            (CYPHER search type)
//   - naturalLanguageSearch   (NATURAL_LANGUAGE) fallback branches
//   - extractCypher           (pure helper used by NL path)
//
// Both handlers need a running Neo4j for the success path; we do not pull
// one in. Instead we exercise every early-exit branch — security gates,
// missing config, and fallback-to-graphCompletion paths — which is where
// the bugs tend to hide anyway (a misconfigured gate is worse than a slow
// query).
package http

import (
	"testing"
)

// ── cypherSearch ──

// Security gate: ALLOW_CYPHER_QUERY must be exactly "true" or the handler
// returns 403 before even looking at the rest of the config.
func TestCypherSearch_DisabledByDefault(t *testing.T) {
	t.Setenv("ALLOW_CYPHER_QUERY", "")
	env := newSearchTestEnv(t)
	env.start()

	status, body := env.postSearch(map[string]any{
		"query_text":   "MATCH (n) RETURN n",
		"query_type":   "CYPHER",
		"cypher_query": "MATCH (n) RETURN n LIMIT 1",
	})
	if status != 403 {
		t.Fatalf("status = %d, want 403", status)
	}
	if detail, _ := body["detail"].(string); detail == "" {
		t.Errorf("missing error detail; got %v", body)
	}
}

// Flag set but Neo4j not configured → 503 with explicit detail.
func TestCypherSearch_RequiresNeo4j(t *testing.T) {
	t.Setenv("ALLOW_CYPHER_QUERY", "true")
	env := newSearchTestEnv(t)
	// Neo4jCfg left zero.
	env.start()

	status, body := env.postSearch(map[string]any{
		"query_text":   "q",
		"query_type":   "CYPHER",
		"cypher_query": "MATCH (n) RETURN n LIMIT 1",
	})
	if status != 503 {
		t.Fatalf("status = %d, want 503", status)
	}
	if detail, _ := body["detail"].(string); detail == "" {
		t.Errorf("missing detail; got %v", body)
	}
}

// Empty cypher_query → 400.
func TestCypherSearch_RequiresQuery(t *testing.T) {
	t.Setenv("ALLOW_CYPHER_QUERY", "true")
	env := newSearchTestEnv(t)
	env.cfg.Neo4jCfg = GraphVisualizationConfig{Neo4jURL: "bolt://unused.test:7687"}
	env.start()

	status, _ := env.postSearch(map[string]any{
		"query_text": "q",
		"query_type": "CYPHER",
		// cypher_query omitted
	})
	if status != 400 {
		t.Fatalf("status = %d, want 400", status)
	}
}

// Write-operation keywords must be blocked before touching Neo4j. We check
// every keyword the production code screens for so a future edit to the
// list is caught.
func TestCypherSearch_BlocksWrites(t *testing.T) {
	t.Setenv("ALLOW_CYPHER_QUERY", "true")

	// Spelled in the form the production check looks for (uppercased &
	// including the trailing space for SET).
	writeQueries := []struct {
		name  string
		query string
	}{
		{"CREATE", "CREATE (n:Person {name:'x'}) RETURN n"},
		{"MERGE", "MERGE (n:Person {name:'x'}) RETURN n"},
		{"DELETE", "MATCH (n) DELETE n"},
		{"DETACH", "MATCH (n) DETACH DELETE n"},
		{"SET", "MATCH (n) SET n.x = 1 RETURN n"},
		{"REMOVE", "MATCH (n) REMOVE n.x RETURN n"},
		// Lowercase keyword must still be blocked — the handler upper-cases
		// the query before comparing.
		{"lowercase create", "create (n) return n"},
		{"foreach write", "MATCH (n) FOREACH (_ IN [1] | SET n.x = 1) RETURN n"},
	}

	for _, tc := range writeQueries {
		t.Run(tc.name, func(t *testing.T) {
			env := newSearchTestEnv(t)
			env.cfg.Neo4jCfg = GraphVisualizationConfig{Neo4jURL: "bolt://unused.test:7687"}
			env.start()

			status, body := env.postSearch(map[string]any{
				"query_text":   "q",
				"query_type":   "CYPHER",
				"cypher_query": tc.query,
			})
			if status != 403 {
				t.Fatalf("status = %d, want 403 (write blocked); body=%v", status, body)
			}
		})
	}
}

func TestCypherSearch_BlocksNonReadPrefixes(t *testing.T) {
	t.Setenv("ALLOW_CYPHER_QUERY", "true")
	env := newSearchTestEnv(t)
	env.cfg.Neo4jCfg = GraphVisualizationConfig{Neo4jURL: "bolt://unused.test:7687"}
	env.start()

	status, body := env.postSearch(map[string]any{
		"query_text":   "q",
		"query_type":   "CYPHER",
		"cypher_query": "RETURN 1 AS one",
	})
	if status != 403 {
		t.Fatalf("status = %d, want 403 (non-read-prefix should be blocked); body=%v", status, body)
	}
}

func TestCypherSearch_WriteCanBeEnabled(t *testing.T) {
	t.Setenv("ALLOW_CYPHER_QUERY", "true")
	t.Setenv("ALLOW_CYPHER_WRITE", "true")
	env := newSearchTestEnv(t)
	env.cfg.Neo4jCfg = GraphVisualizationConfig{Neo4jURL: "bolt://unused.test:7687"}
	env.start()

	status, _ := env.postSearch(map[string]any{
		"query_text":   "q",
		"query_type":   "CYPHER",
		"cypher_query": "CREATE (n:Person {name:'x'}) RETURN n",
	})
	// The request is no longer denied by policy, so the next expected
	// failure is connection-level (Neo4j isn't running in this test).
	if status != 500 {
		t.Fatalf("status = %d, want 500 (policy allowed, then connect fails)", status)
	}
}

// Admin/destructive keywords must remain blocked even when ALLOW_CYPHER_WRITE
// is on. A misconfigured deployment that flips the write flag should still
// not be able to drop databases or load arbitrary CSV.
func TestCypherSearch_AdminKeywordsBlockedEvenWithWrite(t *testing.T) {
	t.Setenv("ALLOW_CYPHER_QUERY", "true")
	t.Setenv("ALLOW_CYPHER_WRITE", "true")

	cases := []struct {
		name  string
		query string
	}{
		{"DROP DATABASE", "DROP DATABASE neo4j"},
		{"CREATE CONSTRAINT", "CREATE CONSTRAINT x IF NOT EXISTS FOR (n:Node) REQUIRE n.id IS UNIQUE"},
		{"CREATE INDEX", "CREATE INDEX person_name FOR (p:Person) ON (p.name)"},
		{"DBMS call", "CALL dbms.security.createUser('x','y')"},
		{"TERMINATE", "MATCH (n) WITH n CALL TERMINATE TRANSACTIONS RETURN n"},
		{"LOAD CSV", "LOAD CSV FROM 'http://x' AS line CREATE (n:Row {v:line[0]})"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSearchTestEnv(t)
			env.cfg.Neo4jCfg = GraphVisualizationConfig{Neo4jURL: "bolt://unused.test:7687"}
			env.start()

			status, body := env.postSearch(map[string]any{
				"query_text":   "q",
				"query_type":   "CYPHER",
				"cypher_query": tc.query,
			})
			if status != 403 {
				t.Fatalf("status = %d, want 403 (admin denied even with write flag); body=%v", status, body)
			}
		})
	}
}

// ── naturalLanguageSearch ──

// No Neo4j configured → falls back to graphCompletionSearch. We verify by
// checking search_type flips from NATURAL_LANGUAGE to GRAPH_COMPLETION.
func TestNaturalLanguageSearch_NoNeo4jFallsBack(t *testing.T) {
	env := newSearchTestEnv(t)
	t.Setenv("LLM_ENDPOINT", "")
	t.Setenv("LLM_MODEL", "")
	env.start()

	_, body := env.postSearch(map[string]any{
		"query_text": "who is Alice",
		"query_type": "NATURAL_LANGUAGE",
	})
	if body["search_type"] != "GRAPH_COMPLETION" {
		t.Errorf("search_type = %v, want GRAPH_COMPLETION (fallback)", body["search_type"])
	}
}

// Neo4j set but LLM env unset → also falls back (can't translate NL → Cypher
// without an LLM).
func TestNaturalLanguageSearch_NoLLMFallsBack(t *testing.T) {
	env := newSearchTestEnv(t)
	env.cfg.Neo4jCfg = GraphVisualizationConfig{Neo4jURL: "bolt://unused.test:7687"}
	t.Setenv("LLM_ENDPOINT", "")
	t.Setenv("LLM_MODEL", "")
	env.start()

	_, body := env.postSearch(map[string]any{
		"query_text": "who is Alice",
		"query_type": "NATURAL_LANGUAGE",
	})
	if body["search_type"] != "GRAPH_COMPLETION" {
		t.Errorf("search_type = %v, want GRAPH_COMPLETION (no-LLM fallback)", body["search_type"])
	}
}

// ── extractCypher (pure helper) ──

func TestExtractCypher(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "markdown cypher block",
			in:   "```cypher\nMATCH (n) RETURN n LIMIT 10\n```",
			want: "MATCH (n) RETURN n LIMIT 10",
		},
		{
			name: "markdown generic block",
			in:   "```\nMATCH (n) RETURN n LIMIT 10\n```",
			want: "MATCH (n) RETURN n LIMIT 10",
		},
		{
			name: "plain match",
			in:   "MATCH (n:Person) RETURN n.name LIMIT 5",
			want: "MATCH (n:Person) RETURN n.name LIMIT 5",
		},
		{
			name: "preamble then match",
			in:   "Sure! Here is the query:\nMATCH (n) RETURN n LIMIT 5",
			want: "MATCH (n) RETURN n LIMIT 5",
		},
		{
			name: "no cypher at all",
			in:   "I'm sorry, I can't help with that.",
			want: "",
		},
		{
			name: "empty string",
			in:   "",
			want: "",
		},
		{
			name: "whitespace only",
			in:   "   \n\t ",
			want: "",
		},
		{
			// Capitalised language tag: same logic — strip first line after ```.
			name: "markdown Cypher (capitalised tag)",
			in:   "```Cypher\nMATCH (n) RETURN n\n```",
			want: "MATCH (n) RETURN n",
		},
		{
			// Unclosed code fence: helper falls through to the heuristic
			// branch and returns the substring starting at MATCH.
			name: "unclosed markdown fence",
			in:   "```cypher\nMATCH (n) RETURN n",
			want: "MATCH (n) RETURN n",
		},
		{
			// Lowercase still trips the heuristic: the upper-cased copy
			// contains MATCH at index 0, so the helper returns the raw
			// (lowercase) query unchanged. Policy enforcement happens later
			// in isCypherAllowed (which upper-cases internally) — extractor
			// is intentionally permissive.
			name: "lowercase match without fence",
			in:   "match (n) return n",
			want: "match (n) return n",
		},
		{
			// Bare RETURN (no MATCH): heuristic still trips on RETURN so the
			// raw input is returned. isCypherAllowed must reject it later.
			name: "bare return no match",
			in:   "RETURN 1",
			want: "RETURN 1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractCypher(tc.in)
			if got != tc.want {
				t.Errorf("extractCypher(%q)\n  got  %q\n  want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsCypherAllowed(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		allowWrite bool
		want       bool
	}{
		{name: "read match", query: "MATCH (n) RETURN n", allowWrite: false, want: true},
		{name: "read call", query: "CALL db.labels() YIELD label RETURN label", allowWrite: false, want: true},
		{name: "read unwind", query: "UNWIND [1,2] AS x RETURN x", allowWrite: false, want: true},
		{name: "bare return denied", query: "RETURN 1", allowWrite: false, want: false},
		{name: "write denied by default", query: "CREATE (n) RETURN n", allowWrite: false, want: false},
		{name: "write allowed with flag", query: "CREATE (n) RETURN n", allowWrite: true, want: true},
		{name: "drop always denied", query: "DROP DATABASE neo4j", allowWrite: true, want: false},
		{name: "constraint always denied", query: "CREATE CONSTRAINT x IF NOT EXISTS FOR (n:Node) REQUIRE n.id IS UNIQUE", allowWrite: true, want: false},
		{name: "empty denied", query: "   ", allowWrite: false, want: false},

		// Read prefixes that must be allowed.
		{name: "optional match read", query: "OPTIONAL MATCH (n) RETURN n", allowWrite: false, want: true},
		{name: "with read", query: "WITH 1 AS x RETURN x", allowWrite: false, want: true},

		// Admin / destructive keywords stay denied even with allowWrite=true.
		{name: "drop with write denied", query: "DROP DATABASE neo4j", allowWrite: true, want: false},
		{name: "create index denied", query: "CREATE INDEX x FOR (n:Node) ON (n.id)", allowWrite: true, want: false},
		{name: "dbms call denied", query: "CALL dbms.security.listUsers()", allowWrite: false, want: false},
		{name: "terminate denied", query: "MATCH (n) WITH n CALL TERMINATE TRANSACTIONS RETURN n", allowWrite: true, want: false},
		{name: "load csv denied", query: "LOAD CSV FROM 'file:///x.csv' AS line RETURN line", allowWrite: true, want: false},
		// "DATABASE" keyword anywhere — e.g. SHOW DATABASES — denied as well.
		{name: "show database denied", query: "MATCH (n) WITH n SHOW DATABASE foo RETURN n", allowWrite: true, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isCypherAllowed(tc.query, tc.allowWrite)
			if got != tc.want {
				t.Fatalf("isCypherAllowed(%q, allowWrite=%v) = %v, want %v", tc.query, tc.allowWrite, got, tc.want)
			}
		})
	}
}
