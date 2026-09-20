package chatimport

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func buildCursorFixtureDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE cursorDiskKV (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	rows := []struct{ k, v string }{
		{"composerData:dd01", `{"composerId":"dd01","name":"Проверка плагина","createdAt":1758176588756,"currentDir":"/Users/demo/src/wp"}`},
		{"composerData:dd02", `{"composerId":"dd02","createdAt":1758180359884}`}, // no name → title fallback
		{"composerData:dd03", `{"composerId":"dd03","createdAt":1758180000000}`}, // no bubbles → dropped
		{"bubbleId:dd01:b1", `{"bubbleId":"b1","type":1,"text":"Сделай бэкап-плагин"}`},
		{"bubbleId:dd01:b2", `{"bubbleId":"b2","type":2,"text":"Разбираю структуру: wp-content/uploads..."}`},
		{"bubbleId:dd01:b3", `{"bubbleId":"b3","type":2,"text":"","toolFormerData":{"tool":39}}`}, // tool-only → skipped
		{"bubbleId:dd01:b4", `{"bubbleId":"b4","type":2,"text":"Готово","allThinkingBlocks":[{"text":"сначала осмотр"}]}`},
		{"bubbleId:dd02:b5", `{"bubbleId":"b5","type":1,"text":"Сгенерируй конфиг nginx"}`},
		{"bubbleId:dd02:b6", `{"bubbleId":"b6","type":2,"text":"server { listen 443; }"}`},
		{"bubbleId:dd01:b8", `{"bubbleId":"b8","type":2,"text":"","allThinkingBlocks":[{"text":"шаг один"},{"text":"шаг два"},{"text":""}]}`},
		{"bubbleId:ddXX:b7", `{"bubbleId":"b7","type":1,"text":"orphan"}`}, // no composer → skipped
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO cursorDiskKV (key, value) VALUES (?, ?)`, r.k, r.v); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestParseCursorChats(t *testing.T) {
	convs, stats, err := ParseCursorChats(buildCursorFixtureDB(t), DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(convs) != 2 {
		t.Fatalf("conversations = %d, want 2 (dd03 has no bubbles, orphan dropped)", len(convs))
	}
	first := convs[0]
	if first.SessionID != "dd01" || first.Title != "Проверка плагина" {
		t.Fatalf("first conversation wrong: %+v", first)
	}
	if first.CreatedAt != time.UnixMilli(1758176588756).UTC().Format(time.RFC3339) {
		t.Fatalf("createdAt = %q", first.CreatedAt)
	}
	if len(first.Messages) != 6 {
		t.Fatalf("messages = %d, want 6 (user, assistant, b4 text+thinking, b8 two thinking; tool-only b3 skipped)", len(first.Messages))
	}
	if first.Messages[0].Role != "user" || first.Messages[1].Role != "assistant" {
		t.Fatalf("roles wrong: %+v", first.Messages)
	}
	if m := first.Messages[3]; m.Kind != KindReasoning || m.Content != "сначала осмотр" || m.ExternalID != "b4#t0" {
		t.Fatalf("thinking block lost: %+v", m)
	}
	if last := first.Messages[5]; last.ExternalID != "b8#t1" {
		t.Fatalf("second thinking of b8 lost: %+v", last)
	}
	if second := convs[1]; second.Title == "" {
		t.Fatal("title fallback from first user message missing")
	}
	// Skipped: dd03 empty conversation, tool-only bubble, orphan bubble.
	if stats.Skipped < 3 {
		t.Fatalf("skipped = %d, want >= 3", stats.Skipped)
	}
}

func TestParseCursorChatsWithoutReasoning(t *testing.T) {
	opts := DefaultParseOptions()
	opts.IncludeReasoning = false
	convs, _, err := ParseCursorChats(buildCursorFixtureDB(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, c := range convs {
		total += len(c.Messages)
	}
	if total != 5 { // dd01: b1+b2+b4text; dd02: b5+b6; thinking excluded.
		t.Fatalf("total messages = %d, want 5 (thinking excluded)", total)
	}
}

// Multiple thinking blocks in one bubble must all survive: they get indexed
// external ids (#t0, #t1) instead of colliding on a single #t.
func TestParseCursorChatsMultipleThinkingBlocks(t *testing.T) {
	convs, _, err := ParseCursorChats(buildCursorFixtureDB(t), DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	var thinking []Message
	for _, c := range convs {
		for _, m := range c.Messages {
			if m.Kind == KindReasoning {
				thinking = append(thinking, m)
			}
		}
	}
	if len(thinking) != 3 { // b4: 1 block; b8: 2 non-empty blocks.
		t.Fatalf("thinking messages = %d, want 3: %+v", len(thinking), thinking)
	}
	ids := map[string]bool{}
	for _, m := range thinking {
		ids[m.ExternalID] = true
	}
	if !ids["b4#t0"] || !ids["b8#t0"] || !ids["b8#t1"] {
		t.Fatalf("indexed thinking ids wrong: %v", ids)
	}
}

// A state.vscdb without cursorDiskKV is an empty source, not an error.
func TestParseCursorChatsNoTableIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	convs, stats, err := ParseCursorChats(path, DefaultParseOptions())
	if err != nil {
		t.Fatalf("empty db must not error: %v", err)
	}
	if len(convs) != 0 || stats.Messages != 0 {
		t.Fatalf("empty db gave conversations: %+v", convs)
	}
}
