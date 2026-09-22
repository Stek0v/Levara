//go:build !linux && !darwin

package governor

// readOSRSS — no OS source on this platform; ProcessRSS falls back to the
// MemStats.Sys over-approximation (pauses too early, never too late).
func readOSRSS() uint64 { return 0 }
