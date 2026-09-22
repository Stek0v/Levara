package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stek0v/levara/pkg/llm"
)

// stubDistillProvider returns a canned (fenced, noisy) completion so the
// defensive parser is exercised the way small local models behave.
type stubDistillProvider struct{ content string }

func (p *stubDistillProvider) Name() string { return "stub" }

func (p *stubDistillProvider) ChatCompletion(_ context.Context, _ llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return &llm.CompletionResponse{Content: p.content}, nil
}

type distillDeps struct {
	*fakeDeps
	prov llm.Provider
}

func (d *distillDeps) LLMProvider() llm.Provider { return d.prov }

func distillTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "distill.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE memories (id TEXT PRIMARY KEY, key TEXT, value TEXT, type TEXT, owner_id TEXT,
			collection_name TEXT, room TEXT, hall TEXT, is_pinned INTEGER DEFAULT 0, pin_priority INTEGER DEFAULT 0,
			created_at TEXT, updated_at TEXT, UNIQUE(key, owner_id, collection_name))`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureChatImportTables(db); err != nil {
		t.Fatal(err)
	}
	title := "Выбор storage layer"
	for i, m := range []struct{ role, kind, content string }{
		{"developer", "system", "<app-context> boilerplate"},
		{"user", "text", "Что выбрать: SQLite или Postgres для Pi?"},
		{"assistant", "reasoning", "Ограничение RAM на Pi — ключевой фактор"},
		{"assistant", "text", "Берём SQLite: лимит RAM на Pi, WAL покрывает наши записи"},
		{"assistant", "tool_call", "exec\nsqlite3 pragma"},
		{"tool", "tool_result", "wal"},
	} {
		if _, err := db.Exec(`INSERT INTO chat_import_messages
			(id, run_id, platform, session_id, session_title, external_id, ordinal, role, kind, model, content, source_created_at, metadata, imported_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			fmt.Sprintf("id-%d", i), "run-1", "codex", "sess-d1", title, fmt.Sprintf("x-%d", i), i,
			m.role, m.kind, "gpt-5.2-codex", m.content, "2026-09-15T10:00:00Z", "{}", "2026-09-20T13:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func ensureChatImportTables(db *sql.DB) error {
	for _, stmt := range chatimportTestSchema() {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func chatimportTestSchema() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS chat_import_runs (
			id TEXT PRIMARY KEY, platform TEXT, source_path TEXT DEFAULT '', source_sha256 TEXT DEFAULT '',
			status TEXT DEFAULT 'running', imported_count INTEGER DEFAULT 0, skipped_count INTEGER DEFAULT 0,
			warned_count INTEGER DEFAULT 0, warnings TEXT DEFAULT '[]', started_at TEXT, finished_at TEXT DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS chat_import_messages (
			id TEXT PRIMARY KEY, run_id TEXT, platform TEXT, session_id TEXT, session_title TEXT DEFAULT '',
			external_id TEXT, ordinal INTEGER, role TEXT, kind TEXT, model TEXT DEFAULT '', content TEXT DEFAULT '',
			source_created_at TEXT DEFAULT '', metadata TEXT DEFAULT '{}', imported_at TEXT,
			UNIQUE(external_id, session_id, platform))`,
	}
}

func TestChatDistillEndToEnd(t *testing.T) {
	db := distillTestDB(t)
	deps := &distillDeps{fakeDeps: &fakeDeps{db: db}, prov: &stubDistillProvider{content: "Вот что я вытащил:\n```json\n[{\"key\": \"Pi Storage Choice\", \"value\": \"Выбран SQLite из-за лимита RAM на Pi; WAL покрывает нагрузку записи.\"},\n{\"key\": \"\", \"value\": \"пустой ключ должен отфильтроваться\"},\n{\"key\": \"второй\", \"value\": \"Запасная запись\"}]\n```"}}

	res := ToolChatDistill(context.Background(), deps, map[string]any{
		"platform": "codex", "session_id": "sess-d1", "hall": "decision",
	})
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	var payload struct {
		Saved      int                `json:"saved"`
		Candidates []DistillCandidate `json:"candidates"`
		Hall       string             `json:"hall"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Saved != 2 || len(payload.Candidates) != 2 {
		t.Fatalf("saved=%d candidates=%d, want 2/2 (empty key filtered; Cyrillic key transliterated)", payload.Saved, len(payload.Candidates))
	}
	c := payload.Candidates[0]
	if c.Key != "pi-storage-choice" {
		t.Fatalf("key not slugified: %q", c.Key)
	}
	if !strings.Contains(c.Value, "источник: codex session sess-d1") || !strings.Contains(c.Value, "Выбор storage layer") {
		t.Fatalf("provenance missing: %q", c.Value)
	}

	var value, room, hall string
	if err := db.QueryRow(`SELECT value, room, hall FROM memories WHERE key='pi-storage-choice'`).Scan(&value, &room, &hall); err != nil {
		t.Fatalf("memory not saved: %v", err)
	}
	if hall != "decision" || room != "chat-import" || !strings.Contains(value, "SQLite") {
		t.Fatalf("memory row wrong: %q/%q/%q", value, room, hall)
	}

	// Idempotent re-distill: same key upserts, no duplicate rows.
	_ = ToolChatDistill(context.Background(), deps, map[string]any{"platform": "codex", "session_id": "sess-d1"})
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("memories = %d, want 2 (upsert not duplicate)", count)
	}
}

func TestChatDistillDryRunAndValidation(t *testing.T) {
	db := distillTestDB(t)
	deps := &distillDeps{fakeDeps: &fakeDeps{db: db}, prov: &stubDistillProvider{content: `[{"key":"k","value":"v"}]`}}

	res := ToolChatDistill(context.Background(), deps, map[string]any{"platform": "chatgpt", "session_id": "x"})
	if !res.IsError {
		t.Fatal("unknown platform must error")
	}
	res = ToolChatDistill(context.Background(), deps, map[string]any{"platform": "codex", "session_id": "missing"})
	if !res.IsError {
		t.Fatal("missing session must error")
	}
	res = ToolChatDistill(context.Background(), deps, map[string]any{"platform": "codex", "session_id": "sess-d1", "dry_run": true})
	if res.IsError {
		t.Fatalf("dry_run error: %+v", res.Content)
	}
	var dry struct {
		DryRun bool `json:"dry_run"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &dry); err != nil || !dry.DryRun {
		t.Fatalf("dry_run flag missing: %s", res.Content[0].Text)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("dry_run wrote %d rows", count)
	}
}

func TestParseDistillCandidates(t *testing.T) {
	got, err := parseDistillCandidates("просто текст", 5)
	if err == nil {
		t.Fatalf("no JSON must error, got %v", got)
	}
	got, err = parseDistillCandidates("```json\n[{\"key\":\"A B\",\"value\":\"v\"}]\n```", 5)
	if err != nil || len(got) != 1 || got[0].Key != "a-b" {
		t.Fatalf("fenced parse: %v %v", got, err)
	}
	got, err = parseDistillCandidates("```json\n{\"key\":\"Solo Choice\",\"value\":\"v\"}\n```", 5)
	if err != nil || len(got) != 1 || got[0].Key != "solo-choice" {
		t.Fatalf("single-object fallback: %v %v", got, err)
	}
	got, err = parseDistillCandidates(`[{"key":"a","value":"1"},{"key":"b","value":"2"},{"key":"c","value":"3"}]`, 2)
	if err != nil || len(got) != 2 {
		t.Fatalf("max cap: %v %v", got, err)
	}
}

func TestSlugifyDistillKeyCyrillic(t *testing.T) {
	for in, want := range map[string]string{
		"Задача":                "zadacha",
		"Выбор storage layer":   "vybor-storage-layer",
		"Pi-хранилище (SQLite)": "pi-hranilische-sqlite",
		"  mixed СЛУЧАЙ 42 ":    "mixed-sluchai-42",
	} {
		if got := slugifyDistillKey(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseDistillCandidatesPairShape(t *testing.T) {
	got, err := parseDistillCandidates("```json\n[{\"project-structure\": \"TS-ядро с MCP\"}, {\"key\":\"k2\",\"value\":\"v2\"}]\n```", 5)
	if err != nil || len(got) != 2 {
		t.Fatalf("pair-shape parse: %v %v", got, err)
	}
	if got[0].Key != "project-structure" || got[0].Value != "TS-ядро с MCP" {
		t.Fatalf("pair-shape entry wrong: %+v", got[0])
	}
	if got[1].Key != "k2" || got[1].Value != "v2" {
		t.Fatalf("kv entry wrong: %+v", got[1])
	}
}
