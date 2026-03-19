// Package llmproxy provides an OpenAI-compatible HTTP proxy for LLM APIs
// with in-flight deduplication, response caching, and rate limiting.
//
// When Cognee cognify processes 100 chunks, many generate similar/identical
// LLM prompts (e.g. same entity extraction template). Without dedup,
// each sends a separate LLM call (5-30s each). The proxy:
//
//  1. Checks cache → instant return (0.18ms)
//  2. Dedup: if identical request in-flight → wait for first result
//  3. Forward to real LLM API → cache response → return to all waiters
//
// Result: N identical prompts → 1 LLM call instead of N.
package llmproxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stek0v/cognevra/pkg/llmcache"
)

// Config for the LLM proxy.
type Config struct {
	UpstreamURL string        // real LLM API (e.g. http://localhost:11434/v1)
	Cache       *llmcache.Cache
	MaxInFlight int           // max concurrent LLM requests (default 10)
}

// Proxy is the LLM dedup+cache proxy.
type Proxy struct {
	upstream    string
	cache       *llmcache.Cache
	client      *http.Client
	inflight    map[string]*inflightReq
	inflightMu  sync.Mutex
	sem         chan struct{} // concurrency limiter
	stats       Stats
}

type inflightReq struct {
	done     chan struct{}
	response []byte
	status   int
	err      error
}

// Stats tracks proxy metrics.
type Stats struct {
	CacheHits    atomic.Int64
	CacheMisses  atomic.Int64
	Dedups       atomic.Int64
	Forwards     atomic.Int64
	Errors       atomic.Int64
}

// New creates a proxy.
func New(cfg Config) *Proxy {
	maxInFlight := cfg.MaxInFlight
	if maxInFlight <= 0 {
		maxInFlight = 10
	}
	return &Proxy{
		upstream: cfg.UpstreamURL,
		cache:    cfg.Cache,
		client: &http.Client{
			Timeout: 120 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        32,
				MaxIdleConnsPerHost: 16,
			},
		},
		inflight: make(map[string]*inflightReq),
		sem:      make(chan struct{}, maxInFlight),
	}
}

// chatRequest is the OpenAI-compatible chat completion request.
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Stream      bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// requestKey generates a dedup key from the request body.
func requestKey(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// cacheKey generates a cache key from parsed request.
func cacheKeyFromReq(req *chatRequest) string {
	prompt := ""
	systemPrompt := ""
	for _, m := range req.Messages {
		if m.Role == "system" {
			systemPrompt += m.Content
		} else {
			prompt += m.Content
		}
	}
	return llmcache.Key(req.Model, prompt, systemPrompt, float32(req.Temperature))
}

// ServeHTTP handles proxied requests.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Health check
	if r.URL.Path == "/health" {
		s := p.GetStats()
		json.NewEncoder(w).Encode(map[string]any{
			"status":      "ok",
			"cache_hits":  s["cache_hits"],
			"dedups":      s["dedups"],
			"forwards":    s["forwards"],
			"cache_size":  p.cache.Stats().Size,
		})
		return
	}

	// Only proxy POST to chat/completions paths
	if r.Method != "POST" {
		p.forwardDirect(w, r)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), 400)
		return
	}
	r.Body.Close()

	// Parse request for cache key
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		// Not a chat request — forward as-is
		p.forwardBody(w, r, body)
		return
	}

	// Skip streaming requests (can't cache)
	if req.Stream {
		p.forwardBody(w, r, body)
		return
	}

	// Step 1: Check cache
	if p.cache != nil {
		cacheKey := cacheKeyFromReq(&req)
		if cached, hit := p.cache.Get(cacheKey); hit {
			p.stats.CacheHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-LLM-Proxy", "cache-hit")
			w.Write([]byte(cached))
			return
		}
		p.stats.CacheMisses.Add(1)
	}

	// Step 2: Dedup — check if identical request is in-flight
	dedupKey := requestKey(body)

	p.inflightMu.Lock()
	if existing, ok := p.inflight[dedupKey]; ok {
		p.inflightMu.Unlock()
		// Wait for the first request to complete
		p.stats.Dedups.Add(1)
		<-existing.done
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-LLM-Proxy", "dedup")
		if existing.err != nil {
			http.Error(w, existing.err.Error(), 502)
			return
		}
		w.WriteHeader(existing.status)
		w.Write(existing.response)
		return
	}

	// Register in-flight request
	ifr := &inflightReq{done: make(chan struct{})}
	p.inflight[dedupKey] = ifr
	p.inflightMu.Unlock()

	// Step 3: Forward to upstream (with concurrency limit)
	p.sem <- struct{}{} // acquire semaphore
	p.stats.Forwards.Add(1)

	upstreamReq, _ := http.NewRequestWithContext(r.Context(), "POST",
		p.upstream+r.URL.Path, bytes.NewReader(body))
	upstreamReq.Header.Set("Content-Type", "application/json")
	// Copy auth headers
	if auth := r.Header.Get("Authorization"); auth != "" {
		upstreamReq.Header.Set("Authorization", auth)
	}

	resp, err := p.client.Do(upstreamReq)
	<-p.sem // release semaphore

	if err != nil {
		ifr.err = err
		ifr.status = 502
		close(ifr.done)
		p.inflightMu.Lock()
		delete(p.inflight, dedupKey)
		p.inflightMu.Unlock()
		p.stats.Errors.Add(1)
		http.Error(w, "upstream: "+err.Error(), 502)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	// Step 4: Cache response (only for 200 OK)
	if resp.StatusCode == 200 && p.cache != nil {
		cacheKey := cacheKeyFromReq(&req)
		p.cache.Put(cacheKey, string(respBody), req.Model)
	}

	// Complete in-flight request (notify all waiters)
	ifr.response = respBody
	ifr.status = resp.StatusCode
	close(ifr.done)

	p.inflightMu.Lock()
	delete(p.inflight, dedupKey)
	p.inflightMu.Unlock()

	// Return to original caller
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-LLM-Proxy", "forward")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

// forwardDirect proxies non-POST requests as-is.
func (p *Proxy) forwardDirect(w http.ResponseWriter, r *http.Request) {
	upReq, _ := http.NewRequestWithContext(r.Context(), r.Method,
		p.upstream+r.URL.Path, r.Body)
	for k, v := range r.Header {
		upReq.Header[k] = v
	}
	resp, err := p.client.Do(upReq)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// forwardBody proxies with pre-read body.
func (p *Proxy) forwardBody(w http.ResponseWriter, r *http.Request, body []byte) {
	upReq, _ := http.NewRequestWithContext(r.Context(), "POST",
		p.upstream+r.URL.Path, bytes.NewReader(body))
	upReq.Header.Set("Content-Type", "application/json")
	if auth := r.Header.Get("Authorization"); auth != "" {
		upReq.Header.Set("Authorization", auth)
	}
	resp, err := p.client.Do(upReq)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	defer resp.Body.Close()
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// GetStats returns proxy statistics.
func (p *Proxy) GetStats() map[string]int64 {
	return map[string]int64{
		"cache_hits": p.stats.CacheHits.Load(),
		"cache_misses": p.stats.CacheMisses.Load(),
		"dedups": p.stats.Dedups.Load(),
		"forwards": p.stats.Forwards.Load(),
		"errors": p.stats.Errors.Load(),
	}
}

// ListenAndServe starts the proxy HTTP server.
func (p *Proxy) ListenAndServe(addr string) error {
	log.Printf("[llm-proxy] listening on %s → %s (cache=%d, max_inflight=%d)",
		addr, p.upstream, p.cache.Stats().MaxSize, cap(p.sem))
	return http.ListenAndServe(addr, p)
}

// StartBackground starts the proxy in a goroutine. Returns stop function.
func StartBackground(addr string, cfg Config) (stop func(), err error) {
	proxy := New(cfg)
	server := &http.Server{Addr: addr, Handler: proxy}
	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("[llm-proxy] error: %v", err)
		}
	}()
	log.Printf("[llm-proxy] started on %s → %s", addr, cfg.UpstreamURL)
	return func() {
		server.Close()
		log.Printf("[llm-proxy] stopped")
	}, nil
}
