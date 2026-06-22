// Package structuredextract calls a schema-driven document extraction sidecar
// and converts successful JSON extractions into RAG-indexable text.
package structuredextract

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Client calls a lift-compatible structured extraction sidecar.
type Client struct {
	Endpoint string
	HTTP     *http.Client
	Timeout  time.Duration
}

// Request is the JSON contract Levara sends to the sidecar.
type Request struct {
	Filename      string   `json:"filename"`
	ContentBase64 string   `json:"content_base64"`
	Schema        string   `json:"schema,omitempty"`
	PageRange     []int    `json:"page_range,omitempty"`
	Tags          []string `json:"tags,omitempty"`
}

// Result is the normalized sidecar response.
type Result struct {
	Extraction json.RawMessage `json:"extraction,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
	Raw        string          `json:"raw,omitempty"`
	Error      bool            `json:"error"`
	TokenCount int             `json:"token_count,omitempty"`
	Pages      int             `json:"pages,omitempty"`
}

// Extract sends a file and schema to the configured sidecar.
func (c Client) Extract(ctx context.Context, data []byte, filename, schema string, pageRange []int) (Result, error) {
	if strings.TrimSpace(c.Endpoint) == "" {
		return Result{}, fmt.Errorf("structured extraction endpoint is not configured")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}

	reqBody := Request{
		Filename:      filename,
		ContentBase64: base64.StdEncoding.EncodeToString(data),
		Schema:        schema,
		PageRange:     pageRange,
	}
	rawBody, err := json.Marshal(reqBody)
	if err != nil {
		return Result{}, fmt.Errorf("marshal structured extraction request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.Endpoint, bytes.NewReader(rawBody))
	if err != nil {
		return Result{}, fmt.Errorf("create structured extraction request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("structured extraction call: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("structured extraction status %d: %s", resp.StatusCode, truncate(string(respBody), 300))
	}

	var out Result
	if err := json.Unmarshal(respBody, &out); err != nil {
		return Result{}, fmt.Errorf("decode structured extraction response: %w", err)
	}
	if len(out.Extraction) == 0 || string(out.Extraction) == "null" {
		out.Error = true
	}
	return out, nil
}

// ProjectionMarkdown turns a structured JSON object into stable markdown for
// the existing text ingest/search path.
func ProjectionMarkdown(title string, extraction json.RawMessage) string {
	var v any
	if err := json.Unmarshal(extraction, &v); err != nil {
		return ""
	}
	var b strings.Builder
	if strings.TrimSpace(title) != "" {
		b.WriteString("# Structured extraction: ")
		b.WriteString(title)
		b.WriteString("\n\n")
	}
	writeValue(&b, "", v, 0)
	return strings.TrimSpace(b.String())
}

func writeValue(b *strings.Builder, key string, v any, depth int) {
	switch x := v.(type) {
	case map[string]any:
		if key != "" {
			b.WriteString(strings.Repeat("#", min(depth+2, 6)))
			b.WriteString(" ")
			b.WriteString(key)
			b.WriteString("\n\n")
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			writeValue(b, k, x[k], depth+1)
		}
	case []any:
		if key != "" {
			b.WriteString(strings.Repeat("#", min(depth+2, 6)))
			b.WriteString(" ")
			b.WriteString(key)
			b.WriteString("\n\n")
		}
		for i, item := range x {
			switch item.(type) {
			case map[string]any, []any:
				b.WriteString("- item ")
				fmt.Fprint(b, i+1)
				b.WriteString("\n")
				writeValue(b, "", item, depth+1)
			default:
				b.WriteString("- ")
				b.WriteString(formatScalar(item))
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	default:
		if key != "" {
			b.WriteString("- ")
			b.WriteString(key)
			b.WriteString(": ")
			b.WriteString(formatScalar(x))
			b.WriteString("\n")
		}
	}
}

func formatScalar(v any) string {
	if v == nil {
		return "null"
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return fmt.Sprintf("%v", x)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		raw, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprint(x)
		}
		return string(raw)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
