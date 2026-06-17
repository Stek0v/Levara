// Package vsa implements small deterministic Vector-Symbolic Architecture
// primitives for graph fact indexing.
package vsa

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"math/rand"
)

// Vector is a bipolar hypervector. Every component is -1 or +1.
type Vector []int8

// Counts is a superposition of multiple bipolar hypervectors.
type Counts []int16

const DefaultDim = 1024

// Symbol deterministically maps an identifier to a bipolar hypervector.
func Symbol(id string, dim int) Vector {
	if dim <= 0 {
		dim = DefaultDim
	}
	seed := seedFromString(id)
	rng := rand.New(rand.NewSource(int64(seed)))
	v := make(Vector, dim)
	for i := range v {
		if rng.Uint64()&1 == 0 {
			v[i] = -1
		} else {
			v[i] = 1
		}
	}
	return v
}

// Bind composes two bipolar vectors. For bipolar vectors, bind is its own
// inverse, so Bind(Bind(a, b), a) reconstructs b.
func Bind(a, b Vector) (Vector, error) {
	if len(a) != len(b) {
		return nil, fmt.Errorf("dimension mismatch: %d != %d", len(a), len(b))
	}
	out := make(Vector, len(a))
	for i := range a {
		out[i] = a[i] * b[i]
	}
	return out, nil
}

// BindCounts binds a bipolar vector with a superposed count vector.
func BindCounts(key Vector, counts Counts) (Counts, error) {
	if len(key) != len(counts) {
		return nil, fmt.Errorf("dimension mismatch: %d != %d", len(key), len(counts))
	}
	out := make(Counts, len(key))
	for i := range key {
		out[i] = int16(key[i]) * counts[i]
	}
	return out, nil
}

// Add adds a fact vector into a superposition.
func Add(counts Counts, v Vector) (Counts, error) {
	if len(counts) == 0 {
		counts = make(Counts, len(v))
	}
	if len(counts) != len(v) {
		return nil, fmt.Errorf("dimension mismatch: %d != %d", len(counts), len(v))
	}
	for i := range v {
		counts[i] += int16(v[i])
	}
	return counts, nil
}

// Sign converts a count vector back to a bipolar vector. Ties are resolved to
// +1 to keep the operation deterministic.
func Sign(counts Counts) Vector {
	out := make(Vector, len(counts))
	for i, c := range counts {
		if c < 0 {
			out[i] = -1
		} else {
			out[i] = 1
		}
	}
	return out
}

// Similarity returns cosine-like similarity for bipolar vectors in [-1, 1].
func Similarity(a, b Vector) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("dimension mismatch: %d != %d", len(a), len(b))
	}
	if len(a) == 0 {
		return 0, nil
	}
	var dot int
	for i := range a {
		dot += int(a[i] * b[i])
	}
	return float64(dot) / float64(len(a)), nil
}

// CountSimilarity scores a candidate vector against a count vector.
func CountSimilarity(counts Counts, candidate Vector) (float64, error) {
	if len(counts) != len(candidate) {
		return 0, fmt.Errorf("dimension mismatch: %d != %d", len(counts), len(candidate))
	}
	var dot int64
	var normCounts int64
	for i := range counts {
		dot += int64(counts[i]) * int64(candidate[i])
		normCounts += int64(counts[i]) * int64(counts[i])
	}
	if normCounts == 0 || len(candidate) == 0 {
		return 0, nil
	}
	return float64(dot) / (math.Sqrt(float64(normCounts)) * math.Sqrt(float64(len(candidate)))), nil
}

// EncodeFact encodes subject --predicate--> object as a self-invertible VSA
// binding: subject ⊗ predicate ⊗ object.
func EncodeFact(subjectID, predicate, objectID string, dim int) (Vector, error) {
	s := Symbol("entity:"+subjectID, dim)
	p := Symbol("predicate:"+predicate, dim)
	o := Symbol("entity:"+objectID, dim)
	sp, err := Bind(s, p)
	if err != nil {
		return nil, err
	}
	return Bind(sp, o)
}

// QueryKey returns subject ⊗ predicate. Binding a shard with this key estimates
// likely object vectors.
func QueryKey(subjectID, predicate string, dim int) (Vector, error) {
	s := Symbol("entity:"+subjectID, dim)
	p := Symbol("predicate:"+predicate, dim)
	return Bind(s, p)
}

func seedFromString(s string) uint64 {
	sum := sha256.Sum256([]byte(s))
	seed := binary.LittleEndian.Uint64(sum[:8])
	if seed == 0 {
		seed = uint64(bits.Reverse64(0x9e3779b97f4a7c15))
	}
	return seed
}
