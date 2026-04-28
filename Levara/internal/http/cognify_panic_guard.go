package http

import (
	"fmt"
	"log"
	"runtime/debug"

	"github.com/stek0v/levara/internal/metrics"
)

// runWithPanicGuard executes fn and converts a panic into an error. It also
// bumps the levara_cognify_panics_total{stage=<stage>} counter and logs a
// stack trace. stageFn is a closure so we read the most recent stage
// value at recover time (the pipeline mutates it as it progresses).
//
// This is the unit-testable seam for the inner cognify goroutine's panic
// handling. See cognifyHandler in api.go.
func runWithPanicGuard(runID string, stageFn func() string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			stage := stageFn()
			metrics.CognifyPanics.WithLabelValues(stage).Inc()
			log.Printf("cognify pipeline panic run_id=%s stage=%s panic=%v\n%s",
				runID, stage, r, debug.Stack())
			err = fmt.Errorf("pipeline panic: %v", r)
		}
	}()
	return fn()
}
