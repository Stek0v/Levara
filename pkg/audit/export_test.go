package audit

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestAsyncExporterNeverBlocks proves the core invariant: a slow/stuck sink
// must never block the calling request. With the worker wedged in a blocking
// deliver, a flood of LogEvent calls returns near-instantly and the overflow is
// dropped (and counted), not queued unbounded or blocked.
func TestAsyncExporterNeverBlocks(t *testing.T) {
	release := make(chan struct{})
	var started atomic.Bool
	deliver := func(Event) error {
		started.Store(true)
		<-release
		return nil
	}
	e := NewAsyncExporter(deliver, ExportConfig{BufferSize: 4})

	const n = 1000
	start := time.Now()
	for i := 0; i < n; i++ {
		e.LogEvent(Event{Source: "t", Type: "op"})
	}
	elapsed := time.Since(start)
	if elapsed > 200*time.Millisecond {
		t.Fatalf("LogEvent loop blocked behind a stuck sink: %v", elapsed)
	}

	deadline := time.Now().Add(time.Second)
	for !started.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !started.Load() {
		t.Fatal("worker never engaged the sink")
	}

	if got := e.Stats().Dropped; got == 0 {
		t.Fatalf("expected events dropped under backpressure, got Dropped=0 (stats=%+v)", e.Stats())
	}

	close(release)
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := e.Stats().Delivered; got == 0 {
		t.Fatalf("expected drained events delivered after release, got Delivered=0")
	}
	// Sanity: nothing is created/delivered beyond what was enqueued.
	st := e.Stats()
	if st.Delivered > st.Enqueued {
		t.Fatalf("delivered %d > enqueued %d", st.Delivered, st.Enqueued)
	}
}

func TestAsyncExporterRetriesThenDelivers(t *testing.T) {
	var attempts atomic.Int32
	deliver := func(Event) error {
		if attempts.Add(1) < 3 {
			return errors.New("transient")
		}
		return nil
	}
	e := NewAsyncExporter(deliver, ExportConfig{BufferSize: 8, MaxRetries: 5, RetryBackoff: time.Millisecond})
	e.LogEvent(Event{Source: "t"})
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	st := e.Stats()
	if st.Delivered != 1 {
		t.Fatalf("Delivered = %d, want 1 (stats=%+v)", st.Delivered, st)
	}
	if st.Failed != 0 {
		t.Fatalf("Failed = %d, want 0", st.Failed)
	}
	if st.Retried != 2 {
		t.Fatalf("Retried = %d, want 2 (two failures before the third attempt succeeded)", st.Retried)
	}
}

func TestAsyncExporterFailsAfterMaxRetries(t *testing.T) {
	deliver := func(Event) error { return errors.New("always") }
	e := NewAsyncExporter(deliver, ExportConfig{BufferSize: 8, MaxRetries: 2, RetryBackoff: time.Millisecond})
	e.LogEvent(Event{Source: "t"})
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	st := e.Stats()
	if st.Failed != 1 {
		t.Fatalf("Failed = %d, want 1 (stats=%+v)", st.Failed, st)
	}
	if st.Delivered != 0 {
		t.Fatalf("Delivered = %d, want 0", st.Delivered)
	}
	if st.Retried != 2 {
		t.Fatalf("Retried = %d, want 2 (MaxRetries) before giving up", st.Retried)
	}
}

// TestSanitizeEventScrubsLeaks asserts the export boundary cannot emit markdown
// bodies, private file paths, raw snippets, secrets, or raw tokens, while
// preserving short safe scalar tags.
func TestSanitizeEventScrubsLeaks(t *testing.T) {
	e := Event{
		Source:  "workspace.write",
		Type:    "operation",
		Subject: "proj-1",
		ActorID: "user-1",
		Outcome: "ok",
		Metadata: map[string]any{
			"path":          "/Users/stek0v/private/secret.md",
			"snippet":       "line one\nline two with detail",
			"markdown":      "# Heading\n**bold** text",
			"api_key":       "sk-supersecretvalue",
			"session_token": "tok-raw-deadbeef",
			"embedding":     []float64{0.11, 0.22, 0.33},
			"branch":        "main",
			"status":        200,
			"access":        "granted",
		},
	}
	clean := SanitizeEvent(e)
	b, err := json.Marshal(clean)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(b)
	for _, leak := range []string{
		"private", "secret.md", "/Users", // private path
		"line two with detail", // raw snippet body
		"Heading", "**bold**",  // markdown content
		"sk-supersecretvalue", // secret value
		"tok-raw-deadbeef",    // raw token value
		"0.22",                // raw vector component
	} {
		if strings.Contains(js, leak) {
			t.Fatalf("leaked %q in exported event JSON: %s", leak, js)
		}
	}

	// Secret-keyed metadata is dropped entirely.
	if _, ok := clean.Metadata["api_key"]; ok {
		t.Fatal("api_key not dropped")
	}
	if _, ok := clean.Metadata["session_token"]; ok {
		t.Fatal("session_token not dropped")
	}
	// Vector collapsed to a descriptor, not raw components.
	if v, _ := clean.Metadata["embedding"].(string); !strings.HasPrefix(v, "<vector") {
		t.Fatalf("embedding = %v, want <vector …> descriptor", clean.Metadata["embedding"])
	}
	// Short safe scalars survive — the audit stays useful.
	if clean.Metadata["branch"] != "main" {
		t.Fatalf("branch = %v, want main", clean.Metadata["branch"])
	}
	if clean.Metadata["access"] != "granted" {
		t.Fatalf("access = %v, want granted", clean.Metadata["access"])
	}
	if clean.Metadata["status"] != 200 {
		t.Fatalf("status = %v, want 200", clean.Metadata["status"])
	}
	// Unsafe values are redacted (present as a marker, not dropped) so the
	// event still records that a field existed.
	if clean.Metadata["path"] != "<redacted>" {
		t.Fatalf("path = %v, want <redacted>", clean.Metadata["path"])
	}
}

func TestEventFileSinkWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	sink, err := NewEventFileSink(dir, 30)
	if err != nil {
		t.Fatalf("NewEventFileSink: %v", err)
	}
	if err := sink.WriteEvent(Event{
		Source:   "workspace.write",
		Type:     "operation",
		Subject:  "p",
		ActorID:  "u",
		Outcome:  "ok",
		Metadata: map[string]any{"branch": "main"},
	}); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	files, _ := filepath.Glob(filepath.Join(dir, "audit-*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("want 1 jsonl file, got %v", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var ev Event
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatalf("decode line %q: %v", data, err)
	}
	if ev.Source != "workspace.write" || ev.Subject != "p" || ev.Outcome != "ok" {
		t.Fatalf("decoded event = %+v", ev)
	}
	if ev.TS == "" {
		t.Fatal("TS not auto-stamped on write")
	}
	if ev.Metadata["branch"] != "main" {
		t.Fatalf("metadata = %v", ev.Metadata)
	}

	// After Close, writes fail rather than panic on a closed file.
	if err := sink.WriteEvent(Event{Source: "x"}); !errors.Is(err, errSinkClosed) {
		t.Fatalf("WriteEvent after Close = %v, want errSinkClosed", err)
	}
}

func TestEventFileSinkRetentionPrunes(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "audit-2000-01-01.jsonl")
	if err := os.WriteFile(oldPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().AddDate(0, 0, -100)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// Construction rotates → prunes files older than retentionDays(30).
	sink, err := NewEventFileSink(dir, 30)
	if err != nil {
		t.Fatalf("NewEventFileSink: %v", err)
	}
	defer sink.Close()

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old file not pruned (stat err=%v)", err)
	}
	// The freshly-opened active file survives the prune.
	files, _ := filepath.Glob(filepath.Join(dir, "audit-*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("want only the active file after prune, got %v", files)
	}
}
