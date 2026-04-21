// qwen3_adapter.go — adapter that turns Qwen3-Reranker (served via
// llama-server or any OpenAI-compatible completion endpoint) into the
// Cohere-compat rerank API that pkg/rerank.Client already speaks.
//
// Qwen3-Reranker is a cross-encoder: it scores a query-document pair by
// emitting a single "yes" or "no" token, and the probability of "yes"
// is the relevance score. That's different from the native Cohere
// contract (one request returns scores for every document), so we wrap
// it: one adapter call = N completion calls, one per document, with
// logprob aggregation.
//
// Wire-up:
//   - Stand the reranker up as `llama-server -m qwen3-reranker-0.6b-q8_0.gguf
//     --port 9002 --n-gpu-layers 999 --host 0.0.0.0`.
//   - Point RERANK_ENDPOINT at http://host:9002/qwen3-rerank (handled by
//     this adapter's HTTP server, see QwenRerankHTTPHandler below).
//   - Or, if you prefer in-process without an extra server, call
//     NewQwen3Client directly and swap it into APIConfig.RerankEndpoint
//     via a custom dial — but the HTTP front is easier to deploy.
//
// If a native Cohere-compat reranker (BGE, Mixedbread, etc.) is
// available, prefer that and skip this adapter — fewer moving parts.
package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Qwen3 chat template for the reranker role. The model was fine-tuned
// to respond with "yes" / "no" after being asked whether a document
// meets the query requirement. We use the logprob of the "yes" token
// (token id varies by tokeniser — we ask for top_logprobs and pick the
// "yes" lexeme from the response).
const qwen3RerankSystem = `Judge whether the Document meets the requirements based on the Query and the Instruct provided. Note that the answer can only be "yes" or "no".`

const qwen3RerankInstruct = `Given a web search query, retrieve relevant passages that answer the query.`

// Qwen3Client scores query-document pairs via a Qwen3-Reranker served
// through an OpenAI-compat /v1/chat/completions endpoint (llama-server
// satisfies this). Each Rerank call fans out to len(documents) parallel
// completions, then sorts by P("yes").
type Qwen3Client struct {
	endpoint    string // base URL, e.g. http://10.23.0.64:9002
	model       string // model name reported to the server
	topN        int
	concurrency int
	httpClient  *http.Client
}

// NewQwen3Client builds an adapter. endpoint must point at the
// llama-server root; the adapter appends /v1/chat/completions itself.
// concurrency caps how many query-document pairs fly in parallel — 4
// is a good default on a single-GPU host (reranker + embedder + LLM
// share VRAM bandwidth).
func NewQwen3Client(endpoint, model string, topN, timeoutMs, concurrency int) *Qwen3Client {
	if timeoutMs <= 0 {
		timeoutMs = 5000
	}
	if concurrency <= 0 {
		concurrency = 4
	}
	return &Qwen3Client{
		endpoint:    endpoint,
		model:       model,
		topN:        topN,
		concurrency: concurrency,
		httpClient: &http.Client{
			Timeout: time.Duration(timeoutMs) * time.Millisecond,
		},
	}
}

// Enabled mirrors Client.Enabled for callers that accept either type
// behind an interface.
func (c *Qwen3Client) Enabled() bool { return c != nil && c.endpoint != "" }

// Rerank satisfies the same contract as Client.Rerank — query +
// documents in, Results sorted by descending score out.
func (c *Qwen3Client) Rerank(ctx context.Context, query string, documents []string) ([]Result, error) {
	if !c.Enabled() {
		return nil, nil
	}
	if len(documents) == 0 {
		return []Result{}, nil
	}

	results := make([]Result, len(documents))
	sem := make(chan struct{}, c.concurrency)
	var wg sync.WaitGroup
	var firstErrMu sync.Mutex
	var firstErr error

	for i, doc := range documents {
		wg.Add(1)
		go func(idx int, text string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			score, err := c.scorePair(ctx, query, text)
			if err != nil {
				firstErrMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				firstErrMu.Unlock()
				// score stays 0 — lowest rank. Better than failing the
				// whole Rerank call when a single pair misbehaves.
				results[idx] = Result{Index: idx, Score: 0}
				return
			}
			results[idx] = Result{Index: idx, Score: score}
		}(i, doc)
	}
	wg.Wait()

	if firstErr != nil && allZero(results) {
		// Every pair errored — surface the failure. Partial errors get
		// logged via the individual zero scores but don't abort the
		// whole batch.
		return nil, fmt.Errorf("qwen3-rerank: %w", firstErr)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	topN := c.topN
	if topN > 0 && topN < len(results) {
		results = results[:topN]
	}
	return results, nil
}

func allZero(rs []Result) bool {
	for _, r := range rs {
		if r.Score != 0 {
			return false
		}
	}
	return true
}

// qwen3ChatReq is the subset of OpenAI /v1/chat/completions we need.
// logprobs + top_logprobs let us pull P("yes") without parsing the
// generated text (which would require an exact string match).
type qwen3ChatReq struct {
	Model        string           `json:"model"`
	Messages     []qwen3Message   `json:"messages"`
	MaxTokens    int              `json:"max_tokens"`
	Temperature  float32          `json:"temperature"`
	Logprobs     bool             `json:"logprobs"`
	TopLogprobs  int              `json:"top_logprobs"`
	Stream       bool             `json:"stream"`
}

type qwen3Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type qwen3ChatResp struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Logprobs *struct {
			Content []struct {
				Token       string `json:"token"`
				Logprob     float64 `json:"logprob"`
				TopLogprobs []struct {
					Token   string  `json:"token"`
					Logprob float64 `json:"logprob"`
				} `json:"top_logprobs"`
			} `json:"content"`
		} `json:"logprobs,omitempty"`
	} `json:"choices"`
}

// scorePair issues one completion and returns P("yes") ∈ [0, 1].
// Fallback when logprobs aren't supplied: parse the assistant's text and
// return 1.0 for "yes", 0.0 otherwise — less discriminating but always
// produces a usable ordering.
func (c *Qwen3Client) scorePair(ctx context.Context, query, document string) (float64, error) {
	body, err := json.Marshal(qwen3ChatReq{
		Model:       c.model,
		Messages:    qwen3Messages(query, document),
		MaxTokens:   1,
		Temperature: 0,
		Logprobs:    true,
		TopLogprobs: 5, // yes, no, maybe, Yes, YES are all plausible top tokens
		Stream:      false,
	})
	if err != nil {
		return 0, fmt.Errorf("marshal: %w", err)
	}

	url := c.endpoint
	if !endsWithPath(url, "/v1/chat/completions") {
		url = trimTrailingSlash(url) + "/v1/chat/completions"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}

	var out qwen3ChatResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("decode: %w", err)
	}
	if len(out.Choices) == 0 {
		return 0, fmt.Errorf("empty choices")
	}

	// Preferred path: logprobs tell us exactly how confident the model is.
	if out.Choices[0].Logprobs != nil && len(out.Choices[0].Logprobs.Content) > 0 {
		first := out.Choices[0].Logprobs.Content[0]
		// first.Token is the actually sampled token. If it's "yes"-ish,
		// the logprob IS the probability of yes. If not, search
		// top_logprobs for a "yes" entry and use its logprob.
		if isYes(first.Token) {
			return math.Exp(first.Logprob), nil
		}
		for _, alt := range first.TopLogprobs {
			if isYes(alt.Token) {
				return math.Exp(alt.Logprob), nil
			}
		}
		// "yes" wasn't even in top-5 → very low confidence.
		return 0.01, nil
	}

	// Fallback: text match. Binary score but stable ordering if most
	// pairs match the same way.
	if isYes(out.Choices[0].Message.Content) {
		return 1.0, nil
	}
	return 0.0, nil
}

func qwen3Messages(query, document string) []qwen3Message {
	return []qwen3Message{
		{Role: "system", Content: qwen3RerankSystem},
		{Role: "user", Content: fmt.Sprintf(
			"<Instruct>: %s\n\n<Query>: %s\n\n<Document>: %s",
			qwen3RerankInstruct, query, document,
		)},
	}
}

// isYes is intentionally lenient — "yes", "Yes", " yes", "YES" all count.
// Trailing whitespace + leading-space variants happen because tokenisers
// emit " yes" as one token in English continuation contexts.
func isYes(tok string) bool {
	t := trimSpace(tok)
	if len(t) == 0 {
		return false
	}
	switch t {
	case "yes", "Yes", "YES":
		return true
	}
	return false
}

// trimSpace + endsWithPath + trimTrailingSlash are inlined so this file
// stays a single self-contained adapter. pkg/rerank already imports
// enough — dropping one more helper was overkill.
func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n') {
		end--
	}
	return s[start:end]
}

func endsWithPath(url, suffix string) bool {
	if len(url) < len(suffix) {
		return false
	}
	return url[len(url)-len(suffix):] == suffix
}

func trimTrailingSlash(s string) string {
	if len(s) > 0 && s[len(s)-1] == '/' {
		return s[:len(s)-1]
	}
	return s
}

// QwenRerankHTTPHandler wraps a Qwen3Client so it speaks the Cohere
// rerank wire format — accept POST with {query, documents, top_n},
// return {results: [{index, relevance_score}]}. Mount this on a side
// server (or the Levara gateway) so the existing pkg/rerank.Client
// doesn't need to know anything about Qwen3.
//
// Usage:
//
//	mux := http.NewServeMux()
//	mux.Handle("/rerank", rerank.QwenRerankHTTPHandler(qwen))
//	go http.ListenAndServe(":9003", mux)
//
// Then RERANK_ENDPOINT=http://host:9003/rerank on the Levara side.
func QwenRerankHTTPHandler(q *Qwen3Client) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req rerankRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		results, err := q.Rerank(r.Context(), req.Query, req.Documents)
		if err != nil {
			http.Error(w, "rerank: "+err.Error(), http.StatusBadGateway)
			return
		}
		// Cohere format: results with index + relevance_score.
		out := struct {
			Results []map[string]any `json:"results"`
		}{}
		for _, res := range results {
			out.Results = append(out.Results, map[string]any{
				"index":           res.Index,
				"relevance_score": res.Score,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}
