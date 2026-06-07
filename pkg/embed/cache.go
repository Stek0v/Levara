package embed

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"sync"
)

// Cache stores text→vector mappings to avoid redundant embedding calls.
// Thread-safe LRU with optional disk persistence.
type Cache struct {
	mu      sync.RWMutex
	items   map[string][]float32 // SHA256(text) → vector
	order   []string
	maxSize int
	hits    int64
	misses  int64
	// Persistence
	file *os.File
}

type diskVec struct {
	Key    string    `json:"k"`
	Vector []float32 `json:"v"`
}

// NewCache creates an in-memory embedding cache.
func NewCache(maxSize int) *Cache {
	if maxSize <= 0 {
		maxSize = 50000
	}
	return &Cache{
		items:   make(map[string][]float32, maxSize),
		order:   make([]string, 0, maxSize),
		maxSize: maxSize,
	}
}

// NewPersistentCache creates a cache that loads from and appends to disk.
func NewPersistentCache(maxSize int, path string) (*Cache, error) {
	c := NewCache(maxSize)

	// Load existing
	if f, err := os.Open(path); err == nil {
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
		loaded := 0
		for scanner.Scan() {
			var entry diskVec
			if json.Unmarshal(scanner.Bytes(), &entry) == nil && len(entry.Vector) > 0 {
				c.items[entry.Key] = entry.Vector
				c.order = append(c.order, entry.Key)
				loaded++
			}
		}
		f.Close()
		if loaded > 0 {
			log.Printf("[embed-cache] loaded %d vectors from %s", loaded, path)
		}
	}

	// Open for append
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return c, err // return cache without persistence
	}
	c.file = f
	return c, nil
}

// TextKey generates cache key from text.
func TextKey(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:16]) // 128-bit, enough for dedup
}

// Get retrieves a cached vector. Returns nil, false on miss.
func (c *Cache) Get(text string) ([]float32, bool) {
	key := TextKey(text)
	c.mu.RLock()
	vec, ok := c.items[key]
	c.mu.RUnlock()
	if ok {
		c.mu.Lock()
		c.hits++
		c.mu.Unlock()
		return vec, true
	}
	c.mu.Lock()
	c.misses++
	c.mu.Unlock()
	return nil, false
}

// Put stores a text→vector mapping.
func (c *Cache) Put(text string, vector []float32) {
	key := TextKey(text)
	c.mu.Lock()

	if _, exists := c.items[key]; !exists {
		// Evict if full
		for len(c.items) >= c.maxSize && len(c.order) > 0 {
			delete(c.items, c.order[0])
			c.order = c.order[1:]
		}
		c.order = append(c.order, key)
	}
	c.items[key] = vector
	c.mu.Unlock()

	// Persist
	if c.file != nil {
		data, _ := json.Marshal(diskVec{Key: key, Vector: vector})
		c.mu.Lock()
		c.file.Write(data)
		c.file.Write([]byte("\n"))
		c.mu.Unlock()
	}
}

// GetMulti returns cached vectors for multiple texts.
// Returns vectors (nil for misses) and indices of misses.
func (c *Cache) GetMulti(texts []string) (vectors [][]float32, missIndices []int) {
	vectors = make([][]float32, len(texts))
	for i, t := range texts {
		if vec, ok := c.Get(t); ok {
			vectors[i] = vec
		} else {
			missIndices = append(missIndices, i)
		}
	}
	return
}

// PutMulti stores multiple text→vector pairs.
func (c *Cache) PutMulti(texts []string, vecs [][]float32) {
	for i, t := range texts {
		if i < len(vecs) && vecs[i] != nil {
			c.Put(t, vecs[i])
		}
	}
}

// Stats returns cache statistics.
func (c *Cache) Stats() (size int, hits, misses int64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items), c.hits, c.misses
}

// Close the persistence file.
func (c *Cache) Close() error {
	if c.file != nil {
		return c.file.Close()
	}
	return nil
}
