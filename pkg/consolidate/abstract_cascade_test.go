package consolidate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// feedbackFake returns the same deficient draft from Summarize and a scripted
// answer from SummarizeWithFeedback, tracking how the repair ladder uses it.
type feedbackFake struct {
	draft          string
	replies        []string // one entry per SummarizeWithFeedback call
	err            error    // error returned by SummarizeWithFeedback
	calls          int
	lastViolations string
}

func (f *feedbackFake) Summarize(_ context.Context, _ []string) (string, error) {
	return f.draft, nil
}

func (f *feedbackFake) SummarizeWithFeedback(_ context.Context, _ []string, violations string) (string, error) {
	f.calls++
	f.lastViolations = violations
	if f.err != nil {
		return "", f.err
	}
	if len(f.replies) == 0 {
		return f.draft, nil // stubborn model: repeats the deficient draft
	}
	rep := f.replies[0]
	f.replies = f.replies[1:]
	return rep, nil
}

var cascadeSources = []string{"Pi runs potion sidecar on 9101", "potion model is 256-dim"}

func TestAbstractValue_FeedbackRepairAccepted(t *testing.T) {
	s := &feedbackFake{
		draft:   "Pi runs the potion sidecar.", // drops 9101 and 256
		replies: []string{"Pi runs the potion sidecar on 9101; the model is 256-dim."},
	}

	got, err := AbstractValue(context.Background(), s, cascadeSources)
	if err != nil {
		t.Fatalf("err = %v, want feedback-repaired summary", err)
	}
	if s.calls != 1 {
		t.Fatalf("feedback calls = %d, want 1", s.calls)
	}
	if !strings.Contains(s.lastViolations, "9101") {
		t.Fatalf("violations %q must name the dropped units", s.lastViolations)
	}
	if strings.Contains(got, LedgerMarker) {
		t.Fatalf("repaired summary %q must not carry a ledger tail", got)
	}
}

func TestAbstractValue_FeedbackExhaustedFallsToLedger(t *testing.T) {
	s := &feedbackFake{draft: "Pi runs the potion sidecar."} // stubborn: same draft every time

	got, err := AbstractValue(context.Background(), s, cascadeSources)
	if err != nil {
		t.Fatalf("err = %v, want ledger fallback after exhausted retries", err)
	}
	if s.calls != MaxRepairAttempts {
		t.Fatalf("feedback calls = %d, want %d", s.calls, MaxRepairAttempts)
	}
	if !strings.Contains(got, LedgerMarker) || !strings.Contains(got, "9101") {
		t.Fatalf("summary %q must carry the ledger tail with missing units", got)
	}
}

func TestAbstractValue_FeedbackErrorFallsToLedger(t *testing.T) {
	s := &feedbackFake{draft: "Pi runs the potion sidecar.", err: errors.New("llm down")}

	got, err := AbstractValue(context.Background(), s, cascadeSources)
	if err != nil {
		t.Fatalf("err = %v, want ledger fallback after feedback error", err)
	}
	if !strings.Contains(got, LedgerMarker) {
		t.Fatalf("summary %q must carry the ledger tail", got)
	}
}

func TestAbstractValue_InventedStaysFatalAfterFeedback(t *testing.T) {
	s := &feedbackFake{
		draft:   "potion model is 256-dim and runs on port 9999.", // 9999 invented
		replies: []string{"potion model is 256-dim on port 9999."},
	}

	_, err := AbstractValue(context.Background(), s, []string{"potion model is 256-dim"})
	if err == nil {
		t.Fatal("err = nil, want hallucination failure after feedback")
	}
	if !strings.Contains(err.Error(), "invented") {
		t.Fatalf("err = %v, want invented-numbers violation", err)
	}
}

func TestCoverageViolations_CompositeReassembly(t *testing.T) {
	src := []string{"VPN: Pritunl 194.84.83.138:17100/tcp"}
	if v := CoverageViolations(src, "Pritunl VPN endpoint is 194.84.83.138, port 17100."); v != "" {
		t.Fatalf("reassembled composite rejected: %v", v)
	}
	if v := CoverageViolations(src, "Pritunl VPN endpoint."); v == "" {
		t.Fatal("dropped composite must be reported")
	} else if !strings.Contains(v, "194.84.83.138:17100") {
		t.Fatalf("violation %q must name the whole unit", v)
	}
}

func TestNumberUnits_CompositesAndIdentifiers(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"VPN: Pritunl 194.84.83.138:17100/tcp", []string{"194.84.83.138:17100"}},
		{"Agent Harnesses, arXiv:2609.24972", []string{"2609.24972"}},
		{"checkpoint 2026-10-01 13:29", []string{"2026", "10", "01", "13:29"}},
		{"sync node redis-170-test ready", nil},
		{"ключ ~/.ssh/id_ed25519 работает", nil},
		{"коммит f427778 смержен", nil},
		{"potion model is 256-dim", []string{"256"}},
		{"правило проекта v2", []string{"2"}},
		{"sidecar on 9101", []string{"9101"}},
	}
	for _, c := range cases {
		got := numberUnits(c.text)
		if len(got) != len(c.want) {
			t.Errorf("numberUnits(%q) = %v, want %v", c.text, got, c.want)
			continue
		}
		for _, w := range c.want {
			if !got[w] {
				t.Errorf("numberUnits(%q) missing %q (got %v)", c.text, w, got)
			}
		}
	}
}

func TestAbstractValue_LedgerPreservesInventedCheck(t *testing.T) {
	// The ledger only ever appends source units, so a repaired record can never
	// smuggle in a number the sources never had.
	s := fakeSummarizer{out: fmt.Sprintf("Pi runs the potion sidecar on %d.", 9102)} // 9102 invented
	_, err := AbstractValue(context.Background(), s, cascadeSources)
	if err == nil {
		t.Fatal("err = nil, want invented-number failure")
	}
}

// Credential-blob fragments (live labirint-1 evidence): the username
// IM-ADM-VMW@vsphere.local shatters into IM/ADM/VMW, the password
// npPciJah!G3B2npPciJah!G3B2 into G3B2/G3B2npPciJah. A summary that keeps the
// meaningful parts (host, manager product) and rewords/drops the credentials
// must pass — propagating password pieces into consolidated records is a
// secret-hygiene violation, not a coverage failure.
func TestCoverageViolations_CredentialFragments(t *testing.T) {
	src := []string{"vCenter 10.121.102.100: govc, вход IM-ADM-VMW@vsphere.local. " +
		"AD password INVALID. CE6810 пароль npPciJah!G3B2npPciJah!G3B2 (Passwork 'huawei')."}
	out := "vCenter 10.121.102.100 управляется через govc (вход VMW@vsphere.local); " +
		"оборудование CE6810 в Passwork, AD-пароль сброшен."
	if v := CoverageViolations(src, out); v != "" {
		t.Fatalf("credential-aware summary rejected: %v", v)
	}
}

// A two-fragment compound counts as one fact: keeping any fragment covers the
// whole. Dropping every fragment still fails.
func TestCoverageViolations_CompoundCoverage(t *testing.T) {
	src := []string{"gateway HNSW-IndexCluster serves traffic"}
	if v := CoverageViolations(src, "the IndexCluster gateway serves traffic"); v != "" {
		t.Fatalf("compound partially covered must pass: %v", v)
	}
	if v := CoverageViolations(src, "the gateway serves traffic"); v == "" {
		t.Fatal("fully dropped compound must be reported")
	}
}

// Rewording away the common nouns Port/INVALID/ID must not count as entity
// loss (live cascade-probe evidence from labirint-1 / ub-main-1).
func TestIsEntityToken_CommonTechWordsAndSecrets(t *testing.T) {
	for _, tok := range []string{"Port", "INVALID", "ID"} {
		if isEntityToken(tok) {
			t.Errorf("%q should be a stopword-class token, got entity", tok)
		}
	}
	for _, tok := range []string{"G3B2npPciJah", "Xk29fQq77Bz"} {
		if isEntityToken(tok) {
			t.Errorf("%q is secret-shaped, got entity", tok)
		}
	}
	for _, tok := range []string{"RSA", "Passwork", "HNSW"} {
		if !isEntityToken(tok) {
			t.Errorf("%q should stay a real entity", tok)
		}
	}
	if !isEntityToken("Redis7") {
		t.Error("digit-bearing product name must stay an entity")
	}
}
