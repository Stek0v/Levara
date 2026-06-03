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

func TestAbstractValue_RejectsEmptySources(t *testing.T) {
	s := fakeSummarizer{out: "anything the model says"}
	_, err := AbstractValue(context.Background(), s, nil)
	if err == nil {
		t.Fatal("err = nil, want error for empty sources")
	}
}

// A single dropped real entity in a large entity set is within tolerance
// (entity coverage is fraction-based, not all-or-nothing). 1/12 ≈ 8% ≤ 10%.
func TestAbstractValue_AllowsSmallFractionEntityDrop(t *testing.T) {
	sources := []string{
		"Levara Pi DeepSeek Potion Hnsw Wal Bm25 Rrf Cognify Neo Postgres Ollama work",
	}
	// Drops Ollama only: 1 of 12 genuine entities ≈ 8%.
	s := fakeSummarizer{out: "Levara Pi DeepSeek Potion Hnsw Wal Bm25 Rrf Cognify Neo Postgres summary"}
	if _, err := AbstractValue(context.Background(), s, sources); err != nil {
		t.Fatalf("err = %v, want nil (8%% entity drop within tolerance)", err)
	}
}

// Dropping a large fraction of entities still fails the guard. 1/3 ≈ 33% > 10%.
func TestAbstractValue_RejectsLargeFractionEntityDrop(t *testing.T) {
	sources := []string{"Levara Pi DeepSeek"}
	s := fakeSummarizer{out: "Levara Pi only"} // drops DeepSeek
	if _, err := AbstractValue(context.Background(), s, sources); err == nil {
		t.Fatal("err = nil, want reject (33% entity drop exceeds tolerance)")
	}
}

// P2.5 regression: the live `localllm` cluster was rejected because the summary
// dropped "REPL" and "Real" — a code keyword and a sentence-start common word,
// neither a meaning-bearing entity. entityRe matches every capitalized token, so
// rewording such stopwords away counted as dropped entities and tripped the
// coverage guard. Stopword-class tokens must not be counted as entities.
func TestAbstractValue_IgnoresStopwordCapitalizedTokens(t *testing.T) {
	sources := []string{"Real progress: the Levara REPL on Pi now answers. NULL means absent."}
	// Keeps the only true entities (Levara, Pi) but rewords away the capitalized
	// stopwords Real/REPL/NULL. Under the old all-caps guard this was 3/5 = 60%.
	s := fakeSummarizer{out: "the Levara shell on Pi now answers; absent values are unset."}
	if _, err := AbstractValue(context.Background(), s, sources); err != nil {
		t.Fatalf("err = %v, want nil (dropped tokens are stopwords, not entities)", err)
	}
}

// The stopword gate must not weaken coverage for genuine entities: an all-caps
// acronym that is a real identifier (HNSW) still counts, so dropping it fails.
func TestAbstractValue_RealAcronymStillCounts(t *testing.T) {
	sources := []string{"HNSW index"}
	s := fakeSummarizer{out: "the index"} // drops HNSW (a real entity, not a stopword)
	if _, err := AbstractValue(context.Background(), s, sources); err == nil {
		t.Fatal("err = nil, want reject (dropped real acronym HNSW)")
	}
}
