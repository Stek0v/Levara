package vsa

import "testing"

func TestSymbolDeterministic(t *testing.T) {
	a := Symbol("entity:auth", 256)
	b := Symbol("entity:auth", 256)
	c := Symbol("entity:db", 256)
	same, err := Similarity(a, b)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := Similarity(a, c)
	if err != nil {
		t.Fatal(err)
	}
	if same != 1 {
		t.Fatalf("same symbol similarity=%v, want 1", same)
	}
	if diff > 0.25 {
		t.Fatalf("different symbol similarity=%v, want near 0", diff)
	}
}

func TestBindIsSelfInverse(t *testing.T) {
	a := Symbol("a", 512)
	b := Symbol("b", 512)
	ab, err := Bind(a, b)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := Bind(ab, a)
	if err != nil {
		t.Fatal(err)
	}
	sim, err := Similarity(recovered, b)
	if err != nil {
		t.Fatal(err)
	}
	if sim != 1 {
		t.Fatalf("recovered similarity=%v, want 1", sim)
	}
}

func TestFactSuperpositionRanksCorrectObject(t *testing.T) {
	const dim = 1024
	fact1, err := EncodeFact("auth", "calls", "postgres", dim)
	if err != nil {
		t.Fatal(err)
	}
	fact2, err := EncodeFact("api", "calls", "auth", dim)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := Add(nil, fact1)
	if err != nil {
		t.Fatal(err)
	}
	counts, err = Add(counts, fact2)
	if err != nil {
		t.Fatal(err)
	}

	key, err := QueryKey("auth", "calls", dim)
	if err != nil {
		t.Fatal(err)
	}
	estimate, err := BindCounts(key, counts)
	if err != nil {
		t.Fatal(err)
	}
	postgresScore, err := CountSimilarity(estimate, Symbol("entity:postgres", dim))
	if err != nil {
		t.Fatal(err)
	}
	authScore, err := CountSimilarity(estimate, Symbol("entity:auth", dim))
	if err != nil {
		t.Fatal(err)
	}
	if postgresScore <= authScore {
		t.Fatalf("postgres score=%v auth score=%v, want postgres higher", postgresScore, authScore)
	}
}
