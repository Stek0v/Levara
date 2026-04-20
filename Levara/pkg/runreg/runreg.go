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

// isTerminal reports whether a status has reached a final state. Terminal
// runs are safe to evict from the registry after the retention window.
func (s *Status) isTerminal() bool {
	return s.Status == "COMPLETED" || s.Status == "FAILED"
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

// Delete removes runID from the registry. No-op when absent. Used by the
// TTL janitor; callers generally don't need this directly (let terminal
// runs expire on their own schedule).
func (r *Registry) Delete(runID string) {
	r.runs.Delete(runID)
}

// PruneTerminalOlderThan removes every terminal run whose StartedAt is more
// than age ago. Returns the count of evicted entries. Runs still in the
// RUNNING state are always retained — no size cap on active runs, since a
// stuck RUNNING run is a bug we'd rather see than silently drop.
//
// A single pass is O(N). The registry is an in-memory sync.Map so callers
// shouldn't run prune in the request hot-path — the StartJanitor helper
// below is the usual integration point.
func (r *Registry) PruneTerminalOlderThan(age time.Duration) int {
	cutoff := time.Now().Add(-age)
	evicted := 0
	r.runs.Range(func(k, v any) bool {
		s, ok := v.(*Status)
		if !ok || s == nil {
			// Shouldn't happen, but evict garbage rather than leaking it.
			r.runs.Delete(k)
			evicted++
			return true
		}
		if !s.isTerminal() {
			return true
		}
		if s.StartedAt.Before(cutoff) {
			r.runs.Delete(k)
			evicted++
		}
		return true
	})
	return evicted
}

// StartJanitor launches a goroutine that periodically calls
// PruneTerminalOlderThan(age). Returns a stop function that cancels the
// janitor and waits for the in-flight tick to finish.
//
// Defaults suggested by the 20.04 review M3: age=1h, interval=10m. Prior
// to this the registry grew unbounded for the lifetime of the process, so
// long-lived nodes accumulated a *Status per cognify run indefinitely.
func (r *Registry) StartJanitor(interval, age time.Duration) (stop func()) {
	if interval <= 0 || age <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				r.PruneTerminalOlderThan(age)
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}
