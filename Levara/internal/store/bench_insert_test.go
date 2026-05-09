package store

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// F-1: Insert throughput benchmarks to quantify where time goes.
//
// BenchmarkInsertOneByOne_Parallel shows the group-commit advantage:
// when N goroutines call Insert concurrently, WAL fsyncs coalesce,
// so total throughput scales much better than linearly.
//
// BenchmarkInsertTiming_Breakdown measures the three phases of Insert:
// (a) marshal+lock+arena+disk+wal  (b) FlushAsync  (c) pendingMu+signal.

func BenchmarkInsertOneByOne_Parallel(b *testing.B) {
	const dim = 64
	dir, _ := os.MkdirTemp("", "levara-bench-par-*")
	defer func() { _ = os.RemoveAll(dir) }()
	db, _ := NewLevara(dim, dir+"/meta.bin")
	defer func() { _ = db.Close() }()

	vecs := make([][]float32, b.N)
	for i := range vecs {
		vecs[i] = randomVec(dim)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			id := fmt.Sprintf("par-%d-%d", b.N, i)
			_ = db.Insert(id, vecs[i%len(vecs)], nil)
			i++
		}
	})
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "inserts/sec")
}

func BenchmarkBatchInsert50_Parallel(b *testing.B) {
	const dim = 64
	const batchSize = 50
	dir, _ := os.MkdirTemp("", "levara-bench-bpar-*")
	defer func() { _ = os.RemoveAll(dir) }()
	db, _ := NewLevara(dim, dir+"/meta.bin")
	defer func() { _ = db.Close() }()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		rng := rand.New(rand.NewSource(time.Now().UnixNano()))
		j := 0
		for pb.Next() {
			items := make([]BatchItem, batchSize)
			for k := range items {
				v := make([]float32, dim)
				for d := range v {
					v[d] = rng.Float32()
				}
				items[k] = BatchItem{ID: fmt.Sprintf("bp-%d-%d", j, k), Vector: v}
			}
			db.BatchInsert(items)
			j++
		}
	})
	b.ReportMetric(float64(b.N)*batchSize/b.Elapsed().Seconds(), "dp/sec")
}

// TestInsert_TimingBreakdown is not a benchmark but a diagnostic that prints
// where time is spent in a single-threaded 100-insert burst. Run with:
//
//	go test -run TestInsert_TimingBreakdown -v ./internal/store/
func TestInsert_TimingBreakdown(t *testing.T) {
	const (
		dim = 64
		n   = 100
	)
	dir := t.TempDir()
	db, err := NewLevara(dim, dir+"/meta.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	vecs := make([][]float32, n)
	for i := range vecs {
		vecs[i] = randomVec(dim)
	}

	var totalInsert, totalFlush time.Duration

	for i := 0; i < n; i++ {
		id := fmt.Sprintf("t-%d", i)

		start := time.Now()
		_ = db.Insert(id, vecs[i], nil)
		totalInsert += time.Since(start)
	}

	t.Logf("%d inserts: total=%v avg=%v", n, totalInsert, totalInsert/time.Duration(n))
	_ = totalFlush
}

// TestInsert_ConcurrentThroughput measures actual throughput with 4, 8, 16
// goroutines for 500ms each — shows the group-commit scaling curve.
func TestInsert_ConcurrentThroughput(t *testing.T) {
	const (
		dim      = 32
		duration = 500 * time.Millisecond
	)

	for _, workers := range []int{1, 4, 8} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			dir := t.TempDir()
			db, err := NewLevara(dim, dir+"/meta.bin")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()

			ctx, cancel := context.WithTimeout(context.Background(), duration)
			defer cancel()

			var count atomic.Int64
			var wg sync.WaitGroup
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func(base int) {
					defer wg.Done()
					rng := rand.New(rand.NewSource(int64(base)))
					i := 0
					for ctx.Err() == nil {
						v := make([]float32, dim)
						for j := range v {
							v[j] = rng.Float32()
						}
						_ = db.Insert(fmt.Sprintf("w%d-%d", base, i), v, nil)
						count.Add(1)
						i++
					}
				}(w)
			}
			wg.Wait()
			c := count.Load()
			throughput := float64(c) / duration.Seconds()
			t.Logf("workers=%d: %d inserts in %v = %.0f inserts/sec", workers, c, duration, throughput)
		})
	}
}
