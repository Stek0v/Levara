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
	TauLow  float64 // cluster edge threshold
	TauHigh float64 // mechanical-merge gate
	TopK    int     // neighbors fetched per candidate when building the graph
}

// DefaultConfig returns the production defaults.
func DefaultConfig() Config {
	return Config{TauLow: 0.85, TauHigh: 0.97, TopK: 8}
}
