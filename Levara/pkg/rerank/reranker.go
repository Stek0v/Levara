package rerank

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

// Result holds a single reranked item.
type Result struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

// Client calls an external reranker service (Cohere rerank API compatible).
// If URL is empty, Rerank is a no-op returning input order unchanged.
type Client struct {
	url        string
	model      string
	topN       int
	httpClient *http.Client
}

// NewClient creates a reranker client.
// If url is empty, Rerank() returns input order unchanged (no-op).
// timeoutMs sets the HTTP timeout; 0 defaults to 5000ms.
func NewClient(url, model string, topN, timeoutMs int) *Client {
	if timeoutMs <= 0 {
		timeoutMs = 5000
	}
	return &Client{
		url:   url,
		model: model,
		topN:  topN,
		httpClient: &http.Client{
			Timeout: time.Duration(timeoutMs) * time.Millisecond,
		},
	}
}

// Enabled returns true if the reranker is configured (url is non-empty).
func (c *Client) Enabled() bool {
	return c != nil && c.url != ""
}

// rerankRequest is the Cohere-compatible rerank API request body.
type rerankRequest struct {
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	Model     string   `json:"model,omitempty"`
	TopN      int      `json:"top_n,omitempty"`
}

// rerankResultItem supports both Cohere ("relevance_score") and alternative ("score") formats.
type rerankResultItem struct {
	Index          int      `json:"index"`
	RelevanceScore *float64 `json:"relevance_score,omitempty"`
	Score          *float64 `json:"score,omitempty"`
}

func (r rerankResultItem) getScore() float64 {
	if r.RelevanceScore != nil {
		return *r.RelevanceScore
	}
	if r.Score != nil {
		return *r.Score
	}
	return 0
}

// rerankResponse wraps the API response.
type rerankResponse struct {
	Results []rerankResultItem `json:"results"`
}

// Rerank sends query + documents to the reranker and returns results
// sorted by relevance score descending.
//
// If the client is not configured (empty URL), returns nil, nil (no-op).
// If documents is empty, returns empty slice.
//
// API contract (Cohere-compatible):
//
//	POST /rerank
//	{"query": "...", "documents": ["..."], "model": "...", "top_n": N}
//	-> {"results": [{"index": 0, "relevance_score": 0.95}, ...]}
func (c *Client) Rerank(ctx context.Context, query string, documents []string) ([]Result, error) {
	if !c.Enabled() {
		return nil, nil
	}
	if len(documents) == 0 {
		return []Result{}, nil
	}

	topN := c.topN
	if topN <= 0 || topN > len(documents) {
		topN = len(documents)
	}

	body, err := json.Marshal(rerankRequest{
		Query:     query,
		Documents: documents,
		Model:     c.model,
		TopN:      topN,
	})
	if err != nil {
		return nil, fmt.Errorf("rerank marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rerank request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rerank call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("rerank HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var rr rerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return nil, fmt.Errorf("rerank decode: %w", err)
	}

	results := make([]Result, len(rr.Results))
	for i, r := range rr.Results {
		results[i] = Result{
			Index: r.Index,
			Score: r.getScore(),
		}
	}

	// Sort by score descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results, nil
}
