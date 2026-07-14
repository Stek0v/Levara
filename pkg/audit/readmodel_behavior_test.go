package audit

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestReadModelPersistsMemoryBehaviorFlags(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rm, err := NewReadModel(db, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer rm.Close()
	now := time.Now().UTC()
	rm.Log(Entry{RequestID: "e1", TS: now.Format(time.RFC3339Nano), Tool: "save_memory", Outcome: OutcomeOK, BlindSave: true, RepeatSave: true})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		rows, err := rm.EventsForTrajectories(t.Context(), EventFilter{Since: now.Add(-time.Second), Limit: 10})
		if err == nil && len(rows) == 1 {
			if !rows[0].BlindSave || !rows[0].RepeatSave {
				t.Fatalf("row flags=%+v", rows[0])
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("audit row not projected")
}

func TestReadModelAddsMemoryBehaviorColumnsToExistingTable(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "audit-old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE mcp_audit_events (
		id TEXT PRIMARY KEY, ts TEXT NOT NULL, session_id TEXT, agent_id TEXT,
		client_name TEXT, client_version TEXT, toolset TEXT, tool TEXT NOT NULL,
		collection_name TEXT, args_json TEXT, latency_ms INTEGER NOT NULL,
		outcome TEXT NOT NULL, result_count INTEGER NOT NULL DEFAULT 0,
		zero_result INTEGER NOT NULL DEFAULT 0, request_bytes INTEGER NOT NULL DEFAULT 0,
		response_bytes INTEGER NOT NULL DEFAULT 0, trace_id TEXT, error_message TEXT
	)`)
	if err != nil {
		t.Fatal(err)
	}
	rm, err := NewReadModel(db, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer rm.Close()
	rm.Log(Entry{RequestID: "e1", TS: time.Now().UTC().Format(time.RFC3339Nano), Tool: "save_memory", Outcome: OutcomeOK, BlindSave: true})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var blind int
		err := db.QueryRow(`SELECT blind_save FROM mcp_audit_events WHERE id = 'e1'`).Scan(&blind)
		if err == nil {
			if blind != 1 {
				t.Fatalf("blind_save=%d want 1", blind)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("audit row not projected into migrated table")
}
