package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// setupChatTestDB builds the interactions schema for chat-tool tests.
// Columns match the production shape; only the ones ToolSaveChat writes
// and Recall/Search read matter here.
func setupChatTestDB(t *testing.T) *fakeDeps {
	t.Helper()
	f, _ := os.CreateTemp("", "mcp-chat-test-*.db")
	path := f.Name()
	f.Close()

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
		os.Remove(path)
	})

	stmt := `CREATE TABLE interactions (
		id TEXT PRIMARY KEY, session_id TEXT, user_id TEXT,
		query TEXT, response TEXT, search_type TEXT,
		created_at TEXT
	)`
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("create interactions: %v", err)
	}
	return &fakeDeps{db: db}
}

// ── ToolSaveChat ──

func TestToolSaveChat_RequiresSessionID(t *testing.T) {
	deps := setupChatTestDB(t)
	got := ToolSaveChat(context.Background(), deps, map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	if !got.IsError {
		t.Fatalf("missing session_id: IsError = false, want true")
	}
	if !strings.Contains(got.Content[0].Text, "'session_id' required") {
		t.Errorf("content = %q", got.Content[0].Text)
	}
}

func TestToolSaveChat_RequiresMessages(t *testing.T) {
	// Missing or empty messages array → IsError.
	deps := setupChatTestDB(t)
	cases := []map[string]any{
		{"session_id": "s1"},
		{"session_id": "s1", "messages": []any{}},
	}
	for i, args := range cases {
		got := ToolSaveChat(context.Background(), deps, args)
		if !got.IsError {
			t.Errorf("case %d: IsError = false, want true", i)
		}
	}
}

func TestToolSaveChat_NilDBIsError(t *testing.T) {
	// Chat storage is the tool's only purpose — absent DB is hard error.
	got := ToolSaveChat(context.Background(), nilDBDeps{}, map[string]any{
		"session_id": "s1",
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
	})
	if !got.IsError {
		t.Fatalf("nil DB: IsError = false, want true")
	}
	if !strings.Contains(got.Content[0].Text, "database not configured") {
		t.Errorf("content = %q", got.Content[0].Text)
	}
}

func TestToolSaveChat_MapsRoleToColumn(t *testing.T) {
	// user → query column; everything else → response column. Save 3
	// messages covering user / assistant / system and verify the rows.
	deps := setupChatTestDB(t)

	got := ToolSaveChat(context.Background(), deps, map[string]any{
		"session_id": "s1",
		"messages": []any{
			map[string]any{"role": "user", "content": "question"},
			map[string]any{"role": "assistant", "content": "answer"},
			map[string]any{"role": "system", "content": "sysmsg"},
		},
	})
	if got.IsError {
		t.Fatalf("IsError = true; content=%+v", got.Content)
	}
	if !strings.Contains(got.Content[0].Text, "Saved 3 messages") {
		t.Errorf("content = %q, want 'Saved 3 messages'", got.Content[0].Text)
	}

	// user row: query="question" response=""
	var q, r string
	if err := deps.db.QueryRow("SELECT query, response FROM interactions WHERE query = 'question'").Scan(&q, &r); err != nil {
		t.Fatalf("user row: %v", err)
	}
	if q != "question" || r != "" {
		t.Errorf("user row: q=%q r=%q, want q=question r=''", q, r)
	}

	// assistant + system rows: query="" response=content
	var cnt int
	if err := deps.db.QueryRow("SELECT COUNT(*) FROM interactions WHERE query = '' AND response IN ('answer','sysmsg')").Scan(&cnt); err != nil {
		t.Fatalf("non-user count: %v", err)
	}
	if cnt != 2 {
		t.Errorf("non-user rows = %d, want 2", cnt)
	}
}

func TestToolSaveChat_SkipsInvalidMessages(t *testing.T) {
	// Messages missing role or content are silently skipped (saved
	// counter only reflects accepted ones). Non-map entries also skip.
	deps := setupChatTestDB(t)

	got := ToolSaveChat(context.Background(), deps, map[string]any{
		"session_id": "s1",
		"messages": []any{
			map[string]any{"role": "user", "content": "ok"},
			map[string]any{"role": "user"},             // missing content
			map[string]any{"content": "orphan"},         // missing role
			"not-a-map",                                 // wrong type
			map[string]any{"role": "user", "content": ""}, // empty content
		},
	})
	if got.IsError {
		t.Fatalf("IsError = true")
	}
	if !strings.Contains(got.Content[0].Text, "Saved 1 messages") {
		t.Errorf("content = %q, want 'Saved 1 messages'", got.Content[0].Text)
	}
	var cnt int
	deps.db.QueryRow("SELECT COUNT(*) FROM interactions").Scan(&cnt)
	if cnt != 1 {
		t.Errorf("row count = %d, want 1", cnt)
	}
}

// ── ToolRecallChat ──

func TestToolRecallChat_RequiresSessionID(t *testing.T) {
	got := ToolRecallChat(context.Background(), setupChatTestDB(t), map[string]any{})
	if !got.IsError {
		t.Fatalf("missing session_id: IsError = false, want true")
	}
}

func TestToolRecallChat_NilDBReturnsEmpty(t *testing.T) {
	// Read contract: absent DB → "[]", not an error.
	got := ToolRecallChat(context.Background(), nilDBDeps{}, map[string]any{"session_id": "s1"})
	if got.IsError {
		t.Fatalf("nil DB: IsError = true, want false")
	}
	if got.Content[0].Text != "[]" {
		t.Errorf("content = %q, want []", got.Content[0].Text)
	}
}

func TestToolRecallChat_EmptySessionReturnsNotFoundMessage(t *testing.T) {
	// Schema exists but no rows for this session → specific message,
	// not "[]". Matches pre-refactor contract.
	deps := setupChatTestDB(t)
	got := ToolRecallChat(context.Background(), deps, map[string]any{"session_id": "missing"})
	if got.IsError {
		t.Fatalf("IsError = true")
	}
	if !strings.Contains(got.Content[0].Text, "No messages found") {
		t.Errorf("content = %q", got.Content[0].Text)
	}
}

func TestToolRecallChat_ExplodesRowsIntoMessages(t *testing.T) {
	// A row with both query and response emits TWO ToolResult messages
	// (user + assistant). A row with only query emits ONE (user).
	// Ordering is created_at ASC (pre-refactor contract).
	deps := setupChatTestDB(t)
	deps.db.Exec(`INSERT INTO interactions (id, session_id, query, response, created_at) VALUES
		('i1', 's1', 'q1', 'r1', '2026-01-01T00:00:00Z'),
		('i2', 's1', 'q2', '', '2026-01-02T00:00:00Z')`)

	got := ToolRecallChat(context.Background(), deps, map[string]any{"session_id": "s1"})
	if got.IsError {
		t.Fatalf("IsError = true")
	}
	var msgs []map[string]any
	if err := json.Unmarshal([]byte(got.Content[0].Text), &msgs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Expect 3 messages: user(q1), assistant(r1), user(q2).
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3 (i1 emits 2 + i2 emits 1)", len(msgs))
	}
	if msgs[0]["role"] != "user" || msgs[0]["content"] != "q1" {
		t.Errorf("msg[0] = %+v, want user/q1", msgs[0])
	}
	if msgs[1]["role"] != "assistant" || msgs[1]["content"] != "r1" {
		t.Errorf("msg[1] = %+v, want assistant/r1", msgs[1])
	}
	if msgs[2]["role"] != "user" || msgs[2]["content"] != "q2" {
		t.Errorf("msg[2] = %+v, want user/q2", msgs[2])
	}
}

func TestToolRecallChat_ScopesToSession(t *testing.T) {
	// Other sessions' messages must not leak in.
	deps := setupChatTestDB(t)
	deps.db.Exec(`INSERT INTO interactions (id, session_id, query, response, created_at) VALUES
		('a', 's1', 'mine', '', '2026-01-01T00:00:00Z'),
		('b', 's2', 'yours', '', '2026-01-01T00:00:00Z')`)

	got := ToolRecallChat(context.Background(), deps, map[string]any{"session_id": "s1"})
	var msgs []map[string]any
	json.Unmarshal([]byte(got.Content[0].Text), &msgs)
	if len(msgs) != 1 || msgs[0]["content"] != "mine" {
		t.Errorf("got %+v, want single 'mine'", msgs)
	}
}

// ── ToolSearchChats ──

func TestToolSearchChats_RequiresQuery(t *testing.T) {
	got := ToolSearchChats(context.Background(), setupChatTestDB(t), map[string]any{})
	if !got.IsError {
		t.Fatalf("missing query: IsError = false, want true")
	}
}

func TestToolSearchChats_NilDBReturnsEmpty(t *testing.T) {
	got := ToolSearchChats(context.Background(), nilDBDeps{}, map[string]any{"query": "anything"})
	if got.IsError {
		t.Fatalf("nil DB: IsError = true, want false")
	}
	if got.Content[0].Text != "[]" {
		t.Errorf("content = %q, want []", got.Content[0].Text)
	}
}

func TestToolSearchChats_MatchesQueryOrResponse(t *testing.T) {
	// Substring search hits the query column OR the response column.
	// "hello" matches i1 via query, "planet" matches i2 via response.
	deps := setupChatTestDB(t)
	deps.db.Exec(`INSERT INTO interactions (id, session_id, query, response, created_at) VALUES
		('i1', 's1', 'hello world', 'reply', '2026-01-01T00:00:00Z'),
		('i2', 's1', 'unrelated',   'hi planet', '2026-01-02T00:00:00Z'),
		('i3', 's1', 'nothing',     'matches', '2026-01-03T00:00:00Z')`)

	got := ToolSearchChats(context.Background(), deps, map[string]any{"query": "hello"})
	var rows []map[string]any
	json.Unmarshal([]byte(got.Content[0].Text), &rows)
	if len(rows) != 1 || rows[0]["id"] != "i1" {
		t.Errorf("hello query: got %+v, want single i1", rows)
	}

	got = ToolSearchChats(context.Background(), deps, map[string]any{"query": "planet"})
	rows = nil
	json.Unmarshal([]byte(got.Content[0].Text), &rows)
	if len(rows) != 1 || rows[0]["id"] != "i2" {
		t.Errorf("planet query: got %+v, want single i2 (matched via response)", rows)
	}
}

func TestToolSearchChats_NoMatchReturnsMessage(t *testing.T) {
	// Zero matches → specific "No matching chats found." text, not "[]".
	deps := setupChatTestDB(t)
	deps.db.Exec(`INSERT INTO interactions (id, session_id, query, response, created_at)
		VALUES ('i1', 's1', 'foo', 'bar', '2026-01-01T00:00:00Z')`)

	got := ToolSearchChats(context.Background(), deps, map[string]any{"query": "zzz"})
	if !strings.Contains(got.Content[0].Text, "No matching chats found") {
		t.Errorf("content = %q", got.Content[0].Text)
	}
}

// ── ToolDiaryWrite ──

// setupDiaryTestDB builds the memories schema with the UNIQUE(key,
// owner_id) constraint ToolDiaryWrite's ON CONFLICT clause targets.
// Can't reuse setupMemoryTestDB — that schema omits the constraint
// because ToolListMemories/Pin/Unpin don't care about upserts.
func setupDiaryTestDB(t *testing.T) *fakeDeps {
	t.Helper()
	f, _ := os.CreateTemp("", "mcp-diary-test-*.db")
	path := f.Name()
	f.Close()

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
		os.Remove(path)
	})

	stmt := `CREATE TABLE memories (
		id TEXT PRIMARY KEY, key TEXT, value TEXT, type TEXT, owner_id TEXT,
		collection_name TEXT, room TEXT, hall TEXT,
		is_pinned INTEGER DEFAULT 0, pin_priority INTEGER DEFAULT 0,
		created_at TEXT, updated_at TEXT,
		UNIQUE(key, owner_id)
	)`
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("create: %v", err)
	}
	return &fakeDeps{db: db}
}

func TestToolDiaryWrite_NilDBIsError(t *testing.T) {
	got := ToolDiaryWrite(context.Background(), nilDBDeps{}, map[string]any{
		"agent": "reviewer", "key": "k", "value": "v",
	})
	if !got.IsError {
		t.Fatalf("nil DB: IsError = false, want true")
	}
}

func TestToolDiaryWrite_RequiresAllThreeFields(t *testing.T) {
	// agent + key + value all required. Missing any → IsError.
	deps := setupDiaryTestDB(t)
	bad := []map[string]any{
		{"key": "k", "value": "v"},
		{"agent": "a", "value": "v"},
		{"agent": "a", "key": "k"},
		{"agent": "", "key": "k", "value": "v"},
	}
	for i, args := range bad {
		got := ToolDiaryWrite(context.Background(), deps, args)
		if !got.IsError {
			t.Errorf("case %d args=%+v: IsError = false, want true", i, args)
		}
	}
}

func TestToolDiaryWrite_InsertsRowWithAgentOwner(t *testing.T) {
	// owner_id = DiaryOwner(agent) = "agent:<name>". type = "diary".
	deps := setupDiaryTestDB(t)

	got := ToolDiaryWrite(context.Background(), deps, map[string]any{
		"agent": "reviewer",
		"key":   "pr-review-notes",
		"value": "saw a race in session.go",
	})
	if got.IsError {
		t.Fatalf("IsError = true; content=%+v", got.Content)
	}
	if !strings.Contains(got.Content[0].Text, "Diary[reviewer] wrote pr-review-notes") {
		t.Errorf("content = %q", got.Content[0].Text)
	}

	var owner, typ, value string
	err := deps.db.QueryRow(`SELECT owner_id, type, value FROM memories WHERE key = 'pr-review-notes'`).Scan(&owner, &typ, &value)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if owner != "agent:reviewer" {
		t.Errorf("owner = %q, want agent:reviewer", owner)
	}
	if typ != "diary" {
		t.Errorf("type = %q, want diary", typ)
	}
}

func TestToolDiaryWrite_UpsertOnConflict(t *testing.T) {
	// Same (key, owner_id) twice → second call updates value + updated_at
	// rather than erroring. This is the reason the SQL has ON CONFLICT.
	deps := setupDiaryTestDB(t)

	ToolDiaryWrite(context.Background(), deps, map[string]any{
		"agent": "reviewer", "key": "k1", "value": "first",
	})
	got := ToolDiaryWrite(context.Background(), deps, map[string]any{
		"agent": "reviewer", "key": "k1", "value": "second",
	})
	if got.IsError {
		t.Fatalf("upsert second call: IsError = true; content=%+v", got.Content)
	}

	var value string
	var count int
	deps.db.QueryRow(`SELECT value FROM memories WHERE key = 'k1'`).Scan(&value)
	deps.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE key = 'k1'`).Scan(&count)
	if count != 1 {
		t.Errorf("row count = %d, want 1 (upsert should not duplicate)", count)
	}
	if value != "second" {
		t.Errorf("value = %q, want 'second' (DO UPDATE SET should apply)", value)
	}
}

func TestToolDiaryWrite_DifferentAgentsNotConflicting(t *testing.T) {
	// Same key under different agents → two distinct rows. The ON
	// CONFLICT key is (key, owner_id), not key alone.
	deps := setupDiaryTestDB(t)

	ToolDiaryWrite(context.Background(), deps, map[string]any{
		"agent": "reviewer", "key": "shared", "value": "A",
	})
	ToolDiaryWrite(context.Background(), deps, map[string]any{
		"agent": "planner", "key": "shared", "value": "B",
	})

	var count int
	deps.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE key = 'shared'`).Scan(&count)
	if count != 2 {
		t.Errorf("row count = %d, want 2 (different agents = different owners)", count)
	}
}

// ── ToolDiaryRead ──

func TestToolDiaryRead_NilDBReturnsEmpty(t *testing.T) {
	got := ToolDiaryRead(context.Background(), nilDBDeps{}, map[string]any{"agent": "reviewer"})
	if got.IsError {
		t.Fatalf("nil DB: IsError = true, want false")
	}
	if got.Content[0].Text != "[]" {
		t.Errorf("content = %q, want []", got.Content[0].Text)
	}
}

func TestToolDiaryRead_RequiresAgent(t *testing.T) {
	got := ToolDiaryRead(context.Background(), setupDiaryTestDB(t), map[string]any{})
	if !got.IsError {
		t.Fatalf("missing agent: IsError = false, want true")
	}
}

func TestToolDiaryRead_EmptyDiaryReturnsPlaceholder(t *testing.T) {
	// No entries for this agent → specific "Diary[X] is empty" message,
	// not "[]". Helps UX distinguish "not configured" from "no writes".
	deps := setupDiaryTestDB(t)
	got := ToolDiaryRead(context.Background(), deps, map[string]any{"agent": "ghost"})
	if got.IsError {
		t.Fatalf("IsError = true")
	}
	if !strings.Contains(got.Content[0].Text, "Diary[ghost] is empty") {
		t.Errorf("content = %q", got.Content[0].Text)
	}
}

func TestToolDiaryRead_ScopesToAgentOwner(t *testing.T) {
	// Only entries with owner_id = "agent:<agent>" returned; shared
	// memories (owner_id = "") do NOT leak into a diary read, which
	// differs from the memory-recall semantics of ListMemories.
	deps := setupDiaryTestDB(t)
	ToolDiaryWrite(context.Background(), deps, map[string]any{
		"agent": "reviewer", "key": "mine", "value": "private note",
	})
	ToolDiaryWrite(context.Background(), deps, map[string]any{
		"agent": "planner", "key": "theirs", "value": "other agent",
	})

	got := ToolDiaryRead(context.Background(), deps, map[string]any{"agent": "reviewer"})
	if got.IsError {
		t.Fatalf("IsError = true")
	}
	var entries []map[string]any
	json.Unmarshal([]byte(got.Content[0].Text), &entries)
	if len(entries) != 1 || entries[0]["key"] != "mine" {
		t.Errorf("got %+v, want single 'mine' entry", entries)
	}
}

func TestToolDiaryRead_QuerySubstringMatches(t *testing.T) {
	// query arg matches key OR value via LIKE.
	deps := setupDiaryTestDB(t)
	ToolDiaryWrite(context.Background(), deps, map[string]any{
		"agent": "reviewer", "key": "auth-note", "value": "login race",
	})
	ToolDiaryWrite(context.Background(), deps, map[string]any{
		"agent": "reviewer", "key": "deploy-note", "value": "rollback plan",
	})

	got := ToolDiaryRead(context.Background(), deps, map[string]any{
		"agent": "reviewer", "query": "login",
	})
	var entries []map[string]any
	json.Unmarshal([]byte(got.Content[0].Text), &entries)
	if len(entries) != 1 || entries[0]["key"] != "auth-note" {
		t.Errorf("got %+v, want single auth-note (matched via value)", entries)
	}
}

func TestToolDiaryRead_CollectionFilter(t *testing.T) {
	// collection arg narrows results by collection_name column.
	deps := setupDiaryTestDB(t)
	ToolDiaryWrite(context.Background(), deps, map[string]any{
		"agent": "reviewer", "key": "a", "value": "v", "collection": "levara",
	})
	ToolDiaryWrite(context.Background(), deps, map[string]any{
		"agent": "reviewer", "key": "b", "value": "v", "collection": "other",
	})

	got := ToolDiaryRead(context.Background(), deps, map[string]any{
		"agent": "reviewer", "collection": "levara",
	})
	var entries []map[string]any
	json.Unmarshal([]byte(got.Content[0].Text), &entries)
	if len(entries) != 1 || entries[0]["key"] != "a" {
		t.Errorf("got %+v, want single 'a' (filtered by collection)", entries)
	}
}
