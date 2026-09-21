package governor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestSchedulerDistillWaitsForCognify(t *testing.T) {
	var cognifyBusy atomic.Bool
	var distillRan atomic.Int32
	var cognifyRan atomic.Int32

	sched := NewScheduler(
		&Job{
			Name:     "cognify",
			Priority: PriorityCognify,
			Busy:     func() bool { return cognifyBusy.Load() },
			Run:      func(ctx context.Context) { cognifyRan.Add(1) },
			Interval: 10 * time.Millisecond,
		},
		&Job{
			Name:     "distill",
			Priority: PriorityDistill,
			Busy:     func() bool { return true }, // always has candidates
			Run:      func(ctx context.Context) { distillRan.Add(1) },
			Interval: 10 * time.Millisecond,
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)

	// Cognify busy → distill must NOT run
	cognifyBusy.Store(true)
	time.Sleep(100 * time.Millisecond)
	if distillRan.Load() > 0 {
		t.Fatalf("distill ran %d times while cognify busy", distillRan.Load())
	}

	// Cognify idle → distill runs
	cognifyBusy.Store(false)
	time.Sleep(100 * time.Millisecond)
	if distillRan.Load() == 0 {
		t.Fatal("distill never ran after cognify went idle")
	}
	cancel()
}

func TestSchedulerQueryAlwaysWins(t *testing.T) {
	var lowRan atomic.Int32
	sched := NewScheduler(
		&Job{Name: "query", Priority: PriorityQuery, Busy: func() bool { return true },
			Run: func(ctx context.Context) {}, Interval: 10 * time.Millisecond},
		&Job{Name: "low", Priority: PriorityDistill, Busy: func() bool { return true },
			Run: func(ctx context.Context) { lowRan.Add(1) }, Interval: 10 * time.Millisecond},
	)
	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	// With query always busy, low priority should be deferred
	if lowRan.Load() > 2 { // allow small timing slack
		t.Fatalf("low priority ran %d times while query busy", lowRan.Load())
	}
	cancel()
}

func TestSchedulerStatus(t *testing.T) {
	sched := NewScheduler(
		&Job{Name: "a", Priority: PriorityCognify, Busy: func() bool { return true }, Interval: time.Minute},
		&Job{Name: "b", Priority: PriorityDistill, Busy: func() bool { return false }, Interval: time.Minute},
	)
	st := sched.Status()
	if len(st) != 2 || st[0].Name != "a" || !st[0].Busy || st[1].Busy {
		t.Fatalf("status = %+v", st)
	}
	if st[0].Priority != "cognify" || st[1].Priority != "distill" {
		t.Fatalf("priority names: %+v", st)
	}
}
