// Command cascade_probe measures the consolidate repair ladder (composite
// tokenizer → feedback retries → number ledger) against the REAL prod clusters,
// calling the REAL consolidate.AbstractValue. The summarizer is granite4.2:3b
// via Ollama's native /api/chat with think:false — the wireable fix path for
// thinking-default models (prod's OpenAIProvider path cannot disable thinking
// yet; that is a separate owner decision).
//
// Run from benchmark/frida_gate/ after a build:
//
//	go run ./cascade_probe
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/stek0v/levara/pkg/consolidate"
)

const (
	ollamaURL = "http://127.0.0.1:11434"
	model     = "granite4.2:3b"
)

var clusters = map[string][]string{
	"ub-main-1":  {"6675cfb3-30e6-4e63-acd9-a1bc44ed7ddd", "80cf736d-4066-48d3-a4d5-03e8baf68b3b"},
	"ub-main-2":  {"1980ae5c-0dbb-4f5c-b473-70753dc43b7c", "95174cba-fde3-49f5-99eb-481be976ff5d"},
	"unreal-1":   {"1a50089e-805a-44f8-8000-59aa207f13af", "4a5eaacb-d14a-4c8d-a8d2-cf665acdc6dc", "52aa6c76-2064-4aae-ade8-45e95a6bb1d4"},
	"unreal-2":   {"204bda53-87f0-433c-aca2-ad32add53661", "9181a6fc-6698-4e04-b5e9-718504fea571"},
	"unreal-3":   {"56d984b0-c7fa-4613-bb6b-1fbfae41c1d8", "eab47ff2-21d1-4133-bb6b-1fbfae41c1d8"},
	"unreal-4":   {"0d6a6242-2ec5-448a-8328-6587121fdd79", "20b0c74b-e687-487b-aa48-4f80258a9b45"},
	"labirint-1": {"8559384b-321c-47df-91e6-632130cff57b", "c593fd69-4669-4e9d-939f-39cc97ae272f", "cb2fc320-fa03-4a85-ab21-37e56be6f9d3"},
	"labirint-2": {"715d124e-4701-4c5f-88e4-538816fa8164", "7afe173a-99d4-45ab-8524-783719360367"},
}

func loadSources() map[string]string {
	out := map[string]string{}
	var cur string
	var buf []string
	flush := func() {
		if cur != "" {
			out[cur] = strings.Join(buf, "\n")
		}
	}
	data, err := os.ReadFile("data/cluster_sources.tsv")
	if err != nil {
		panic(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if len(line) >= 36 && isUUID(line[:36]) {
			flush()
			cur = line[:36]
			buf = []string{strings.TrimPrefix(line[36:], "\x01")}
			continue
		}
		if cur != "" {
			buf = append(buf, line)
		}
	}
	flush()
	return out
}

func isUUID(s string) bool {
	for i, r := range s {
		switch r {
		case '-':
			if i != 8 && i != 13 && i != 18 && i != 23 {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
				return false
			}
		}
	}
	return len(s) == 36
}

// graniteSummarizer talks to Ollama natively so thinking can be disabled —
// exactly the request shape a prod native-API provider would send.
type graniteSummarizer struct {
	http    *http.Client
	calls   int
	lastErr error
}

func prompt(sources []string, violations string) string {
	var b strings.Builder
	b.WriteString("Combine the following memory notes into ONE concise statement. ")
	b.WriteString("Preserve every fact, number, name, and port exactly. ")
	b.WriteString("Do NOT add any information not present below. Notes:\n")
	for _, s := range sources {
		b.WriteString("- ")
		b.WriteString(s)
		b.WriteString("\n")
	}
	if violations != "" {
		b.WriteString("\nYour previous draft was rejected by the coverage guard:\n")
		b.WriteString(violations)
		b.WriteString("\nRewrite the statement so every listed fact appears verbatim. ")
		b.WriteString("Output only the corrected statement.\n")
	}
	return b.String()
}

func (g *graniteSummarizer) complete(ctx context.Context, sources []string, violations string) (string, error) {
	g.calls++
	total := 0
	for _, s := range sources {
		total += len(s)
	}
	maxTokens := total / 3
	if maxTokens < 512 {
		maxTokens = 512
	}
	if maxTokens > 4096 {
		maxTokens = 4096
	}
	body, _ := json.Marshal(map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": prompt(sources, violations)}},
		"stream":   false,
		"think":    false,
		"options":  map[string]int{"temperature": 0, "num_predict": maxTokens},
	})
	req, _ := http.NewRequestWithContext(ctx, "POST", ollamaURL+"/api/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.http.Do(req)
	if err != nil {
		g.lastErr = err
		return "", err
	}
	defer resp.Body.Close()
	var d struct {
		Message struct {
			Content  string `json:"content"`
			Thinking string `json:"thinking"`
		} `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		g.lastErr = err
		return "", err
	}
	return strings.TrimSpace(d.Message.Content), nil
}

func (g *graniteSummarizer) Summarize(ctx context.Context, sources []string) (string, error) {
	return g.complete(ctx, sources, "")
}

func (g *graniteSummarizer) SummarizeWithFeedback(ctx context.Context, sources []string, violations string) (string, error) {
	return g.complete(ctx, sources, violations)
}

type clusterResult struct {
	Cluster   string `json:"cluster"`
	Outcome   string `json:"outcome"` // clean | retry-repaired | ledger-repaired | failed
	LLMCalls  int    `json:"llm_calls"`
	WallMs    int64  `json:"wall_ms"`
	Error     string `json:"error,omitempty"`
	FinalHead string `json:"final_head,omitempty"`
}

func main() {
	src := loadSources()
	names := make([]string, 0, len(clusters))
	for n := range clusters {
		names = append(names, n)
	}
	sort.Strings(names)

	var results []clusterResult
	for _, name := range names {
		ids := clusters[name]
		var notes []string
		for _, id := range ids {
			if v := src[id]; v != "" {
				notes = append(notes, v)
			}
		}
		if len(notes) < 2 {
			results = append(results, clusterResult{Cluster: name, Outcome: "skip-no-sources"})
			continue
		}
		g := &graniteSummarizer{http: &http.Client{Timeout: 300 * time.Second}}
		t0 := time.Now()
		out, err := consolidate.AbstractValue(context.Background(), g, notes)
		wall := time.Since(t0).Milliseconds()

		r := clusterResult{Cluster: name, LLMCalls: g.calls, WallMs: wall}
		if err != nil {
			r.Outcome = "failed"
			r.Error = err.Error()
			r.FinalHead = truncate(head(out), 140)
		} else {
			r.FinalHead = truncate(head(out), 140)
			switch {
			case strings.Contains(out, consolidate.LedgerMarker) && g.calls > 1:
				r.Outcome = "ledger-repaired-after-retry"
			case strings.Contains(out, consolidate.LedgerMarker):
				r.Outcome = "ledger-repaired"
			case g.calls > 1:
				r.Outcome = "retry-repaired"
			default:
				r.Outcome = "clean"
			}
		}
		results = append(results, r)
		fmt.Printf("%-10s %-26s calls=%d wall=%dms", name, r.Outcome, r.LLMCalls, r.WallMs)
		if r.Error != "" {
			fmt.Printf("  err=%s", truncate(r.Error, 120))
		}
		fmt.Println()
	}

	passed := 0
	for _, r := range results {
		if strings.HasPrefix(r.Outcome, "clean") || strings.Contains(r.Outcome, "repaired") {
			passed++
		}
	}
	fmt.Printf("== cascade: %d/%d clusters produced a lossless abstract\n", passed, len(results))
	f, err := os.Create("data/cascade_results.json")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", " ")
	enc.Encode(map[string]any{"model": model, "think": false, "results": results, "passed": passed})
	fmt.Println("saved data/cascade_results.json")
}

func head(s string) string {
	if i := strings.IndexByte(s, '\n'); i > 0 {
		return s[:i]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
