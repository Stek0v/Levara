// Package decisions is the client for the optional FRIDA-Decisions sidecar
// (deploy/decisions/app.py): a small encoder that answers one yes/no
// ("noul") question about a pair of texts in a single forward pass.
//
// Gate benchmarks on prod data: benchmark/frida_gate — supersession AUC 0.929,
// duplicate AUC 0.990, zero false positives at the calibrated thresholds.
// Deployment rule: the sidecar is CPU-only; sharing MPS with the live embed
// server starves prod recall.
package decisions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	// supersedesInstruction is the calibrated gate prompt (benchmark/frida_gate,
	// supersession set: AUC 0.929, best-F1 0.844 @ threshold 0.05).
	supersedesInstruction = "Определи, является ли запись B обновлением того же факта, что и запись A " +
		"(та же суть, изменённые детали, дата или статус), либо записи про разные факты."
	sameFactInstruction = "Являются ли записи A и B дубликатами одного и того же факта " +
		"(возможно, перефразированного или на другом языке)?"
	pairStateFmt = "ЗАПИСЬ A:\n%s\n\nЗАПИСЬ B:\n%s"

	// DefaultTimeout bounds one sidecar call. The sidecar answers in
	// 0.3–0.6 s on M2-class CPU; the margin absorbs a cold tokenizer.
	DefaultTimeout = 15 * time.Second
)

// Client talks to the decisions sidecar /judge endpoint. Nil-safe: a nil
// *Client (or New("")) is a disabled gate — Enabled reports false.
type Client struct {
	endpoint string
	hc       *http.Client
}

// New builds a client for the sidecar base URL (e.g. http://127.0.0.1:9200).
// Returns nil for an empty endpoint, so callers can wire it unconditionally.
func New(endpoint string, timeoutMs int) *Client {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil
	}
	timeout := DefaultTimeout
	if timeoutMs > 0 {
		timeout = time.Duration(timeoutMs) * time.Millisecond
	}
	return &Client{
		endpoint: strings.TrimRight(endpoint, "/"),
		hc:       &http.Client{Timeout: timeout},
	}
}

// Enabled reports whether the gate has an endpoint to talk to.
func (c *Client) Enabled() bool { return c != nil && c.endpoint != "" }

// Supersedes returns P(newVal is an updated version of oldVal) — the
// consolidation fact gate: a mechanical merge or a synthesized abstraction
// may supersede oldVal only when this probability clears the threshold.
func (c *Client) Supersedes(ctx context.Context, oldVal, newVal string) (float64, error) {
	return c.noul(ctx, fmt.Sprintf(pairStateFmt, oldVal, newVal), supersedesInstruction)
}

// SameFact returns P(a and b are duplicates of one and the same fact).
func (c *Client) SameFact(ctx context.Context, a, b string) (float64, error) {
	return c.noul(ctx, fmt.Sprintf(pairStateFmt, a, b), sameFactInstruction)
}

func (c *Client) noul(ctx context.Context, state, instructions string) (float64, error) {
	if !c.Enabled() {
		return 0, errors.New("decisions: client not configured")
	}
	body, err := json.Marshal(map[string]any{
		"state": state,
		"questions": map[string]any{
			"q": map[string]any{"type": "noul", "instructions": instructions},
		},
	})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/judge", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("decisions: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("decisions: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Answers map[string]struct {
			Noul float64 `json:"noul"`
		} `json:"answers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("decisions: decode: %w", err)
	}
	ans, ok := out.Answers["q"]
	if !ok {
		return 0, errors.New("decisions: missing answer 'q'")
	}
	return ans.Noul, nil
}
