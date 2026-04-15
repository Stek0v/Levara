package llmproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stek0v/cognevra/pkg/llmcache"
)

// T-9 smoke tests for pkg/llmproxy.
// Upstream is a httptest.Server; assertions cover the three documented paths:
// cache-hit, dedup, forward. Dedup test uses a latch to synchronize two
// concurrent identical requests so upstream call count is deterministic.

// chatBody builds a minimal OpenAI chat completion request body.
func chatBody(model, prompt string) []byte {
	b, _ := json.Marshal(chatRequest{
		Model:    model,
		Messages: []chatMessage{{Role: "user", Content: prompt}},
	})
	return b
}

// chatResponse synthesises a minimal OpenAI chat-completion response.
func chatResponse(content string) string {
	return fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":%q}}]}`, content)
}

func TestProxy_Forward_CountsHitsAndPopulatesCache(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		io.WriteString(w, chatResponse("pong"))
	}))
	defer upstream.Close()

	cache := llmcache.New(100, 60*time.Second)
	p := New(Config{UpstreamURL: upstream.URL, Cache: cache})
	srv := httptest.NewServer(p)
	defer srv.Close()

	body := chatBody("m1", "ping")

	// First call → forward, populate cache
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("X-LLM-Proxy") != "forward" {
		t.Errorf("first call: X-LLM-Proxy = %q, want 'forward'", resp.Header.Get("X-LLM-Proxy"))
	}
	if upstreamCalls.Load() != 1 {
		t.Errorf("upstreamCalls after forward = %d, want 1", upstreamCalls.Load())
	}

	// Second identical call → cache hit
	resp2, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.Header.Get("X-LLM-Proxy") != "cache-hit" {
		t.Errorf("second call: X-LLM-Proxy = %q, want 'cache-hit'", resp2.Header.Get("X-LLM-Proxy"))
	}
	if upstreamCalls.Load() != 1 {
		t.Errorf("upstreamCalls after cache hit = %d, want still 1", upstreamCalls.Load())
	}

	stats := p.GetStats()
	if stats["cache_hits"] != 1 || stats["forwards"] != 1 {
		t.Errorf("stats = %+v; want cache_hits=1 forwards=1", stats)
	}
}

func TestProxy_Dedup_CoalescesConcurrentIdenticalRequests(t *testing.T) {
	// Upstream blocks on release until the test signals it, so we can be sure
	// both requests are in-flight BEFORE the first one completes. Only then
	// can dedup be observed — once the first finishes, the cache would kick in.
	release := make(chan struct{})
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		<-release
		io.WriteString(w, chatResponse("shared"))
	}))
	defer upstream.Close()

	// NO cache — otherwise the dedup path would be masked by a cache hit on
	// retry. cacheMu=nil is fine here; proxy handles nil cache.
	p := New(Config{UpstreamURL: upstream.URL})
	srv := httptest.NewServer(p)
	defer srv.Close()

	body := chatBody("m1", "race")

	var wg sync.WaitGroup
	headers := make([]string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Errorf("req %d: %v", idx, err)
				return
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			headers[idx] = resp.Header.Get("X-LLM-Proxy")
		}(i)
	}

	// Give both goroutines time to enter inflight map.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := upstreamCalls.Load(); got != 1 {
		t.Errorf("upstreamCalls = %d, want 1 (dedup should coalesce)", got)
	}

	// Exactly one request should be labelled 'forward' and one 'dedup'.
	var forwards, dedups int
	for _, h := range headers {
		switch h {
		case "forward":
			forwards++
		case "dedup":
			dedups++
		}
	}
	if forwards != 1 || dedups != 1 {
		t.Errorf("headers = %v; want one forward + one dedup", headers)
	}
}

func TestProxy_StreamingBypassesCache(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		io.WriteString(w, "stream chunk 1\n")
	}))
	defer upstream.Close()

	cache := llmcache.New(100, 60*time.Second)
	p := New(Config{UpstreamURL: upstream.URL, Cache: cache})
	srv := httptest.NewServer(p)
	defer srv.Close()

	body, _ := json.Marshal(chatRequest{
		Model:    "m1",
		Messages: []chatMessage{{Role: "user", Content: "stream me"}},
		Stream:   true,
	})

	// Two calls — both must go upstream because Stream=true skips cache entirely.
	for i := 0; i < 2; i++ {
		resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if got := upstreamCalls.Load(); got != 2 {
		t.Errorf("upstreamCalls = %d, want 2 (stream must bypass cache)", got)
	}
	if got := p.GetStats()["cache_hits"]; got != 0 {
		t.Errorf("cache_hits = %d, want 0", got)
	}
}

func TestProxy_Health(t *testing.T) {
	cache := llmcache.New(10, time.Minute)
	p := New(Config{UpstreamURL: "http://unused", Cache: cache})
	srv := httptest.NewServer(p)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Errorf("health body = %s", body)
	}
}

func TestRequestKey_StableForIdenticalBodies(t *testing.T) {
	a := requestKey([]byte(`{"a":1}`))
	b := requestKey([]byte(`{"a":1}`))
	c := requestKey([]byte(`{"a":2}`))
	if a != b {
		t.Errorf("identical bodies should hash equal")
	}
	if a == c {
		t.Errorf("different bodies should hash differently")
	}
}

func TestCacheKeyFromReq_SeparatesSystemAndUser(t *testing.T) {
	// Messages with the same concatenated text but different role assignments
	// MUST yield different cache keys — otherwise a cached system-prompt
	// reply could leak across sessions.
	k1 := cacheKeyFromReq(&chatRequest{
		Model: "m",
		Messages: []chatMessage{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "hello"},
		},
	})
	k2 := cacheKeyFromReq(&chatRequest{
		Model: "m",
		Messages: []chatMessage{
			{Role: "user", Content: "syshello"},
		},
	})
	if k1 == k2 {
		t.Error("system vs user prompts must not share cache key")
	}
}
