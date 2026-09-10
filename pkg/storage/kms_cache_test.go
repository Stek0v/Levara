package storage

import (
	"context"
	"testing"
	"time"
)

func TestKMSCacheDefaultDisabledAndCopyIsolation(t *testing.T) {
	p, k := newKMSProtocol(t)
	ctx := context.Background()
	cache, err := NewCachedKMS(k, KMSCacheConfig{})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := cache.EncryptDataKey(ctx, EncryptDataKeyRequest{KeyRef: testKeyARN, Plaintext: make([]byte, 32), Context: objectKMSContext("x")})
	if err != nil {
		t.Fatal(err)
	}
	req := DecryptDataKeyRequest{CiphertextKeyRef: wrapped.CiphertextKeyRef, Context: objectKMSContext("x")}
	for i := 0; i < 2; i++ {
		if _, err := cache.DecryptDataKey(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	if p.count("Decrypt") != 2 || cache.Stats().Entries != 0 {
		t.Fatal("default cache retained plaintext key")
	}
	enabled, _ := NewCachedKMS(k, KMSCacheConfig{TTL: time.Minute, MaxEntries: 1, MaxBytes: 4096})
	first, err := enabled.DecryptDataKey(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	first.Plaintext[0] = 255
	second, err := enabled.DecryptDataKey(ctx, req)
	if err != nil || second.Plaintext[0] != 0 {
		t.Fatal("caller mutated cached key")
	}
	enabled.Clear()
	if enabled.Stats().Bytes != 0 {
		t.Fatal("clear retained key material")
	}
}
func TestKMSCacheExpiryDoesNotRenewOrServeStaleOnOutage(t *testing.T) {
	p, k := newKMSProtocol(t)
	ctx := context.Background()
	cache, _ := NewCachedKMS(k, KMSCacheConfig{TTL: 100 * time.Millisecond})
	wrapped, err := k.EncryptDataKey(ctx, EncryptDataKeyRequest{KeyRef: testKeyARN, Plaintext: make([]byte, 32), Context: objectKMSContext("x")})
	if err != nil {
		t.Fatal(err)
	}
	req := DecryptDataKeyRequest{CiphertextKeyRef: wrapped.CiphertextKeyRef, Context: objectKMSContext("x")}
	if _, err := cache.DecryptDataKey(ctx, req); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if _, err := cache.DecryptDataKey(ctx, req); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.fail = true
	p.mu.Unlock()
	time.Sleep(60 * time.Millisecond)
	if _, err := cache.DecryptDataKey(ctx, req); err == nil {
		t.Fatal("cache renewed TTL or served stale key during outage")
	}
	if cache.Stats().Entries != 0 {
		t.Fatal("expired key retained")
	}
}
func TestKMSCacheBoundsAndWritesNeverReuseDEK(t *testing.T) {
	p, k := newKMSProtocol(t)
	ctx := context.Background()
	cache, _ := NewCachedKMS(k, KMSCacheConfig{TTL: time.Minute, MaxEntries: 2, MaxBytes: 4096})
	for _, key := range []string{"a", "b", "c", "d"} {
		r, err := cache.EncryptDataKey(ctx, EncryptDataKeyRequest{KeyRef: testKeyARN, Plaintext: make([]byte, 32), Context: objectKMSContext(key)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cache.DecryptDataKey(ctx, DecryptDataKeyRequest{CiphertextKeyRef: r.CiphertextKeyRef, Context: objectKMSContext(key)}); err != nil {
			t.Fatal(err)
		}
	}
	stats := cache.Stats()
	if stats.Entries > 2 || stats.Bytes > 4096 || p.count("Encrypt") != 4 {
		t.Fatalf("bounds/writes %+v", stats)
	}
}

func TestKMSCacheExpiresIdleKeyMaterial(t *testing.T) {
	_, k := newKMSProtocol(t)
	ctx := context.Background()
	cache, _ := NewCachedKMS(k, KMSCacheConfig{TTL: 20 * time.Millisecond})
	wrapped, err := k.EncryptDataKey(ctx, EncryptDataKeyRequest{KeyRef: testKeyARN, Plaintext: make([]byte, 32), Context: objectKMSContext("x")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.DecryptDataKey(ctx, DecryptDataKeyRequest{CiphertextKeyRef: wrapped.CiphertextKeyRef, Context: objectKMSContext("x")}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	cache.mu.Lock()
	entries := len(cache.entries)
	cache.mu.Unlock()
	if entries != 0 {
		t.Fatal("expired plaintext key remains cached without a subsequent request")
	}
}
