package mcp

// MCP tool: consolidate. Clusters near-duplicate / related memories in a
// collection and either merges them deterministically (newest survives, rest
// superseded) or abstracts a cluster into one synthesized semantic record via
// the LLM. Reversible: every write stamps consolidation_run_id so a later
// guarded journal can undo an unchanged run. dry_run defaults true.
//
// This file also holds the three Deps adapters that bridge the
// transport-independent consolidate engine to the application surface:
//   - sqlStore       → consolidate.Store      (SQL load + apply)
//   - collectionNeighbors → consolidate.NeighborSource (embed + vector search)
//   - llmSummarizer  → consolidate.Summarizer (LLM abstraction)

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/stek0v/levara/internal/metrics"
	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/consolidate"
	"github.com/stek0v/levara/pkg/llm"
)

// collectionNeighbors adapts the embed + vector-search surface to
// consolidate.NeighborSource. collection is the already-resolved vector
// collection (e.g. "_memories_levara").
type collectionNeighbors struct {
	deps       Deps
	collection string
	// dimMismatch is set when CollectionSearch reports the collection's
	// vectors are a different dimension than the server embedder. Every
	// candidate then fails the same way, yielding an empty graph; the flag
	// lets ToolConsolidate surface "incompatible" instead of a silent zero.
	dimMismatch bool
	store       *sqlStore
}

func (n *collectionNeighbors) Edges(ctx context.Context, recs []consolidate.MemoryRecord, cfg consolidate.Config) ([]consolidate.SimEdge, error) {
	if !n.deps.EmbedAvailable() {
		return nil, nil
	}
	if n.store != nil {
		release, err := n.store.providerFence(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
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
		if err := consolidationProviderReady(ctx, n.deps); err != nil {
			return nil, err
		}
		// Embed key+value to match indexMemorySync (tool_save_recall_memory.go),
		// which indexes Embed(key+" "+value). A value-only query vector is
		// asymmetric against the key+value stored vectors and systematically
		// under-scores records whose key carries text, starving the clusterer.
		vec, err := n.deps.Embed(ctx, r.Key+" "+r.Value)
		if err != nil {
			continue // skip this candidate; partial graph is fine
		}
		results, err := n.deps.CollectionSearch(n.collection, vec, cfg.TopK+1)
		if err != nil {
			// A dim mismatch is a collection-level condition: every candidate
			// will fail identically, so flag it once and stop building edges
			// rather than silently returning an empty graph (finding P1.2).
			if errors.Is(err, store.ErrDimMismatch) {
				n.dimMismatch = true
				return nil, nil
			}
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
	deps  Deps
	store *sqlStore
	calls atomic.Int64
}

func (l *llmSummarizer) Summarize(ctx context.Context, sources []string) (string, error) {
	prov := l.deps.LLMProvider()
	if prov == nil {
		return "", fmt.Errorf("consolidate: llm not configured")
	}
	if l.store != nil {
		release, err := l.store.providerFence(ctx)
		if err != nil {
			return "", err
		}
		defer release()
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
	if err := consolidationProviderReady(ctx, l.deps); err != nil {
		return "", err
	}
	l.calls.Add(1)
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
	if deps == nil || deps.DB() == nil {
		return errResult("database not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	collection, _, err := deleteMemoryStringArg(args, "collection")
	if err != nil {
		return errResult(err.Error())
	}
	if collection == "" {
		return errResult("'collection' required (use '_memories' to target the base memory store)")
	}
	// The base memory store keeps its rows at collection_name='' and indexes
	// them in the unprefixed '_memories' vector collection. memoryCollectionName("")
	// maps back to '_memories', so callers target it explicitly by that name; we
	// translate it to the empty SQL filter so the largest store is reachable via
	// on-demand consolidate, not just the janitor sweep (findings P2.2).
	sqlCollection := collection
	if collection == baseMemoryCollection {
		sqlCollection = ""
	}
	room, _, err := deleteMemoryStringArg(args, "room")
	if err != nil {
		return errResult(err.Error())
	}
	hall, _, err := deleteMemoryStringArg(args, "hall")
	if err != nil {
		return errResult(err.Error())
	}
	if hall != "" && !IsValidHall(hall) {
		return errResult("invalid hall")
	}
	dryRun := true
	if raw, exists := args["dry_run"]; exists {
		v, ok := raw.(bool)
		if !ok {
			return errResult("dry_run must be a boolean")
		}
		dryRun = v
	}
	shared := false
	if raw, exists := args["shared"]; exists {
		v, ok := raw.(bool)
		if !ok {
			return errResult("shared must be a boolean")
		}
		shared = v
	}
	runID := uuid.New().String()

	s := &sqlStore{deps: deps, collection: sqlCollection, shared: shared}
	if !dryRun {
		if _, err := s.outbox(); err != nil {
			return errResult(err.Error())
		}
	}
	nbr := &collectionNeighbors{deps: deps, collection: memoryCollectionName(sqlCollection), store: s}
	summarizer := &llmSummarizer{deps: deps, store: s}
	res, err := consolidate.Run(ctx, consolidate.Params{
		Store:      s,
		Neighbors:  nbr,
		Summarizer: summarizer,
		Cfg:        consolidate.DefaultConfig(),
		Collection: sqlCollection, Room: room, Hall: hall,
		RunID: runID, DryRun: dryRun,
	})
	if err != nil {
		metrics.ConsolidationRuns.WithLabelValues("error").Inc()
		return errResult(fmt.Sprintf("consolidate: %v llm_calls=%d", err, summarizer.calls.Load()))
	}
	metrics.ConsolidationRuns.WithLabelValues("ok").Inc()
	metrics.ConsolidationClusters.Add(float64(res.Clusters))
	metrics.ConsolidationActions.Add(float64(len(res.Actions)))
	for i, a := range res.Actions {
		metrics.ConsolidationCharDensity.WithLabelValues(string(a.Kind)).Observe(res.Densities[i])
	}

	mode := "applied"
	if dryRun {
		mode = "dry_run"
	}
	text := fmt.Sprintf(
		"consolidate %s: run=%s candidates=%d clusters=%d actions=%d skipped=%d llm_calls=%d",
		mode, runID, res.Candidates, res.Clusters, len(res.Actions), res.Skipped, summarizer.calls.Load())
	if nbr.dimMismatch {
		metrics.ConsolidationRuns.WithLabelValues("dim_incompatible").Inc()
		text += fmt.Sprintf(
			"\n  warning: collection %q is dim-incompatible with the server embedder "+
				"(vectors indexed at a different dimension) — no edges built; re-embed it to consolidate",
			collection)
	}
	for _, sk := range res.Skips {
		metrics.ConsolidationSkipped.WithLabelValues(consolidationSkipCategory(sk.Reason)).Inc()
		text += fmt.Sprintf("\n  skip [%s]: %s", strings.Join(sk.SourceIDs, ","), sk.Reason)
	}
	return okResult(text)
}

// consolidationSkipCategory buckets a free-form skip reason into a bounded label
// for the levara_consolidation_skipped_total metric. The reason strings carry
// counts and IDs (high cardinality), so we classify by the stable phrase the
// pipeline emits rather than exporting the raw text.
func consolidationSkipCategory(reason string) string {
	switch {
	case strings.Contains(reason, "too large"):
		return "oversized"
	case strings.Contains(reason, "budget"):
		return "llm_budget"
	case strings.Contains(reason, "summary dropped"),
		strings.Contains(reason, "summary invented"),
		strings.Contains(reason, "empty summary"),
		strings.Contains(reason, "no sources"):
		return "coverage_guard"
	default:
		return "other"
	}
}

// consolidationRunner is the consolidate.Runner used by the background
// janitor. RunOnce sweeps every non-internal collection, consolidating each
// against its own _memories_<c> vector sidecar. Failures on one collection are
// aggregated but never abort the sweep.
type consolidationRunner struct {
	deps Deps
	// maxLLMCallsPerSweep caps total Summarizer (LLM/DeepSeek) calls across all
	// collections in one RunOnce. DefaultConfig.MaxLLMCalls is PER collection,
	// so an N-collection sweep can otherwise fan out up to N×24 calls. 0 =
	// unbounded (legacy per-collection-only behaviour). See RunOnce.
	maxLLMCallsPerSweep int
}

// NewConsolidationRunner builds a consolidate.Runner over the given Deps for
// the background janitor (see consolidate.StartJanitor). maxLLMCallsPerSweep
// bounds total LLM calls across the whole sweep (0 = unbounded).
func NewConsolidationRunner(deps Deps, maxLLMCallsPerSweep int) *consolidationRunner {
	return &consolidationRunner{deps: deps, maxLLMCallsPerSweep: maxLLMCallsPerSweep}
}

// RunOnce enumerates authoritative SQL namespaces, skips internal sidecars,
// and consolidates each owner independently under trusted-local authority.
func (r *consolidationRunner) RunOnce(ctx context.Context) error {
	if r.deps == nil || r.deps.DB() == nil {
		return errors.New("database not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if !r.deps.MetadataActor(ctx).TrustedLocal {
		return errors.New("consolidation maintenance requires verified trusted-local authority")
	}
	rows, err := r.deps.DB().QueryContext(ctx, `SELECT DISTINCT collection_name,owner_id FROM memories WHERE superseded_by='' AND valid_until IS NULL AND tier='raw' ORDER BY collection_name,owner_id`)
	if err != nil {
		return err
	}
	type namespace struct{ collection, owner string }
	var namespaces []namespace
	for rows.Next() {
		var ns namespace
		if err := rows.Scan(&ns.collection, &ns.owner); err != nil {
			_ = rows.Close()
			return err
		}
		namespaces = append(namespaces, ns)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	var errs []error
	spent := 0 // LLM calls consumed so far this sweep
	for _, ns := range namespaces {
		c := ns.collection
		// Skip internal vector sidecars: the janitor iterates logical
		// collections and resolves each one's _memories_<c> sidecar via
		// memoryCollectionName below.
		if strings.HasPrefix(c, "_memories") {
			continue
		}
		cfg := consolidate.DefaultConfig()
		// Sweep-wide LLM budget (CONSOLIDATION_MAX_LLM_CALLS_PER_SWEEP): clamp
		// each collection's per-run budget to what's left in the sweep. Once the
		// sweep budget is exhausted, skip the remaining collections this tick —
		// the next janitor tick resumes them, and consolidation is incremental
		// (applied abstractions supersede their sources, so re-runs reach the
		// previously-skipped clusters). Trade-off: a skipped collection's free
		// merges also wait for the next tick; acceptable for a cost-control cap.
		if r.maxLLMCallsPerSweep > 0 {
			remaining := r.maxLLMCallsPerSweep - spent
			if remaining <= 0 {
				metrics.ConsolidationRuns.WithLabelValues("skipped_llm_budget").Inc()
				continue
			}
			if remaining < cfg.MaxLLMCalls {
				cfg.MaxLLMCalls = remaining
			}
		}
		runID := uuid.New().String()
		s := &sqlStore{deps: r.deps, collection: c, maintenanceOwner: &ns.owner}
		if _, err := s.outbox(); err != nil {
			errs = append(errs, err)
			continue
		}
		res, err := consolidate.Run(ctx, consolidate.Params{
			Store:      s,
			Neighbors:  &collectionNeighbors{deps: r.deps, collection: memoryCollectionName(c), store: s},
			Summarizer: &llmSummarizer{deps: r.deps, store: s},
			Cfg:        cfg,
			Collection: c,
			RunID:      runID,
			DryRun:     false,
		})
		if err != nil {
			metrics.ConsolidationRuns.WithLabelValues("error").Inc()
			errs = append(errs, fmt.Errorf("%s: %w", c, err))
			continue
		}
		spent += res.LLMCalls
		metrics.ConsolidationRuns.WithLabelValues("ok").Inc()
	}
	return errors.Join(errs...)
}

// ToolConsolidationRevert reverses a consolidation run: reactivates
// superseded source memories and deletes generated semantic records.
func ToolConsolidationRevert(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	if deps == nil || deps.DB() == nil {
		return errResult("database not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	runID, _, err := deleteMemoryStringArg(args, "run_id")
	if err != nil {
		return errResult(err.Error())
	}
	shared := false
	if raw, exists := args["shared"]; exists {
		var ok bool
		shared, ok = raw.(bool)
		if !ok {
			return errResult("shared must be a boolean")
		}
	}
	if runID == "" {
		return errResult("'run_id' required")
	}
	if err := (&sqlStore{deps: deps, shared: shared}).Revert(ctx, runID); err != nil {
		return errResult("revert: " + err.Error())
	}
	metrics.ConsolidationRuns.WithLabelValues("revert").Inc()
	return okResult("consolidation reverted: run=" + runID)
}

// nowTS / parseTS use RFC3339 to match the format ToolSaveMemory writes for
// created_at/updated_at.
func nowTS() string { return time.Now().UTC().Format(time.RFC3339) }

// errResult / okResult are local helpers mirroring the ToolResult shape used
// across the memory tools (text content; IsError flag for failures).
func errResult(msg string) ToolResult {
	return ToolResult{Content: []Content{{Type: "text", Text: "Error: " + msg}}, IsError: true}
}

func okResult(msg string) ToolResult {
	return statusResult(true, msg)
}
