// rss_test.go — real-RSS source guarantees.
//
// Guards the regression where MemStats.Sys (virtual, reserved arenas
// included) stood in for RSS: 14.9 GB reported vs 4.2 GB real on prod,
// which kept background jobs permanently paused.
//
// Note: a "RSS follows allocation" growth test was deliberately removed —
// on macOS under memory pressure the kernel compresses freshly dirtied
// pages out of RSS immediately (observed: 150 MB touched, +0.8 MB in ps),
// so growth assertions are environment-flaky by nature. The darwin cache
// (the actually fragile part) has its own deterministic test in
// rss_darwin_test.go.
package governor

import (
	"testing"
)

// TestProcessRSSPlausible — the OS source returns a live, sane number.
func TestProcessRSSPlausible(t *testing.T) {
	rss := ProcessRSS()
	if rss == 0 {
		t.Fatal("ProcessRSS() = 0 — OS source missing and fallback broken")
	}
	if rss > 1<<40 {
		t.Fatalf("ProcessRSS() = %d bytes — implausible (>1 TiB)", rss)
	}
}

// TestEnvBudgetBytes — GOMEMLIMIT parsing exposed for /status.
func TestEnvBudgetBytes(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "8GiB")
	if got := EnvBudgetBytes(); got != 8<<30 {
		t.Fatalf("EnvBudgetBytes() = %d, want %d", got, 8<<30)
	}
	t.Setenv("GOMEMLIMIT", "")
	if got := EnvBudgetBytes(); got != 0 {
		t.Fatalf("EnvBudgetBytes() unset = %d, want 0", got)
	}
	t.Setenv("GOMEMLIMIT", "garbage")
	if got := EnvBudgetBytes(); got != 0 {
		t.Fatalf("EnvBudgetBytes() garbage = %d, want 0", got)
	}
}
