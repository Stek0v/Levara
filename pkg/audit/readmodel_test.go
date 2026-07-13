package audit

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestReadModelSummaryAndIdempotency(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rm, err := NewReadModel(db, 8)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rm.Log(Entry{RequestID: "same", TS: now, Tool: "search", Outcome: OutcomeOK, LatencyMS: 10, ZeroResult: true})
	rm.Log(Entry{RequestID: "same", TS: now, Tool: "search", Outcome: OutcomeOK, LatencyMS: 10, ZeroResult: true})
	rm.Log(Entry{RequestID: "error", TS: now, Tool: "recall_memory", Outcome: OutcomeTimeout, LatencyMS: 100})
	deadline := time.Now().Add(time.Second)
	for {
		var n int
		_ = db.QueryRow("SELECT COUNT(*) FROM mcp_audit_events").Scan(&n)
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("projection did not drain: %d", n)
		}
		time.Sleep(time.Millisecond)
	}
	s, err := rm.Summary(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if s.Total != 2 || s.Errors != 1 || s.ZeroResults != 1 || s.P95MS != 10 {
		t.Fatalf("summary=%+v", s)
	}
	rm.Close()
}

func TestReadModelImportEventsAndPrune(t *testing.T) {
	dir := t.TempDir()
	db, _ := sql.Open("sqlite3", "file:"+filepath.Join(dir, "audit.db"))
	defer db.Close()
	rm, err := NewReadModel(db, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer rm.Close()
	old := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano)
	fresh := time.Now().UTC().Format(time.RFC3339Nano)
	lines := `{"request_id":"old","ts":"` + old + `","tool":"search","latency_ms":1,"outcome":"ok"}` + "\n" + `{"request_id":"fresh","ts":"` + fresh + `","tool":"search","latency_ms":2,"outcome":"ok","zero_result":true}` + "\n" + `{broken` + "\n"
	if err = os.WriteFile(filepath.Join(dir, "mcp.log"), []byte(lines), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = rm.ImportDir(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	events, err := rm.Events(context.Background(), EventFilter{Since: time.Now().Add(-time.Hour), Tool: "search", Limit: 10})
	if err != nil || len(events) != 1 || events[0].ID != "fresh" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if n, err := rm.Prune(context.Background(), time.Now().Add(-24*time.Hour)); err != nil || n != 1 {
		t.Fatalf("prune=%d err=%v", n, err)
	}
}
