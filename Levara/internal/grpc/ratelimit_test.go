package grpc

import (
	"context"
	"net"
	"testing"
	"time"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Helper: build ctx with a fake peer.
func ctxWithPeer(ip string) context.Context {
	addr := &net.TCPAddr{IP: net.ParseIP(ip), Port: 12345}
	return peer.NewContext(context.Background(), &peer.Peer{Addr: addr})
}

// Unary interceptor: 3rd call from same peer returns ResourceExhausted.
func TestUnaryRateLimitInterceptor_PerPeer(t *testing.T) {
	// rpm=120 ⇒ 2 rps; burst=2 ⇒ 2 immediate, 3rd rejected.
	pl := NewPeerLimiters(120, 2, 5*time.Minute)
	intercept := UnaryRateLimitInterceptor(pl)

	info := &grpclib.UnaryServerInfo{FullMethod: "/test.Service/Method"}
	handler := func(ctx context.Context, req any) (any, error) { return "ok", nil }

	ctx := ctxWithPeer("10.0.0.1")

	// first two allowed (consume 2-token burst)
	if _, err := intercept(ctx, nil, info, handler); err != nil {
		t.Fatalf("1st call unexpected err: %v", err)
	}
	if _, err := intercept(ctx, nil, info, handler); err != nil {
		t.Fatalf("2nd call unexpected err: %v", err)
	}
	// third rejected
	_, err := intercept(ctx, nil, info, handler)
	if err == nil {
		t.Fatal("3rd call should be rejected")
	}
	if s, _ := status.FromError(err); s.Code() != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v", s.Code())
	}

	// different peer: fresh bucket
	ctx2 := ctxWithPeer("10.0.0.2")
	if _, err := intercept(ctx2, nil, info, handler); err != nil {
		t.Fatalf("different peer should be allowed, got %v", err)
	}
}

// Idle peers are evicted after idleTTL.
func TestPeerLimiters_IdleEviction(t *testing.T) {
	pl := NewPeerLimiters(60, 1, 50*time.Millisecond)

	// touch a peer then wait past idleTTL so pruning kicks in on the next get.
	pl.get("peer-a")
	if len(pl.buckets) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(pl.buckets))
	}
	time.Sleep(80 * time.Millisecond)
	pl.get("peer-b") // triggers prune because idleTTL/2 elapsed
	if _, exists := pl.buckets["peer-a"]; exists {
		t.Fatal("peer-a should have been evicted after idleTTL")
	}
}
