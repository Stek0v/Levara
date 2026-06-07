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
//
// Resilience to a backend restart (the "MCP call hangs ~forever after the
// local :8081 backend is restarted" bug):
//   - Keep-alives are disabled, so a backend restart can never strand the
//     bridge on a dead pooled TCP socket. Each request opens a fresh
//     connection — free on loopback — so it either succeeds promptly or
//     fails fast with connection-refused.
//   - Forwarding errors are echoed back with the *original* JSON-RPC request
//     id (not id=null). An MCP client matches responses to requests by id, so
//     an id=null error never unblocks the pending call — that mismatch, not
//     the bridge itself, is what left "Calling levara…" spinning for an hour.
//   - A transport failure drops the cached Mcp-Session-Id, so the next request
//     (the client re-initializes) establishes a fresh session instead of
//     replaying a session the restarted backend has never heard of.
//   - A watchdog exits the process if our parent (the MCP client) dies, so
//     abandoned bridges don't pile up across sessions.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
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

	// Disable keep-alives: a pooled connection to the local backend goes dead
	// the moment the backend restarts, and the next request would either hang
	// until the timeout or fail — either way stranding the session. A fresh
	// connection per request is negligible on loopback and immune to that.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableKeepAlives = true

	br := &bridge{
		backend: backend + "/mcp",
		token:   token,
		apiKey:  apiKey,
		client:  &http.Client{Timeout: timeout, Transport: tr},
	}

	// Exit if the MCP client that spawned us goes away, so we don't leak an
	// orphaned bridge holding a session. stdin EOF is the normal shutdown
	// signal; this covers the case where the parent dies without closing it.
	go watchParent(os.Getppid())

	return br.run(os.Stdin, os.Stdout)
}

// watchParent polls the parent PID and terminates the process once it changes,
// which on Unix means the original parent exited and we were reparented (to
// launchd/init). Cheap (a getppid syscall every 2s) and platform-portable.
func watchParent(orig int) {
	for {
		time.Sleep(2 * time.Second)
		if os.Getppid() != orig {
			os.Exit(0)
		}
	}
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
			// Surface forwarding failures as JSON-RPC errors on stdout so the
			// client sees a structured failure instead of a dropped connection.
			// Echo the *request* id: a client matches responses to requests by
			// id, so an id=null error would never unblock the pending call.
			writeRPCError(writer, requestID(line), err)
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
		// A transport failure means the connection (and, after a restart, the
		// server-side session) is gone. Drop the cached session id so the next
		// request re-establishes a fresh one instead of replaying a dead one.
		b.mu.Lock()
		b.sessionID = ""
		b.mu.Unlock()
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

// requestID extracts the raw JSON-RPC id from a request line so failures can be
// echoed back against the call the client is waiting on. Notifications (no id)
// and unparseable lines yield null.
func requestID(payload []byte) json.RawMessage {
	var probe struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(payload, &probe) != nil || len(probe.ID) == 0 {
		return json.RawMessage("null")
	}
	return probe.ID
}

// writeRPCError emits a JSON-RPC error response carrying the given id. id is raw
// JSON (a number, string, or null) and the message is JSON-escaped so the line
// is always valid regardless of what the underlying error text contains.
func writeRPCError(w *bufio.Writer, id json.RawMessage, err error) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	msg, _ := json.Marshal(err.Error())
	fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":%s}}`+"\n", id, msg)
	w.Flush()
}
