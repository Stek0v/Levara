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
	Skipped    int    // == len(Skips); kept for backward-compatible summaries
	Skips      []Skip // per-cluster skip reasons
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
			sources := make([]string, 0, len(a.SourceIDs))
			for _, id := range a.SourceIDs {
				sources = append(sources, byID[id].Value)
			}
			val, err := AbstractValue(ctx, p.Summarizer, sources)
			if err != nil {
				res.Skips = append(res.Skips, Skip{SourceIDs: a.SourceIDs, Reason: err.Error()})
				continue
			}
			a.NewValue = val
		}
		final = append(final, a)
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
