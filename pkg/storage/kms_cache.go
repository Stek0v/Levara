package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// KMSCacheConfig permits a short, explicit revocation delay. TTL=0 (default)
// disables plaintext-key caching. A cache hit never renews its original TTL.
type KMSCacheConfig struct {
	TTL        time.Duration
	MaxEntries int
	MaxBytes   int
}
type KMSCacheStats struct {
	Entries, Bytes int
	Hits, Misses   uint64
}
type kmsCacheEntry struct {
	key     []byte
	ref     string
	expires time.Time
	access  uint64
	timer   *time.Timer
}
type CachedKMS struct {
	inner                  KMS
	cfg                    KMSCacheConfig
	mu                     sync.Mutex
	entries                map[[32]byte]kmsCacheEntry
	bytes                  int
	sequence, hits, misses uint64
	generation             uint64
}

func NewCachedKMS(inner KMS, cfg KMSCacheConfig) (*CachedKMS, error) {
	if inner == nil {
		return nil, errors.New("kms cache: KMS required")
	}
	var err error
	cfg, err = cfg.normalized()
	if err != nil {
		return nil, err
	}
	return &CachedKMS{inner: inner, cfg: cfg, entries: make(map[[32]byte]kmsCacheEntry)}, nil
}

func (cfg KMSCacheConfig) Validate() error { _, err := cfg.normalized(); return err }
func (cfg KMSCacheConfig) normalized() (KMSCacheConfig, error) {
	if cfg.MaxEntries == 0 {
		cfg.MaxEntries = 64
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = 2048
	}
	if cfg.TTL < 0 || cfg.TTL > 5*time.Minute || cfg.MaxEntries < 1 || cfg.MaxEntries > 4096 || cfg.MaxBytes < 32 || cfg.MaxBytes > 128<<10 {
		return cfg, errors.New("kms cache: invalid TTL/count/byte limit")
	}
	return cfg, nil
}
func (c *CachedKMS) drop(key [32]byte) {
	e := c.entries[key]
	if e.timer != nil {
		e.timer.Stop()
	}
	clear(e.key)
	c.bytes -= len(e.key) + len(e.ref) + sha256.Size
	delete(c.entries, key)
}
func (c *CachedKMS) expire(now time.Time) {
	for key, e := range c.entries {
		if !now.Before(e.expires) {
			c.drop(key)
		}
	}
}
func (c *CachedKMS) Stats() KMSCacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire(time.Now())
	return KMSCacheStats{len(c.entries), c.bytes, c.hits, c.misses}
}
func (c *CachedKMS) Clear() {
	c.mu.Lock()
	c.generation++
	defer c.mu.Unlock()
	for key := range c.entries {
		c.drop(key)
	}
}
func (c *CachedKMS) EncryptDataKey(ctx context.Context, r EncryptDataKeyRequest) (EncryptDataKeyResponse, error) {
	return c.inner.EncryptDataKey(ctx, r)
}
func (c *CachedKMS) KeyMetadata(ctx context.Context, key string) (KeyMetadata, error) {
	return c.inner.KeyMetadata(ctx, key)
}
func (c *CachedKMS) RotateKeyRef(ctx context.Context, r RotateKeyRefRequest) (RotateKeyRefResponse, error) {
	return c.inner.RotateKeyRef(ctx, r)
}
func (c *CachedKMS) DecryptDataKey(ctx context.Context, r DecryptDataKeyRequest) (DecryptDataKeyResponse, error) {
	if err := ctx.Err(); err != nil {
		return DecryptDataKeyResponse{}, err
	}
	if len(r.CiphertextKeyRef) > 16<<10 || !validKMSContext(r.Context) {
		return DecryptDataKeyResponse{}, errors.New("kms cache: invalid key/context")
	}
	data, _ := json.Marshal(struct {
		Ref     string
		Context map[string]string
	}{r.CiphertextKeyRef, r.Context})
	key := sha256.Sum256(data)
	c.mu.Lock()
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return DecryptDataKeyResponse{}, err
	}
	c.expire(time.Now())
	if e, ok := c.entries[key]; ok && c.cfg.TTL > 0 {
		c.sequence++
		e.access = c.sequence
		c.entries[key] = e
		c.hits++
		result := DecryptDataKeyResponse{Plaintext: append([]byte(nil), e.key...), KeyRef: e.ref}
		c.mu.Unlock()
		return result, nil
	}
	c.misses++
	generation := c.generation
	c.mu.Unlock()
	result, err := c.inner.DecryptDataKey(ctx, r)
	if err != nil {
		return DecryptDataKeyResponse{}, err
	}
	if err = ctx.Err(); err != nil {
		clear(result.Plaintext)
		return DecryptDataKeyResponse{}, err
	}
	if len(result.Plaintext) != 32 || !validKeyARN(result.KeyRef) {
		clear(result.Plaintext)
		return DecryptDataKeyResponse{}, errors.New("kms cache: invalid decrypted key")
	}
	if c.cfg.TTL == 0 {
		return result, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire(time.Now())
	if generation != c.generation {
		return result, nil
	}
	// Concurrent misses may have already populated this key. Keep the first
	// expiry, rather than extending revocation latency on the later response.
	if _, exists := c.entries[key]; exists {
		return result, nil
	}
	entryBytes := len(result.Plaintext) + len(result.KeyRef) + sha256.Size
	if entryBytes > c.cfg.MaxBytes {
		return result, nil
	}
	for len(c.entries) >= c.cfg.MaxEntries || c.bytes+entryBytes > c.cfg.MaxBytes {
		var oldest [32]byte
		first := true
		var access uint64
		for k, e := range c.entries {
			if first || e.access < access {
				oldest = k
				access = e.access
				first = false
			}
		}
		c.drop(oldest)
	}
	c.sequence++
	entry := kmsCacheEntry{key: append([]byte(nil), result.Plaintext...), ref: result.KeyRef, expires: time.Now().Add(c.cfg.TTL), access: c.sequence}
	entry.timer = time.AfterFunc(c.cfg.TTL, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if current, ok := c.entries[key]; ok && !time.Now().Before(current.expires) {
			c.drop(key)
		}
	})
	c.entries[key] = entry
	c.bytes += entryBytes
	return result, nil
}
