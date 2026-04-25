package store

import (
	"os"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/stek0v/cognevra/internal/metrics"
)

// TestWALRecoveryMetricIncrement covers T16 observability: every NewLevara
// startup must bump levara_wal_recoveries_total{result="ok"} so operators can
// alert on unexpected restart bumps.
func TestWALRecoveryMetricIncrement(t *testing.T) {
	dir, err := os.MkdirTemp("", "levara-wal-metric-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(dir)

	before := testutil.ToFloat64(metrics.WALRecoveriesTotal.WithLabelValues("ok"))

	db, err := NewLevara(8, dir+"/meta.bin")
	if err != nil {
		t.Fatalf("NewLevara: %v", err)
	}
	defer db.Close()

	after := testutil.ToFloat64(metrics.WALRecoveriesTotal.WithLabelValues("ok"))
	if after-before < 1 {
		t.Fatalf("levara_wal_recoveries_total{result=ok} did not increment: before=%v after=%v", before, after)
	}
}
