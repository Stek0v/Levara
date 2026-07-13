package http

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestMemoryScaffoldProposalsFromFindingsCollapseDuplicates(t *testing.T) {
	db := newScaffoldDB(t)
	run := memoryReviewRun{
		ID:     "run-1",
		Status: "completed",
		Scope:  memoryReviewScope{Collection: "levara"},
		Findings: []memoryReviewFinding{
			{ID: "f1", Category: "blind_save", Severity: "high", Summary: "Blind save pattern", Evidence: "save before recall", Recommendation: "Add consult-before-write reminder"},
			{ID: "f2", Category: "blind_save", Severity: "high", Summary: "Blind save pattern", Evidence: "save before recall", Recommendation: "Add consult-before-write reminder"},
			{ID: "f3", Category: "useful_memory_pattern", Severity: "low", Summary: "Good pattern"},
		},
	}
	ctx := context.Background()
	proposals, err := createMemoryScaffoldProposalsFromReview(ctx, db, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(proposals) != 2 {
		t.Fatalf("returned proposals=%d want 2 attempts", len(proposals))
	}
	list, err := listMemoryScaffoldProposals(ctx, db, scaffoldProposalFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("stored proposals=%d want 1 after digest collapse", len(list))
	}
	if list[0].Target != "project_agents" || list[0].Status != "open" || len(list[0].SourceFindingIDs) != 2 {
		t.Fatalf("proposal=%+v", list[0])
	}
}

func TestMemoryScaffoldProposalDecisionTransitions(t *testing.T) {
	db := newScaffoldDB(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	proposal := memoryScaffoldProposal{
		ID:               "p1",
		Target:           "memory_policy",
		Collection:       "levara",
		Summary:          "Clarify room policy",
		CurrentProblem:   "wrong hall",
		ProposedChange:   "Document halls",
		Risk:             "Medium risk",
		Status:           "open",
		SourceFindingIDs: []string{"f1"},
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	proposal.Digest = scaffoldProposalDigest(proposal)
	ctx := context.Background()
	if _, err := upsertMemoryScaffoldProposal(ctx, db, proposal); err != nil {
		t.Fatal(err)
	}
	approved, err := decideMemoryScaffoldProposal(ctx, db, "p1", "approved", "looks good", "root")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != "approved" || approved.DecidedBy != "root" {
		t.Fatalf("approved=%+v", approved)
	}
	if _, err := decideMemoryScaffoldProposal(ctx, db, "p1", "rejected", "", "root"); err == nil {
		t.Fatal("terminal proposal should reject second decision")
	}
	if _, err := decideMemoryScaffoldProposal(ctx, db, "missing", "approved", "", "root"); err != sql.ErrNoRows {
		t.Fatalf("missing err=%v", err)
	}
}

func TestMemoryScaffoldProposalAPI(t *testing.T) {
	db := newScaffoldDB(t)
	if _, err := db.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, is_superuser INTEGER DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id, is_superuser) VALUES ('root', 1), ('user', 0)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	proposal := memoryScaffoldProposal{
		ID:               "p1",
		Target:           "project_agents",
		Collection:       "levara",
		Summary:          "Add consult reminder",
		CurrentProblem:   "blind saves",
		ProposedChange:   "Update AGENTS.md wording",
		Risk:             "Low risk",
		Status:           "open",
		SourceFindingIDs: []string{"f1"},
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	proposal.Digest = scaffoldProposalDigest(proposal)
	if _, err := upsertMemoryScaffoldProposal(context.Background(), db, proposal); err != nil {
		t.Fatal(err)
	}

	cfg := APIConfig{DB: db}
	app := fiber.New()
	app.Get("/api/v1/memory-scaffold/proposals", memoryScaffoldProposalListHandler(cfg))
	app.Get("/api/v1/memory-scaffold/proposals/:id", memoryScaffoldProposalDetailHandler(cfg))
	app.Post("/api/v1/memory-scaffold/proposals/:id/decision", func(c *fiber.Ctx) error {
		c.Locals("user_id", "root")
		return memoryScaffoldProposalDecisionHandler(cfg)(c)
	})

	resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/memory-scaffold/proposals?status=open&collection=levara", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("list status=%d", resp.StatusCode)
	}
	var list struct {
		Proposals []memoryScaffoldProposal `json:"proposals"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Proposals) != 1 || list.Proposals[0].ID != "p1" {
		t.Fatalf("list=%+v", list)
	}

	resp, err = app.Test(httptest.NewRequest("GET", "/api/v1/memory-scaffold/proposals/p1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("detail status=%d", resp.StatusCode)
	}

	req := httptest.NewRequest("POST", "/api/v1/memory-scaffold/proposals/p1/decision", bytes.NewBufferString(`{"status":"approved","note":"ship"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("decision status=%d", resp.StatusCode)
	}
	var decided memoryScaffoldProposal
	if err := json.NewDecoder(resp.Body).Decode(&decided); err != nil {
		t.Fatal(err)
	}
	if decided.Status != "approved" || decided.DecisionNote != "ship" {
		t.Fatalf("decided=%+v", decided)
	}

	req = httptest.NewRequest("POST", "/api/v1/memory-scaffold/proposals/p1/decision", bytes.NewBufferString(`{"status":"rejected"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Fatalf("second decision status=%d want 400", resp.StatusCode)
	}
}

func TestMemoryScaffoldDecisionRequiresAdmin(t *testing.T) {
	db := newScaffoldDB(t)
	if _, err := db.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, is_superuser INTEGER DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id, is_superuser) VALUES ('user', 0)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	proposal := memoryScaffoldProposal{
		ID:               "p1",
		Target:           "project_agents",
		Collection:       "levara",
		Summary:          "Admin gate",
		CurrentProblem:   "needs decision",
		ProposedChange:   "no auto apply",
		Risk:             "Low risk",
		Status:           "open",
		SourceFindingIDs: []string{"f1"},
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	proposal.Digest = scaffoldProposalDigest(proposal)
	if _, err := upsertMemoryScaffoldProposal(context.Background(), db, proposal); err != nil {
		t.Fatal(err)
	}
	app := fiber.New()
	app.Post("/api/v1/memory-scaffold/proposals/:id/decision", func(c *fiber.Ctx) error {
		c.Locals("user_id", "user")
		return memoryScaffoldProposalDecisionHandler(APIConfig{DB: db})(c)
	})
	req := httptest.NewRequest("POST", "/api/v1/memory-scaffold/proposals/p1/decision", bytes.NewBufferString(`{"status":"approved"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Fatalf("status=%d want 403", resp.StatusCode)
	}
}

func newScaffoldDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "scaffold.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureMemoryScaffoldSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
