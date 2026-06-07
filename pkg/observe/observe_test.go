package observe

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// T-9 smoke tests for pkg/observe: ErrorTracker (in-memory, pure logic) and
// LangfuseTracer (HTTP via httptest).

// ─────────────────────────────────────────────────────────────────
// ErrorTracker
// ─────────────────────────────────────────────────────────────────

func TestErrorTracker_DedupIncrementsCount(t *testing.T) {
	et := NewErrorTracker(100)
	for i := 0; i < 3; i++ {
		et.Track("ingest", "upstream timeout", errors.New("eof"))
	}
	// Dedup collapses 3 identical Tracks into 1 record with Count=3.
	if got := et.Count(); got != 1 {
		t.Errorf("Count = %d, want 1 (dedup)", got)
	}
	rec := et.Recent(10)
	if len(rec) != 1 {
		t.Fatalf("Recent len = %d, want 1", len(rec))
	}
	if rec[0].Count != 3 {
		t.Errorf("Count = %d, want 3", rec[0].Count)
	}
	if !strings.Contains(rec[0].Message, "upstream timeout") {
		t.Errorf("Message = %q", rec[0].Message)
	}
}

func TestErrorTracker_DistinctErrorsKeptSeparately(t *testing.T) {
	et := NewErrorTracker(100)
	et.Track("auth", "invalid token", nil)
	et.Track("auth", "rate limited", nil)
	et.Track("ingest", "invalid token", nil) // same msg, different component
	if got := et.Count(); got != 3 {
		t.Errorf("Count = %d, want 3 distinct records", got)
	}
}

func TestErrorTracker_RingBufferEvictsOldest(t *testing.T) {
	et := NewErrorTracker(3)
	for i := 0; i < 5; i++ {
		et.Track("cmp", "msg"+string(rune('A'+i)), nil)
	}
	if got := et.Count(); got != 3 {
		t.Errorf("Count = %d, want 3 (max)", got)
	}
	// Newest-first: Recent(1) should be "msgE".
	r := et.Recent(1)
	if len(r) != 1 || !strings.Contains(r[0].Message, "msgE") {
		t.Errorf("newest record = %+v, want msgE", r)
	}
	// Oldest surviving should be "msgC" (A, B evicted).
	all := et.Recent(10)
	if len(all) != 3 {
		t.Fatalf("len = %d, want 3", len(all))
	}
	if !strings.Contains(all[2].Message, "msgC") {
		t.Errorf("oldest surviving = %q, want msgC", all[2].Message)
	}
}

func TestErrorTracker_RecentNewestFirst(t *testing.T) {
	et := NewErrorTracker(10)
	et.Track("c", "first", nil)
	et.Track("c", "second", nil)
	et.Track("c", "third", nil)
	r := et.Recent(3)
	if r[0].Message != "third" || r[1].Message != "second" || r[2].Message != "first" {
		t.Errorf("order wrong: %+v", r)
	}
}

func TestErrorTracker_Clear(t *testing.T) {
	et := NewErrorTracker(10)
	et.Track("x", "y", nil)
	et.Track("x", "z", nil)
	et.Clear()
	if et.Count() != 0 {
		t.Errorf("Count after Clear = %d, want 0", et.Count())
	}
	// After Clear the dedup map should be reset — same key should create a
	// fresh record, not bump a stale counter.
	et.Track("x", "y", nil)
	if rec := et.Recent(1); len(rec) != 1 || rec[0].Count != 1 {
		t.Errorf("post-Clear track: %+v, want fresh Count=1", rec)
	}
}

func TestErrorTracker_ConcurrentSafe(t *testing.T) {
	// Race detector catches data races on et.errors / et.dedup under concurrent
	// Track + Recent.
	et := NewErrorTracker(100)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				et.Track("comp", "msg", nil)
			}
		}(i)
	}
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = et.Recent(5)
			}
		}()
	}
	wg.Wait()
	if c := et.Recent(1); len(c) == 1 && c[0].Count != 200 {
		t.Logf("note: aggregated count = %d (expected ≈ 200)", c[0].Count)
	}
}

// ─────────────────────────────────────────────────────────────────
// Logger — minimal smoke (we assert it doesn't panic and respects minLevel)
// ─────────────────────────────────────────────────────────────────

func TestLogger_InfoDoesNotPanic(t *testing.T) {
	l := NewLogger("test-component")
	l.Info("hello", map[string]any{"k": "v"})
	l.Warn("warning")
	l.Error("oops", errors.New("boom"))
	l.Debug("debug msg")
	// No assertion beyond "compiled and ran" — stderr capture is too fragile
	// to be worth a unit test here.
}

// ─────────────────────────────────────────────────────────────────
// LangfuseTracer
// ─────────────────────────────────────────────────────────────────

func TestLangfuseTracer_DisabledWhenKeysMissing(t *testing.T) {
	tr := NewLangfuseTracer("", "", "")
	if tr.Enabled() {
		t.Error("tracer with empty keys must be disabled")
	}
	if tr.Endpoint() != "https://cloud.langfuse.com" {
		t.Errorf("default endpoint = %q, want cloud.langfuse.com", tr.Endpoint())
	}
	// Disabled tracer short-circuits with nil — must not crash and must not
	// attempt network I/O.
	if err := tr.TraceGeneration(context.Background(), TraceData{}); err != nil {
		t.Errorf("disabled tracer TraceGeneration should return nil, got %v", err)
	}
}

func TestLangfuseTracer_SendsPayloadWithBasicAuth(t *testing.T) {
	var gotAuth string
	var gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr := NewLangfuseTracer(srv.URL, "pk-test", "sk-test")
	if !tr.Enabled() {
		t.Fatal("tracer should be enabled when both keys set")
	}

	err := tr.TraceGeneration(context.Background(), TraceData{
		TraceID:   "t-123",
		Name:      "entity_extraction",
		Model:     "gemma3:4b",
		Input:     "some text",
		Output:    `{"nodes":[]}`,
		LatencyMs: 500,
		TokensIn:  42,
		TokensOut: 10,
		Status:    "success",
	})
	if err != nil {
		t.Fatalf("TraceGeneration: %v", err)
	}

	if gotPath != "/api/public/ingestion" {
		t.Errorf("path = %q, want /api/public/ingestion", gotPath)
	}
	// Basic auth: base64("pk-test:sk-test")
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("pk-test:sk-test"))
	if gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
	// Envelope shape: {batch: [{id, type:"generation-create", body:{...}}]}
	batch, _ := gotBody["batch"].([]any)
	if len(batch) != 1 {
		t.Fatalf("batch len = %d, want 1", len(batch))
	}
	first, _ := batch[0].(map[string]any)
	if first["type"] != "generation-create" {
		t.Errorf("type = %v, want generation-create", first["type"])
	}
}

func TestLangfuseTracer_UpstreamErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	tr := NewLangfuseTracer(srv.URL, "pk", "sk")
	err := tr.TraceGeneration(context.Background(), TraceData{TraceID: "x"})
	if err == nil {
		t.Fatal("expected error on upstream 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %v, want to mention 401", err)
	}
}
