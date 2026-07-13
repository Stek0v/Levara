package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"testing"

	"github.com/stek0v/levara/pipeline"
)

// Smoke tests for the MCP tool registry. These lock in the contract that
// Claude Code, Cursor, Cline, and other MCP clients see when they call
// tools/list — field names + required fields + inputSchema validity.

func TestToolDescriptors_NotEmpty(t *testing.T) {
	tools := ToolDescriptors()
	if len(tools) < 15 {
		t.Errorf("got %d tools, want ≥ 15 (Levara advertises ~25)", len(tools))
	}
}

func TestToolDescriptors_EveryToolHasRequiredFields(t *testing.T) {
	for _, tool := range ToolDescriptors() {
		if tool.Name == "" {
			t.Errorf("tool with empty Name: %+v", tool)
		}
		if tool.Description == "" {
			t.Errorf("tool %q missing Description", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %q missing InputSchema", tool.Name)
			continue
		}
		// MCP clients reject schemas without type=object.
		if tool.InputSchema["type"] != "object" {
			t.Errorf("tool %q: InputSchema.type = %v, want 'object'",
				tool.Name, tool.InputSchema["type"])
		}
	}
}

func TestToolDescriptors_NamesUnique(t *testing.T) {
	seen := make(map[string]int)
	for _, tool := range ToolDescriptors() {
		seen[tool.Name]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("tool %q appears %d times", name, n)
		}
	}
}

func TestToolDescriptors_FreshSlicePerCall(t *testing.T) {
	// Callers must not be able to corrupt the canonical list by mutating the
	// returned slice. The function returns a new slice literal each call.
	a := ToolDescriptors()
	a[0].Name = "CORRUPTED"
	b := ToolDescriptors()
	if b[0].Name == "CORRUPTED" {
		t.Error("ToolDescriptors returned a shared slice — callers can corrupt")
	}
}

func TestToolDescriptors_JSONMarshalsCleanly(t *testing.T) {
	// MCP wire format: `{"tools": [...]}`. Our tools must round-trip through
	// json.Marshal/Unmarshal so the response handler can emit them verbatim.
	tools := ToolDescriptors()
	raw, err := json.Marshal(map[string]any{"tools": tools})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Tools) != len(tools) {
		t.Errorf("roundtrip length = %d, want %d", len(got.Tools), len(tools))
	}
	// Spot-check first tool's name survives the roundtrip.
	if got.Tools[0]["name"] != tools[0].Name {
		t.Errorf("first tool name lost: got %v, want %q",
			got.Tools[0]["name"], tools[0].Name)
	}
}

// Every advertised tool must carry an OutputSchema so MCP clients can validate
// the structuredContent returned from tools/call. This intentionally uses
// tools/list as the source of truth: adding a new tool without a schema should
// fail at the registry-contract layer before any client-specific smoke test.
func TestToolDescriptors_OutputSchemaCoverage(t *testing.T) {
	for _, tool := range ToolDescriptors() {
		if tool.OutputSchema == nil {
			t.Errorf("tool %q lacks OutputSchema", tool.Name)
			continue
		}
		if tool.OutputSchema["type"] != "object" {
			t.Errorf("tool %q OutputSchema.type = %v, want 'object'",
				tool.Name, tool.OutputSchema["type"])
		}
	}
}

// OutputSchema must round-trip through JSON alongside InputSchema.
func TestToolDescriptors_OutputSchemaMarshalsCleanly(t *testing.T) {
	for _, tool := range ToolDescriptors() {
		if tool.OutputSchema == nil {
			continue
		}
		raw, err := json.Marshal(tool)
		if err != nil {
			t.Errorf("tool %q marshal failed: %v", tool.Name, err)
			continue
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("tool %q unmarshal failed: %v", tool.Name, err)
			continue
		}
		if _, ok := got["outputSchema"]; !ok {
			t.Errorf("tool %q: outputSchema key missing after roundtrip", tool.Name)
		}
	}
}

func TestToolOutputsMatchRegisteredSchemas_RoundTrip(t *testing.T) {
	byName := make(map[string]Tool)
	for _, tool := range ToolDescriptors() {
		byName[tool.Name] = tool
	}

	memoryDeps := setupMemoryTestDB(t)
	seedMemory(t, memoryDeps.db, "m1", "alpha", "memory body", "project", "", "", "auth", "fact", 1, 8)

	recallDeps := setupSaveRecallMemoryDB(t)
	ToolSaveMemory(context.Background(), recallDeps, map[string]any{"key": "alpha", "value": "memory body"})

	diaryDeps := setupDiaryTestDB(t)
	ToolDiaryWrite(context.Background(), diaryDeps, map[string]any{"agent": "reviewer", "key": "note", "value": "body"})

	searchDeps := &fakeDeps{
		lexicalCollections: []string{"default"},
		lexicalFn: func(collection, query string, topK int) ([]LexicalResult, error) {
			return []LexicalResult{{ID: "lex-1", Score: 1.5, Metadata: []byte(`{"text":"hello"}`)}}, nil
		},
	}

	crossDeps := setupCrossSearchDB(t)
	crossDeps.searchPipelineFn = func(bool) SearchPipeline {
		return &fakeSearchPipeline{
			byText: func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error) {
				return []pipeline.ScoredResult{{ID: coll + "-1", Score: 0.5, Metadata: []byte(`{"text":"hit"}`)}}, nil
			},
		}
	}

	wakeDeps := setupWakeUpTestDB(t)
	consolidateDeps := newConsolidateDeps(t)
	dataDeps := &fakeDeps{
		db:          setupListDataTestDB(t),
		hasColls:    true,
		collections: []string{"default"},
	}
	addDeps := &fakeDeps{storagePath: t.TempDir()}
	chatDeps := setupChatTestDB(t)
	ToolSaveChat(context.Background(), chatDeps, map[string]any{
		"session_id": "s1",
		"messages":   []any{map[string]any{"role": "user", "content": "hello"}},
	})
	projectDeps := setupProjectDB(t)
	projectDeps.collectionMetas = map[string]CollectionInfo{
		"levara": {Name: "levara", Records: 1, Dim: 3, Metric: "cosine", EmbedModel: "model-a"},
	}
	driftDeps := &fakeDeps{
		collections: []string{"levara"},
		hasColls:    true,
		baseCfg:     projectDeps.baseCfg,
		collectionMetas: map[string]CollectionInfo{
			"levara": {Name: "levara", Records: 1, Dim: 3, Metric: "cosine", EmbedModel: "old-model"},
		},
	}
	communityDeps := setupCommunityDB(t)
	communityDeps.db.Exec(`INSERT INTO graph_communities (id, level, parent_id, member_count, summary) VALUES ('c1', 0, '', 3, 'summary')`)
	queryDeps := setupQueryEntityDB(t)
	queryDeps.db.Exec(`INSERT INTO graph_nodes (id, name, type, updated_at) VALUES ('n1', 'auth', 'service', '2026-01-01T00:00:00Z')`)
	queryDeps.db.Exec(`INSERT INTO graph_nodes (id, name, type, updated_at) VALUES ('n2', 'db', 'service', '2026-01-01T00:00:00Z')`)
	queryDeps.db.Exec(`INSERT INTO graph_edges (id, source_id, target_id, relationship_name, properties, valid_from, valid_until, superseded_by, confidence, updated_at) VALUES ('e1', 'n1', 'n2', 'calls', '{}', '2026-01-01T00:00:00Z', NULL, '', 0.9, '2026-03-01T00:00:00Z')`)
	gitDeps := &fakeDeps{
		searchPipelineFn: func(bool) SearchPipeline {
			return &fakeSearchPipeline{
				byText: func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error) {
					return []pipeline.ScoredResult{{ID: "commit-1", Score: 0.7, Metadata: []byte(`{"text":"commit"}`)}}, nil
				},
			}
		},
	}
	pruneDeps := setupPruneTestDB(t)
	deleteDeps := setupDepsTestDB(t)
	deleteDeps.db.Exec(`INSERT INTO datasets (id) VALUES ('ds-delete')`)
	feedbackDeps := setupFeedbackTestDB(t)
	session := &Session{}
	repoDir := t.TempDir()
	mustRunGit(t, repoDir, "init")
	mustRunGit(t, repoDir, "config", "user.email", "test@example.com")
	mustRunGit(t, repoDir, "config", "user.name", "Test User")
	if err := os.WriteFile(repoDir+"/file.txt", []byte("hello"), 0o644); err != nil {
		t.Fatalf("write git fixture: %v", err)
	}
	mustRunGit(t, repoDir, "add", "file.txt")
	mustRunGit(t, repoDir, "commit", "-m", "initial")

	cases := []struct {
		name string
		res  ToolResult
	}{
		{"save_memory", ToolSaveMemory(context.Background(), recallDeps, map[string]any{"key": "beta", "value": "body"})},
		{"recall_memory", ToolRecallMemory(context.Background(), recallDeps, map[string]any{"query": "alpha"})},
		{"list_memories", ToolListMemories(context.Background(), memoryDeps, map[string]any{})},
		{"pin_memory", ToolPinMemory(context.Background(), memoryDeps, map[string]any{"key": "alpha"})},
		{"unpin_memory", ToolUnpinMemory(context.Background(), memoryDeps, map[string]any{"key": "alpha"})},
		{"wake_up", ToolWakeUp(context.Background(), wakeDeps, map[string]any{})},
		{"delete_memory", ToolDeleteMemory(context.Background(), memoryDeps, map[string]any{"key": "alpha"})},
		{"consolidate", ToolConsolidate(context.Background(), consolidateDeps, map[string]any{"collection": "levara", "dry_run": true})},
		{"diary_write", ToolDiaryWrite(context.Background(), diaryDeps, map[string]any{"agent": "reviewer", "key": "other", "value": "body"})},
		{"diary_read", ToolDiaryRead(context.Background(), diaryDeps, map[string]any{"agent": "reviewer"})},
		{"search", ToolSearch(context.Background(), searchDeps, map[string]any{"search_query": "hello", "search_type": "CHUNKS_LEXICAL"})},
		{"cross_search", ToolCrossSearch(context.Background(), crossDeps, map[string]any{"search_query": "hello", "collections": []any{"alpha"}})},
		{"sync", ToolSync(context.Background(), memoryDeps, map[string]any{"remote_url": "http://example.test/api/v1"})},
		{"levara_instructions", ToolLevaraInstructions(context.Background(), nil, nil)},
		{"delete", ToolDelete(context.Background(), deleteDeps, map[string]any{"dataset_id": "ds-delete"})},
		{"prune", ToolPrune(context.Background(), pruneDeps)},
		{"list_data", ToolListData(context.Background(), dataDeps, map[string]any{})},
		{"add", ToolAdd(context.Background(), addDeps, map[string]any{"data": "hello", "dataset_name": "docs"})},
		{"save_chat", ToolSaveChat(context.Background(), chatDeps, map[string]any{"session_id": "s2", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})},
		{"recall_chat", ToolRecallChat(context.Background(), chatDeps, map[string]any{"session_id": "s1"})},
		{"search_chats", ToolSearchChats(context.Background(), chatDeps, map[string]any{"query": "hello"})},
		{"get_project_context", ToolGetProjectContext(context.Background(), projectDeps, map[string]any{"collection": "levara"})},
		{"set_context", ToolSetContext(session, memoryDeps, map[string]any{"collection": "levara"})},
		{"add_feedback", ToolAddFeedback(context.Background(), feedbackDeps, map[string]any{"query": "q", "rating": float64(5)})},
		{"get_feedback_stats", ToolGetFeedbackStats(context.Background(), feedbackDeps, map[string]any{})},
		{"check_drift", ToolCheckDrift(context.Background(), driftDeps, map[string]any{})},
		{"list_communities", ToolListCommunities(context.Background(), communityDeps, map[string]any{"min_members": float64(1)})},
		{"prune_graph", ToolPruneGraph(context.Background(), communityDeps, map[string]any{})},
		{"query_entity", ToolQueryEntity(context.Background(), queryDeps, map[string]any{"name": "auth"})},
		{"codify", ToolCodify(context.Background(), nilDBDeps{}, map[string]any{"code": "package main\nfunc main() {}\n", "filename": "main.go"})},
		{"git_search", ToolGitSearch(context.Background(), gitDeps, map[string]any{"query": "commit"})},
		{"analyze_commits", ToolAnalyzeCommits(context.Background(), &fakeDeps{}, map[string]any{"repo_path": repoDir, "limit": float64(1)})},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.res.IsError {
				t.Fatalf("tool returned error: %s", tc.res.Content[0].Text)
			}
			assertOutputSchema(t, byName[tc.name], tc.res)
		})
	}
}

func TestToolDescriptors_DoNotAdvertiseKnownStaleOutputFields(t *testing.T) {
	byName := make(map[string]Tool)
	for _, tool := range ToolDescriptors() {
		byName[tool.Name] = tool
	}

	searchProps := schemaProps(t, byName["search"].OutputSchema)
	if _, ok := searchProps["total"]; ok {
		t.Fatal("search OutputSchema still advertises stale total field")
	}
	if _, ok := searchProps["reranked"]; !ok {
		t.Fatal("search OutputSchema missing reranked field")
	}

	doctorProps := schemaProps(t, byName["doctor"].OutputSchema)
	for _, stale := range []string{"overall"} {
		if _, ok := doctorProps[stale]; ok {
			t.Fatalf("doctor OutputSchema still advertises stale %s field", stale)
		}
	}
	checks, _ := doctorProps["checks"].(map[string]any)
	items, _ := checks["items"].(map[string]any)
	checkProps := schemaProps(t, items)
	for _, stale := range []string{"detail", "hint"} {
		if _, ok := checkProps[stale]; ok {
			t.Fatalf("doctor check OutputSchema still advertises stale %s field", stale)
		}
	}
}

func assertOutputSchema(t *testing.T, tool Tool, res ToolResult) {
	t.Helper()
	if tool.OutputSchema == nil {
		t.Fatalf("tool %q has no OutputSchema", tool.Name)
	}
	if res.StructuredContent == nil {
		t.Fatalf("tool %q returned no structuredContent with OutputSchema", tool.Name)
	}
	if len(res.Content) == 0 {
		t.Fatalf("tool %q returned no content", tool.Name)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &payload); err != nil {
		t.Fatalf("tool %q returned non-object JSON: %v; raw=%q", tool.Name, err, res.Content[0].Text)
	}
	if err := validateJSONSchema(tool.OutputSchema, payload, "$"); err != nil {
		t.Fatalf("tool %q text mirror does not match OutputSchema: %v; raw=%s", tool.Name, err, res.Content[0].Text)
	}
	structured, err := normalizeJSONValue(res.StructuredContent)
	if err != nil {
		t.Fatalf("tool %q structuredContent is not JSON-serializable: %v", tool.Name, err)
	}
	if err := validateJSONSchema(tool.OutputSchema, structured, "$"); err != nil {
		t.Fatalf("tool %q structuredContent does not match OutputSchema: %v; value=%#v", tool.Name, err, structured)
	}
}

func normalizeJSONValue(v any) (any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func schemaProps(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties map: %+v", schema)
	}
	return props
}

func schemaRequired(schema map[string]any) []string {
	raw, _ := schema["required"].([]string)
	return raw
}

func mustRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func validateJSONSchema(schema map[string]any, value any, path string) error {
	if schema == nil {
		return nil
	}
	if enumRaw, ok := schema["enum"].([]string); ok && value != nil {
		got, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s: enum value has type %T, want string", path, value)
		}
		for _, want := range enumRaw {
			if got == want {
				break
			}
			if want == enumRaw[len(enumRaw)-1] {
				return fmt.Errorf("%s: enum value %q not in %v", path, got, enumRaw)
			}
		}
	}

	typ, _ := schema["type"].(string)
	if typ == "" {
		// Some intentionally loose schemas only carry a description.
		return nil
	}

	switch typ {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: type %T, want object", path, value)
		}
		props, _ := schema["properties"].(map[string]any)
		for key := range obj {
			if len(props) == 0 {
				continue
			}
			if _, ok := props[key]; !ok {
				return fmt.Errorf("%s.%s: key not declared in schema", path, key)
			}
		}
		for _, req := range schemaRequired(schema) {
			if _, ok := obj[req]; !ok {
				return fmt.Errorf("%s.%s: missing required key", path, req)
			}
		}
		for key, prop := range props {
			v, ok := obj[key]
			if !ok || v == nil {
				continue
			}
			child, ok := prop.(map[string]any)
			if !ok {
				continue
			}
			if err := validateJSONSchema(child, v, path+"."+key); err != nil {
				return err
			}
		}
	case "array":
		arr, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s: type %T, want array", path, value)
		}
		itemSchema, _ := schema["items"].(map[string]any)
		for i, item := range arr {
			if err := validateJSONSchema(itemSchema, item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s: type %T, want string", path, value)
		}
	case "integer":
		n, ok := value.(float64)
		if !ok || math.Trunc(n) != n {
			return fmt.Errorf("%s: value %v (%T), want integer", path, value, value)
		}
	case "number":
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("%s: type %T, want number", path, value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s: type %T, want boolean", path, value)
		}
	default:
		return fmt.Errorf("%s: unsupported schema type %q", path, typ)
	}
	return nil
}

func TestToolDescriptors_RequiredCoreTools(t *testing.T) {
	// These tool names are referenced by the dispatch switch in the handler
	// and documented in CLAUDE.md. Losing one = silent feature breakage for
	// every MCP client out there.
	required := []string{
		"cognify", "search", "list_data", "delete",
		"save_memory", "recall_memory", "set_context",
		"workspace_access_check", "workspace_context", "workspace_audit_log", "workspace_context_artifacts",
		"workspace_reindex_artifacts", "workspace_ops_status", "workspace_conflicts",
		"workspace_search", "workspace_index", "workspace_delete", "workspace_gc", "workspace_manifest",
		"workspace_read", "workspace_write", "workspace_reindex_paths",
		"workspace_reconcile", "workspace_index_jobs", "workspace_enqueue_index_job", "workspace_retry_index_job",
		"workspace_watch_status", "workspace_run_start", "workspace_run_get",
		"workspace_commit", "workspace_log", "workspace_revert",
	}
	have := make(map[string]struct{})
	for _, t := range ToolDescriptors() {
		have[t.Name] = struct{}{}
	}
	for _, name := range required {
		if _, ok := have[name]; !ok {
			t.Errorf("core tool %q missing from registry", name)
		}
	}
}

func TestToolProfilesAreExplicitAndBackwardCompatible(t *testing.T) {
	if ToolsetName("") != "full" || ToolsetName("unknown") != "full" || ToolsetName("light") != "memory" {
		t.Fatalf("unexpected toolset resolution: empty=%s unknown=%s light=%s", ToolsetName(""), ToolsetName("unknown"), ToolsetName("light"))
	}
	full := ToolDescriptorsForMode("full")
	core := ToolDescriptorsForMode("core")
	if len(core) == 0 || len(core)*100 > len(full)*40 {
		t.Fatalf("core size=%d full=%d, want at least 60%% reduction", len(core), len(full))
	}
	for _, forbidden := range []string{"delete", "workspace_delete", "sync", "consolidate"} {
		if ToolAllowedForMode("core", forbidden) {
			t.Errorf("core unexpectedly allows %s", forbidden)
		}
	}
	for _, required := range []string{"levara_instructions", "set_context", "wake_up", "save_memory", "recall_memory", "search", "doctor"} {
		if !ToolAllowedForMode("core", required) {
			t.Errorf("core missing %s", required)
		}
	}
}
