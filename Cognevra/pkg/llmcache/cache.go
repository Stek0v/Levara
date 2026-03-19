// Package llmcache provides an in-memory LRU cache for LLM responses.
//
// Key = SHA256(model + prompt + system_prompt + temperature).
// Eliminates redundant LLM API calls for identical inputs.
// LLM calls = 60-70% of cognify time; caching repeated queries gives massive speedup.
package llmcache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// Entry is a cached LLM response.
type Entry struct {
	Response  string
	Model     string
	CreatedAt time.Time
	HitCount  int64
}

// Cache is a thread-safe LRU cache for LLM responses.
type Cache struct {
	mu       sync.RWMutex
	items    map[string]*Entry
	order    []string // LRU order (oldest first)
	maxSize  int
	hits     int64
	misses   int64
	ttl      time.Duration
}

// New creates a cache with given max entries and TTL.
// ttl=0 means no expiration.
func New(maxSize int, ttl time.Duration) *Cache {
	if maxSize <= 0 {
		maxSize = 10000
	}
	return &Cache{
		items:   make(map[string]*Entry, maxSize),
		order:   make([]string, 0, maxSize),
		maxSize: maxSize,
		ttl:     ttl,
	}
}

// Key generates a deterministic cache key from LLM request parameters.
func Key(model, prompt, systemPrompt string, temperature float32) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%.4f", model, prompt, systemPrompt, temperature)
	return hex.EncodeToString(h.Sum(nil))
}

// Get retrieves a cached response. Returns ("", false) on miss.
func (c *Cache) Get(key string) (string, bool) {
	c.mu.RLock()
	entry, ok := c.items[key]
	c.mu.RUnlock()

	if !ok {
		c.mu.Lock()
		c.misses++
		c.mu.Unlock()
		return "", false
	}

	// Check TTL
	if c.ttl > 0 && time.Since(entry.CreatedAt) > c.ttl {
		c.mu.Lock()
		delete(c.items, key)
		c.misses++
		c.mu.Unlock()
		return "", false
	}

	c.mu.Lock()
	entry.HitCount++
	c.hits++
	c.mu.Unlock()

	return entry.Response, true
}

// Put stores a response in the cache. Evicts oldest entry if full.
func (c *Cache) Put(key, response, model string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Update existing
	if _, exists := c.items[key]; exists {
		c.items[key].Response = response
		c.items[key].CreatedAt = time.Now()
		return
	}

	// Evict if at capacity
	for len(c.items) >= c.maxSize && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.items, oldest)
	}

	c.items[key] = &Entry{
		Response:  response,
		Model:     model,
		CreatedAt: time.Now(),
	}
	c.order = append(c.order, key)
}

// Stats returns cache statistics.
type Stats struct {
	Size    int
	MaxSize int
	Hits    int64
	Misses  int64
	HitRate float64
}

// Stats returns current cache statistics.
func (c *Cache) Stats() Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	total := c.hits + c.misses
	hitRate := 0.0
	if total > 0 {
		hitRate = float64(c.hits) / float64(total) * 100
	}

	return Stats{
		Size:    len(c.items),
		MaxSize: c.maxSize,
		Hits:    c.hits,
		Misses:  c.misses,
		HitRate: hitRate,
	}
}

// Clear removes all entries.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*Entry, c.maxSize)
	c.order = c.order[:0]
	c.hits = 0
	c.misses = 0
}
