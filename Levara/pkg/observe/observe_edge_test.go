// observe_edge_test.go — FIX-11: edge-case rounds to the existing observe
// suite. The base suite already covers ErrorTracker dedup/ring/concurrency,
// Logger smoke, and Langfuse happy path + error path. These add:
//   - LOG_LEVEL env-var gating
//   - Langfuse payload fully populates Metadata + Status + usage counts
//   - ErrorTracker eviction correctly re-indexes the dedup map
package observe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// LOG_LEVEL=ERROR must suppress Info/Warn/Debug entries but allow Error.
// Reading the minLevel field directly is fine — it's the same package.
func TestLogger_LogLevelEnvGating(t *testing.T) {
	t.Setenv("LOG_LEVEL", "ERROR")
	l := NewLogger("test")
	if l.minLevel != LevelError {
		t.Errorf("LOG_LEVEL=ERROR → minLevel = %q, want ERROR", l.minLevel)
	}

	t.Setenv("LOG_LEVEL", "DEBUG")
	l2 := NewLogger("test2")
	if l2.minLevel != LevelDebug {
		t.Errorf("LOG_LEVEL=DEBUG → minLevel = %q, want DEBUG", l2.minLevel)
	}

	// Unknown values fall back to Info (the default).
	t.Setenv("LOG_LEVEL", "TRACE")
	l3 := NewLogger("test3")
	if l3.minLevel != LevelInfo {
		t.Errorf("unknown LOG_LEVEL → minLevel = %q, want INFO default", l3.minLevel)
	}
}

// TraceGeneration must populate the Langfuse body with every field the caller
// passed — missing Status or Metadata in production would lose diagnostic
// context on incident reviews.
func TestLangfuse_FullPayloadFieldsPresent(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	tr := NewLangfuseTracer(srv.URL, "pk", "sk")
	err := tr.TraceGeneration(context.Background(), TraceData{
		TraceID:   "trace-1",
		Name:      "entity_extraction",
		Model:     "gemma3:4b",
		Input:     "prompt text",
		Output:    `{"nodes":["x"]}`,
		LatencyMs: 250,
		TokensIn:  100,
		TokensOut: 42,
		Status:    "success",
		Metadata:  map[string]any{"room": "auth", "attempt": float64(2)},
	})
	if err != nil {
		t.Fatal(err)
	}

	batch, _ := body["batch"].([]any)
	if len(batch) != 1 {
		t.Fatalf("batch len = %d, want 1", len(batch))
	}
	entry := batch[0].(map[string]any)
	b := entry["body"].(map[string]any)

	if b["traceId"] != "trace-1" {
		t.Errorf("traceId = %v", b["traceId"])
	}
	if b["name"] != "entity_extraction" {
		t.Errorf("name = %v", b["name"])
	}
	if b["model"] != "gemma3:4b" {
		t.Errorf("model = %v", b["model"])
	}
	if b["statusMessage"] != "success" {
		t.Errorf("statusMessage = %v, want success", b["statusMessage"])
	}

	usage := b["usage"].(map[string]any)
	if usage["input"] != float64(100) || usage["output"] != float64(42) {
		t.Errorf("usage = %v, want input=100 output=42", usage)
	}

	md := b["metadata"].(map[string]any)
	if md["room"] != "auth" {
		t.Errorf("metadata.room = %v, want auth", md["room"])
	}
}

// Regression: after ring-buffer eviction, the dedup map must stay consistent
// with the errors slice. If re-indexing is wrong, a post-eviction Track on a
// surviving key bumps the wrong record or panics.
func TestErrorTracker_EvictionPreservesDedupConsistency(t *testing.T) {
	et := NewErrorTracker(3)

	// Fill with 5 distinct errors: A B C D E. A+B evicted, leaving C D E.
	for _, msg := range []string{"A", "B", "C", "D", "E"} {
		et.Track("cmp", msg, nil)
	}
	if et.Count() != 3 {
		t.Fatalf("Count = %d, want 3", et.Count())
	}

	// Bumping "C" (the oldest surviving) must increment its Count, not
	// create a duplicate. If the dedup index was left stale at position 2
	// (its original slot), it would now point past the slice and either
	// panic or mis-bump.
	et.Track("cmp", "C", nil)

	records := et.Recent(10)
	if len(records) != 3 {
		t.Fatalf("Recent len = %d, want 3", len(records))
	}

	// Find the record whose message ends with "C" — must have Count=2.
	var cCount int
	for _, r := range records {
		if r.Message == "C" {
			cCount = r.Count
		}
	}
	if cCount != 2 {
		t.Errorf("after re-Track of C, Count = %d, want 2", cCount)
	}

	// Brand-new track must create a fresh record, not collide with anything.
	et.Track("cmp", "F", errors.New("boom"))
	if got := et.Count(); got != 3 { // F replaced D via ring buffer
		t.Errorf("Count after adding F = %d, want 3", got)
	}
}
