package http

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

const memoryScaffoldDDL = `
CREATE TABLE IF NOT EXISTS memory_scaffold_proposals (
	id TEXT PRIMARY KEY,
	digest TEXT NOT NULL,
	target TEXT NOT NULL,
	collection_name TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL,
	current_problem TEXT NOT NULL DEFAULT '',
	proposed_change TEXT NOT NULL DEFAULT '',
	risk TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'open',
	source_run_id TEXT NOT NULL DEFAULT '',
	source_finding_ids_json TEXT NOT NULL DEFAULT '[]',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	decided_at TEXT NOT NULL DEFAULT '',
	decided_by TEXT NOT NULL DEFAULT '',
	decision_note TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_memory_scaffold_proposals_digest ON memory_scaffold_proposals(digest);`

type memoryScaffoldProposal struct {
	ID               string   `json:"id"`
	Digest           string   `json:"digest,omitempty"`
	Target           string   `json:"target"`
	Collection       string   `json:"collection,omitempty"`
	Summary          string   `json:"summary"`
	CurrentProblem   string   `json:"current_problem"`
	ProposedChange   string   `json:"proposed_change"`
	Risk             string   `json:"risk"`
	Status           string   `json:"status"`
	SourceRunID      string   `json:"source_run_id,omitempty"`
	SourceFindingIDs []string `json:"source_finding_ids,omitempty"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
	DecidedAt        string   `json:"decided_at,omitempty"`
	DecidedBy        string   `json:"decided_by,omitempty"`
	DecisionNote     string   `json:"decision_note,omitempty"`
}

type scaffoldDecisionRequest struct {
	Status string `json:"status"`
	Note   string `json:"note"`
}

func memoryScaffoldProposalListHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "database unavailable"})
		}
		if err := ensureMemoryScaffoldSchema(c.UserContext(), cfg.DB); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "memory scaffold schema unavailable"})
		}
		proposals, err := listMemoryScaffoldProposals(c.UserContext(), cfg.DB, scaffoldProposalFilter{
			Status:     c.Query("status"),
			Collection: c.Query("collection"),
			Target:     c.Query("target"),
			Limit:      c.QueryInt("limit", 50),
			Offset:     c.QueryInt("offset", 0),
		})
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "memory scaffold proposal list failed"})
		}
		return c.JSON(fiber.Map{"proposals": proposals})
	}
}

func memoryScaffoldProposalDetailHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "database unavailable"})
		}
		if err := ensureMemoryScaffoldSchema(c.UserContext(), cfg.DB); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "memory scaffold schema unavailable"})
		}
		proposal, err := getMemoryScaffoldProposal(c.UserContext(), cfg.DB, c.Params("id"))
		if errors.Is(err, sql.ErrNoRows) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "memory scaffold proposal not found"})
		}
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "memory scaffold proposal detail failed"})
		}
		return c.JSON(proposal)
	}
}

func memoryScaffoldProposalDecisionHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "database unavailable"})
		}
		if err := requireSuperuser(c, cfg); err != nil {
			return err
		}
		if err := ensureMemoryScaffoldSchema(c.UserContext(), cfg.DB); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "memory scaffold schema unavailable"})
		}
		var req scaffoldDecisionRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "invalid request body"})
		}
		proposal, err := decideMemoryScaffoldProposal(c.UserContext(), cfg.DB, c.Params("id"), req.Status, req.Note, c.Locals("user_id"))
		if errors.Is(err, sql.ErrNoRows) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "memory scaffold proposal not found"})
		}
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(proposal)
	}
}

func createMemoryScaffoldProposalsFromReview(ctx context.Context, db *sql.DB, run memoryReviewRun) ([]memoryScaffoldProposal, error) {
	if db == nil || run.Status != "completed" || len(run.Findings) == 0 {
		return nil, nil
	}
	if err := ensureMemoryScaffoldSchema(ctx, db); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var out []memoryScaffoldProposal
	for _, f := range run.Findings {
		proposal, ok := proposalFromFinding(run, f, now)
		if !ok {
			continue
		}
		inserted, err := upsertMemoryScaffoldProposal(ctx, db, proposal)
		if err != nil {
			return nil, err
		}
		out = append(out, inserted)
	}
	return out, nil
}

func proposalFromFinding(run memoryReviewRun, finding memoryReviewFinding, now string) (memoryScaffoldProposal, bool) {
	target := targetForFindingCategory(finding.Category)
	if target == "" {
		return memoryScaffoldProposal{}, false
	}
	summary := fallbackString(finding.Summary, "Improve memory scaffold for "+finding.Category)
	change := fallbackString(finding.Recommendation, defaultScaffoldChange(finding.Category))
	proposal := memoryScaffoldProposal{
		ID:               uuid.New().String(),
		Target:           target,
		Collection:       run.Scope.Collection,
		Summary:          truncateReviewText(summary, 500),
		CurrentProblem:   truncateReviewText(finding.Evidence, 1000),
		ProposedChange:   truncateReviewText(change, 1200),
		Risk:             riskForSeverity(finding.Severity),
		Status:           "open",
		SourceRunID:      run.ID,
		SourceFindingIDs: []string{finding.ID},
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	proposal.Digest = scaffoldProposalDigest(proposal)
	return proposal, true
}

func targetForFindingCategory(category string) string {
	switch category {
	case "missed_recall", "blind_save":
		return "project_agents"
	case "duplicate_save", "wrong_room_hall", "noisy_memory":
		return "memory_policy"
	case "scaffold_recommendation":
		return "project_agents"
	default:
		return ""
	}
}

func defaultScaffoldChange(category string) string {
	switch category {
	case "missed_recall", "blind_save":
		return "Clarify consult-before-write rules in the project memory scaffold."
	case "duplicate_save":
		return "Clarify duplicate-check and update-vs-new-memory policy."
	case "wrong_room_hall":
		return "Clarify project room taxonomy and allowed hall vocabulary."
	case "noisy_memory":
		return "Clarify do-not-save rules and wake_up pin discipline."
	default:
		return "Review and refine the project memory scaffold."
	}
}

func riskForSeverity(severity string) string {
	switch strings.ToLower(severity) {
	case "high":
		return "High behavior impact; requires careful human review before applying."
	case "low":
		return "Low risk; wording-only scaffold change."
	default:
		return "Medium risk; may change agent memory behavior."
	}
}

func scaffoldProposalDigest(proposal memoryScaffoldProposal) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		proposal.Target,
		proposal.Collection,
		proposal.Summary,
		proposal.CurrentProblem,
		proposal.ProposedChange,
	}, "\x00")))
	return hex.EncodeToString(h[:])
}

func ensureMemoryScaffoldSchema(ctx context.Context, db *sql.DB) error {
	for _, stmt := range strings.Split(memoryScaffoldDDL, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, Q(stmt)); err != nil {
			return err
		}
	}
	return nil
}

func upsertMemoryScaffoldProposal(ctx context.Context, db *sql.DB, proposal memoryScaffoldProposal) (memoryScaffoldProposal, error) {
	existing, err := getMemoryScaffoldProposalByDigest(ctx, db, proposal.Digest)
	if err == nil {
		merged := mergeFindingIDs(existing.SourceFindingIDs, proposal.SourceFindingIDs)
		idsJSON, _ := json.Marshal(merged)
		_, err = db.ExecContext(ctx, Q(`UPDATE memory_scaffold_proposals SET source_finding_ids_json=?, updated_at=? WHERE id=?`), string(idsJSON), proposal.UpdatedAt, existing.ID)
		if err != nil {
			return existing, err
		}
		existing.SourceFindingIDs = merged
		existing.UpdatedAt = proposal.UpdatedAt
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return proposal, err
	}
	idsJSON, _ := json.Marshal(proposal.SourceFindingIDs)
	_, err = db.ExecContext(ctx, Q(`INSERT INTO memory_scaffold_proposals (id,digest,target,collection_name,summary,current_problem,proposed_change,risk,status,source_run_id,source_finding_ids_json,created_at,updated_at,decided_at,decided_by,decision_note) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		proposal.ID, proposal.Digest, proposal.Target, proposal.Collection, proposal.Summary, proposal.CurrentProblem, proposal.ProposedChange, proposal.Risk, proposal.Status, proposal.SourceRunID, string(idsJSON), proposal.CreatedAt, proposal.UpdatedAt, proposal.DecidedAt, proposal.DecidedBy, proposal.DecisionNote)
	return proposal, err
}

func getMemoryScaffoldProposalByDigest(ctx context.Context, db *sql.DB, digest string) (memoryScaffoldProposal, error) {
	row := db.QueryRowContext(ctx, Q(`SELECT id,digest,target,collection_name,summary,current_problem,proposed_change,risk,status,source_run_id,source_finding_ids_json,created_at,updated_at,decided_at,decided_by,decision_note FROM memory_scaffold_proposals WHERE digest=?`), digest)
	return scanMemoryScaffoldProposal(row)
}

type scaffoldProposalFilter struct {
	Status, Collection, Target string
	Limit, Offset              int
}

func listMemoryScaffoldProposals(ctx context.Context, db *sql.DB, f scaffoldProposalFilter) ([]memoryScaffoldProposal, error) {
	limit := normalizePageLimit(f.Limit)
	offset := maxInt(f.Offset, 0)
	query := `SELECT id,digest,target,collection_name,summary,current_problem,proposed_change,risk,status,source_run_id,source_finding_ids_json,created_at,updated_at,decided_at,decided_by,decision_note FROM memory_scaffold_proposals WHERE 1=1`
	args := []any{}
	for _, x := range []struct{ column, value string }{{"status", f.Status}, {"collection_name", f.Collection}, {"target", f.Target}} {
		if strings.TrimSpace(x.value) != "" {
			query += " AND " + x.column + "=?"
			args = append(args, strings.TrimSpace(x.value))
		}
	}
	query += " ORDER BY updated_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	rows, err := db.QueryContext(ctx, Q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []memoryScaffoldProposal{}
	for rows.Next() {
		proposal, err := scanMemoryScaffoldProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, proposal)
	}
	return out, rows.Err()
}

func getMemoryScaffoldProposal(ctx context.Context, db *sql.DB, id string) (memoryScaffoldProposal, error) {
	row := db.QueryRowContext(ctx, Q(`SELECT id,digest,target,collection_name,summary,current_problem,proposed_change,risk,status,source_run_id,source_finding_ids_json,created_at,updated_at,decided_at,decided_by,decision_note FROM memory_scaffold_proposals WHERE id=?`), id)
	return scanMemoryScaffoldProposal(row)
}

type scaffoldProposalScanner interface {
	Scan(dest ...any) error
}

func scanMemoryScaffoldProposal(scanner scaffoldProposalScanner) (memoryScaffoldProposal, error) {
	var p memoryScaffoldProposal
	var idsJSON string
	if err := scanner.Scan(&p.ID, &p.Digest, &p.Target, &p.Collection, &p.Summary, &p.CurrentProblem, &p.ProposedChange, &p.Risk, &p.Status, &p.SourceRunID, &idsJSON, &p.CreatedAt, &p.UpdatedAt, &p.DecidedAt, &p.DecidedBy, &p.DecisionNote); err != nil {
		return p, err
	}
	_ = json.Unmarshal([]byte(idsJSON), &p.SourceFindingIDs)
	return p, nil
}

func decideMemoryScaffoldProposal(ctx context.Context, db *sql.DB, id, status, note string, actor any) (memoryScaffoldProposal, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if status != "approved" && status != "rejected" {
		return memoryScaffoldProposal{}, errors.New("status must be approved or rejected")
	}
	proposal, err := getMemoryScaffoldProposal(ctx, db, id)
	if err != nil {
		return proposal, err
	}
	if proposal.Status != "open" {
		return proposal, errors.New("only open proposals can be decided")
	}
	actorID, _ := actor.(string)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, Q(`UPDATE memory_scaffold_proposals SET status=?, updated_at=?, decided_at=?, decided_by=?, decision_note=? WHERE id=?`),
		status, now, now, actorID, truncateReviewText(note, 1000), id)
	if err != nil {
		return proposal, err
	}
	proposal.Status = status
	proposal.UpdatedAt = now
	proposal.DecidedAt = now
	proposal.DecidedBy = actorID
	proposal.DecisionNote = truncateReviewText(note, 1000)
	return proposal, nil
}

func mergeFindingIDs(a, b []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, id := range append(a, b...) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func fallbackString(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
