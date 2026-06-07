package audit

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSanitizeArgsDropsSecrets(t *testing.T) {
	in := map[string]any{
		"query":      "hello",
		"password":   "hunter2",
		"api_key":    "sk-xxx",
		"auth_token": "bearer-abc",
		"token":      "raw",
		"user_id":    "alice",
	}
	out := SanitizeArgs(in)
	for _, k := range []string{"password", "api_key", "auth_token", "token"} {
		if _, ok := out[k]; ok {
			t.Errorf("secret key %q must be dropped", k)
		}
	}
	if out["query"] != "hello" {
		t.Errorf("non-secret %q clobbered: %v", "query", out["query"])
	}
}

func TestSanitizeArgsCollapsesVectors(t *testing.T) {
	in := map[string]any{
		"vector":    []float64{0, 1, 0},
		"embedding": []float32{1, 0, 0, 0},
	}
	out := SanitizeArgs(in)
	v, ok := out["vector"].(string)
	if !ok || !strings.HasPrefix(v, "<vector len=3 norm=1.0000") {
		t.Errorf("vector summary wrong: %q", v)
	}
	e, ok := out["embedding"].(string)
	if !ok || !strings.HasPrefix(e, "<vector len=4 norm=1.0000") {
		t.Errorf("embedding summary wrong: %q", e)
	}
}

func TestSanitizeArgsTruncatesLongStrings(t *testing.T) {
	long := strings.Repeat("x", 1000)
	out := SanitizeArgs(map[string]any{"q": long})
	got := out["q"].(string)
	if len(got) > maxFieldChars+5 {
		t.Errorf("string not truncated, len=%d", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncation marker missing: %q", got[len(got)-5:])
	}
}

func TestSanitizeArgsMarshalsNested(t *testing.T) {
	out := SanitizeArgs(map[string]any{
		"filter": map[string]any{"room": "auth", "tags": []any{"a", "b"}},
	})
	s, ok := out["filter"].(string)
	if !ok {
		t.Fatalf("nested map should serialize to string, got %T", out["filter"])
	}
	if !strings.Contains(s, `"room":"auth"`) {
		t.Errorf("nested map not serialized: %q", s)
	}
}

func TestLoggerWritesJSONLine(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger(&buf)
	l.Log(Entry{Tool: "search", LatencyMS: 12, Outcome: OutcomeOK})
	line := buf.String()
	if !strings.HasSuffix(line, "\n") {
		t.Errorf("expected newline terminator: %q", line)
	}
	var got Entry
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got.Tool != "search" || got.LatencyMS != 12 || got.Outcome != OutcomeOK {
		t.Errorf("entry roundtrip failed: %+v", got)
	}
	if got.TS == "" {
		t.Errorf("TS auto-fill missing")
	}
}

func TestEventSinkFunc(t *testing.T) {
	var got Event
	sink := EventSinkFunc(func(e Event) { got = e })
	sink.LogEvent(Event{Source: "workspace", Type: "write", Subject: "payments"})
	if got.Source != "workspace" || got.Type != "write" || got.Subject != "payments" {
		t.Fatalf("event=%+v, want forwarded event", got)
	}
}

func TestFileLoggerRotatesAndPrunes(t *testing.T) {
	dir := t.TempDir()
	fl, err := NewFileLogger(dir, 30)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	fl.Log(Entry{Tool: "search", Outcome: OutcomeOK})

	// Drop an ancient log file that should be pruned on next rotation.
	old := filepath.Join(dir, "mcp-2020-01-01.log.gz")
	if err := os.WriteFile(old, []byte("dead"), 0o644); err != nil {
		t.Fatal(err)
	}
	ancient := time.Now().AddDate(0, 0, -90)
	if err := os.Chtimes(old, ancient, ancient); err != nil {
		t.Fatal(err)
	}

	// Force rotation by pretending tomorrow.
	if err := fl.rotate(time.Now().UTC().AddDate(0, 0, 1)); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("ancient log should be pruned, stat err=%v", err)
	}

	// Previous day's .log should have been gzipped.
	entries, _ := os.ReadDir(dir)
	var gzCount int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".log.gz") {
			gzCount++
		}
	}
	if gzCount == 0 {
		t.Errorf("expected gzipped rotation artefact, got entries=%v", names(entries))
	}
	_ = fl.Close()
}

func names(es []os.DirEntry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}
