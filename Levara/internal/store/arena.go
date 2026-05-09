package store

import (
	"fmt"
	"math"
	"sync"
	"unsafe"
)

// PageSizeBytes is the size of each arena page in bytes (4 MB).
const PageSizeBytes = 4 * 1024 * 1024 // 4MB

// VectorArena is a paged memory pool for storing fixed-dimension float32 vectors.
//
// Vectors are stored contiguously within 4 MB pages. Each page holds
// PageSizeBytes / (dim * 4) vectors. This layout keeps hot vectors cache-local
// and avoids per-vector heap allocations.
//
// All vectors are L2-normalized on insertion so that dot-product distance is
// equivalent to cosine distance, which is required by the HNSW index.
type VectorArena struct {
	mu sync.RWMutex

	dim   int
	pages [][]byte

	// Metadata to trace position
	currentPageIdx int
	currentVecIdx  int
	vectorsPerPage int
	totalVectors   uint32
}

// NewVectorArena creates a VectorArena for vectors of the given dimension.
// No pages are allocated until the first [VectorArena.Add] call.
func NewVectorArena(dim int) *VectorArena {

	vecSizeBytes := dim * 4 // 4bytes per float32
	count := PageSizeBytes / vecSizeBytes

	return &VectorArena{

		dim:            dim,
		pages:          make([][]byte, 0),
		currentPageIdx: 0,
		currentVecIdx:  0,
		totalVectors:   0,
		vectorsPerPage: count,
	}
}

// Add L2-normalizes vector in-place, stores it in the arena, and returns its
// global index. The index is stable and used by [HNSWIndex] as ArenaOffset.
// Returns an error if the vector length does not match the arena dimension or
// if the vector contains NaN/Inf values.
func (a *VectorArena) Add(vector []float32) (uint32, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(vector) != a.dim {
		return 0, fmt.Errorf("vector dimension mismatch expected %d got %d", a.dim, len(vector))
	}

	var mag2 float64
	for _, v := range vector {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return 0, fmt.Errorf("vector contains NaN or Inf values")
		}
		mag2 += float64(v) * float64(v)
	}

	// L2-normalize so dot product == cosine similarity (required by HNSW dist).
	if mag2 > 0 {
		invNorm := float32(1.0 / math.Sqrt(mag2))
		for i := range vector {
			vector[i] *= invNorm
		}
	}

	if a.currentVecIdx >= a.vectorsPerPage || len(a.pages) == 0 {
		// Allocate a new page
		newPage := make([]byte, a.dim*4*a.vectorsPerPage) // dim * 4bytes * vectorsPerPage
		a.pages = append(a.pages, newPage)

		if len(a.pages) > 1 {
			a.currentPageIdx++
		}
		a.currentVecIdx = 0
	}

	// 1. Calculate offset in bytes
	// (index * dim * 4bytes)
	offset := a.currentVecIdx * a.dim * 4

	// 2. Ensure we are writing to the LATEST page
	// Using len(a.pages)-1 is safer than trusting currentPageIdx if sync gets weird
	targetPage := a.pages[len(a.pages)-1]
	destination := targetPage[offset : offset+(a.dim*4)]

	// 3. Unsafe Copy
	// Note: This relies on architecture being Little Endian (Standard on x86/ARM)
	srcPtr := unsafe.Pointer(&vector[0])
	srcBytes := unsafe.Slice((*byte)(srcPtr), len(vector)*4)

	copy(destination, srcBytes)

	// 4. Calculate Global ID
	// Logic: (Completed Pages * Size) + Current Index
	globalId := uint32((len(a.pages)-1)*a.vectorsPerPage + a.currentVecIdx)

	a.currentVecIdx++
	a.totalVectors++

	return globalId, nil
}

// Get returns a copy of the vector at the given global index.
// Returns an error if index is out of range.
func (a *VectorArena) Get(index uint32) ([]float32, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	// 1. Calculate page and offset
	if index >= a.totalVectors {
		return nil, fmt.Errorf("Index out of bounds")
	}

	pageIdx := int(index) / a.vectorsPerPage
	vecIdxInPage := int(index) % a.vectorsPerPage

	// 2. Calculate byte offset within the page
	offset := vecIdxInPage * a.dim * 4
	rawbytes := a.pages[pageIdx][offset : offset+(a.dim*4)]

	// 3. Convert bytes to []float32 (Zero copy view)
	// For safety making a copy now

	out := make([]float32, a.dim)

	ptr := unsafe.Pointer(&rawbytes[0])
	srcFloats := unsafe.Slice((*float32)(ptr), a.dim)

	// Convert bytes back to float32 slice
	copy(out, srcFloats)

	return out, nil
}

// GetUnsafe returns a zero-copy slice into the arena page.
// The caller MUST hold db.mu.RLock (or write lock) for the lifetime of the
// returned slice — the backing memory belongs to the arena.
func (a *VectorArena) GetUnsafe(index uint32) ([]float32, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if index >= a.totalVectors {
		return nil, fmt.Errorf("index out of bounds")
	}
	pageIdx := int(index) / a.vectorsPerPage
	vecIdxInPage := int(index) % a.vectorsPerPage
	offset := vecIdxInPage * a.dim * 4
	rawbytes := a.pages[pageIdx][offset : offset+(a.dim*4)]
	return unsafe.Slice((*float32)(unsafe.Pointer(&rawbytes[0])), a.dim), nil
}

// GetNoLock returns a zero-copy slice WITHOUT acquiring any lock.
// Use ONLY when the caller guarantees no concurrent writes to the arena
// (e.g., inside HNSW.Add which holds the HNSW write lock, preventing
// concurrent inserts that could modify pages).
func (a *VectorArena) GetNoLock(index uint32) []float32 {
	pageIdx := int(index) / a.vectorsPerPage
	vecIdxInPage := int(index) % a.vectorsPerPage
	// Defensive bounds check: if the arena was swapped out from under us
	// (e.g. Clear() during a Raft snapshot restore) a stale idx would index
	// past a.pages and panic. Returning nil lets HNSW callsites skip the
	// stale node — they already tolerate nil from nodeByOffset.
	if pageIdx < 0 || pageIdx >= len(a.pages) {
		return nil
	}
	offset := vecIdxInPage * a.dim * 4
	page := a.pages[pageIdx]
	if offset < 0 || offset+(a.dim*4) > len(page) {
		return nil
	}
	rawbytes := page[offset : offset+(a.dim*4)]
	return unsafe.Slice((*float32)(unsafe.Pointer(&rawbytes[0])), a.dim)
}

// Returns the total number of vectors stored
func (a *VectorArena) Size() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return int(a.totalVectors)
}
