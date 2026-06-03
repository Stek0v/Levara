// Package consolidate plans and applies memory-consolidation: collapsing
// clusters of near-duplicate / related memory records into one, reversibly.
package consolidate

import "time"

// MemoryRecord is the minimal projection of a memories row the engine needs.
type MemoryRecord struct {
	ID        string
	Key       string
	Value     string
	Room      string
	Hall      string
	CreatedAt time.Time
}

// SimEdge is an undirected similarity edge between two candidate records.
type SimEdge struct {
	A, B  string
	Score float64 // cosine similarity in [-1, 1]
}

// Cluster is a connected component of candidate record ids plus its internal edges.
type Cluster struct {
	IDs   []string
	Edges []SimEdge
}

// ActionKind discriminates the two consolidation strategies.
type ActionKind string

const (
	ActionMerge    ActionKind = "merge"    // deterministic: keep newest, supersede rest
	ActionAbstract ActionKind = "abstract" // LLM: synthesize a new semantic record, supersede all
)

// Action is one planned consolidation operation over a cluster.
type Action struct {
	Kind       ActionKind
	SurvivorID string   // merge: the kept (newest) record id; abstract: "" (a new record is created)
	NewValue   string   // abstract: synthesized text; merge: ""
	SourceIDs  []string // records to supersede
	Room       string
	Hall       string
}

// Config holds tunable thresholds.
type Config struct {
	TauLow          float64 // cluster edge threshold
	TauHigh         float64 // mechanical-merge gate
	TopK            int     // neighbors fetched per candidate when building the graph
	MaxAbstractSize int     // max records in an abstract cluster; larger ones are skipped (0 = unbounded)
}

// DefaultConfig returns the production defaults.
//
// TauLow is 0.90 (not 0.85): the looser threshold over-clustered topically
// adjacent but distinct notes into one giant abstract cluster (findings P2.5).
// MaxAbstractSize caps how many records a single LLM abstraction may cover;
// beyond it the cluster is skipped rather than truncated into a guard failure.
func DefaultConfig() Config {
	return Config{TauLow: 0.90, TauHigh: 0.97, TopK: 8, MaxAbstractSize: 6}
}
