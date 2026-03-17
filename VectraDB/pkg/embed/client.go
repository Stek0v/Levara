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
)

// Client calls an OpenAI-compatible embedding API (embed-server, Ollama, etc.).
type Client struct {
	url        string
	model      string
	batchSize  int
	httpClient *http.Client
}

// NewClient creates an embedding client with connection pooling.
func NewClient(url, model string, batchSize int) *Client {
	return &Client{
		url:       url,
		model:     model,
		batchSize: batchSize,
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

// embeddingRequest is the OpenAI-compatible request format.
type embeddingRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

// embeddingResponse is the OpenAI-compatible response format.
type embeddingResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// EmbedTexts embeds multiple texts, batching by batchSize.
// Returns one vector per input text, in the same order.
func (c *Client) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	allVecs := make([][]float32, len(texts))

	for start := 0; start < len(texts); start += c.batchSize {
		end := start + c.batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[start:end]

		vecs, err := c.embedBatch(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("batch [%d:%d]: %w", start, end, err)
		}

		for i, v := range vecs {
			allVecs[start+i] = v
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
func (c *Client) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
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

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embed API status %d: %s", resp.StatusCode, string(body))
	}

	var result embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	// Sort by index to ensure correct order
	sort.Slice(result.Data, func(i, j int) bool {
		return result.Data[i].Index < result.Data[j].Index
	})

	vecs := make([][]float32, len(result.Data))
	for i, d := range result.Data {
		vecs[i] = d.Embedding
	}

	return vecs, nil
}
