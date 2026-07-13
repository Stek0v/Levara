package http

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stek0v/levara/pkg/mcp"
)

func TestSaveMemoryGuardrailBlindSaveWarningAndAudit(t *testing.T) {
	db := newMCPMemoryBehaviorDB(t)
	sink := &captureSink{}
	h := &mcpHandler{cfg: APIConfig{DB: db, MCPAudit: sink}, sessions: mcp.NewSessionStore()}
	sess := &mcp.Session{ID: "s1"}

	result := h.executeTool(t.Context(), sess, "save_memory", map[string]any{
		"key": "k", "value": "v", "collection": "levara", "room": "mcp", "hall": "fact",
	})
	if result.IsError {
		t.Fatalf("save_memory error: %+v", result.Content)
	}
	behavior := decodeMemoryBehavior(t, result)
	if behavior["blind_save"] != true || behavior["repeat_save"] != false {
		t.Fatalf("behavior=%+v", behavior)
	}
	if len(sink.entries) != 1 || !sink.entries[0].BlindSave || sink.entries[0].RepeatSave {
		t.Fatalf("audit=%+v", sink.entries)
	}
}

func TestSaveMemoryGuardrailNoBlindAfterRecall(t *testing.T) {
	db := newMCPMemoryBehaviorDB(t)
	sink := &captureSink{}
	h := &mcpHandler{cfg: APIConfig{DB: db, MCPAudit: sink}, sessions: mcp.NewSessionStore()}
	sess := &mcp.Session{ID: "s1"}

	recall := h.executeTool(t.Context(), sess, "recall_memory", map[string]any{"query": "anything", "collection": "levara"})
	if recall.IsError {
		t.Fatalf("recall_memory error: %+v", recall.Content)
	}
	result := h.executeTool(t.Context(), sess, "save_memory", map[string]any{
		"key": "k", "value": "v", "collection": "levara", "room": "mcp", "hall": "fact",
	})
	if result.IsError {
		t.Fatalf("save_memory error: %+v", result.Content)
	}
	if behavior := decodeOptionalMemoryBehavior(t, result); behavior != nil {
		t.Fatalf("unexpected behavior warning after recall: %+v", behavior)
	}
	if len(sink.entries) != 2 || sink.entries[1].BlindSave || sink.entries[1].RepeatSave {
		t.Fatalf("audit=%+v", sink.entries)
	}
}

func TestSaveMemoryGuardrailRepeatWarningAndAudit(t *testing.T) {
	db := newMCPMemoryBehaviorDB(t)
	sink := &captureSink{}
	h := &mcpHandler{cfg: APIConfig{DB: db, MCPAudit: sink}, sessions: mcp.NewSessionStore()}
	sess := &mcp.Session{ID: "s1", MemoryConsulted: true}

	first := h.executeTool(t.Context(), sess, "save_memory", map[string]any{
		"key": "k", "value": "v1", "collection": "levara", "room": "mcp", "hall": "fact",
	})
	if first.IsError {
		t.Fatalf("first save error: %+v", first.Content)
	}
	second := h.executeTool(t.Context(), sess, "save_memory", map[string]any{
		"key": "k", "value": "v2", "collection": "levara", "room": "mcp", "hall": "fact",
	})
	if second.IsError {
		t.Fatalf("second save error: %+v", second.Content)
	}
	behavior := decodeMemoryBehavior(t, second)
	if behavior["repeat_key"] != true || behavior["repeat_save"] != true || behavior["blind_save"] != false {
		t.Fatalf("behavior=%+v", behavior)
	}
	if len(sink.entries) != 2 || sink.entries[1].BlindSave || !sink.entries[1].RepeatSave {
		t.Fatalf("audit=%+v", sink.entries)
	}
}

func decodeMemoryBehavior(t *testing.T, result mcpToolResult) map[string]any {
	t.Helper()
	behavior := decodeOptionalMemoryBehavior(t, result)
	if behavior == nil {
		t.Fatalf("memory_behavior missing in %s", result.Content[0].Text)
	}
	return behavior
}

func decodeOptionalMemoryBehavior(t *testing.T, result mcpToolResult) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	behavior, _ := payload["memory_behavior"].(map[string]any)
	return behavior
}

func newMCPMemoryBehaviorDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "levara.db")+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	SetDBProvider(DBSQLite)
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		SetDBProvider(DBPostgres)
	})
	return db
}
