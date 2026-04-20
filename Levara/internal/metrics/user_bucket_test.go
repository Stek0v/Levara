package metrics

import (
	"testing"
	"time"
)

// Anonymous traffic is always "anon" — UserBucket does not elevate it into
// the top set (overall anon activity is already a stable label).
func TestUserBucket_AnonStaysAnon(t *testing.T) {
	b := NewUserBucket(3, 0)

	for i := 0; i < 100; i++ {
		b.Observe("")
	}
	b.Refresh()
	if got := b.Label(""); got != "anon" {
		t.Errorf("empty userID → %q, want \"anon\"", got)
	}
}

// Before any refresh the top set is empty, so every non-anon user maps
// to "other". This mirrors production where the first minute after
// startup has no promoted users yet.
func TestUserBucket_InitialOther(t *testing.T) {
	b := NewUserBucket(3, 0)
	if got := b.Label("alice"); got != "other" {
		t.Errorf("pre-refresh label = %q, want \"other\"", got)
	}
}

// After refresh, the topN heaviest users get their real IDs; the rest
// collapse into "other". This is the core cardinality guarantee.
func TestUserBucket_TopNPromoted(t *testing.T) {
	b := NewUserBucket(2, 0)

	// Fabricate counts: alice=10, bob=8, carol=5, dave=1.
	for i := 0; i < 10; i++ {
		b.Observe("alice")
	}
	for i := 0; i < 8; i++ {
		b.Observe("bob")
	}
	for i := 0; i < 5; i++ {
		b.Observe("carol")
	}
	b.Observe("dave")

	b.Refresh()

	if got := b.Label("alice"); got != "alice" {
		t.Errorf("alice (top 1) → %q, want \"alice\"", got)
	}
	if got := b.Label("bob"); got != "bob" {
		t.Errorf("bob (top 2) → %q, want \"bob\"", got)
	}
	if got := b.Label("carol"); got != "other" {
		t.Errorf("carol (below top 2) → %q, want \"other\"", got)
	}
	if got := b.Label("dave"); got != "other" {
		t.Errorf("dave (below top 2) → %q, want \"other\"", got)
	}
	if got := b.TopSetSize(); got != 2 {
		t.Errorf("TopSetSize = %d, want 2", got)
	}
}

// Counts must reset on refresh — a heavy user from one window shouldn't
// stay promoted forever once activity moves elsewhere. Without this,
// someone who ran a benchmark on Monday would hold a label slot all week.
func TestUserBucket_CountsResetOnRefresh(t *testing.T) {
	b := NewUserBucket(1, 0)

	for i := 0; i < 10; i++ {
		b.Observe("heavy")
	}
	b.Refresh()
	if b.Label("heavy") != "heavy" {
		t.Fatal("heavy should be promoted after first refresh")
	}

	// New window — different user dominates.
	for i := 0; i < 10; i++ {
		b.Observe("newcomer")
	}
	// heavy intentionally quiet this window.
	b.Refresh()
	if got := b.Label("newcomer"); got != "newcomer" {
		t.Errorf("newcomer after 2nd refresh → %q, want \"newcomer\"", got)
	}
	if got := b.Label("heavy"); got != "other" {
		t.Errorf("heavy demoted after 2nd refresh → %q, want \"other\"", got)
	}
}

// Background goroutine: refresh fires on the ticker and stop exits cleanly.
func TestUserBucket_BackgroundRefresh(t *testing.T) {
	b := NewUserBucket(1, 20*time.Millisecond)
	defer b.Stop()

	b.Observe("only-user")
	// Wait up to 200ms for the ticker to fire at least once.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if b.Label("only-user") == "only-user" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("background refresh did not promote only-user within 200ms")
}

// Stop on a bucket built without a ticker is a no-op — don't hang.
func TestUserBucket_StopWithoutTicker(t *testing.T) {
	b := NewUserBucket(1, 0)
	done := make(chan struct{})
	go func() { b.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop() on tickerless bucket blocked > 1s")
	}
}
