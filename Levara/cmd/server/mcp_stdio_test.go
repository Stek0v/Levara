package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMCPBackend captures forwarded requests and returns scripted bodies.
type fakeMCPBackend struct {
	mu          sync.Mutex
	requests    []recordedRequest
	responses   [][]byte
	sessionToID string // value returned on first request via Mcp-Session-Id header
	failStatus  int    // when >0, every request gets this HTTP status
	authHeader  string
}

type recordedRequest struct {
	body      []byte
	sessionID string
	authz     string
	apiKey    string
}

func newFakeMCPBackend() (*fakeMCPBackend, *httptest.Server) {
	f := &fakeMCPBackend{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, recordedRequest{
			body:      body,
			sessionID: r.Header.Get("Mcp-Session-Id"),
			authz:     r.Header.Get("Authorization"),
			apiKey:    r.Header.Get("X-API-Key"),
		})
		idx := len(f.requests) - 1
		var resp []byte
		if idx < len(f.responses) {
			resp = f.responses[idx]
		} else {
			resp = []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)
		}
		fail := f.failStatus
		sid := f.sessionToID
		f.mu.Unlock()
		if sid != "" && idx == 0 {
			w.Header().Set("Mcp-Session-Id", sid)
		}
		if fail > 0 {
			w.WriteHeader(fail)
			w.Write(resp)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(resp)
	}))
	return f, srv
}

func TestBridge_ForwardsAndCapturesSessionID(t *testing.T) {
	fake, srv := newFakeMCPBackend()
	defer srv.Close()
	fake.sessionToID = "sess-abc"
	fake.responses = [][]byte{
		[]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26"}}`),
		[]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`),
	}

	br := &bridge{backend: srv.URL + "/mcp", client: srv.Client()}
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n",
	)
	var out bytes.Buffer
	if err := br.run(in, &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(fake.requests) != 2 {
		t.Fatalf("requests=%d, want 2", len(fake.requests))
	}
	if fake.requests[0].sessionID != "" {
		t.Errorf("first request sent session id %q, want empty", fake.requests[0].sessionID)
	}
	if fake.requests[1].sessionID != "sess-abc" {
		t.Errorf("second request session id = %q, want sess-abc", fake.requests[1].sessionID)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("output lines=%d, want 2; got=%q", len(lines), out.String())
	}
	for i, line := range lines {
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Errorf("line[%d] not valid JSON: %v (%q)", i, err, line)
		}
	}
}

func TestBridge_SendsBearerToken(t *testing.T) {
	fake, srv := newFakeMCPBackend()
	defer srv.Close()
	fake.responses = [][]byte{[]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)}

	br := &bridge{backend: srv.URL + "/mcp", token: "secret-jwt", client: srv.Client()}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")
	if err := br.run(in, io.Discard); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := fake.requests[0].authz; got != "Bearer secret-jwt" {
		t.Errorf("Authorization = %q, want Bearer secret-jwt", got)
	}
}

func TestBridge_SendsAPIKey(t *testing.T) {
	fake, srv := newFakeMCPBackend()
	defer srv.Close()
	fake.responses = [][]byte{[]byte(`{}`)}

	br := &bridge{backend: srv.URL + "/mcp", apiKey: "k-1234", client: srv.Client()}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")
	if err := br.run(in, io.Discard); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := fake.requests[0].apiKey; got != "k-1234" {
		t.Errorf("X-API-Key = %q, want k-1234", got)
	}
	if got := fake.requests[0].authz; got != "" {
		t.Errorf("Authorization = %q, want empty when only api-key set", got)
	}
}

func TestRunMCPStdio_RejectsTokenAndAPIKeyTogether(t *testing.T) {
	err := runMCPStdio([]string{"serve", "--backend", "http://x", "--token", "j", "--api-key", "k"})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("err=%v, want mutually-exclusive error", err)
	}
}

func TestBridge_OmitsAuthHeaderWhenTokenEmpty(t *testing.T) {
	fake, srv := newFakeMCPBackend()
	defer srv.Close()
	fake.responses = [][]byte{[]byte(`{}`)}

	br := &bridge{backend: srv.URL + "/mcp", client: srv.Client()}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")
	_ = br.run(in, io.Discard)
	if got := fake.requests[0].authz; got != "" {
		t.Errorf("Authorization sent without token = %q, want empty", got)
	}
}

func TestBridge_BackendErrorSurfacesAsJSONRPCError(t *testing.T) {
	fake, srv := newFakeMCPBackend()
	defer srv.Close()
	fake.failStatus = 503
	fake.responses = [][]byte{[]byte(`upstream down`)}

	br := &bridge{backend: srv.URL + "/mcp", client: srv.Client()}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")
	var out bytes.Buffer
	if err := br.run(in, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	var r map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &r); err != nil {
		t.Fatalf("output not JSON: %v (%q)", err, out.String())
	}
	errObj, ok := r["error"].(map[string]any)
	if !ok {
		t.Fatalf("response missing error field: %v", r)
	}
	if code, _ := errObj["code"].(float64); code != -32000 {
		t.Errorf("error.code = %v, want -32000", code)
	}
	msg, _ := errObj["message"].(string)
	if !strings.Contains(msg, "503") {
		t.Errorf("error.message = %q, want it to include 503 status", msg)
	}
}

// Regression: the symptom that left "Calling levara…" spinning for an hour was
// the bridge replying to a failed forward with id=null. An MCP client matches
// responses to requests by id, so the pending call never resolves. The error
// must carry the original request id.
func TestBridge_ErrorResponseEchoesRequestID(t *testing.T) {
	fake, srv := newFakeMCPBackend()
	defer srv.Close()
	fake.failStatus = 503
	fake.responses = [][]byte{[]byte(`upstream down`)}

	br := &bridge{backend: srv.URL + "/mcp", client: srv.Client()}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call"}` + "\n")
	var out bytes.Buffer
	if err := br.run(in, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	var r map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &r); err != nil {
		t.Fatalf("output not JSON: %v (%q)", err, out.String())
	}
	if id, _ := r["id"].(float64); id != 7 {
		t.Errorf("error response id = %v (%T), want 7 — client matches by id", r["id"], r["id"])
	}
}

// A string request id must round-trip unquoted-and-then-requoted correctly.
func TestBridge_ErrorResponseEchoesStringID(t *testing.T) {
	fake, srv := newFakeMCPBackend()
	defer srv.Close()
	fake.failStatus = 500
	fake.responses = [][]byte{[]byte(`boom`)}

	br := &bridge{backend: srv.URL + "/mcp", client: srv.Client()}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":"abc-1","method":"ping"}` + "\n")
	var out bytes.Buffer
	_ = br.run(in, &out)
	var r map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &r); err != nil {
		t.Fatalf("output not JSON: %v (%q)", err, out.String())
	}
	if id, _ := r["id"].(string); id != "abc-1" {
		t.Errorf("error response id = %v, want \"abc-1\"", r["id"])
	}
}

// After a transport failure (backend gone) the cached session id must be
// cleared so the next request doesn't replay a session the restarted backend
// has never heard of.
func TestBridge_TransportErrorClearsSession(t *testing.T) {
	br := &bridge{
		backend: "http://127.0.0.1:1/mcp", // port 1: connection refused, fast
		client:  &http.Client{Timeout: 2 * time.Second},
	}
	br.sessionID = "stale-session"
	if _, err := br.forward([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)); err == nil {
		t.Fatal("expected a transport error against an unreachable backend")
	}
	if br.sessionID != "" {
		t.Errorf("sessionID = %q, want cleared after transport error", br.sessionID)
	}
}

func TestRequestID(t *testing.T) {
	cases := map[string]string{
		`{"id":42,"method":"x"}`:      "42",
		`{"id":"s","method":"x"}`:     `"s"`,
		`{"method":"notify"}`:         "null", // notification: no id
		`not json at all`:             "null",
		`{"id":null,"method":"ping"}`: "null",
	}
	for in, want := range cases {
		if got := string(requestID([]byte(in))); got != want {
			t.Errorf("requestID(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestBridge_SkipsBlankLines(t *testing.T) {
	fake, srv := newFakeMCPBackend()
	defer srv.Close()
	fake.responses = [][]byte{[]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)}

	br := &bridge{backend: srv.URL + "/mcp", client: srv.Client()}
	in := strings.NewReader("\n\n  \n" + `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n\n")
	var out bytes.Buffer
	_ = br.run(in, &out)

	if len(fake.requests) != 1 {
		t.Errorf("requests=%d, want 1 (blank lines must be skipped)", len(fake.requests))
	}
	if got := strings.Count(strings.TrimSpace(out.String()), "\n") + 1; got != 1 {
		t.Errorf("output lines=%d, want 1", got)
	}
}

func TestRunMCPStdio_RejectsUnknownSubcommand(t *testing.T) {
	if err := runMCPStdio([]string{"frobnicate"}); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("err=%v, want unknown-subcommand error", err)
	}
}

func TestRunMCPStdio_RequiresArgs(t *testing.T) {
	if err := runMCPStdio(nil); err == nil {
		t.Errorf("expected usage error for empty args")
	}
}
