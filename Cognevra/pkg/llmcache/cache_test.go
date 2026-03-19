package llmcache

import (
	"testing"
	"time"
)

func TestKeyDeterministic(t *testing.T) {
	k1 := Key("gpt-4", "hello", "system", 0.0)
	k2 := Key("gpt-4", "hello", "system", 0.0)
	if k1 != k2 {
		t.Errorf("same inputs should produce same key: %s != %s", k1, k2)
	}
}

func TestKeyDifferentInputs(t *testing.T) {
	k1 := Key("gpt-4", "hello", "system", 0.0)
	k2 := Key("gpt-4", "hello", "system", 0.1)
	if k1 == k2 {
		t.Error("different temperature should produce different key")
	}
}

func TestPutGet(t *testing.T) {
	c := New(100, 0)
	key := Key("model", "prompt", "sys", 0.0)

	c.Put(key, "response text", "model")

	resp, ok := c.Get(key)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if resp != "response text" {
		t.Errorf("expected 'response text', got %q", resp)
	}
}

func TestGetMiss(t *testing.T) {
	c := New(100, 0)
	_, ok := c.Get("nonexistent")
	if ok {
		t.Error("expected cache miss")
	}
}

func TestEviction(t *testing.T) {
	c := New(3, 0)
	c.Put("k1", "v1", "m")
	c.Put("k2", "v2", "m")
	c.Put("k3", "v3", "m")
	c.Put("k4", "v4", "m") // should evict k1

	_, ok := c.Get("k1")
	if ok {
		t.Error("k1 should have been evicted")
	}

	_, ok = c.Get("k4")
	if !ok {
		t.Error("k4 should exist")
	}
}

func TestTTLExpiration(t *testing.T) {
	c := New(100, 50*time.Millisecond)
	key := Key("m", "p", "s", 0)
	c.Put(key, "resp", "m")

	// Should hit immediately
	_, ok := c.Get(key)
	if !ok {
		t.Fatal("expected hit before TTL")
	}

	// Wait for TTL
	time.Sleep(60 * time.Millisecond)

	_, ok = c.Get(key)
	if ok {
		t.Error("expected miss after TTL")
	}
}

func TestStats(t *testing.T) {
	c := New(100, 0)
	key := Key("m", "p", "s", 0)
	c.Put(key, "resp", "m")

	c.Get(key)          // hit
	c.Get(key)          // hit
	c.Get("nonexistent") // miss

	stats := c.Stats()
	if stats.Hits != 2 {
		t.Errorf("hits: expected 2, got %d", stats.Hits)
	}
	if stats.Misses != 1 {
		t.Errorf("misses: expected 1, got %d", stats.Misses)
	}
	if stats.Size != 1 {
		t.Errorf("size: expected 1, got %d", stats.Size)
	}
	if stats.HitRate < 66 || stats.HitRate > 67 {
		t.Errorf("hit rate: expected ~66.7%%, got %.1f%%", stats.HitRate)
	}
}

func TestClear(t *testing.T) {
	c := New(100, 0)
	c.Put("k1", "v1", "m")
	c.Put("k2", "v2", "m")
	c.Clear()

	stats := c.Stats()
	if stats.Size != 0 {
		t.Errorf("size after clear: expected 0, got %d", stats.Size)
	}
}

func TestUpdateExisting(t *testing.T) {
	c := New(100, 0)
	key := "k1"
	c.Put(key, "old", "m")
	c.Put(key, "new", "m")

	resp, _ := c.Get(key)
	if resp != "new" {
		t.Errorf("expected updated value 'new', got %q", resp)
	}

	// Size should still be 1
	if c.Stats().Size != 1 {
		t.Errorf("size should be 1 after update, got %d", c.Stats().Size)
	}
}

func BenchmarkPutGet(b *testing.B) {
	c := New(10000, 0)
	key := Key("model", "benchmark prompt text here", "system", 0.0)
	c.Put(key, "cached response", "model")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Get(key)
	}
}

func BenchmarkKey(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Key("gpt-4o-mini", "Extract entities from the following text about quantum computing and NLP", "You are a knowledge graph builder", 0.0)
	}
}
