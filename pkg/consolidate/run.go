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
	// Gate is the optional semantic fact gate (e.g. the FRIDA-Decisions
	// sidecar). Nil = gate off (plain cosine/threshold behaviour).
	Gate FactGate
	// GateThreshold is the minimum Supersedes probability for an action to
	// stand. <= 0 selects DefaultGateThreshold.
	GateThreshold float64
}

// FactGate is an optional semantic duplicate-checker consulted before a
// planned action is accepted: a mechanical merge must semantically supersede
// each of its sources, and an LLM abstraction's synthesis must supersede each
// source it would retire. Implementations (pkg/decisions sidecar) fail open
// at the call site — a gate outage must never stall consolidation.
type FactGate interface {
	// Supersedes reports P(newRec is an updated version of oldRec): the same
	// fact with newer details, not a different fact.
	Supersedes(ctx context.Context, oldRec, newRec MemoryRecord) (float64, error)
}

// DefaultGateThreshold is the calibrated merge/supersession threshold from
// benchmark/frida_gate (supersession set: precision 1.0, best-F1 0.844 at
// 0.05; the model's noul mass sits low for this phrasing, so the naive 0.5
// would reject most true pairs).
const DefaultGateThreshold = 0.05

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
	LLMCalls   int       // Summarizer (LLM) calls attempted this run; lets a multi-collection sweep enforce a budget across runs
	// Fact-gate counters (all zero when Params.Gate is nil). GateErrors
	// counts fail-open consultations: the action stood because the gate was
	// unreachable or errored.
	GateChecked  int
	GateRejected int
	GateErrors   int
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
			// a large collection (findings P3.3). Semantics:
			//   - Scope is per Run() = per collection, not per janitor sweep.
			//   - Counts ATTEMPTS, not successes: llmCalls++ precedes the call,
			//     so a coverage-guard rejection still consumes budget (the
			//     DeepSeek request was already made). This keeps the cap a true
			//     ceiling on outbound calls; counting only successes would let a
			//     reject-heavy collection exceed it.
			//   - Under exhaustion the surviving subset is order-dependent
			//     (arbitrary), but self-heals across applied reruns: successful
			//     abstractions supersede their sources, which then drop out of
			//     Candidates, so the next run reaches the previously-skipped
			//     clusters. dry_run previews do not progress (nothing applied).
			//     Exhaustion is observable via the llm_budget skip metric.
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
		// Semantic fact gate: placed after NewValue is filled (the
		// abstraction check scores the synthesis against its sources) and
		// before the action is accepted. A merge must supersede every
		// source; an abstraction's synthesis must supersede every source it
		// would retire. Rejections skip the whole cluster — sources stay
		// intact and the cluster is re-examined on the next sweep. A gate
		// error fails open: the action stands, the error is counted (the
		// gate is additive protection, not an availability dependency).
		// An abstraction rejection still counts its LLM call (spent above).
		if p.Gate != nil {
			res.GateChecked++
			ok, reason, gerr := gateAction(ctx, p.Gate, p.GateThreshold, a, byID)
			switch {
			case gerr != nil:
				res.GateErrors++
			case !ok:
				res.GateRejected++
				res.Skips = append(res.Skips, Skip{SourceIDs: a.SourceIDs, Reason: reason})
				continue
			}
		}
		final = append(final, a)
		res.Densities = append(res.Densities, actionCharDensity(a, byID))
	}
	res.Actions = final
	res.Skipped = len(res.Skips)
	res.LLMCalls = llmCalls

	if !p.DryRun && len(final) > 0 {
		if err := p.Store.Apply(ctx, p.RunID, final); err != nil {
			return res, err
		}
	}
	return res, nil
}

// gateAction consults the fact gate for one planned action and reports
// whether it may proceed. The worst (lowest) pairwise probability decides:
// merge pairs each source against the survivor, abstraction pairs each
// source against the synthesized value.
func gateAction(ctx context.Context, g FactGate, threshold float64, a Action, byID map[string]MemoryRecord) (bool, string, error) {
	if threshold <= 0 {
		threshold = DefaultGateThreshold
	}
	newVal := byID[a.SurvivorID]
	if a.Kind == ActionAbstract {
		newVal = MemoryRecord{Value: a.NewValue}
	}
	worst := 1.0
	worstOld := ""
	for _, src := range a.SourceIDs {
		p, err := g.Supersedes(ctx, byID[src], newVal)
		if err != nil {
			return false, "", err
		}
		if p < worst {
			worst, worstOld = p, src
		}
	}
	if worst < threshold {
		return false, fmt.Sprintf("decision gate: %s does not supersede %s (p=%.2f < %.2f)",
			labelNew(a), worstOld, worst, threshold), nil
	}
	return true, "", nil
}

func labelNew(a Action) string {
	if a.Kind == ActionMerge {
		return "survivor"
	}
	return "synthesis"
}
