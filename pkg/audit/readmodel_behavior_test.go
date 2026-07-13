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
