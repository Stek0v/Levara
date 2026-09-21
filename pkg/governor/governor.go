// governor.go — A3: resource governor.
//
// The single source of truth for "can this job run right now without
// hurting the host". Knows RSS, memory budget, and per-job pressure
// state. Background jobs call CanWork() before each work unit.
//
// Design principles:
//  1. Quality first: with no budget configured, nothing is ever paused.
//  2. Hysteresis: pause at >80%, resume at <60% — no oscillation.
//  3. The governor itself must be cheap: counters and one MemStats read.
package governor

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Governor tracks memory pressure and coordinates job pauses.
type Governor struct {
	mu        sync.RWMutex
	budget    uint64 // bytes; 0 = no budget (never pause)
	paused    map[string]bool
	lastRSS   uint64
	lastCheck time.Time
}

// NewGovernor creates a governor with the given memory budget in bytes.
// A budget of 0 disables pausing entirely — quality always comes first.
func NewGovernor(budgetBytes uint64) *Governor {
	return &Governor{
		budget: budgetBytes,
		paused: make(map[string]bool),
	}
}

// NewGovernorFromEnv reads GOMEMLIMIT (e.g. "8GiB") or returns
// a no-budget governor if unset/invalid.
func NewGovernorFromEnv() *Governor {
	v := strings.TrimSpace(os.Getenv("GOMEMLIMIT"))
	if v == "" {
		return NewGovernor(0)
	}
	return NewGovernor(parseSizeBytes(v))
}

// CanWork is the combined check: pause if over threshold, resume if
// under. Returns true when the job should proceed with its work unit.
func (g *Governor) CanWork(jobName string) bool {
	if g == nil || g.budget == 0 {
		return true // no budget = quality first, never pause
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	rss := g.currentRSS()
	g.lastRSS = rss
	g.lastCheck = time.Now()
	ratio := float64(rss) / float64(g.budget)

	switch {
	case ratio > 0.80:
		g.paused[jobName] = true
		return false
	case ratio < 0.60:
		delete(g.paused, jobName)
		return true
	default:
		// Between 60% and 80%: keep current state (hysteresis)
		return !g.paused[jobName]
	}
}

// PressureLevel classifies current memory usage.
type PressureLevel string

const (
	PressureLow      PressureLevel = "low"
	PressureNormal   PressureLevel = "normal"
	PressureElevated PressureLevel = "elevated"
	PressureCritical PressureLevel = "critical"
	PressureUnknown  PressureLevel = "unknown"
)

// Pressure returns the current memory pressure level and utilization ratio.
func (g *Governor) Pressure() (PressureLevel, float64) {
	if g == nil || g.budget == 0 {
		return PressureUnknown, 0
	}
	g.mu.Lock()
	rss := g.currentRSS()
	g.lastRSS = rss
	g.lastCheck = time.Now()
	g.mu.Unlock()

	ratio := float64(rss) / float64(g.budget)
	switch {
	case ratio > 0.90:
		return PressureCritical, ratio
	case ratio > 0.80:
		return PressureElevated, ratio
	case ratio > 0.50:
		return PressureNormal, ratio
	default:
		return PressureLow, ratio
	}
}

// BudgetBytes returns the configured budget (0 = unlimited).
func (g *Governor) BudgetBytes() uint64 {
	if g == nil {
		return 0
	}
	return g.budget
}

// PausedJobs returns the names of currently paused jobs.
func (g *Governor) PausedJobs() []string {
	if g == nil {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	names := make([]string, 0, len(g.paused))
	for name := range g.paused {
		names = append(names, name)
	}
	return names
}

// currentRSS reads the process memory from Go's MemStats.
// Sys is an approximation of RSS; adequate for pressure detection.
func (g *Governor) currentRSS() uint64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.Sys
}

// parseSizeBytes parses "8GiB", "512MiB", "1073741824" into bytes.
func parseSizeBytes(s string) uint64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if n, err := strconv.ParseUint(s, 10, 64); err == nil {
		return n
	}
	multipliers := []struct {
		suffix string
		mult   uint64
	}{
		{"KiB", 1024}, {"MiB", 1024 * 1024}, {"GiB", 1024 * 1024 * 1024},
		{"TiB", 1024 * 1024 * 1024 * 1024},
		{"KB", 1000}, {"MB", 1000 * 1000}, {"GB", 1000 * 1000 * 1000},
	}
	for _, m := range multipliers {
		if strings.HasSuffix(s, m.suffix) {
			numStr := strings.TrimSpace(strings.TrimSuffix(s, m.suffix))
			if n, err := strconv.ParseUint(numStr, 10, 64); err == nil {
				return n * m.mult
			}
		}
	}
	return 0
}
