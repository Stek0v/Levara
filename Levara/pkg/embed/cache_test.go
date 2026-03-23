package embed

import (
	"path/filepath"
	"testing"
)

func TestCachePutGet(t *testing.T) {
	c := NewCache(100)
	vec := []float32{0.1, 0.2, 0.3}
	c.Put("hello world", vec)

	got, ok := c.Get("hello world")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if len(got) != 3 || got[0] != 0.1 {
		t.Errorf("unexpected vector: %v", got)
	}
}

func TestCacheMiss(t *testing.T) {
	c := NewCache(100)
	_, ok := c.Get("not cached")
	if ok {
		t.Error("expected miss")
	}
}

func TestCacheEviction(t *testing.T) {
	c := NewCache(3)
	c.Put("a", []float32{1})
	c.Put("b", []float32{2})
	c.Put("c", []float32{3})
	c.Put("d", []float32{4}) // evicts "a"

	_, ok := c.Get("a")
	if ok {
		t.Error("expected 'a' evicted")
	}
	_, ok = c.Get("d")
	if !ok {
		t.Error("expected 'd' present")
	}
}

func TestCacheGetMulti(t *testing.T) {
	c := NewCache(100)
	c.Put("cached1", []float32{1, 2})
	c.Put("cached2", []float32{3, 4})

	vecs, misses := c.GetMulti([]string{"cached1", "not_cached", "cached2"})
	if vecs[0] == nil || vecs[2] == nil {
		t.Error("expected hits for cached1 and cached2")
	}
	if vecs[1] != nil {
		t.Error("expected nil for not_cached")
	}
	if len(misses) != 1 || misses[0] != 1 {
		t.Errorf("expected miss index [1], got %v", misses)
	}
}

func TestCacheStats(t *testing.T) {
	c := NewCache(100)
	c.Put("x", []float32{1})
	c.Get("x")     // hit
	c.Get("y")     // miss

	size, hits, misses := c.Stats()
	if size != 1 || hits != 1 || misses != 1 {
		t.Errorf("stats: size=%d hits=%d misses=%d", size, hits, misses)
	}
}

func TestCachePersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "embed_cache.jsonl")

	c1, err := NewPersistentCache(100, path)
	if err != nil {
		t.Fatal(err)
	}
	c1.Put("text1", []float32{0.1, 0.2, 0.3})
	c1.Put("text2", []float32{0.4, 0.5, 0.6})
	c1.Close()

	// Reload
	c2, err := NewPersistentCache(100, path)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()

	vec, ok := c2.Get("text1")
	if !ok {
		t.Fatal("expected hit after reload")
	}
	if len(vec) != 3 || vec[0] != 0.1 {
		t.Errorf("unexpected vector after reload: %v", vec)
	}

	size, _, _ := c2.Stats()
	if size != 2 {
		t.Errorf("expected 2 entries after reload, got %d", size)
	}
}
