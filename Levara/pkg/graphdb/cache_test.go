package graphdb

import (
	"testing"
	"time"
)

func TestCacheHitMiss(t *testing.T) {
	cw := &CachedWriter{
		cache:   make(map[string]*cacheEntry),
		maxSize: 100,
		ttl:     time.Minute,
	}

	// Miss
	_, ok := cw.getCached("key1")
	if ok {
		t.Error("expected miss on empty cache")
	}

	// Put
	cw.putCache("key1", GraphReadResult{
		Nodes: []ReadNode{{ID: "n1", Label: "Test"}},
	})

	// Hit
	result, ok := cw.getCached("key1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if len(result.Nodes) != 1 || result.Nodes[0].ID != "n1" {
		t.Errorf("unexpected cached result: %v", result)
	}

	stats := cw.CacheStats()
	if stats.Hits != 1 || stats.Misses != 1 {
		t.Errorf("stats: hits=%d misses=%d, expected 1/1", stats.Hits, stats.Misses)
	}
}

func TestCacheTTL(t *testing.T) {
	cw := &CachedWriter{
		cache:   make(map[string]*cacheEntry),
		maxSize: 100,
		ttl:     50 * time.Millisecond,
	}

	cw.putCache("k1", GraphReadResult{Nodes: []ReadNode{{ID: "n1"}}})

	// Hit before TTL
	_, ok := cw.getCached("k1")
	if !ok {
		t.Fatal("expected hit before TTL")
	}

	time.Sleep(60 * time.Millisecond)

	// Miss after TTL
	_, ok = cw.getCached("k1")
	if ok {
		t.Error("expected miss after TTL")
	}
}

func TestCacheInvalidate(t *testing.T) {
	cw := &CachedWriter{
		cache:   make(map[string]*cacheEntry),
		maxSize: 100,
		ttl:     time.Minute,
	}

	cw.putCache("k1", GraphReadResult{Nodes: []ReadNode{{ID: "n1"}}})
	cw.putCache("k2", GraphReadResult{Nodes: []ReadNode{{ID: "n2"}}})

	cw.Invalidate()

	_, ok := cw.getCached("k1")
	if ok {
		t.Error("expected miss after invalidate")
	}
	if cw.CacheStats().Size != 0 {
		t.Errorf("size after invalidate: %d", cw.CacheStats().Size)
	}
}

func TestCacheEviction(t *testing.T) {
	cw := &CachedWriter{
		cache:   make(map[string]*cacheEntry),
		maxSize: 3,
		ttl:     time.Minute,
	}

	cw.putCache("k1", GraphReadResult{})
	cw.putCache("k2", GraphReadResult{})
	cw.putCache("k3", GraphReadResult{})
	cw.putCache("k4", GraphReadResult{}) // evicts oldest

	if cw.CacheStats().Size != 3 {
		t.Errorf("size after eviction: expected 3, got %d", cw.CacheStats().Size)
	}
}

func BenchmarkCacheGet(b *testing.B) {
	cw := &CachedWriter{
		cache:   make(map[string]*cacheEntry),
		maxSize: 10000,
		ttl:     time.Minute,
	}
	cw.putCache("bench", GraphReadResult{
		Nodes: make([]ReadNode, 100),
		Edges: make([]ReadEdge, 50),
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cw.getCached("bench")
	}
}
