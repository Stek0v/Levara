package consolidate

import (
	"context"
	"fmt"
)

// Store loads candidate records and applies/reverts consolidation actions.
type Store interface {
	Candidates(ctx context.Context, collection, room, hall string) ([]MemoryRecord, error)
	Apply(ctx context.Context, runID string, actions []Action) error
}

// NeighborSource builds the similarity-edge set for the candidates.
type NeighborSource interface {
	Edges(ctx context.Context, recs []MemoryRecord, cfg Config) ([]SimEdge, error)
}

// Params bundles the inputs for one consolidation run.
type Params struct {
	Store      Store
	Neighbors  NeighborSource
	Summarizer Summarizer
	Cfg        Config
	Collection string
	Room       string
	Hall       string
	RunID      string
	DryRun     bool
}

// Skip records a cluster that was found but not acted on, with the reason
// (coverage-guard rejection or oversized cluster) for operator visibility.
type Skip struct {
	SourceIDs []string
	Reason    string
}

// Result reports what a run planned (and, when not dry, applied).
type Result struct {
	Candidates int
	Clusters   int
	Actions    []Action
	Densities  []float64 // char-retention ratio per Action, aligned with Actions
	Skipped    int       // == len(Skips); kept for backward-compatible summaries
	Skips      []Skip    // per-cluster skip reasons
}

// actionCharDensity is survivor chars / total source chars for one action — the
// compression ratio that flags over-aggressive consolidation. For a merge the
// survivor is the kept record and the total spans every cluster member; for an
// abstraction the survivor is the synthesized text and the total spans the
// superseded sources. Returns 0 (not NaN) when the sources carry no characters.
func actionCharDensity(a Action, byID map[string]MemoryRecord) float64 {
	var survivorChars int
	ids := a.SourceIDs
	switch a.Kind {
	case ActionMerge:
		survivorChars = len(byID[a.SurvivorID].Value)
		ids = append([]string{a.SurvivorID}, a.SourceIDs...)
	case ActionAbstract:
		survivorChars = len(a.NewValue)
	}
	total := 0
	for _, id := range ids {
		total += len(byID[id].Value)
	}
	if total == 0 {
		return 0
	}
	return float64(survivorChars) / float64(total)
}

// Run executes the full pipeline: load → edges → cluster → plan → (fill LLM) → apply.
func Run(ctx context.Context, p Params) (Result, error) {
	recs, err := p.Store.Candidates(ctx, p.Collection, p.Room, p.Hall)
	if err != nil {
		return Result{}, err
	}
	byID := make(map[string]MemoryRecord, len(recs))
	for _, r := range recs {
		byID[r.ID] = r
	}

	edges, err := p.Neighbors.Edges(ctx, recs, p.Cfg)
	if err != nil {
		return Result{}, err
	}
	clusters := ClusterComponents(edges, p.Cfg.TauLow)
	actions := Plan(byID, clusters, p.Cfg)

	res := Result{Candidates: len(recs), Clusters: len(clusters)}
	var final []Action
	llmCalls := 0
	for _, a := range actions {
		if a.Kind == ActionAbstract {
			// Oversized clusters always overflow the LLM token budget and
			// degrade into truncation-induced guard failures — skip them up
			// front, before spending an LLM call, with an explicit reason.
			if p.Cfg.MaxAbstractSize > 0 && len(a.SourceIDs) > p.Cfg.MaxAbstractSize {
				res.Skips = append(res.Skips, Skip{
					SourceIDs: a.SourceIDs,
					Reason: fmt.Sprintf("cluster too large for abstraction (%d > %d)",
						len(a.SourceIDs), p.Cfg.MaxAbstractSize),
				})
				continue
			}
			// Per-run LLM budget: once the cap is hit, skip the remaining
			// abstract clusters rather than fan out unbounded DeepSeek cost on
			// a large collection (findings P3.3).
			if p.Cfg.MaxLLMCalls > 0 && llmCalls >= p.Cfg.MaxLLMCalls {
				res.Skips = append(res.Skips, Skip{
					SourceIDs: a.SourceIDs,
					Reason:    fmt.Sprintf("LLM call budget exhausted (%d calls)", p.Cfg.MaxLLMCalls),
				})
				continue
			}
			sources := make([]string, 0, len(a.SourceIDs))
			for _, id := range a.SourceIDs {
				sources = append(sources, byID[id].Value)
			}
			llmCalls++
			val, err := AbstractValue(ctx, p.Summarizer, sources)
			if err != nil {
				res.Skips = append(res.Skips, Skip{SourceIDs: a.SourceIDs, Reason: err.Error()})
				continue
			}
			a.NewValue = val
		}
		final = append(final, a)
		res.Densities = append(res.Densities, actionCharDensity(a, byID))
	}
	res.Actions = final
	res.Skipped = len(res.Skips)

	if !p.DryRun && len(final) > 0 {
		if err := p.Store.Apply(ctx, p.RunID, final); err != nil {
			return res, err
		}
	}
	return res, nil
}
