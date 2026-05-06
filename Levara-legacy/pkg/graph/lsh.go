package graph

import (
	"math"
	"math/rand"
)

// LSHIndex is a Locality-Sensitive Hash index for fast approximate
// near-duplicate detection using random hyperplane projections.
//
// For cosine similarity, each hash bit = sign(dot(vector, random_hyperplane)).
// Vectors with similar direction produce same bit patterns.
// Probability of same bit: P = 1 - arccos(sim) / π
//
// With L hash tables of K bits each:
// - Build: O(n * L * K * dim)
// - Query: O(L * bucket_size * dim) — only compare candidates in same bucket
// - vs brute-force: O(n * dim) per query
type LSHIndex struct {
	tables     []hashTable
	numTables  int // L: number of hash tables
	numBits    int // K: bits per hash
	dim        int
	vectors    [][]float32
	ids        []int // original indices
}

type hashTable struct {
	hyperplanes [][]float32       // K hyperplanes, each of dimension dim
	buckets     map[uint64][]int  // hash → list of vector indices
}

// NewLSH creates an LSH index.
// numTables (L): more tables = higher recall, more memory. Default 10.
// numBits (K): more bits = fewer candidates, lower recall. Default 8.
func NewLSH(dim, numTables, numBits int) *LSHIndex {
	if numTables <= 0 {
		numTables = 10
	}
	if numBits <= 0 {
		numBits = 8
	}

	rng := rand.New(rand.NewSource(42)) // deterministic for reproducibility

	tables := make([]hashTable, numTables)
	for t := 0; t < numTables; t++ {
		hp := make([][]float32, numBits)
		for k := 0; k < numBits; k++ {
			plane := make([]float32, dim)
			for d := 0; d < dim; d++ {
				plane[d] = float32(rng.NormFloat64())
			}
			hp[k] = plane
		}
		tables[t] = hashTable{
			hyperplanes: hp,
			buckets:     make(map[uint64][]int),
		}
	}

	return &LSHIndex{
		tables:    tables,
		numTables: numTables,
		numBits:   numBits,
		dim:       dim,
	}
}

// Build indexes all vectors.
func (idx *LSHIndex) Build(vectors [][]float32) {
	idx.vectors = vectors
	idx.ids = make([]int, len(vectors))
	for i := range vectors {
		idx.ids[i] = i
	}

	for t := range idx.tables {
		idx.tables[t].buckets = make(map[uint64][]int, len(vectors)/4)
		for i, vec := range vectors {
			h := idx.hash(&idx.tables[t], vec)
			idx.tables[t].buckets[h] = append(idx.tables[t].buckets[h], i)
		}
	}
}

// QueryCandidates returns indices of vectors that are likely similar
// to the query vector (appear in same bucket in any table).
func (idx *LSHIndex) QueryCandidates(vec []float32) []int {
	seen := make(map[int]bool)
	var candidates []int

	for t := range idx.tables {
		h := idx.hash(&idx.tables[t], vec)
		for _, i := range idx.tables[t].buckets[h] {
			if !seen[i] {
				seen[i] = true
				candidates = append(candidates, i)
			}
		}
	}

	return candidates
}

func (idx *LSHIndex) hash(table *hashTable, vec []float32) uint64 {
	var h uint64
	for k, plane := range table.hyperplanes {
		dot := dotProduct(vec, plane)
		if dot >= 0 {
			h |= 1 << uint(k)
		}
	}
	return h
}

func dotProduct(a, b []float32) float32 {
	var sum float32
	for i := range a {
		if i >= len(b) {
			break
		}
		sum += a[i] * b[i]
	}
	return sum
}

// SemanticDedupLSH uses LSH for fast near-duplicate detection.
// Much faster than brute-force for large inputs (1000+ vectors).
//
// Algorithm:
// 1. Build LSH index on all vectors — O(n * L * K * dim)
// 2. For each vector, get LSH candidates — O(L * bucket_size)
// 3. Verify candidates with exact cosine — O(candidates * dim)
// 4. Greedy dedup: first occurrence wins
//
// Complexity: O(n * L * K * dim + n * avg_candidates * dim)
// vs brute-force: O(n² * dim)
func SemanticDedupLSH(vectors [][]float32, threshold float32, numTables, numBits int) SemanticDedupResult {
	if threshold <= 0 {
		threshold = 0.95
	}
	n := len(vectors)
	if n == 0 {
		return SemanticDedupResult{}
	}
	if n < 100 {
		// For small inputs, brute-force is faster (no LSH overhead)
		return SemanticDedup(vectors, threshold)
	}

	dim := len(vectors[0])
	lsh := NewLSH(dim, numTables, numBits)
	lsh.Build(vectors)

	removed := make(map[int]bool)
	keptSet := make(map[int]bool)
	var kept []int
	var removedList []int
	var pairs []DedupPair

	for i := 0; i < n; i++ {
		if removed[i] {
			continue
		}

		candidates := lsh.QueryCandidates(vectors[i])

		isDup := false
		var bestMatch int
		var bestSim float32

		for _, c := range candidates {
			if c >= i || !keptSet[c] {
				continue // only compare against already-kept items with lower index
			}
			sim := cosineSimilarity(vectors[i], vectors[c])
			if sim > threshold && sim > bestSim {
				isDup = true
				bestMatch = c
				bestSim = sim
			}
		}

		if isDup {
			removed[i] = true
			removedList = append(removedList, i)
			pairs = append(pairs, DedupPair{
				KeptIdx: bestMatch, RemovedIdx: i, Similarity: bestSim,
			})
		} else {
			kept = append(kept, i)
			keptSet[i] = true
		}
	}

	// Handle items that had no LSH candidates
	for i := 0; i < n; i++ {
		if !removed[i] && !keptSet[i] {
			kept = append(kept, i)
		}
	}

	return SemanticDedupResult{Kept: kept, Removed: removedList, Pairs: pairs}
}

// cosineSimilarityNorm computes cosine for pre-normalized vectors (just dot product).
func cosineSimilarityNorm(a, b []float32) float32 {
	return dotProduct(a, b)
}

// normalizeVector normalizes vector to unit length.
func normalizeVector(v []float32) []float32 {
	var norm float32
	for _, x := range v {
		norm += x * x
	}
	norm = float32(math.Sqrt(float64(norm)))
	if norm == 0 {
		return v
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x / norm
	}
	return out
}
