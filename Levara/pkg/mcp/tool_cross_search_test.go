package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stek0v/cognevra/pipeline"
)

// setupCrossSearchDB returns a fakeDeps whose DB has a populated
// memories table with a mix of ordinary and sensitive-key rows.
// Collection "alpha" has 2 public + 1 sensitive row; "beta" has 1
// public row. Cross-collection rows (collection_name='') are added
// so tests can cover the collection_name='' OR-branch.
func setupCrossSearchDB(t *testing.T) *fakeDeps {
	t.Helper()
	deps := setupDepsTestDB(t)
	mustExec(t, deps.db, `
		CREATE TABLE IF NOT EXISTS memories (
			id TEXT PRIMARY KEY, key TEXT, value TEXT, type TEXT,
			owner_id TEXT DEFAULT '', collection_name TEXT DEFAULT '',
			room TEXT DEFAULT '', hall TEXT DEFAULT '',
			is_pinned INTEGER DEFAULT 0, pin_priority INTEGER DEFAULT 0,
			created_at TEXT, updated_at TEXT
		)
	`)
	seed := []struct{ id, key, value, typ, coll string }{
		{"m1", "alpha-note", "alpha body hello", "fact", "alpha"},
		{"m2", "alpha-followup", "no match here", "fact", "alpha"},
		{"m3", "alpha-api_key", "hello sensitive", "fact", "alpha"},
		{"m4", "beta-doc", "beta body hello", "fact", "beta"},
		{"m5", "shared-hello", "global hello", "fact", ""},
	}
	for _, s := range seed {
		mustExec(t, deps.db,
			"INSERT INTO memories (id, key, value, type, collection_name, updated_at) VALUES (?, ?, ?, ?, ?, datetime('now'))",
			s.id, s.key, s.value, s.typ, s.coll)
	}
	return deps
}

// mustExec runs an sqlite statement and fails the test on error —
// cross-search tests use a real sqlite DB so the SQL path is
// exercised end-to-end.
func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// crossSearchResp is the JSON shape the tool returns. Tests decode
// into this instead of traversing map[string]any so field names
// are checked at compile time.
type crossSearchResp struct {
	Results []struct {
		Collection string           `json:"collection"`
		Vectors    []map[string]any `json:"vectors,omitempty"`
		Memories   []map[string]any `json:"memories,omitempty"`
	} `json:"results"`
	Collections []string `json:"collections"`
	Query       string   `json:"query"`
}

func decodeCrossSearch(t *testing.T, res ToolResult) crossSearchResp {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected IsError; text=%q", res.Content[0].Text)
	}
	var out crossSearchResp
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Fatalf("decode: %v — raw: %s", err, res.Content[0].Text)
	}
	return out
}

func TestToolCrossSearch_MissingQuery(t *testing.T) {
	res := ToolCrossSearch(context.Background(), &fakeDeps{}, map[string]any{})
	if !res.IsError {
		t.Fatal("want IsError when search_query missing")
	}
	if !strings.Contains(res.Content[0].Text, "'search_query' required") {
		t.Errorf("wrong error text: %q", res.Content[0].Text)
	}
}

func TestToolCrossSearch_EmptyCollections(t *testing.T) {
	res := ToolCrossSearch(context.Background(), &fakeDeps{}, map[string]any{
		"search_query": "q",
	})
	if !res.IsError {
		t.Fatal("want IsError when collections missing")
	}
	if !strings.Contains(res.Content[0].Text, "'collections' array required") {
		t.Errorf("wrong error text: %q", res.Content[0].Text)
	}
}

func TestToolCrossSearch_TooManyCollections(t *testing.T) {
	six := []any{"c1", "c2", "c3", "c4", "c5", "c6"}
	res := ToolCrossSearch(context.Background(), &fakeDeps{}, map[string]any{
		"search_query": "q",
		"collections":  six,
	})
	if !res.IsError {
		t.Fatal("want IsError when >5 collections")
	}
	if !strings.Contains(res.Content[0].Text, "max 5 collections") {
		t.Errorf("wrong error text: %q", res.Content[0].Text)
	}
}

func TestToolCrossSearch_EmbedNotConfigured(t *testing.T) {
	// searchPipelineFn not set → NewSearchPipeline returns nil.
	res := ToolCrossSearch(context.Background(), &fakeDeps{}, map[string]any{
		"search_query": "q",
		"collections":  []any{"alpha"},
	})
	if res.IsError {
		t.Errorf("unexpected IsError; text=%q", res.Content[0].Text)
	}
	if res.Content[0].Text != "No results (embedding service not configured)" {
		t.Errorf("wrong text: %q", res.Content[0].Text)
	}
}

func TestToolCrossSearch_VectorsAndMemoriesBothReturned(t *testing.T) {
	deps := setupCrossSearchDB(t)
	deps.searchPipelineFn = func(doRerank bool) SearchPipeline {
		return &fakeSearchPipeline{
			byText: func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error) {
				// Return one vector hit per collection so we can verify
				// per-collection emission without over-specifying.
				return []pipeline.ScoredResult{
					{ID: coll + "-v1", Score: 0.9, Metadata: json.RawMessage(`{"from":"` + coll + `"}`)},
				}, nil
			},
		}
	}

	res := ToolCrossSearch(context.Background(), deps, map[string]any{
		"search_query": "hello",
		"collections":  []any{"alpha", "beta"},
	})
	resp := decodeCrossSearch(t, res)

	if resp.Query != "hello" {
		t.Errorf("query=%q, want hello", resp.Query)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("got %d collection blocks, want 2", len(resp.Results))
	}

	alpha := resp.Results[0]
	if alpha.Collection != "alpha" || len(alpha.Vectors) != 1 {
		t.Errorf("alpha: %+v", alpha)
	}
	// alpha memories: 2 public key-matches (alpha-note, alpha-followup — wait,
	// "hello" appears in alpha-note.value only) plus the shared-hello row
	// (collection_name='' OR-branch matches all collections).
	var alphaKeys []string
	for _, m := range alpha.Memories {
		alphaKeys = append(alphaKeys, m["key"].(string))
	}
	if !contains(alphaKeys, "alpha-note") {
		t.Errorf("alpha missing alpha-note; got %v", alphaKeys)
	}
	if !contains(alphaKeys, "shared-hello") {
		t.Errorf("alpha missing shared-hello (shared row); got %v", alphaKeys)
	}
	// Sensitive key must be dropped even though its value matches.
	if contains(alphaKeys, "alpha-api_key") {
		t.Errorf("alpha-api_key (sensitive) leaked: %v", alphaKeys)
	}
}

func TestToolCrossSearch_IncludeMemoriesFalseSkipsSQL(t *testing.T) {
	deps := setupCrossSearchDB(t)
	deps.searchPipelineFn = func(bool) SearchPipeline {
		return &fakeSearchPipeline{
			byText: func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error) {
				return []pipeline.ScoredResult{{ID: "v1", Score: 0.5, Metadata: nil}}, nil
			},
		}
	}

	res := ToolCrossSearch(context.Background(), deps, map[string]any{
		"search_query":     "hello",
		"collections":      []any{"alpha"},
		"include_memories": false,
	})
	resp := decodeCrossSearch(t, res)
	if len(resp.Results) != 1 {
		t.Fatalf("got %d blocks, want 1", len(resp.Results))
	}
	if len(resp.Results[0].Memories) != 0 {
		t.Errorf("memories should be empty when include_memories=false; got %+v", resp.Results[0].Memories)
	}
	if len(resp.Results[0].Vectors) != 1 {
		t.Errorf("vectors should still run; got %+v", resp.Results[0].Vectors)
	}
}

func TestToolCrossSearch_TopKRespected(t *testing.T) {
	// Pass top_k and assert it flows to the pipeline + SQL LIMIT.
	var gotTopK int
	deps := setupCrossSearchDB(t)
	deps.searchPipelineFn = func(bool) SearchPipeline {
		return &fakeSearchPipeline{
			byText: func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error) {
				gotTopK = topK
				return nil, nil
			},
		}
	}

	ToolCrossSearch(context.Background(), deps, map[string]any{
		"search_query": "hello",
		"collections":  []any{"alpha"},
		"top_k":        float64(2),
	})
	if gotTopK != 2 {
		t.Errorf("SearchByText topK=%d, want 2", gotTopK)
	}
}

func TestToolCrossSearch_DefaultTopK(t *testing.T) {
	var gotTopK int
	deps := setupCrossSearchDB(t)
	deps.searchPipelineFn = func(bool) SearchPipeline {
		return &fakeSearchPipeline{
			byText: func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error) {
				gotTopK = topK
				return nil, nil
			},
		}
	}
	ToolCrossSearch(context.Background(), deps, map[string]any{
		"search_query": "hello",
		"collections":  []any{"alpha"},
	})
	if gotTopK != crossSearchDefaultTopK {
		t.Errorf("default topK=%d, want %d", gotTopK, crossSearchDefaultTopK)
	}
}

func TestToolCrossSearch_MemoryValueIsTruncated(t *testing.T) {
	deps := setupCrossSearchDB(t)
	// Seed a row with a long value. The JSON response should carry
	// Truncate(value, 200) — at most 200 chars, with "..." tail.
	longVal := strings.Repeat("x", 500) + " hello " + strings.Repeat("y", 500)
	mustExec(t, deps.db,
		"INSERT INTO memories (id, key, value, type, collection_name, updated_at) VALUES (?, ?, ?, ?, ?, datetime('now'))",
		"long1", "long-key", longVal, "fact", "alpha")

	deps.searchPipelineFn = func(bool) SearchPipeline { return &fakeSearchPipeline{} }

	res := ToolCrossSearch(context.Background(), deps, map[string]any{
		"search_query": "hello",
		"collections":  []any{"alpha"},
	})
	resp := decodeCrossSearch(t, res)
	var longValueBack string
	for _, m := range resp.Results[0].Memories {
		if m["key"].(string) == "long-key" {
			longValueBack = m["value"].(string)
		}
	}
	if longValueBack == "" {
		t.Fatal("long-key row not returned")
	}
	if len(longValueBack) > crossSearchMemoryValueMaxLen {
		t.Errorf("value not truncated: len=%d, max=%d", len(longValueBack), crossSearchMemoryValueMaxLen)
	}
	if !strings.HasSuffix(longValueBack, "...") {
		t.Errorf("truncated value missing '...' suffix: %q", longValueBack)
	}
}

// ── pure-helper tests ──

func TestIsSensitiveKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"api_key", true},
		{"API_KEY", true},
		{"service.apikey", true},
		{"password", true},
		{"user_secret", true},
		{"my_token_v2", true},
		{"private_key", true},
		{"credential_store", true},
		{"passwd_check", true},
		{"ordinary_key", false},
		{"note", false},
		{"", false},
		{"apiKey", true}, // substring match ignores case
	}
	for _, c := range cases {
		if got := isSensitiveKey(c.key); got != c.want {
			t.Errorf("isSensitiveKey(%q) = %v, want %v", c.key, got, c.want)
		}
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
