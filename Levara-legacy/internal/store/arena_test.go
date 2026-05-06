package store

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
)

// ── Dimension validation ─────────────────────────────────────────────────────

func TestArena_Add_CorrectDimension(t *testing.T) {
	a := NewVectorArena(4)
	idx, err := a.Add([]float32{1, 2, 3, 4})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idx != 0 {
		t.Fatalf("expected index 0, got %d", idx)
	}
}

func TestArena_Add_DimensionMismatch_TooFew(t *testing.T) {
	a := NewVectorArena(4)
	_, err := a.Add([]float32{1, 2, 3})
	if err == nil {
		t.Fatal("expected error for dim mismatch (too few)")
	}
	if !strings.Contains(err.Error(), "expected 4 got 3") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestArena_Add_DimensionMismatch_TooMany(t *testing.T) {
	a := NewVectorArena(4)
	_, err := a.Add([]float32{1, 2, 3, 4, 5})
	if err == nil {
		t.Fatal("expected error for dim mismatch (too many)")
	}
	if !strings.Contains(err.Error(), "expected 4 got 5") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestArena_Add_EmptyVector(t *testing.T) {
	a := NewVectorArena(4)
	_, err := a.Add([]float32{})
	if err == nil {
		t.Fatal("expected error for empty vector")
	}
	if !strings.Contains(err.Error(), "expected 4 got 0") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// ── NaN / Inf validation ─────────────────────────────────────────────────────

func TestArena_Add_NaN_Rejected(t *testing.T) {
	a := NewVectorArena(3)
	_, err := a.Add([]float32{1, float32(math.NaN()), 3})
	if err == nil {
		t.Fatal("expected error for NaN")
	}
	if !strings.Contains(err.Error(), "NaN or Inf") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestArena_Add_PosInf_Rejected(t *testing.T) {
	a := NewVectorArena(3)
	_, err := a.Add([]float32{1, float32(math.Inf(1)), 3})
	if err == nil {
		t.Fatal("expected error for +Inf")
	}
	if !strings.Contains(err.Error(), "NaN or Inf") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestArena_Add_NegInf_Rejected(t *testing.T) {
	a := NewVectorArena(3)
	_, err := a.Add([]float32{1, float32(math.Inf(-1)), 3})
	if err == nil {
		t.Fatal("expected error for -Inf")
	}
	if !strings.Contains(err.Error(), "NaN or Inf") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestArena_Add_DimCheckBeforeNaNCheck(t *testing.T) {
	a := NewVectorArena(4)
	// Vector has 3 elements (wrong dim) AND contains NaN — dim error should come first
	_, err := a.Add([]float32{float32(math.NaN()), float32(math.NaN()), float32(math.NaN())})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "expected 4 got 3") {
		t.Fatalf("expected dimension error before NaN error, got: %v", err)
	}
}

// ── Multiple inserts ─────────────────────────────────────────────────────────

func TestArena_Add_MultipleInserts_MixedDims(t *testing.T) {
	a := NewVectorArena(4)
	_, err := a.Add([]float32{1, 2, 3, 4})
	if err != nil {
		t.Fatalf("first insert should succeed: %v", err)
	}
	_, err = a.Add([]float32{1, 2, 3})
	if err == nil {
		t.Fatal("second insert with wrong dim should fail")
	}
	if !strings.Contains(err.Error(), "expected 4 got 3") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// ── Concurrent access ────────────────────────────────────────────────────────

func TestArena_Add_Concurrent(t *testing.T) {
	const dim = 4
	a := NewVectorArena(dim)

	var wg sync.WaitGroup
	// 50 correct + 50 wrong dimension
	errs := make([]error, 100)

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = a.Add([]float32{1, 2, 3, 4})
		}(i)
	}
	for i := 50; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = a.Add([]float32{1, 2}) // wrong dim
		}(i)
	}
	wg.Wait()

	correctOK := 0
	wrongFailed := 0
	for i := 0; i < 50; i++ {
		if errs[i] == nil {
			correctOK++
		}
	}
	for i := 50; i < 100; i++ {
		if errs[i] != nil {
			wrongFailed++
		}
	}
	if correctOK != 50 {
		t.Fatalf("expected 50 correct inserts, got %d", correctOK)
	}
	if wrongFailed != 50 {
		t.Fatalf("expected 50 wrong-dim failures, got %d", wrongFailed)
	}
	if a.Size() != 50 {
		t.Fatalf("expected arena size 50, got %d", a.Size())
	}
}

// ── Zero dimension panic ─────────────────────────────────────────────────────

func TestArena_NewVectorArena_ZeroDim_Panics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for dim=0 (division by zero in PageSizeBytes/vecSizeBytes)")
		}
		t.Logf("recovered panic (expected): %v", r)
	}()
	_ = NewVectorArena(0)
	// If Add is reached, attempt it to trigger panic during page allocation
	// (depends on where Go catches the division by zero)
	t.Fatal("should not reach here")
}

// ── Roundtrip: Add then Get ──────────────────────────────────────────────────

func TestArena_Add_Get_Roundtrip(t *testing.T) {
	a := NewVectorArena(3)
	input := []float32{1.5, 2.5, 3.5}
	idx, err := a.Add(input)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	got, err := a.Get(idx)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	for i := range input {
		if got[i] != input[i] {
			t.Fatalf("mismatch at [%d]: expected %f, got %f", i, input[i], got[i])
		}
	}
}

// ── Sequential index correctness ─────────────────────────────────────────────

func TestArena_Add_SequentialIndices(t *testing.T) {
	a := NewVectorArena(2)
	for i := 0; i < 100; i++ {
		idx, err := a.Add([]float32{float32(i), float32(i + 1)})
		if err != nil {
			t.Fatalf("insert %d failed: %v", i, err)
		}
		if idx != uint32(i) {
			t.Fatalf("expected index %d, got %d", i, idx)
		}
	}
	if a.Size() != 100 {
		t.Fatalf("expected size 100, got %d", a.Size())
	}
}

// ── Page boundary crossing ───────────────────────────────────────────────────

func TestArena_Add_CrossesPageBoundary(t *testing.T) {
	// dim=4 → vecSize=16 bytes → vectorsPerPage = 4MB/16 = 262144
	// We'll use a large dim to force page crossing with fewer inserts
	const dim = 1024 // vecSize = 4096 bytes → vectorsPerPage = 4MB/4096 = 1024
	a := NewVectorArena(dim)

	vec := make([]float32, dim)
	for i := range vec {
		vec[i] = float32(i)
	}

	// Insert enough to cross at least one page boundary
	for i := 0; i < 1025; i++ {
		idx, err := a.Add(vec)
		if err != nil {
			t.Fatalf("insert %d failed: %v", i, err)
		}
		_ = idx
	}
	if a.Size() != 1025 {
		t.Fatalf("expected 1025 vectors, got %d", a.Size())
	}

	// Verify retrieval from second page
	got, err := a.Get(1024)
	if err != nil {
		t.Fatalf("Get(1024) failed: %v", err)
	}
	if len(got) != dim {
		t.Fatalf("expected dim %d, got %d", dim, len(got))
	}
	for i := range got {
		if got[i] != vec[i] {
			t.Fatalf("mismatch at [%d] after page boundary: got %f, want %f", i, got[i], vec[i])
		}
	}
	fmt.Printf("  Page boundary test passed: 1025 vectors of dim=%d, pages=%d\n", dim, len(a.pages))
}
