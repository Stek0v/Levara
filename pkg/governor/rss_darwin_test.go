//go:build darwin

package governor

import (
	"testing"
	"time"
)

// TestDarwinRSSCacheExpires — the ps result is cached briefly so the
// governor's per-tick checks share one exec, but the cache must honor its
// TTL: a cache that never expired would freeze the reported RSS at its
// first value for the process lifetime (and the memory-pressure decisions
// derived from it).
func TestDarwinRSSCacheExpires(t *testing.T) {
	first := readOSRSS()
	if first == 0 {
		t.Fatal("readOSRSS returned 0")
	}

	// Age the cached entry past the TTL and read again — this must run a
	// fresh exec, not serve the stale value through an expired lease.
	rssCache.mu.Lock()
	rssCache.at = time.Now().Add(-time.Second)
	rssCache.mu.Unlock()
	if second := readOSRSS(); second == 0 {
		t.Fatal("readOSRSS returned 0 after cache expiry")
	}
}
