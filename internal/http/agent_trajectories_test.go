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

func TestAgentTrajectoriesAPI(t *testing.T) {
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
	rm.Log(audit.Entry{RequestID: "e1", TS: now.Format(time.RFC3339Nano), TraceID: "tr", Tool: "recall_memory", Outcome: audit.OutcomeOK, Collection: "levara", ClientName: "codex", RequestBytes: 10, ResponseBytes: 20})
	rm.Log(audit.Entry{RequestID: "e2", TS: now.Add(time.Second).Format(time.RFC3339Nano), TraceID: "tr", Tool: "save_memory", Outcome: audit.OutcomeOK, Collection: "levara", ClientName: "codex", RequestBytes: 30, ResponseBytes: 40})
	waitAuditRows(t, db, 2)

	app := fiber.New()
	app.Get("/api/v1/agent-trajectories", agentTrajectoriesHandler(APIConfig{MCPAuditReadModel: rm}))
	app.Get("/api/v1/agent-trajectories/:id", agentTrajectoryDetailHandler(APIConfig{MCPAuditReadModel: rm}))

	resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/agent-trajectories?collection=levara&limit=1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var list struct {
		Total        int `json:"total"`
		Trajectories []struct {
			ID       string `json:"id"`
			Counters struct {
				RecallCount   int `json:"recall_count"`
				SaveCount     int `json:"save_count"`
				RequestBytes  int `json:"request_bytes"`
				ResponseBytes int `json:"response_bytes"`
			} `json:"counters"`
			Events []any `json:"events"`
		} `json:"trajectories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || len(list.Trajectories) != 1 {
		t.Fatalf("list=%+v", list)
	}
	tr := list.Trajectories[0]
	if tr.ID != "trace:tr" || tr.Counters.RecallCount != 1 || tr.Counters.SaveCount != 1 || tr.Counters.RequestBytes != 40 || tr.Counters.ResponseBytes != 60 {
		t.Fatalf("trajectory=%+v", tr)
	}
	if len(tr.Events) != 0 {
		t.Fatalf("list endpoint should omit events")
	}

	resp, err = app.Test(httptest.NewRequest("GET", "/api/v1/agent-trajectories/trace:tr", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("detail status=%d", resp.StatusCode)
	}
	var detail struct {
		ID     string `json:"id"`
		Events []struct {
			Tool string `json:"tool"`
			Args any    `json:"args,omitempty"`
		} `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.ID != "trace:tr" || len(detail.Events) != 2 || detail.Events[0].Tool != "recall_memory" {
		t.Fatalf("detail=%+v", detail)
	}
	if detail.Events[0].Args != nil {
		t.Fatalf("non-admin detail leaked args: %+v", detail.Events[0].Args)
	}
}

func TestAgentTrajectoriesUnavailable(t *testing.T) {
	app := fiber.New()
	app.Get("/api/v1/agent-trajectories", agentTrajectoriesHandler(APIConfig{}))
	resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/agent-trajectories", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 503 {
		t.Fatalf("status=%d want 503", resp.StatusCode)
	}
}

func waitAuditRows(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var n int
		_ = db.QueryRow("SELECT COUNT(*) FROM mcp_audit_events").Scan(&n)
		if n == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	var n int
	_ = db.QueryRow("SELECT COUNT(*) FROM mcp_audit_events").Scan(&n)
	t.Fatalf("audit rows=%d want %d", n, want)
}
