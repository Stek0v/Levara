package embed

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestPriorityGateForegroundJumpsQueue(t *testing.T) {
	g := NewPriorityGate(1)
	ctx := context.Background()

	// Occupy the only slot with a background request.
	if err := g.acquire(ctx, true); err != nil {
		t.Fatal(err)
	}
	bgAcquired := make(chan struct{})
	go func() {
		if err := g.acquire(ctx, true); err != nil {
			t.Error(err)
		}
		close(bgAcquired)
	}()

	// A background waiter parks; foreground must take the freed slot first.
	time.Sleep(20 * time.Millisecond)
	fgAcquired := make(chan struct{})
	go func() {
		if err := g.acquire(ctx, false); err != nil {
			t.Error(err)
		}
		close(fgAcquired)
	}()
	time.Sleep(20 * time.Millisecond)

	g.release() // slot frees: fg and bg race, fg must win
	<-fgAcquired
	select {
	case <-bgAcquired:
		t.Fatal("background acquired before foreground")
	default:
	}
	g.release() // fg done → the parked background proceeds
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-bgAcquired:
			g.release()
			return
		case <-deadline:
			t.Fatal("background never acquired after foreground released")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPriorityGateBackgroundYieldsToWaitingForeground(t *testing.T) {
	g := NewPriorityGate(1)
	ctx := context.Background()
	if err := g.acquire(ctx, true); err != nil { // bg holds the slot
		t.Fatal(err)
	}
	bgQueued := make(chan struct{})
	go func() {
		close(bgQueued)
		if err := g.acquire(ctx, true); err != nil {
			t.Error(err)
		}
		g.release()
	}()
	<-bgQueued
	time.Sleep(20 * time.Millisecond)

	fgAcquired := make(chan error, 1)
	go func() { fgAcquired <- g.acquire(ctx, false) }()
	for i := 0; i < 100 && g.waitingForeground() == 0; i++ {
		time.Sleep(5 * time.Millisecond) // fg must be parked before we free
	}

	g.release() // free slot: fg queued, so bg must keep waiting
	deadline := time.After(2 * time.Second)
	for done := false; !done; {
		select {
		case err := <-fgAcquired:
			if err != nil {
				t.Fatal(err)
			}
			done = true
		case <-deadline:
			t.Fatal("foreground did not take the freed slot")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	g.release() // fg done → bg may proceed
}

func TestPriorityGateContextCancel(t *testing.T) {
	g := NewPriorityGate(1)
	if err := g.acquire(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = g.acquire(ctx, true) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	// The cancelled waiter must not leak a slot: after releasing the
	// holder, a fresh acquire must succeed promptly.
	g.release()
	done := make(chan error, 1)
	go func() { done <- g.acquire(context.Background(), false) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gate wedged after cancel")
	}
}

// End-to-end through the HTTP client: a slow upstream plus a saturated
// background lane must not delay a foreground embed beyond one slot's
// service time.
func TestClientPriorityLaneUnderLoad(t *testing.T) {
	var mu sync.Mutex
	inflight := 0
	maxInflight := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inflight++
		if inflight > maxInflight {
			maxInflight = inflight
		}
		mu.Unlock()
		time.Sleep(150 * time.Millisecond)
		fmt.Fprint(w, `{"data":[{"embedding":[0.1,0.2],"index":0}]}`)
		mu.Lock()
		inflight--
		mu.Unlock()
	}))
	defer srv.Close()

	gate := NewPriorityGate(4)
	fg := NewClient(srv.URL, "m", 1, 4).WithPriorityGate(gate)
	bg := fg.WithBackground().WithConcurrency(2)

	// Saturate the background lane.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = bg.EmbedTexts(context.Background(), []string{"corpus chunk"})
		}()
	}
	time.Sleep(50 * time.Millisecond) // background occupies its lane

	start := time.Now()
	if _, err := fg.EmbedTexts(context.Background(), []string{"user query"}); err != nil {
		t.Fatal(err)
	}
	// Foreground must complete well before the whole background backlog
	// (8 × 150ms with 2 lanes ≈ 600ms): it waits for at most the gate.
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("foreground embed took %v; lane priority not effective", d)
	}
	wg.Wait()

	mu.Lock()
	if maxInflight > 4 {
		t.Fatalf("gate exceeded capacity: %d", maxInflight)
	}
	mu.Unlock()
}
