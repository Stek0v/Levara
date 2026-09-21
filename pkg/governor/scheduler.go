// scheduler.go — A2: job scheduler with priorities.
//
// Generalizes P4's embedder PriorityGate from "one resource, two lanes"
// to "N background jobs, M priority classes". The scheduler decides
// WHICH daemon works WHEN: distillation pauses while cognify has backlog,
// rag-janitor yields to source-ingest, and user queries always win.
//
// The scheduler does NOT manage individual resource concurrency (that's
// the embedder gate's job); it manages JOB-LEVEL ordering — preventing
// five independent goroutines from hammering the same upstream while
// pretending they don't know about each other.
package governor

import (
	"context"
	"sync"
	"time"
)

// Priority orders job classes; lower runs first.
type Priority int

const (
	PriorityQuery        Priority = iota // user search/recall — always wins
	PrioritySourceIngest                 // sources daemon scan (light, frequent)
	PriorityCognify                      // corpus vectorization
	PriorityRagJanitor                   // derivative migration
	PriorityDistill                      // LLM distillation (lowest)
)

func (p Priority) String() string {
	switch p {
	case PriorityQuery:
		return "query"
	case PrioritySourceIngest:
		return "source-ingest"
	case PriorityCognify:
		return "cognify"
	case PriorityRagJanitor:
		return "rag-janitor"
	case PriorityDistill:
		return "distill"
	default:
		return "unknown"
	}
}

// Job is a named background task with a priority and a health signal.
type Job struct {
	Name     string
	Priority Priority
	// Busy reports whether the job currently has work to do. A job that
	// is not busy never blocks lower-priority jobs.
	Busy func() bool
	// Run executes one unit of work; returns when the unit is done.
	Run func(ctx context.Context)
	// Interval between run attempts (even when idle, for polling).
	Interval time.Duration
}

// Scheduler coordinates background jobs so they don't compete blindly.
// It does NOT preempt a running job — it defers STARTING the next
// lower-priority job while a higher-priority job is busy.
type Scheduler struct {
	mu      sync.Mutex
	jobs    []*Job
	stop    chan struct{}
	stopped sync.Once
}

// NewScheduler creates a coordinator for the given jobs.
func NewScheduler(jobs ...*Job) *Scheduler {
	return &Scheduler{jobs: jobs, stop: make(chan struct{})}
}

// Start launches all jobs under coordination. Each job runs in its own
// goroutine but checks CanRun before each work unit.
func (s *Scheduler) Start(ctx context.Context) {
	for _, job := range s.jobs {
		go s.runJob(ctx, job)
	}
}

// Stop signals all jobs to terminate.
func (s *Scheduler) Stop() {
	s.stopped.Do(func() { close(s.stop) })
}

// runJob is the per-job loop: sleep interval → check if allowed → run.
func (s *Scheduler) runJob(ctx context.Context, job *Job) {
	ticker := time.NewTicker(job.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-ticker.C:
			if !job.Busy() {
				continue // nothing to do, don't block others
			}
			if !s.CanRun(job) {
				continue // higher priority is busy — wait next tick
			}
			job.Run(ctx)
		}
	}
}

// CanRun reports whether the job may start a work unit now: true when
// no higher-priority job is currently busy. Job-level coordination —
// resource-level gates (embedder, LLM) are separate.
func (s *Scheduler) CanRun(job *Job) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, other := range s.jobs {
		if other.Priority < job.Priority && other.Busy != nil && other.Busy() {
			return false
		}
	}
	return true
}

// Status returns a snapshot of job states for the /status endpoint.
type JobStatus struct {
	Name     string `json:"name"`
	Priority string `json:"priority"`
	Busy     bool   `json:"busy"`
}

func (s *Scheduler) Status() []JobStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]JobStatus, 0, len(s.jobs))
	for _, j := range s.jobs {
		busy := false
		if j.Busy != nil {
			busy = j.Busy()
		}
		out = append(out, JobStatus{Name: j.Name, Priority: j.Priority.String(), Busy: busy})
	}
	return out
}
