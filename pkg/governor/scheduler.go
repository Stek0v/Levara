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
	running map[*Job]bool // jobs currently executing their Run
	stop    chan struct{}
	stopped sync.Once
}

// NewScheduler creates a coordinator for the given jobs.
func NewScheduler(jobs ...*Job) *Scheduler {
	return &Scheduler{
		jobs:    jobs,
		running: make(map[*Job]bool, len(jobs)),
		stop:    make(chan struct{}),
	}
}

// Start launches all jobs under coordination, staggered so their ticks do
// not coincide. Every job sharing one interval and one start moment meant
// a higher-priority Run (source-ingest's ~10s cursor scan, or its
// post-boot full import) eclipsed the lower jobs' CanRun check on EVERY
// tick — rag-janitor never got a tick of its own (2026-09-22: rag frozen
// at 50/504 while scans ran fine). Offsetting each job by its share of the
// interval gives lower-priority ticks their own phase.
func (s *Scheduler) Start(ctx context.Context) {
	n := len(s.jobs)
	for i, job := range s.jobs {
		go func(i int, job *Job) {
			if i > 0 && n > 1 && job.Interval > 0 {
				time.Sleep(time.Duration(i) * job.Interval / time.Duration(n))
			}
			s.runJob(ctx, job)
		}(i, job)
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
				continue // a higher-priority job is mid-Run — wait next tick
			}
			s.setRunning(job, true)
			job.Run(ctx)
			s.setRunning(job, false)
		}
	}
}

func (s *Scheduler) setRunning(job *Job, on bool) {
	s.mu.Lock()
	s.running[job] = on
	s.mu.Unlock()
}

// CanRun reports whether the job may start a work unit now: true when no
// higher-priority job is currently EXECUTING its Run. Busy() signals that
// work EXISTS — it must not gate other jobs: source-ingest reports busy
// unconditionally ("always poll sources"), and gating on it starved
// rag-janitor and distill-janitor permanently (observed 2026-09-22: rag
// frozen at 50/504 for a day while a long import ran its course).
func (s *Scheduler) CanRun(job *Job) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, other := range s.jobs {
		if other.Priority < job.Priority && s.running[other] {
			return false
		}
	}
	return true
}

// Status returns a snapshot of job states for the /status endpoint.
type JobStatus struct {
	Name     string `json:"name"`
	Priority string `json:"priority"`
	Busy     bool   `json:"busy"`    // has work queued (its own gate to run)
	Running  bool   `json:"running"` // currently executing its Run
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
		out = append(out, JobStatus{Name: j.Name, Priority: j.Priority.String(), Busy: busy, Running: s.running[j]})
	}
	return out
}
