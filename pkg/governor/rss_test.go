// rss_test.go — real-RSS source guarantees.
//
// Guards the regression where MemStats.Sys (virtual, reserved arenas
// included) stood in for RSS: 14.9 GB reported vs 4.2 GB real on prod,
// which kept background jobs permanently paused.
package governor

import (
	"runtime"
	"testing"
	"time"
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

// TestProcessRSSTracksAllocation — RSS follows a real touched allocation.
// Catches a frozen/stale source (e.g. a cache that never expires or a
// peak-only reading after a later free). Margins are loose on purpose:
// CI schedulers can reclaim pages between touch and read.
func TestProcessRSSTracksAllocation(t *testing.T) {
	before := ProcessRSS()
	buf := make([]byte, 150<<20)
	for i := 0; i < len(buf); i += 4096 {
		buf[i] = 1
	}
	runtime.GC()
	time.Sleep(400 * time.Millisecond) // darwin ps cache TTL is 250ms
	after := ProcessRSS()
	if after < before+64<<20 {
		t.Fatalf("RSS did not follow a 150 MB allocation: before=%d after=%d", before, after)
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
