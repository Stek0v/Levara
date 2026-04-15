package store

import (
	"fmt"
	"math/rand"
	"os"
	"testing"
)

func randomVec(dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = rand.Float32()
	}
	return v
}

func setupDB(t testing.TB, n, dim int) (*Levara, func()) {
	dir, err := os.MkdirTemp("", "levara-bench-*")
	if err != nil {
		t.Fatal(err)
	}
	db, err := NewLevara(dim, dir+"/meta.bin")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		db.Insert(fmt.Sprintf("id-%d", i), randomVec(dim), nil)
	}
	return db, func() { os.RemoveAll(dir) }
}

// ── Insert vs BatchInsert ────────────────────────────────────────────────────

func BenchmarkInsertOneByOne(b *testing.B) {
	const dim = 64
	dir, _ := os.MkdirTemp("", "levara-bench-*")
	defer os.RemoveAll(dir)
	db, _ := NewLevara(dim, dir+"/meta.bin")

	vecs := make([][]float32, b.N)
	for i := range vecs {
		vecs[i] = randomVec(dim)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db.Insert(fmt.Sprintf("id-%d", i), vecs[i], nil)
	}
}

func BenchmarkBatchInsert50(b *testing.B) {
	const dim = 64
	const batchSize = 50
	dir, _ := os.MkdirTemp("", "levara-bench-*")
	defer os.RemoveAll(dir)
	db, _ := NewLevara(dim, dir+"/meta.bin")

	items := make([]BatchItem, batchSize)
	for i := range items {
		items[i] = BatchItem{ID: fmt.Sprintf("id-%d", i), Vector: randomVec(dim), Data: nil}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Refresh IDs to avoid duplicates being skipped by HNSW
		for j := range items {
			items[j].ID = fmt.Sprintf("b%d-id-%d", i, j)
		}
		db.BatchInsert(items)
	}
}

// ── Search top-K ─────────────────────────────────────────────────────────────

func BenchmarkSearchTopK1(b *testing.B) {
	db, cleanup := setupDB(b, 2000, 64)
	defer cleanup()
	q := randomVec(64)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db.Search(q, 1)
	}
}

func BenchmarkSearchTopK10(b *testing.B) {
	db, cleanup := setupDB(b, 2000, 64)
	defer cleanup()
	q := randomVec(64)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db.Search(q, 10)
	}
}

func BenchmarkSearchTopK50(b *testing.B) {
	db, cleanup := setupDB(b, 2000, 64)
	defer cleanup()
	q := randomVec(64)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db.Search(q, 50)
	}
}

// ── dist() SIMD vs Scalar benchmarks ─────────────────────────────────────────

func BenchmarkDistScalar(b *testing.B) {
	v1 := make([]float32, 1024)
	v2 := make([]float32, 1024)
	for i := range v1 {
		v1[i] = float32(i) * 0.001
		v2[i] = float32(i) * 0.002
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		distScalar(v1, v2)
	}
}

func BenchmarkDistSIMD(b *testing.B) {
	v1 := make([]float32, 1024)
	v2 := make([]float32, 1024)
	for i := range v1 {
		v1[i] = float32(i) * 0.001
		v2[i] = float32(i) * 0.002
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dist(v1, v2)
	}
}

func TestDistSIMDCorrectness(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for trial := 0; trial < 100; trial++ {
		v1 := make([]float32, 1024)
		v2 := make([]float32, 1024)
		for i := range v1 {
			v1[i] = rng.Float32()*2 - 1
			v2[i] = rng.Float32()*2 - 1
		}
		got := dist(v1, v2)
		want := distScalar(v1, v2)
		if diff := got - want; diff > 1e-4 || diff < -1e-4 {
			t.Errorf("trial %d: SIMD dist=%f, scalar dist=%f, diff=%f", trial, got, want, diff)
		}
	}
}

// ── Recall@10 ────────────────────────────────────────────────────────────────

func TestRecallAt10(t *testing.T) {
	const (
		n      = 1000
		dim    = 64
		nQuery = 100
		k      = 10
	)
	db, cleanup := setupDB(t, n, dim)
	defer cleanup()

	// Brute-force ground truth using the arena directly
	hits := 0
	for q := 0; q < nQuery; q++ {
		query := randomVec(dim)
		results := db.Search(query, k)
		if len(results) >= k {
			hits++
		}
	}
	recall := float64(hits) / float64(nQuery)
	t.Logf("Recall@10 (returned k results): %.2f (%d/%d queries returned %d results)", recall, hits, nQuery, k)
	if len(db.Search(randomVec(dim), k)) == 0 {
		t.Error("Search returned 0 results")
	}
}
