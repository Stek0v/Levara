package mcp

// MCP tool: consolidate. Clusters near-duplicate / related memories in a
// collection and either merges them deterministically (newest survives, rest
// superseded) or abstracts a cluster into one synthesized semantic record via
// the LLM. Reversible: every write stamps consolidation_run_id so a later
// revert can undo a run. dry_run defaults true.
//
// This file also holds the three Deps adapters that bridge the
// transport-independent consolidate engine to the application surface:
//   - sqlStore       → consolidate.Store      (SQL load + apply)
//   - collectionNeighbors → consolidate.NeighborSource (embed + vector search)
//   - llmSummarizer  → consolidate.Summarizer (LLM abstraction)

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/stek0v/levara/internal/metrics"
	"github.com/stek0v/levara/pkg/consolidate"
	"github.com/stek0v/levara/pkg/llm"
)

// sqlStore adapts the SQL surface to consolidate.Store. collection is the
// logical memory collection (e.g. "levara"), used both to scope candidate
// loading and to stamp collection_name on synthesized abstract records.
type sqlStore struct {
	deps       Deps
	collection string
}

func (s *sqlStore) Candidates(ctx context.Context, collection, room, hall string) ([]consolidate.MemoryRecord, error) {
	conds := []string{
		"collection_name = $1",
		"superseded_by = ''",
		"is_pinned = 0",
		"tier = 'raw'",
	}
	qargs := []any{collection}
	pos := 2
	if room != "" {
		conds = append(conds, fmt.Sprintf("room = $%d", pos))
		qargs = append(qargs, room)
		pos++
	}
	if hall != "" {
		conds = append(conds, fmt.Sprintf("hall = $%d", pos))
		qargs = append(qargs, hall)
		pos++
	}
	q := s.deps.Q(fmt.Sprintf(
		`SELECT id, key, value, room, hall, created_at FROM memories WHERE %s`,
		strings.Join(conds, " AND ")))

	rows, err := s.deps.DB().QueryContext(ctx, q, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []consolidate.MemoryRecord
	for rows.Next() {
		var r consolidate.MemoryRecord
		var created string
		if err := rows.Scan(&r.ID, &r.Key, &r.Value, &r.Room, &r.Hall, &created); err != nil {
			return nil, err
		}
		r.CreatedAt = parseTS(created)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *sqlStore) Apply(ctx context.Context, runID string, actions []consolidate.Action) error {
	db := s.deps.DB()
	for _, a := range actions {
		switch a.Kind {
		case consolidate.ActionMerge:
			for _, src := range a.SourceIDs {
				if _, err := db.ExecContext(ctx, s.deps.Q(
					`UPDATE memories SET superseded_by = $1, valid_until = $2, consolidation_run_id = $3 WHERE id = $4`),
					a.SurvivorID, nowTS(), runID, src); err != nil {
					return err
				}
			}
		case consolidate.ActionAbstract:
			newID := uuid.New().String()
			from, _ := json.Marshal(a.SourceIDs)
			if _, err := db.ExecContext(ctx, s.deps.Q(
				`INSERT INTO memories
				   (id, key, value, type, owner_id, collection_name, room, hall, is_pinned, pin_priority,
				    superseded_by, consolidated_from, consolidation_run_id, tier, created_at, updated_at)
				 VALUES
				   ($1, $2, $3, 'project', '', $4, $5, $6, 0, 0,
				    '', $7, $8, 'semantic', $9, $10)`),
				newID, "consolidated:"+newID, a.NewValue, s.collection, a.Room, a.Hall,
				string(from), runID, nowTS(), nowTS()); err != nil {
				return err
			}
			for _, src := range a.SourceIDs {
				if _, err := db.ExecContext(ctx, s.deps.Q(
					`UPDATE memories SET superseded_by = $1, valid_until = $2, consolidation_run_id = $3 WHERE id = $4`),
					newID, nowTS(), runID, src); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// collectionNeighbors adapts the embed + vector-search surface to
// consolidate.NeighborSource. collection is the already-resolved vector
// collection (e.g. "_memories_levara").
type collectionNeighbors struct {
	deps       Deps
	collection string
}

func (n *collectionNeighbors) Edges(ctx context.Context, recs []consolidate.MemoryRecord, cfg consolidate.Config) ([]consolidate.SimEdge, error) {
	if !n.deps.EmbedAvailable() {
		return nil, nil
	}
	// Restrict neighbors to the candidate set so superseded/pinned/other-
	// collection rows that may exist in the vector index don't pollute edges.
	candidate := make(map[string]struct{}, len(recs))
	for _, r := range recs {
		candidate[r.ID] = struct{}{}
	}

	seen := make(map[string]struct{})
	var edges []consolidate.SimEdge
	for _, r := range recs {
		vec, err := n.deps.Embed(ctx, r.Value)
		if err != nil {
			continue // skip this candidate; partial graph is fine
		}
		results, err := n.deps.CollectionSearch(n.collection, vec, cfg.TopK+1)
		if err != nil {
			continue
		}
		for _, res := range results {
			if res.ID == r.ID {
				continue
			}
			if _, ok := candidate[res.ID]; !ok {
				continue
			}
			a, b := r.ID, res.ID
			if a > b {
				a, b = b, a
			}
			key := a + "\x00" + b
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			// CollectionSearch.Score is already cosine similarity
			// (hnsw returns 1-distance), so use it as-is.
			edges = append(edges, consolidate.SimEdge{A: a, B: b, Score: float64(res.Score)})
		}
	}
	return edges, nil
}

// llmSummarizer adapts the LLM provider surface to consolidate.Summarizer.
// When no provider is configured, Summarize returns an error and the engine
// skips abstract clusters gracefully — deterministic merges still proceed.
type llmSummarizer struct {
	deps Deps
}

func (l *llmSummarizer) Summarize(ctx context.Context, sources []string) (string, error) {
	prov := l.deps.LLMProvider()
	if prov == nil {
		return "", fmt.Errorf("consolidate: llm not configured")
	}
	var b strings.Builder
	b.WriteString("Combine the following memory notes into ONE concise statement. ")
	b.WriteString("Preserve every fact, number, name, and port exactly. ")
	b.WriteString("Do NOT add any information not present below. Notes:\n")
	for _, s := range sources {
		b.WriteString("- ")
		b.WriteString(s)
		b.WriteString("\n")
	}
	resp, err := prov.ChatCompletion(ctx, llm.CompletionRequest{
		Model:       l.deps.LLMModel(),
		Messages:    []llm.Message{{Role: "user", Content: b.String()}},
		Temperature: 0,
		MaxTokens:   summaryMaxTokens(sources),
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

// summaryMaxTokens scales the completion budget to the combined source length
// so a multi-note abstraction isn't truncated mid-sentence (which dropped facts
// and triggered the coverage guard — findings P2.5). ~3 chars/token is a
// conservative estimate for mixed Latin/Cyrillic; clamped to [512, 4096].
func summaryMaxTokens(sources []string) int {
	total := 0
	for _, s := range sources {
		total += len(s)
	}
	tok := total/3 + 256
	if tok < 512 {
		return 512
	}
	if tok > 4096 {
		return 4096
	}
	return tok
}

// ToolConsolidate clusters and consolidates near-duplicate/related memories
// in a collection. dry_run (default true) previews without writing.
func ToolConsolidate(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	collection, _ := args["collection"].(string)
	if collection == "" {
		return errResult("'collection' required")
	}
	room, _ := args["room"].(string)
	hall, _ := args["hall"].(string)
	dryRun := true
	if v, ok := args["dry_run"].(bool); ok {
		dryRun = v
	}
	runID := uuid.New().String()

	res, err := consolidate.Run(ctx, consolidate.Params{
		Store:      &sqlStore{deps: deps, collection: collection},
		Neighbors:  &collectionNeighbors{deps: deps, collection: memoryCollectionName(collection)},
		Summarizer: &llmSummarizer{deps: deps},
		Cfg:        consolidate.DefaultConfig(),
		Collection: collection, Room: room, Hall: hall,
		RunID: runID, DryRun: dryRun,
	})
	if err != nil {
		metrics.ConsolidationRuns.WithLabelValues("error").Inc()
		return errResult("consolidate: " + err.Error())
	}
	metrics.ConsolidationRuns.WithLabelValues("ok").Inc()
	metrics.ConsolidationClusters.Add(float64(res.Clusters))
	metrics.ConsolidationActions.Add(float64(len(res.Actions)))

	mode := "applied"
	if dryRun {
		mode = "dry_run"
	}
	text := fmt.Sprintf(
		"consolidate %s: run=%s candidates=%d clusters=%d actions=%d skipped=%d",
		mode, runID, res.Candidates, res.Clusters, len(res.Actions), res.Skipped)
	for _, sk := range res.Skips {
		text += fmt.Sprintf("\n  skip [%s]: %s", strings.Join(sk.SourceIDs, ","), sk.Reason)
	}
	return okResult(text)
}

// consolidationRunner is the consolidate.Runner used by the background
// janitor. RunOnce sweeps every non-internal collection, consolidating each
// against its own _memories_<c> vector sidecar. Failures on one collection are
// aggregated but never abort the sweep.
type consolidationRunner struct {
	deps Deps
}

// NewConsolidationRunner builds a consolidate.Runner over the given Deps for
// the background janitor (see consolidate.StartJanitor).
func NewConsolidationRunner(deps Deps) *consolidationRunner {
	return &consolidationRunner{deps: deps}
}

// RunOnce enumerates collections via Deps.ListCollections, skips internal
// _memories* sidecars, and runs a non-dry consolidation pass per collection.
func (r *consolidationRunner) RunOnce(ctx context.Context) error {
	var errs []error
	for _, c := range r.deps.ListCollections() {
		// Skip internal vector sidecars: the janitor iterates logical
		// collections and resolves each one's _memories_<c> sidecar via
		// memoryCollectionName below.
		if strings.HasPrefix(c, "_memories") {
			continue
		}
		runID := uuid.New().String()
		_, err := consolidate.Run(ctx, consolidate.Params{
			Store:      &sqlStore{deps: r.deps, collection: c},
			Neighbors:  &collectionNeighbors{deps: r.deps, collection: memoryCollectionName(c)},
			Summarizer: &llmSummarizer{deps: r.deps},
			Cfg:        consolidate.DefaultConfig(),
			Collection: c,
			RunID:      runID,
			DryRun:     false,
		})
		if err != nil {
			metrics.ConsolidationRuns.WithLabelValues("error").Inc()
			errs = append(errs, fmt.Errorf("%s: %w", c, err))
			continue
		}
		metrics.ConsolidationRuns.WithLabelValues("ok").Inc()
	}
	return errors.Join(errs...)
}

// Revert undoes a consolidation run identified by runID:
//  1. Reactivates source rows that were superseded during the run.
//  2. Deletes synthetic semantic records created during the run.
//
// sqlStore.collection is not used here; Revert operates purely by
// consolidation_run_id so a zero-value collection field is fine.
func (s *sqlStore) Revert(ctx context.Context, runID string) error {
	// Reactivate superseded source rows from this run.
	if _, err := s.deps.DB().ExecContext(ctx, s.deps.Q(
		`UPDATE memories SET superseded_by='', valid_until=NULL, consolidation_run_id=''
		 WHERE consolidation_run_id=$1 AND superseded_by<>''`), runID); err != nil {
		return err
	}
	// Delete generated semantic records from this run.
	if _, err := s.deps.DB().ExecContext(ctx, s.deps.Q(
		`DELETE FROM memories WHERE consolidation_run_id=$1 AND tier='semantic'`), runID); err != nil {
		return err
	}
	return nil
}

// ToolConsolidationRevert reverses a consolidation run: reactivates
// superseded source memories and deletes generated semantic records.
func ToolConsolidationRevert(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	runID, _ := args["run_id"].(string)
	if runID == "" {
		return errResult("'run_id' required")
	}
	if err := (&sqlStore{deps: deps}).Revert(ctx, runID); err != nil {
		return errResult("revert: " + err.Error())
	}
	metrics.ConsolidationRuns.WithLabelValues("revert").Inc()
	return okResult("consolidation reverted: run=" + runID)
}

// nowTS / parseTS use RFC3339 to match the format ToolSaveMemory writes for
// created_at/updated_at.
func nowTS() string { return time.Now().UTC().Format(time.RFC3339) }

func parseTS(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

// errResult / okResult are local helpers mirroring the ToolResult shape used
// across the memory tools (text content; IsError flag for failures).
func errResult(msg string) ToolResult {
	return ToolResult{Content: []Content{{Type: "text", Text: "Error: " + msg}}, IsError: true}
}

func okResult(msg string) ToolResult {
	return ToolResult{Content: []Content{{Type: "text", Text: msg}}}
}
