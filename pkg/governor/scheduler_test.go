package governor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func waitForCond(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

// TestSchedulerDefersWhileHigherPriorityRuns — a lower-priority job waits
// ONLY while a higher-priority job is actually executing its Run.
func TestSchedulerDefersWhileHigherPriorityRuns(t *testing.T) {
	release := make(chan struct{})
	var cognifyStarted atomic.Bool
	var distillRan atomic.Int32

	sched := NewScheduler(
		&Job{
			Name:     "cognify",
			Priority: PriorityCognify,
			Busy:     func() bool { return true },
			Run: func(ctx context.Context) {
				cognifyStarted.Store(true)
				<-release // hold the Run open
			},
			Interval: 5 * time.Millisecond,
		},
		&Job{
			Name:     "distill",
			Priority: PriorityDistill,
			Busy:     func() bool { return true },
			Run:      func(ctx context.Context) { distillRan.Add(1) },
			Interval: 5 * time.Millisecond,
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx)

	waitForCond(t, 2*time.Second, cognifyStarted.Load)
	time.Sleep(100 * time.Millisecond)
	if distillRan.Load() != 0 {
		t.Fatalf("distill ran %d times while cognify Run was in flight", distillRan.Load())
	}

	close(release)
	waitForCond(t, 2*time.Second, func() bool { return distillRan.Load() > 0 })
}

// TestSchedulerBusyFlagDoesNotStarve — regression for the 2026-09-22 prod
// stall: source-ingest reports Busy()=true unconditionally ("always poll
// sources"); gating lower jobs on the Busy FLAG starved rag-janitor and
// distill-janitor permanently (rag frozen at 50/504 for a day). A busy
// flag must gate only the job's OWN execution.
func TestSchedulerBusyFlagDoesNotStarve(t *testing.T) {
	var lowRan atomic.Int32
	sched := NewScheduler(
		&Job{Name: "source-ingest", Priority: PrioritySourceIngest, Busy: func() bool { return true },
			Run: func(ctx context.Context) {}, Interval: 10 * time.Millisecond},
		&Job{Name: "rag-janitor", Priority: PriorityRagJanitor, Busy: func() bool { return true },
			Run: func(ctx context.Context) { lowRan.Add(1) }, Interval: 10 * time.Millisecond},
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx)
	time.Sleep(200 * time.Millisecond)
	if lowRan.Load() == 0 {
		t.Fatal("low-priority job starved by a busy-flagged higher-priority job")
	}
}

func TestSchedulerStatus(t *testing.T) {
	release := make(chan struct{})
	sched := NewScheduler(
		&Job{Name: "a", Priority: PriorityCognify, Busy: func() bool { return true },
			Run: func(ctx context.Context) { <-release }, Interval: 10 * time.Millisecond},
		&Job{Name: "b", Priority: PriorityDistill, Busy: func() bool { return false }, Interval: time.Minute},
	)
	go sched.runJob(context.Background(), sched.jobs[0])
	waitForCond(t, 2*time.Second, func() bool { return sched.Status()[0].Running })

	st := sched.Status()
	if len(st) != 2 || st[0].Name != "a" || !st[0].Busy || st[1].Busy {
		t.Fatalf("status = %+v", st)
	}
	if !st[0].Running || st[1].Running {
		t.Fatalf("running flags: %+v", st)
	}
	if st[0].Priority != "cognify" || st[1].Priority != "distill" {
		t.Fatalf("priority names: %+v", st)
	}
	close(release)
}
