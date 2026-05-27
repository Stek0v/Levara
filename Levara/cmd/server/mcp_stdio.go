// mcp_stdio.go — `levara mcp serve` subcommand: stdio↔HTTP MCP bridge.
//
// Reads newline-delimited JSON-RPC messages from stdin, forwards each as
// POST <backend>/mcp, writes the response to stdout (one JSON object per
// line). The bridge tracks the Mcp-Session-Id header returned by the
// HTTP MCP endpoint and replays it on every subsequent request so the
// remote server keeps a single session for the lifetime of stdin.
//
// Use:
//
//	levara mcp serve --backend http://127.0.0.1:8080 [--token <jwt>]
//
// Auth: --token flag overrides LEVARA_TOKEN env. Empty token means no
// Authorization header is sent (dev-mode servers without -require-auth).
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

func runMCPStdio(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		subcmd  string
		backend string
		token   string
		apiKey  string
		timeout time.Duration
	)
	fs.StringVar(&backend, "backend", "http://127.0.0.1:8080", "Levara HTTP base URL (the bridge POSTs to <backend>/mcp)")
	fs.StringVar(&token, "token", os.Getenv("LEVARA_TOKEN"), "JWT bearer token (24h TTL); falls back to LEVARA_TOKEN env. Use --api-key for long-lived auth.")
	fs.StringVar(&apiKey, "api-key", os.Getenv("LEVARA_API_KEY"), "Long-lived API key (X-API-Key header); falls back to LEVARA_API_KEY env. Mutually exclusive with --token.")
	fs.DurationVar(&timeout, "timeout", 30*time.Second, "Per-request HTTP timeout")

	if len(args) == 0 {
		return fmt.Errorf("usage: levara mcp <serve> --backend <url> [--token <jwt>]")
	}
	subcmd, rest := args[0], args[1:]
	if subcmd != "serve" {
		return fmt.Errorf("unknown mcp subcommand %q (expected: serve)", subcmd)
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	backend = strings.TrimRight(backend, "/")
	if backend == "" {
		return fmt.Errorf("--backend required")
	}
	if token != "" && apiKey != "" {
		return fmt.Errorf("--token and --api-key are mutually exclusive")
	}

	br := &bridge{
		backend: backend + "/mcp",
		token:   token,
		apiKey:  apiKey,
		client:  &http.Client{Timeout: timeout},
	}
	return br.run(os.Stdin, os.Stdout)
}

type bridge struct {
	backend string
	token   string
	apiKey  string
	client  *http.Client

	mu        sync.Mutex
	sessionID string
}

func (b *bridge) run(in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	// MCP messages can be large (search results, cognify status). Allow up
	// to 4 MiB per line — matches the HTTP body limit on the server side.
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	writer := bufio.NewWriter(out)
	defer writer.Flush()
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		respBody, err := b.forward(line)
		if err != nil {
			// Forwarding errors are surfaced as JSON-RPC errors on stdout
			// so the MCP client sees a structured failure instead of a
			// dropped connection. The request ID is not recovered here —
			// JSON-RPC permits id=null for transport-level errors.
			fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":null,"error":{"code":-32000,"message":%q}}`+"\n", err.Error())
			writer.Flush()
			continue
		}
		writer.Write(respBody)
		writer.WriteByte('\n')
		writer.Flush()
	}
	return scanner.Err()
}

func (b *bridge) forward(payload []byte) ([]byte, error) {
	req, err := http.NewRequest("POST", b.backend, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	if b.apiKey != "" {
		req.Header.Set("X-API-Key", b.apiKey)
	}
	b.mu.Lock()
	sid := b.sessionID
	b.mu.Unlock()
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if newSID := resp.Header.Get("Mcp-Session-Id"); newSID != "" {
		b.mu.Lock()
		b.sessionID = newSID
		b.mu.Unlock()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("backend %s returned %d: %s", b.backend, resp.StatusCode, truncate(body, 200))
	}
	return body, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
