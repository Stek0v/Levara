package http

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

func TestAgentTrajectoriesFiltersPaginationAndAdminArgs(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, is_superuser INTEGER DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id, is_superuser) VALUES ('root', 1), ('user', 0)`); err != nil {
		t.Fatal(err)
	}
	rm, err := audit.NewReadModel(db, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer rm.Close()
	now := time.Now().UTC()
	rm.Log(audit.Entry{RequestID: "a1", TS: now.Format(time.RFC3339Nano), TraceID: "a", Tool: "save_memory", Outcome: audit.OutcomeOK, Collection: "levara", ClientName: "codex", Args: audit.SanitizeArgs(map[string]any{"key": "a", "password": "secret"})})
	rm.Log(audit.Entry{RequestID: "b1", TS: now.Add(time.Second).Format(time.RFC3339Nano), TraceID: "b", Tool: "search", Outcome: audit.OutcomeOK, Collection: "levara", ClientName: "claude"})
	rm.Log(audit.Entry{RequestID: "c1", TS: now.Add(2 * time.Second).Format(time.RFC3339Nano), TraceID: "c", Tool: "save_memory", Outcome: audit.OutcomeOK, Collection: "other", ClientName: "codex"})
	waitAuditRows(t, db, 3)

	app := fiber.New()
	cfg := APIConfig{DB: db, MCPAuditReadModel: rm}
	app.Get("/api/v1/agent-trajectories", agentTrajectoriesHandler(cfg))
	app.Get("/api/v1/admin/agent-trajectories", func(c *fiber.Ctx) error {
		c.Locals("user_id", "root")
		return agentTrajectoriesHandler(cfg)(c)
	})
	app.Get("/api/v1/admin/agent-trajectories/:id", func(c *fiber.Ctx) error {
		c.Locals("user_id", "root")
		return agentTrajectoryDetailHandler(cfg)(c)
	})
	app.Get("/api/v1/user/agent-trajectories", func(c *fiber.Ctx) error {
		c.Locals("user_id", "user")
		return agentTrajectoriesHandler(cfg)(c)
	})

	resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/agent-trajectories?collection=levara&client=codex&tool=save_memory&limit=1&offset=0", nil))
	if err != nil {
		t.Fatal(err)
	}
	var filtered struct {
		Total        int `json:"total"`
		Trajectories []struct {
			ID     string `json:"id"`
			Events []any  `json:"events"`
		} `json:"trajectories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&filtered); err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 1 || len(filtered.Trajectories) != 1 || filtered.Trajectories[0].ID != "trace:a" || len(filtered.Trajectories[0].Events) != 0 {
		t.Fatalf("filtered=%+v", filtered)
	}

	resp, err = app.Test(httptest.NewRequest("GET", "/api/v1/admin/agent-trajectories/trace:a?include_args=true", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("admin status=%d", resp.StatusCode)
	}
	body := map[string]any{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(body)
	if !strings.Contains(string(raw), `"key"`) || strings.Contains(string(raw), "secret") {
		t.Fatalf("admin args not present/sanitized correctly: %s", raw)
	}

	resp, err = app.Test(httptest.NewRequest("GET", "/api/v1/user/agent-trajectories?include_args=true", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Fatalf("non-admin include_args status=%d want 403", resp.StatusCode)
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
