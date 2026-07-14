package audit

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func openPostgresAuditTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("LEVARA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LEVARA_TEST_POSTGRES_DSN is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	schema := fmt.Sprintf("audit_test_%d", time.Now().UnixNano())
	schema = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, schema)
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		db.Close()
		t.Fatalf("create schema: %v", err)
	}
	if _, err := db.Exec(`SET search_path TO ` + schema); err != nil {
		db.Close()
		t.Fatalf("set search_path: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
		_ = db.Close()
	})
	return db
}

func TestReadModelPostgresSummaryEventsAndIdempotency(t *testing.T) {
	db := openPostgresAuditTestDB(t)
	rm, err := NewReadModel(db, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer rm.Close()

	now := time.Now().UTC()
	rm.Log(Entry{RequestID: "same", TS: now.Format(time.RFC3339Nano), Tool: "search", Outcome: OutcomeOK, LatencyMS: 10, ResultCount: 0, ZeroResult: true, Collection: "levara", ClientName: "codex"})
	rm.Log(Entry{RequestID: "same", TS: now.Format(time.RFC3339Nano), Tool: "search", Outcome: OutcomeOK, LatencyMS: 10, ResultCount: 0, ZeroResult: true, Collection: "levara", ClientName: "codex"})
	rm.Log(Entry{RequestID: "save", TS: now.Add(time.Second).Format(time.RFC3339Nano), Tool: "save_memory", Outcome: OutcomeOK, LatencyMS: 5, Collection: "levara", ClientName: "codex", TraceID: "tr", BlindSave: true, RepeatSave: true, Args: map[string]any{"key": "k", "password": "[redacted]"}})
	rm.Log(Entry{RequestID: "err", TS: now.Add(2 * time.Second).Format(time.RFC3339Nano), Tool: "recall_memory", Outcome: OutcomeTimeout, LatencyMS: 100, Collection: "levara", ClientName: "codex"})

	deadline := time.Now().Add(2 * time.Second)
	for {
		var n int
		_ = db.QueryRow("SELECT COUNT(*) FROM mcp_audit_events").Scan(&n)
		if n == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("projection did not drain, rows=%d health=%+v", n, rm.Health())
		}
		time.Sleep(time.Millisecond)
	}

	s, err := rm.Summary(context.Background(), now.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if s.Total != 3 || s.Errors != 1 || s.ZeroResults != 1 || s.ByTool["save_memory"] != 1 {
		t.Fatalf("summary=%+v", s)
	}

	rows, err := rm.EventsForTrajectories(context.Background(), EventFilter{Since: now.Add(-time.Second), Collection: "levara", IncludeArgs: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows=%d want 3: %+v", len(rows), rows)
	}
	var sawSave bool
	for _, row := range rows {
		if row.ID == "save" {
			sawSave = true
			if !row.BlindSave || !row.RepeatSave || row.TraceID != "tr" || !strings.Contains(string(row.Args), "redacted") {
				t.Fatalf("save row=%+v args=%s", row, string(row.Args))
			}
		}
	}
	if !sawSave {
		t.Fatalf("save event not found: %+v", rows)
	}
}

func TestReadModelPostgresPrune(t *testing.T) {
	db := openPostgresAuditTestDB(t)
	rm, err := NewReadModel(db, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer rm.Close()
	old := time.Now().Add(-48 * time.Hour).UTC()
	fresh := time.Now().UTC()
	if err := rm.insertBatch(context.Background(), []Entry{
		{RequestID: "old", TS: old.Format(time.RFC3339Nano), Tool: "search", Outcome: OutcomeOK},
		{RequestID: "fresh", TS: fresh.Format(time.RFC3339Nano), Tool: "search", Outcome: OutcomeOK},
	}); err != nil {
		t.Fatal(err)
	}
	n, err := rm.Prune(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("prune n=%d err=%v", n, err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mcp_audit_events WHERE id = $1`, "fresh").Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("fresh rows=%d want 1", remaining)
	}
}

func TestReadModelPostgresDetectsDriver(t *testing.T) {
	db := openPostgresAuditTestDB(t)
	rm, err := NewReadModel(db, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer rm.Close()
	if !rm.postgres {
		t.Fatalf("postgres driver not detected: %T", db.Driver())
	}
	if got := rm.bind("SELECT ?::text, ?::int"); got != "SELECT $1::text, $2::int" {
		t.Fatalf("bind=%q", got)
	}
}

func TestReadModelPostgresOldTableMigration(t *testing.T) {
	db := openPostgresAuditTestDB(t)
	if _, err := db.Exec(`CREATE TABLE mcp_audit_events (
		id TEXT PRIMARY KEY, ts TEXT NOT NULL, session_id TEXT, agent_id TEXT,
		client_name TEXT, client_version TEXT, toolset TEXT, tool TEXT NOT NULL,
		collection_name TEXT, args_json TEXT, latency_ms INTEGER NOT NULL,
		outcome TEXT NOT NULL, result_count INTEGER NOT NULL DEFAULT 0,
		zero_result INTEGER NOT NULL DEFAULT 0, request_bytes INTEGER NOT NULL DEFAULT 0,
		response_bytes INTEGER NOT NULL DEFAULT 0, trace_id TEXT, error_message TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	rm, err := NewReadModel(db, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer rm.Close()
	if err := rm.insertBatch(context.Background(), []Entry{{RequestID: "e1", TS: time.Now().UTC().Format(time.RFC3339Nano), Tool: "save_memory", Outcome: OutcomeOK, BlindSave: true}}); err != nil {
		t.Fatal(err)
	}
	var blind int
	if err := db.QueryRow(`SELECT blind_save FROM mcp_audit_events WHERE id = $1`, "e1").Scan(&blind); err != nil {
		t.Fatal(err)
	}
	if blind != 1 {
		t.Fatalf("blind_save=%d want 1", blind)
	}
}
