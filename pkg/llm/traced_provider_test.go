package llm

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeTracer struct {
	enabled bool
	mu      sync.Mutex
	traces  []TracerData
}

func (f *fakeTracer) Enabled() bool { return f.enabled }
func (f *fakeTracer) TraceGeneration(_ context.Context, td TracerData) error {
	f.mu.Lock()
	f.traces = append(f.traces, td)
	f.mu.Unlock()
	return nil
}

type tracedFakeProvider struct {
	resp *CompletionResponse
	err  error
	n    int32
}

func (p *tracedFakeProvider) Name() string { return "fake" }
func (p *tracedFakeProvider) ChatCompletion(_ context.Context, _ CompletionRequest) (*CompletionResponse, error) {
	atomic.AddInt32(&p.n, 1)
	return p.resp, p.err
}

func waitTraces(t *fakeTracer, want int, deadline time.Duration) int {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		t.mu.Lock()
		n := len(t.traces)
		t.mu.Unlock()
		if n >= want {
			return n
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.traces)
}

func TestTracedProvider_NilTracerUnwrapped(t *testing.T) {
	fp := &tracedFakeProvider{resp: &CompletionResponse{Content: "x"}}
	p := NewTracedProvider(fp, nil)
	if p != fp {
		t.Errorf("nil tracer should return original provider")
	}
}

func TestTracedProvider_DisabledTracerUnwrapped(t *testing.T) {
	fp := &tracedFakeProvider{resp: &CompletionResponse{Content: "x"}}
	p := NewTracedProvider(fp, &fakeTracer{enabled: false})
	if p != fp {
		t.Errorf("disabled tracer should return original provider")
	}
}

func TestTracedProvider_ErrorAlwaysTraced(t *testing.T) {
	tr := &fakeTracer{enabled: true}
	fp := &tracedFakeProvider{err: errors.New("boom")}
	p := NewTracedProvider(fp, tr).(*TracedProvider)
	p.SampleRate = 0.0 // disable success sampling

	for i := 0; i < 5; i++ {
		_, _ = p.ChatCompletion(context.Background(), CompletionRequest{Model: "m"})
	}
	got := waitTraces(tr, 5, 500*time.Millisecond)
	if got != 5 {
		t.Errorf("expected 5 error traces, got %d", got)
	}
	for _, td := range tr.traces {
		if td.Status != "error" {
			t.Errorf("expected status=error, got %q", td.Status)
		}
	}
}

func TestTracedProvider_SuccessSampleRateZero(t *testing.T) {
	tr := &fakeTracer{enabled: true}
	fp := &tracedFakeProvider{resp: &CompletionResponse{Content: "ok"}}
	p := NewTracedProvider(fp, tr).(*TracedProvider)
	p.SampleRate = 0.0

	for i := 0; i < 50; i++ {
		_, _ = p.ChatCompletion(context.Background(), CompletionRequest{Model: "m"})
	}
	time.Sleep(50 * time.Millisecond)
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.traces) != 0 {
		t.Errorf("rate=0 should drop all successes, got %d traces", len(tr.traces))
	}
}

func TestTracedProvider_SuccessSampleRateOne(t *testing.T) {
	tr := &fakeTracer{enabled: true}
	fp := &tracedFakeProvider{resp: &CompletionResponse{Content: "ok"}}
	p := NewTracedProvider(fp, tr).(*TracedProvider)
	p.SampleRate = 1.0

	for i := 0; i < 20; i++ {
		_, _ = p.ChatCompletion(context.Background(), CompletionRequest{Model: "m"})
	}
	got := waitTraces(tr, 20, 500*time.Millisecond)
	if got != 20 {
		t.Errorf("rate=1 should trace all successes, got %d", got)
	}
}

func TestTracedProvider_DefaultSampleRate(t *testing.T) {
	tr := &fakeTracer{enabled: true}
	fp := &tracedFakeProvider{resp: &CompletionResponse{Content: "ok"}}
	p := NewTracedProvider(fp, tr).(*TracedProvider)
	if p.SampleRate != defaultSampleRate {
		t.Errorf("expected default %v, got %v", defaultSampleRate, p.SampleRate)
	}
}
