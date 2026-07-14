package http

import (
	"bytes"
	"context"
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
	llmmock "github.com/stek0v/levara/pkg/llm/mock"
)

func TestMemoryReviewPromptOmitsRawArgsAndParser(t *testing.T) {
	db, rm := newReviewAuditFixture(t)
	now := time.Now().UTC()
	rm.Log(audit.Entry{RequestID: "e1", TS: now.Format(time.RFC3339Nano), TraceID: "tr", Tool: "save_memory", Outcome: audit.OutcomeOK, Collection: "levara", ClientName: "codex", Args: map[string]any{"key": "k", "password": "secret"}})
	waitAuditRows(t, db, 1)

	traces, err := loadReviewTrajectories(context.Background(), APIConfig{MCPAuditReadModel: rm}, memoryReviewScope{Hours: 24, Collection: "levara", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	prompt := buildMemoryReviewPrompt(traces)
	if strings.Contains(prompt, "secret") || strings.Contains(prompt, "password") || strings.Contains(prompt, `"key"`) {
		t.Fatalf("prompt leaked raw args: %s", prompt)
	}
	parsed, err := parseMemoryReviewFindings("```json\n{\"summary\":\"ok\",\"findings\":[{\"category\":\"blind_save\",\"severity\":\"high\",\"trajectory_id\":\"trace:tr\",\"summary\":\"blind\",\"evidence\":\"save before recall\",\"recommendation\":\"recall first\"}]}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Summary != "ok" || len(parsed.Findings) != 1 || parsed.Findings[0].Category != "blind_save" {
		t.Fatalf("parsed=%+v", parsed)
	}
	if _, err := parseMemoryReviewFindings("{not json"); err == nil {
		t.Fatal("malformed LLM response should fail")
	}
}

func TestMemoryReviewDryRunDoesNotPersist(t *testing.T) {
	db, rm := newReviewAuditFixture(t)
	defer db.Close()
	now := time.Now().UTC()
	rm.Log(audit.Entry{RequestID: "e1", TS: now.Format(time.RFC3339Nano), TraceID: "tr", Tool: "save_memory", Outcome: audit.OutcomeOK, Collection: "levara", ClientName: "codex"})
	waitAuditRows(t, db, 1)

	app := fiber.New()
	app.Post("/api/v1/memory-reviews/run", memoryReviewRunHandler(APIConfig{DB: db, MCPAuditReadModel: rm}))
	req := httptest.NewRequest("POST", "/api/v1/memory-reviews/run", bytes.NewBufferString(`{"collection":"levara","dry_run":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM memory_review_runs").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("dry-run persisted %d runs", n)
	}
}

func TestMemoryReviewRunListDetailWithMockLLM(t *testing.T) {
	t.Setenv("LLM_MODEL", "mock-model")
	db, rm := newReviewAuditFixture(t)
	defer db.Close()
	now := time.Now().UTC()
	rm.Log(audit.Entry{RequestID: "r1", TS: now.Format(time.RFC3339Nano), TraceID: "tr", Tool: "recall_memory", Outcome: audit.OutcomeOK, Collection: "levara", ClientName: "codex", ResultCount: 0, ZeroResult: true})
	rm.Log(audit.Entry{RequestID: "s1", TS: now.Add(time.Second).Format(time.RFC3339Nano), TraceID: "tr", Tool: "save_memory", Outcome: audit.OutcomeOK, Collection: "levara", ClientName: "codex"})
	waitAuditRows(t, db, 2)

	llm := llmmock.New().OnAny().Reply(`{"summary":"reviewed","findings":[
		{"category":"missed_recall","severity":"medium","trajectory_id":"trace:tr","summary":"missed useful recall","evidence":"zero result then save","recommendation":"try reformulation"},
		{"category":"blind_save","severity":"high","trajectory_id":"trace:tr","summary":"blind save","evidence":"save without prior successful consult","recommendation":"recall before write"},
		{"category":"scaffold_recommendation","severity":"low","trajectory_id":"trace:tr","summary":"document room policy","evidence":"memory write pattern","recommendation":"add room taxonomy proposal"}
	]}`)
	app := fiber.New()
	cfg := APIConfig{DB: db, MCPAuditReadModel: rm, LLMProvider: llm.Provider()}
	app.Post("/api/v1/memory-reviews/run", memoryReviewRunHandler(cfg))
	app.Get("/api/v1/memory-reviews", memoryReviewListHandler(cfg))
	app.Get("/api/v1/memory-reviews/:id", memoryReviewDetailHandler(cfg))

	req := httptest.NewRequest("POST", "/api/v1/memory-reviews/run", bytes.NewBufferString(`{"collection":"levara","limit":5}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("run status=%d", resp.StatusCode)
	}
	var run memoryReviewRun
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" || run.Summary != "reviewed" || len(run.Findings) != 3 {
		t.Fatalf("run=%+v", run)
	}

	resp, err = app.Test(httptest.NewRequest("GET", "/api/v1/memory-reviews", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("list status=%d", resp.StatusCode)
	}
	var list struct {
		Runs []memoryReviewRun `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Runs) != 1 || list.Runs[0].ID != run.ID {
		t.Fatalf("list=%+v", list)
	}

	resp, err = app.Test(httptest.NewRequest("GET", "/api/v1/memory-reviews/"+run.ID, nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("detail status=%d", resp.StatusCode)
	}
	var detail memoryReviewRun
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.ID != run.ID || len(detail.Findings) != 3 {
		t.Fatalf("detail=%+v", detail)
	}
}

func TestMemoryReviewRunFailsWithoutLLM(t *testing.T) {
	db, rm := newReviewAuditFixture(t)
	defer db.Close()
	now := time.Now().UTC()
	rm.Log(audit.Entry{RequestID: "e1", TS: now.Format(time.RFC3339Nano), TraceID: "tr", Tool: "save_memory", Outcome: audit.OutcomeOK, Collection: "levara", ClientName: "codex"})
	waitAuditRows(t, db, 1)

	app := fiber.New()
	app.Post("/api/v1/memory-reviews/run", memoryReviewRunHandler(APIConfig{DB: db, MCPAuditReadModel: rm}))
	req := httptest.NewRequest("POST", "/api/v1/memory-reviews/run", bytes.NewBufferString(`{"collection":"levara"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 503 {
		t.Fatalf("status=%d want 503", resp.StatusCode)
	}
	var run memoryReviewRun
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || !strings.Contains(run.Error, "LLM") {
		t.Fatalf("run=%+v", run)
	}
}

func TestMemoryReviewRunFailsOnMalformedLLMResponse(t *testing.T) {
	t.Setenv("LLM_MODEL", "mock-model")
	db, rm := newReviewAuditFixture(t)
	now := time.Now().UTC()
	rm.Log(audit.Entry{RequestID: "e1", TS: now.Format(time.RFC3339Nano), TraceID: "tr", Tool: "save_memory", Outcome: audit.OutcomeOK, Collection: "levara", ClientName: "codex"})
	waitAuditRows(t, db, 1)

	llm := llmmock.New().OnAny().Reply(`not-json`)
	app := fiber.New()
	app.Post("/api/v1/memory-reviews/run", memoryReviewRunHandler(APIConfig{DB: db, MCPAuditReadModel: rm, LLMProvider: llm.Provider()}))
	req := httptest.NewRequest("POST", "/api/v1/memory-reviews/run", bytes.NewBufferString(`{"collection":"levara"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 503 {
		t.Fatalf("status=%d want 503", resp.StatusCode)
	}
	var run memoryReviewRun
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || !strings.Contains(run.Error, "valid JSON") {
		t.Fatalf("run=%+v", run)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM memory_review_runs WHERE status = 'failed'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("failed runs=%d want 1", n)
	}
}

func TestMemoryReviewNormalizesUnknownCategoryAndSeverity(t *testing.T) {
	run := memoryReviewRun{ID: "run", Status: "running", Scope: memoryReviewScope{Collection: "levara"}}
	parsed := memoryReviewLLMResponse{Summary: "ok"}
	parsed.Findings = append(parsed.Findings, struct {
		Category       string `json:"category"`
		Severity       string `json:"severity"`
		TrajectoryID   string `json:"trajectory_id"`
		Summary        string `json:"summary"`
		Evidence       string `json:"evidence"`
		Recommendation string `json:"recommendation"`
	}{Category: "new_category", Severity: "critical", Summary: "unknown", Evidence: "e", Recommendation: "r"})
	raw, _ := json.Marshal(parsed)
	llm := llmmock.New().OnAny().Reply(string(raw))
	t.Setenv("LLM_MODEL", "mock-model")
	got := executeMemoryReview(context.Background(), APIConfig{LLMProvider: llm.Provider()}, run, nil, "prompt")
	if got.Status != "completed" || len(got.Findings) != 1 {
		t.Fatalf("run=%+v", got)
	}
	if got.Findings[0].Category != "scaffold_recommendation" || got.Findings[0].Severity != "medium" {
		t.Fatalf("finding=%+v", got.Findings[0])
	}
}

func newReviewAuditFixture(t *testing.T) (*sql.DB, *audit.ReadModel) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
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
