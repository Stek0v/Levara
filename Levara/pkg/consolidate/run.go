package consolidate

import "context"

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

// Result reports what a run planned (and, when not dry, applied).
type Result struct {
	Candidates int
	Clusters   int
	Actions    []Action
	Skipped    int
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
			sources := make([]string, 0, len(a.SourceIDs))
			for _, id := range a.SourceIDs {
				sources = append(sources, byID[id].Value)
			}
			val, err := AbstractValue(ctx, p.Summarizer, sources)
			if err != nil {
				res.Skipped++
				continue
			}
			a.NewValue = val
		}
		final = append(final, a)
	}
	res.Actions = final

	if !p.DryRun && len(final) > 0 {
		if err := p.Store.Apply(ctx, p.RunID, final); err != nil {
			return res, err
		}
	}
	return res, nil
}
