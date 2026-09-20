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
	if len(first.Messages) != 4 {
		t.Fatalf("messages = %d, want 4 (user, assistant, b4 text + b4 thinking; tool-only b3 skipped)", len(first.Messages))
	}
	if first.Messages[0].Role != "user" || first.Messages[1].Role != "assistant" {
		t.Fatalf("roles wrong: %+v", first.Messages)
	}
	if last := first.Messages[3]; last.Kind != KindReasoning || last.Content != "сначала осмотр" || last.ExternalID != "b4#t" {
		t.Fatalf("thinking block lost: %+v", last)
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
