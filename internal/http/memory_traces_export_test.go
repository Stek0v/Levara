package http

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stek0v/levara/pkg/audit"
)

func TestMemoryTraceExportStreamsGoodSanitizedJSONL(t *testing.T) {
	db, rm := newTraceExportFixture(t)
	now := time.Now().UTC()
	rm.Log(audit.Entry{RequestID: "r1", TS: now.Format(time.RFC3339Nano), TraceID: "good", Tool: "recall_memory", Outcome: audit.OutcomeOK, Collection: "levara", ClientName: "codex", ResultCount: 1, Args: map[string]any{"query": "token secret"}})
	rm.Log(audit.Entry{RequestID: "s1", TS: now.Add(time.Second).Format(time.RFC3339Nano), TraceID: "good", Tool: "save_memory", Outcome: audit.OutcomeOK, Collection: "levara", ClientName: "codex", Args: map[string]any{"key": "k", "value": "password"}})
	rm.Log(audit.Entry{RequestID: "b1", TS: now.Add(2 * time.Second).Format(time.RFC3339Nano), TraceID: "bad", Tool: "save_memory", Outcome: audit.OutcomeOK, Collection: "levara", ClientName: "codex", RepeatSave: true})
	waitAuditRows(t, db, 3)

	app := traceExportApp(db, rm, "root")
	resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/memory-traces/export?collection=levara&quality=good", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	scanner := bufio.NewScanner(strings.NewReader(body))
	var rows []curatedMemoryTrace
	for scanner.Scan() {
		var row curatedMemoryTrace
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	if len(rows) != 1 || rows[0].TrajectoryID != "trace:good" || rows[0].ReasonLabel != "good_memory_behavior" {
		t.Fatalf("rows=%+v", rows)
	}
	if strings.Contains(body, "password") || strings.Contains(body, "token secret") {
		t.Fatalf("export leaked raw args: %s", body)
	}
}

func TestMemoryTraceExportEmptyAndAdminRequired(t *testing.T) {
	db, rm := newTraceExportFixture(t)
	app := traceExportApp(db, rm, "root")
	resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/memory-traces/export?collection=missing", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("empty status=%d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if body := string(raw); body != "" {
		t.Fatalf("empty export body=%q", body)
	}

	denied := traceExportApp(db, rm, "user")
	resp, err = denied.Test(httptest.NewRequest("GET", "/api/v1/memory-traces/export", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Fatalf("denied status=%d", resp.StatusCode)
	}
}

func newTraceExportFixture(t *testing.T) (*sql.DB, *audit.ReadModel) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "trace-export.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, is_superuser INTEGER DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id, is_superuser) VALUES ('root', 1), ('user', 0)`); err != nil {
		t.Fatal(err)
	}
	rm, err := audit.NewReadModel(db, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		rm.Close()
		db.Close()
	})
	return db, rm
}

func traceExportApp(db *sql.DB, rm *audit.ReadModel, userID string) *fiber.App {
	app := fiber.New()
	app.Get("/api/v1/memory-traces/export", func(c *fiber.Ctx) error {
		c.Locals("user_id", userID)
		return memoryTraceExportHandler(APIConfig{DB: db, MCPAuditReadModel: rm})(c)
	})
	return app
}
