// Package runreg is an in-memory registry of background pipeline runs
// (cognify, analyze-commits, ...). It was extracted from internal/http as
// preparation for F-4 wave 3j, which moves the cognify tools out of
// internal/http and into pkg/mcp. The registry is the shared mutable state
// those tools need, so it must live in a neutral package both packages can
// import without creating a cycle.
//
// Behavior matches the pre-refactor sync.Map: each run is a *Status pointer
// and Store/Load callers mutate the struct directly. Fire-and-forget update
// goroutines therefore do not require the registry to expose an update API.
package runreg

import (
	"sync"
	"time"
)

// Status is the public view of a single background run. JSON tags match the
// pre-refactor internal/http.pipelineRunStatus so REST and SSE clients see
// identical payloads.
type Status struct {
	RunID     string    `json:"pipeline_run_id"`
	Status    string    `json:"status"` // RUNNING, COMPLETED, FAILED
	Stage     string    `json:"stage"`
	Message   string    `json:"message"`
	Chunks    int       `json:"chunks_created"`
	Entities  int       `json:"entities_extracted"`
	Edges     int       `json:"edges_extracted"`
	ElapsedMs int64     `json:"elapsed_ms"`
	StartedAt time.Time `json:"started_at"`
}

// Registry stores *Status by run ID. Safe for concurrent use. The zero value
// is usable but prefer New() for clarity at the call site.
type Registry struct {
	runs sync.Map // runID → *Status
}

// New returns an empty Registry.
func New() *Registry { return &Registry{} }

// Store associates s with runID, overwriting any previous value.
func (r *Registry) Store(runID string, s *Status) {
	r.runs.Store(runID, s)
}

// Load returns the Status for runID, or nil and false if absent.
func (r *Registry) Load(runID string) (*Status, bool) {
	v, ok := r.runs.Load(runID)
	if !ok {
		return nil, false
	}
	return v.(*Status), true
}
