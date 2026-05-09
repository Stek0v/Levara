package http

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	nethttp "net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

// memory_events_e2e_test.go — wire-format and filter coverage for
// GET /memories/stream. The bus-level fan-out is exercised in
// memory_events_test.go; this file pins down the SSE framing and the
// owner_id × type × key_prefix filter precedence that external clients
// rely on.
//
// We spin a real net.Listener instead of using app.Test because app.Test
// buffers the whole response and the SSE handler only returns when the
// request context is cancelled — which app.Test never does.

func newSSETestServer(t *testing.T) (addr string, cleanup func()) {
	t.Helper()
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/memories/stream", memoryEventsStreamHandler())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = app.Listener(ln)
	}()

	return ln.Addr().String(), func() {
		_ = app.Shutdown()
		wg.Wait()
	}
}

// dialSSE opens the stream, blocks until the "ready" frame arrives so the
// caller knows the subscription is registered on the bus before publishing,
// and returns a buffered reader plus a cancel function.
func dialSSE(t *testing.T, addr, query string) (*bufio.Reader, context.CancelFunc, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	url := "http://" + addr + "/memories/stream"
	if query != "" {
		url += "?" + query
	}
	req, err := nethttp.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		cancel()
		t.Fatalf("new request: %v", err)
	}
	resp, err := nethttp.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("do: %v", err)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		cancel()
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		resp.Body.Close()
		cancel()
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	br := bufio.NewReader(resp.Body)

	// Block until the hello frame arrives so the subscriber is registered
	// on memoryEvents before the test calls Publish.
	if err := waitForFrame(br, 2*time.Second, "ready"); err != nil {
		resp.Body.Close()
		cancel()
		t.Fatalf("waiting for ready: %v", err)
	}

	return br, cancel, func() { resp.Body.Close() }
}

// readFrame reads one SSE frame (lines until blank line) and returns the
// parsed event name + data payload. Comments (lines beginning with ":") and
// the trailing blank line are stripped.
type sseFrame struct {
	event string
	data  string
}

func readFrame(br *bufio.Reader, deadline time.Duration) (sseFrame, error) {
	type result struct {
		f   sseFrame
		err error
	}
	ch := make(chan result, 1)
	go func() {
		var f sseFrame
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				ch <- result{f, err}
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if f.event != "" || f.data != "" {
					ch <- result{f, nil}
					return
				}
				continue
			}
			if strings.HasPrefix(line, ":") {
				continue
			}
			if strings.HasPrefix(line, "event: ") {
				f.event = strings.TrimPrefix(line, "event: ")
			} else if strings.HasPrefix(line, "data: ") {
				f.data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	select {
	case r := <-ch:
		return r.f, r.err
	case <-time.After(deadline):
		return sseFrame{}, fmt.Errorf("timeout waiting for frame")
	}
}

func waitForFrame(br *bufio.Reader, deadline time.Duration, wantEvent string) error {
	f, err := readFrame(br, deadline)
	if err != nil {
		return err
	}
	if f.event != wantEvent {
		return fmt.Errorf("event = %q, want %q", f.event, wantEvent)
	}
	return nil
}

// publishWithRetry races the test against bus.Subscribe finishing on the
// server side. The hello frame guarantees Subscribe completed, but we still
// give a tick of grace on the very first publish in case the goroutine
// hasn't entered its select yet.
func publishWithRetry(ev MemoryEvent) {
	memoryEvents.Publish(ev)
}

func TestSSE_WireFormat_SavedEventEmitted(t *testing.T) {
	addr, stop := newSSETestServer(t)
	defer stop()

	br, cancel, closeBody := dialSSE(t, addr, "owner_id=u1")
	defer closeBody()
	defer cancel()

	want := MemoryEvent{
		Kind: "memory.saved", Key: "k1", Value: "v1",
		Type: "fact", OwnerID: "u1", Timestamp: "2026-05-09T00:00:00Z",
	}
	publishWithRetry(want)

	frame, err := readFrame(br, 2*time.Second)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if frame.event != "memory.saved" {
		t.Errorf("event = %q, want memory.saved", frame.event)
	}
	var got MemoryEvent
	if err := json.Unmarshal([]byte(frame.data), &got); err != nil {
		t.Fatalf("unmarshal data %q: %v", frame.data, err)
	}
	if got != want {
		t.Errorf("payload mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestSSE_FilterByOwnerID(t *testing.T) {
	addr, stop := newSSETestServer(t)
	defer stop()

	// Caller subscribes as u1 — events for u2 must be filtered out, events
	// with empty OwnerID must pass through (broadcast semantics).
	br, cancel, closeBody := dialSSE(t, addr, "owner_id=u1")
	defer closeBody()
	defer cancel()

	publishWithRetry(MemoryEvent{Kind: "memory.saved", Key: "skip", OwnerID: "u2", Timestamp: "t1"})
	publishWithRetry(MemoryEvent{Kind: "memory.saved", Key: "broadcast", OwnerID: "", Timestamp: "t2"})
	publishWithRetry(MemoryEvent{Kind: "memory.saved", Key: "keep", OwnerID: "u1", Timestamp: "t3"})

	gotKeys := readKeys(t, br, 2)
	if want := []string{"broadcast", "keep"}; !equalStrings(gotKeys, want) {
		t.Errorf("keys = %v, want %v", gotKeys, want)
	}
}

func TestSSE_FilterByType(t *testing.T) {
	addr, stop := newSSETestServer(t)
	defer stop()

	br, cancel, closeBody := dialSSE(t, addr, "owner_id=u1&type=fact")
	defer closeBody()
	defer cancel()

	publishWithRetry(MemoryEvent{Kind: "memory.saved", Key: "drop1", Type: "event", OwnerID: "u1", Timestamp: "t1"})
	publishWithRetry(MemoryEvent{Kind: "memory.saved", Key: "drop2", Type: "decision", OwnerID: "u1", Timestamp: "t2"})
	publishWithRetry(MemoryEvent{Kind: "memory.saved", Key: "keep", Type: "fact", OwnerID: "u1", Timestamp: "t3"})

	gotKeys := readKeys(t, br, 1)
	if want := []string{"keep"}; !equalStrings(gotKeys, want) {
		t.Errorf("keys = %v, want %v", gotKeys, want)
	}
}

func TestSSE_FilterByKeyPrefix(t *testing.T) {
	addr, stop := newSSETestServer(t)
	defer stop()

	br, cancel, closeBody := dialSSE(t, addr, "owner_id=u1&key_prefix=proj/")
	defer closeBody()
	defer cancel()

	publishWithRetry(MemoryEvent{Kind: "memory.saved", Key: "user/foo", OwnerID: "u1", Timestamp: "t1"})
	publishWithRetry(MemoryEvent{Kind: "memory.saved", Key: "proj/a", OwnerID: "u1", Timestamp: "t2"})
	publishWithRetry(MemoryEvent{Kind: "memory.saved", Key: "proj/b", OwnerID: "u1", Timestamp: "t3"})
	publishWithRetry(MemoryEvent{Kind: "memory.saved", Key: "pro", OwnerID: "u1", Timestamp: "t4"}) // shorter than prefix

	gotKeys := readKeys(t, br, 2)
	if want := []string{"proj/a", "proj/b"}; !equalStrings(gotKeys, want) {
		t.Errorf("keys = %v, want %v", gotKeys, want)
	}
}

func TestSSE_FilterPrecedence_AllThreeMustMatch(t *testing.T) {
	addr, stop := newSSETestServer(t)
	defer stop()

	br, cancel, closeBody := dialSSE(t, addr, "owner_id=u1&type=fact&key_prefix=proj/")
	defer closeBody()
	defer cancel()

	// Each of these fails exactly one of the three filters → all dropped.
	publishWithRetry(MemoryEvent{Kind: "memory.saved", Key: "proj/a", Type: "fact", OwnerID: "u2", Timestamp: "t1"})    // owner mismatch
	publishWithRetry(MemoryEvent{Kind: "memory.saved", Key: "proj/b", Type: "event", OwnerID: "u1", Timestamp: "t2"})   // type mismatch
	publishWithRetry(MemoryEvent{Kind: "memory.saved", Key: "user/c", Type: "fact", OwnerID: "u1", Timestamp: "t3"})    // prefix mismatch
	publishWithRetry(MemoryEvent{Kind: "memory.saved", Key: "proj/ok", Type: "fact", OwnerID: "u1", Timestamp: "t4"}) // all three pass

	gotKeys := readKeys(t, br, 1)
	if want := []string{"proj/ok"}; !equalStrings(gotKeys, want) {
		t.Errorf("keys = %v, want %v", gotKeys, want)
	}
}

func TestSSE_DeletedEventEmitted(t *testing.T) {
	addr, stop := newSSETestServer(t)
	defer stop()

	br, cancel, closeBody := dialSSE(t, addr, "owner_id=u1")
	defer closeBody()
	defer cancel()

	publishWithRetry(MemoryEvent{Kind: "memory.deleted", Key: "k1", OwnerID: "u1", Timestamp: "t1"})

	frame, err := readFrame(br, 2*time.Second)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if frame.event != "memory.deleted" {
		t.Errorf("event = %q, want memory.deleted", frame.event)
	}
}

// readKeys reads n filtered frames and returns the Key field of each. Used
// by filter tests to assert which events survived.
func readKeys(t *testing.T, br *bufio.Reader, n int) []string {
	t.Helper()
	keys := make([]string, 0, n)
	for i := 0; i < n; i++ {
		f, err := readFrame(br, 2*time.Second)
		if err != nil {
			t.Fatalf("read frame %d: %v", i, err)
		}
		var ev MemoryEvent
		if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
			t.Fatalf("unmarshal frame %d: %v", i, err)
		}
		keys = append(keys, ev.Key)
	}
	return keys
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
