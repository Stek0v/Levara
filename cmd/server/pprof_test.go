package main

import (
	"io"
	"net/http"
	"testing"
	"time"
)

// TestStartPprofServerServesHeapProfile — with a bound address the standard
// pprof handlers answer. Uses port 0 so CI never fights over a fixed port.
func TestStartPprofServerServesHeapProfile(t *testing.T) {
	addr, err := startPprofServer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("startPprofServer: %v", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + addr + "/debug/pprof/heap")
	if err != nil {
		t.Fatalf("GET heap profile: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET heap profile status=%d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read heap profile: %v", err)
	}
	// gzip-serialized protobuf profile, always non-trivially sized.
	if len(body) < 100 {
		t.Fatalf("heap profile suspiciously small: %d bytes", len(body))
	}
}

// TestStartPprofServerBadAddrFailsFast — a bind error surfaces at startup,
// not from a background goroutine.
func TestStartPprofServerBadAddrFailsFast(t *testing.T) {
	// Port 1 is privileged and unbindable for an unprivileged process.
	if _, err := startPprofServer("127.0.0.1:1"); err == nil {
		t.Fatal("expected bind error for privileged port, got nil")
	}
}
