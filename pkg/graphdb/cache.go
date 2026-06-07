package graphdb

import (
	"sync"
	"time"
)

// CachedWriter wraps Writer with an in-memory graph cache.
// GraphRead results are cached by mode+params key.
// Cache is invalidated on any write operation (BatchWrite).
type CachedWriter struct {
	*Writer
	mu      sync.RWMutex
	cache   map[string]*cacheEntry
	maxSize int
	ttl     time.Duration
	hits    int64
	misses  int64
}

type cacheEntry struct {
	result    GraphReadResult
	createdAt time.Time
}

// NewCachedWriter wraps an existing Writer with caching.
// maxSize: max cached queries (0 = 1000). ttl: cache duration (0 = 5min).
func NewCachedWriter(w *Writer, maxSize int, ttl time.Duration) *CachedWriter {
	if maxSize <= 0 {
		maxSize = 1000
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &CachedWriter{
		Writer:  w,
		cache:   make(map[string]*cacheEntry, maxSize),
		maxSize: maxSize,
		ttl:     ttl,
	}
}

// CacheStats returns hit/miss stats.
type CacheStats struct {
	Size    int
	Hits    int64
	Misses  int64
	HitRate float64
}

func (cw *CachedWriter) CacheStats() CacheStats {
	cw.mu.RLock()
	defer cw.mu.RUnlock()
	total := cw.hits + cw.misses
	rate := 0.0
	if total > 0 {
		rate = float64(cw.hits) / float64(total) * 100
	}
	return CacheStats{Size: len(cw.cache), Hits: cw.hits, Misses: cw.misses, HitRate: rate}
}

// Invalidate clears the entire cache (called after writes).
func (cw *CachedWriter) Invalidate() {
	cw.mu.Lock()
	cw.cache = make(map[string]*cacheEntry, cw.maxSize)
	cw.mu.Unlock()
}

// BatchWrite writes and invalidates cache.
func (cw *CachedWriter) BatchWrite(ctx interface{ Deadline() (time.Time, bool); Done() <-chan struct{}; Err() error; Value(any) any }, nodes []NodeRecord, edges []EdgeRecord) BatchWriteResult {
	result := cw.Writer.BatchWrite(ctx, nodes, edges)
	cw.Invalidate()
	return result
}

func (cw *CachedWriter) getCached(key string) (GraphReadResult, bool) {
	cw.mu.RLock()
	entry, ok := cw.cache[key]
	cw.mu.RUnlock()

	if !ok {
		cw.mu.Lock()
		cw.misses++
		cw.mu.Unlock()
		return GraphReadResult{}, false
	}

	if time.Since(entry.createdAt) > cw.ttl {
		cw.mu.Lock()
		delete(cw.cache, key)
		cw.misses++
		cw.mu.Unlock()
		return GraphReadResult{}, false
	}

	cw.mu.Lock()
	cw.hits++
	cw.mu.Unlock()
	return entry.result, true
}

func (cw *CachedWriter) putCache(key string, result GraphReadResult) {
	cw.mu.Lock()
	defer cw.mu.Unlock()

	// Evict if full
	if len(cw.cache) >= cw.maxSize {
		// Remove oldest
		var oldestKey string
		var oldestTime time.Time
		for k, v := range cw.cache {
			if oldestKey == "" || v.createdAt.Before(oldestTime) {
				oldestKey = k
				oldestTime = v.createdAt
			}
		}
		if oldestKey != "" {
			delete(cw.cache, oldestKey)
		}
	}

	cw.cache[key] = &cacheEntry{result: result, createdAt: time.Now()}
}

// ReadFullGraph with cache.
func (cw *CachedWriter) ReadFullGraph(ctx interface{ Deadline() (time.Time, bool); Done() <-chan struct{}; Err() error; Value(any) any }) (GraphReadResult, error) {
	key := "full"
	if cached, ok := cw.getCached(key); ok {
		return cached, nil
	}
	result, err := cw.Writer.ReadFullGraph(ctx)
	if err == nil {
		cw.putCache(key, result)
	}
	return result, err
}

// ReadIDFiltered with cache.
func (cw *CachedWriter) ReadIDFiltered(ctx interface{ Deadline() (time.Time, bool); Done() <-chan struct{}; Err() error; Value(any) any }, ids []string) (GraphReadResult, error) {
	key := "ids:" + joinSorted(ids)
	if cached, ok := cw.getCached(key); ok {
		return cached, nil
	}
	result, err := cw.Writer.ReadIDFiltered(ctx, ids)
	if err == nil {
		cw.putCache(key, result)
	}
	return result, err
}

// ReadNeighbours with cache.
func (cw *CachedWriter) ReadNeighbours(ctx interface{ Deadline() (time.Time, bool); Done() <-chan struct{}; Err() error; Value(any) any }, nodeID string) (GraphReadResult, error) {
	key := "nbr:" + nodeID
	if cached, ok := cw.getCached(key); ok {
		return cached, nil
	}
	result, err := cw.Writer.ReadNeighbours(ctx, nodeID)
	if err == nil {
		cw.putCache(key, result)
	}
	return result, err
}

// ReadSubgraph with cache.
func (cw *CachedWriter) ReadSubgraph(ctx interface{ Deadline() (time.Time, bool); Done() <-chan struct{}; Err() error; Value(any) any }, label string, names []string) (GraphReadResult, error) {
	key := "sub:" + label + ":" + joinSorted(names)
	if cached, ok := cw.getCached(key); ok {
		return cached, nil
	}
	result, err := cw.Writer.ReadSubgraph(ctx, label, names)
	if err == nil {
		cw.putCache(key, result)
	}
	return result, err
}

func joinSorted(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	// Simple join — no need to sort for cache key uniqueness in practice
	result := ss[0]
	for _, s := range ss[1:] {
		result += "," + s
	}
	return result
}
