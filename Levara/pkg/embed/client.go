package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/stek0v/levara/internal/metrics"
	"golang.org/x/sync/errgroup"
)

// Client calls an OpenAI-compatible embedding API (embed-server, Ollama, etc.).
type Client struct {
	url         string
	model       string
	batchSize   int
	concurrency int
	httpClient  *http.Client
	cache       *Cache // optional embedding cache
}

// NewClient creates an embedding client with connection pooling.
// concurrency controls how many batch HTTP requests run simultaneously.
// Pass 1 for sequential (default/test-safe), 3+ for production throughput.
func NewClient(url, model string, batchSize, concurrency int) *Client {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Client{
		url:         url,
		model:       model,
		batchSize:   batchSize,
		concurrency: concurrency,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        64,
				MaxIdleConnsPerHost: 32,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// WithCache attaches an embedding cache to the client.
func (c *Client) WithCache(cache *Cache) *Client {
	c.cache = cache
	return c
}

// embeddingRequest is the OpenAI-compatible request format.
type embeddingRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

// embeddingResponse supports both OpenAI and Ollama response formats.
type embeddingResponse struct {
	// OpenAI format
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	// Ollama /api/embed format
	Embeddings [][]float32 `json:"embeddings"`
}

// EmbedTexts embeds multiple texts, batching by batchSize.
// Returns one vector per input text, in the same order.
// Batches are dispatched concurrently up to c.concurrency in-flight at once.
// If cache is attached, cached vectors are returned without HTTP call.
func (c *Client) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	allVecs := make([][]float32, len(texts))

	// Check cache for existing vectors
	var missTexts []string
	var missIndices []int
	if c.cache != nil {
		for i, t := range texts {
			if vec, ok := c.cache.Get(t); ok {
				allVecs[i] = vec
			} else {
				missTexts = append(missTexts, t)
				missIndices = append(missIndices, i)
			}
		}
		if len(missTexts) == 0 {
			return allVecs, nil // all cached
		}
	} else {
		missTexts = texts
		missIndices = make([]int, len(texts))
		for i := range texts {
			missIndices[i] = i
		}
	}

	// Embed only misses
	missVecs := make([][]float32, len(missTexts))
	g, gctx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, c.concurrency)

	for start := 0; start < len(missTexts); start += c.batchSize {
		start := start
		end := start + c.batchSize
		if end > len(missTexts) {
			end = len(missTexts)
		}

		g.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()

			vecs, err := c.embedBatch(gctx, missTexts[start:end])
			if err != nil {
				return fmt.Errorf("batch [%d:%d]: %w", start, end, err)
			}
			for i, v := range vecs {
				missVecs[start+i] = v
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Place miss results back and cache them
	for i, idx := range missIndices {
		allVecs[idx] = missVecs[i]
	}
	if c.cache != nil {
		for i, t := range missTexts {
			if missVecs[i] != nil {
				c.cache.Put(t, missVecs[i])
			}
		}
	}

	return allVecs, nil
}

// EmbedSingle embeds a single text string.
func (c *Client) EmbedSingle(ctx context.Context, text string) ([]float32, error) {
	vecs, err := c.EmbedTexts(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("empty embedding response")
	}
	return vecs[0], nil
}

// embedBatch sends one batch to the embedding API.
func (c *Client) embedBatch(ctx context.Context, texts []string) (vecs [][]float32, err error) {
	defer metrics.ObserveExternalCall("embed", "embed", time.Now(), &err)
	reqBody, err := json.Marshal(embeddingRequest{
		Input: texts,
		Model: c.model,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	embedStart := time.Now()
	resp, err := c.httpClient.Do(req)
	embedDur := time.Since(embedStart).Seconds()
	metrics.EmbedDuration.Observe(embedDur)
	if err != nil {
		metrics.EmbedRequests.WithLabelValues(c.model, "error").Inc()
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		metrics.EmbedRequests.WithLabelValues(c.model, "error").Inc()
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embed API status %d: %s", resp.StatusCode, string(body))
	}
	metrics.EmbedRequests.WithLabelValues(c.model, "ok").Inc()

	var result embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	// Ollama /api/embed returns {"embeddings": [[...]]} instead of OpenAI's {"data": [...]}
	if len(result.Data) == 0 && len(result.Embeddings) > 0 {
		return result.Embeddings, nil
	}

	sort.Slice(result.Data, func(i, j int) bool {
		return result.Data[i].Index < result.Data[j].Index
	})

	vecs = make([][]float32, len(result.Data))
	for i, d := range result.Data {
		vecs[i] = d.Embedding
	}

	return vecs, nil
}
