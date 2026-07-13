package http

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stek0v/levara/pkg/audit"
)

func TestMemoryBehaviorAPI(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rm, err := audit.NewReadModel(db, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer rm.Close()
	now := time.Now().UTC()
	rm.Log(audit.Entry{RequestID: "r1", TS: now.Format(time.RFC3339Nano), TraceID: "tr1", Tool: "recall_memory", Outcome: audit.OutcomeOK, Collection: "levara", ClientName: "codex", ResultCount: 1})
	rm.Log(audit.Entry{RequestID: "s1", TS: now.Add(time.Second).Format(time.RFC3339Nano), TraceID: "tr1", Tool: "save_memory", Outcome: audit.OutcomeOK, Collection: "levara", ClientName: "codex", Args: map[string]any{"key": "k", "room": "mcp", "hall": "decision"}})
	rm.Log(audit.Entry{RequestID: "s2", TS: now.Add(2 * time.Second).Format(time.RFC3339Nano), TraceID: "tr2", Tool: "save_memory", Outcome: audit.OutcomeOK, Collection: "levara", ClientName: "codex", Args: map[string]any{"key": "k"}})
	waitAuditRows(t, db, 3)

	app := fiber.New()
	app.Get("/api/v1/memory-behavior", memoryBehaviorHandler(APIConfig{MCPAuditReadModel: rm}))
	resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/memory-behavior?collection=levara&hours=24", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var got struct {
		Summary struct {
			TotalTrajectories          int     `json:"total_trajectories"`
			RecallBeforeSaveRate       float64 `json:"recall_before_save_rate"`
			RepeatSaveRate             float64 `json:"repeat_save_rate"`
			SaveWithoutRoomOrHallCount int     `json:"save_without_room_or_hall_count"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Summary.TotalTrajectories != 2 || got.Summary.RecallBeforeSaveRate != 0.5 || got.Summary.RepeatSaveRate != 0.5 || got.Summary.SaveWithoutRoomOrHallCount != 1 {
		t.Fatalf("summary=%+v", got.Summary)
	}
}
