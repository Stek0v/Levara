package consolidate

import (
	"context"
	"errors"
	"testing"
)

// fakeSummarizer returns a canned summary regardless of input.
type fakeSummarizer struct {
	out string
	err error
}

func (f fakeSummarizer) Summarize(_ context.Context, _ []string) (string, error) {
	return f.out, f.err
}

func TestAbstractValue_AcceptsFaithfulSummary(t *testing.T) {
	sources := []string{"Pi runs potion sidecar on 9101", "potion model is 256-dim"}
	s := fakeSummarizer{out: "Pi runs the potion sidecar on 9101; the model is 256-dim."}

	got, err := AbstractValue(context.Background(), s, sources)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got == "" {
		t.Fatal("got empty summary")
	}
}

func TestAbstractValue_RejectsDroppedNumber(t *testing.T) {
	sources := []string{"Pi runs potion sidecar on 9101", "potion model is 256-dim"}
	s := fakeSummarizer{out: "Pi runs the potion sidecar."} // drops 9101 and 256

	_, err := AbstractValue(context.Background(), s, sources)
	if err == nil {
		t.Fatal("err = nil, want coverage failure (dropped numbers)")
	}
}

func TestAbstractValue_RejectsHallucinatedNumber(t *testing.T) {
	sources := []string{"potion model is 256-dim"}
	s := fakeSummarizer{out: "potion model is 256-dim and runs on port 9999."} // 9999 invented

	_, err := AbstractValue(context.Background(), s, sources)
	if err == nil {
		t.Fatal("err = nil, want hallucination failure (invented 9999)")
	}
}

func TestAbstractValue_PropagatesLLMError(t *testing.T) {
	s := fakeSummarizer{err: errors.New("llm down")}
	_, err := AbstractValue(context.Background(), s, []string{"x"})
	if err == nil {
		t.Fatal("err = nil, want propagated llm error")
	}
}
