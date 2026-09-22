// rss.go — real process RSS: the memory the OS actually charged us for.
//
// runtime.MemStats.Sys is NOT RSS. Sys counts address space obtained from
// the OS, including reserved-but-untouched arenas (vector indices, BM25
// snapshots). Measured on prod: Sys reported 14.9 GB while the real
// resident set was 4.2 GB — which made the governor pause background jobs
// at a phantom 186% of budget and /status cry wolf. Platform sources live
// in rss_linux.go and rss_darwin.go.
package governor

import "runtime"

// ProcessRSS returns the current resident set size of this process in
// bytes. Falls back to a Sys-based over-approximation only on platforms
// without a supported OS source (linux and darwin always have one).
func ProcessRSS() uint64 {
	if rss := readOSRSS(); rss > 0 {
		return rss
	}
	// Over-approximation, better than 0: pauses too early, never too late.
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.Sys
}
