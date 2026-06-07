package grpc

import (
	"context"
	"net"
	"runtime"
	"testing"
	"time"

	pb "github.com/stek0v/levara/proto/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Integration coverage for the per-peer rate limiter over a real TCP
// listener. The unit test in ratelimit_test.go uses synthetic peer.Context
// values which don't verify the actual peer-address extraction path
// through gRPC. Here we spin up a minimal server with the interceptor,
// dial twice with different LocalAddrs, and assert that each source IP
// gets its own bucket.
//
// This relies on 127.0.0.2 routing to loopback, which is the default on
// Darwin and on most Linux distros (all of 127.0.0.0/8 is loopback). The
// test skips itself on Windows where the alias isn't configured by default,
// and falls back gracefully if the local bind fails for any reason.
func TestRateLimit_IntegrationPerSourceIP(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("loopback alias 127.0.0.2 not available by default on Windows")
	}

	// Probe that 127.0.0.2 is usable before we commit to the whole fixture.
	if probe, err := net.Listen("tcp", "127.0.0.2:0"); err != nil {
		t.Skipf("127.0.0.2 not reachable on this host (%v) — skipping integration test", err)
	} else {
		probe.Close()
	}

	// Server on 0.0.0.0:random so both loopback aliases reach it.
	lis, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()
	serverPort := lis.Addr().(*net.TCPAddr).Port

	// Tight bucket: 2 tokens, Burst=2, so the 3rd call from any given IP
	// is rejected. idleTTL is long enough that we don't race the janitor
	// during the test.
	pl := NewPeerLimiters(120 /* rpm */, 2 /* burst */, 5*time.Minute)
	srv := grpc.NewServer(grpc.UnaryInterceptor(UnaryRateLimitInterceptor(pl)))

	// Register only the Info method target — we need something that takes
	// Empty and returns quickly. Info is registered by the real Service, so
	// we use a minimal hand-rolled server that satisfies the interface.
	pb.RegisterLevaraServiceServer(srv, &minimalSvc{})
	go srv.Serve(lis)
	defer srv.GracefulStop()

	// dialFrom opens a gRPC client that binds its outgoing connection to
	// the given local IP. This is how we simulate "two clients from
	// different source IPs" in a single-process test.
	dialFrom := func(localIP string) (pb.LevaraServiceClient, func()) {
		dialer := &net.Dialer{
			LocalAddr: &net.TCPAddr{IP: net.ParseIP(localIP)},
			Timeout:   2 * time.Second,
		}
		conn, err := grpc.NewClient(
			net.JoinHostPort(localIP, fmtInt(serverPort)),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
				return dialer.DialContext(ctx, "tcp", addr)
			}),
		)
		if err != nil {
			t.Fatalf("dial from %s: %v", localIP, err)
		}
		return pb.NewLevaraServiceClient(conn), func() { conn.Close() }
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Client A from 127.0.0.1: burn the two-token burst, then expect 429.
	clientA, closeA := dialFrom("127.0.0.1")
	defer closeA()
	if _, err := clientA.Info(ctx, &pb.Empty{}); err != nil {
		t.Fatalf("127.0.0.1 call 1: %v", err)
	}
	if _, err := clientA.Info(ctx, &pb.Empty{}); err != nil {
		t.Fatalf("127.0.0.1 call 2: %v", err)
	}
	if _, err := clientA.Info(ctx, &pb.Empty{}); err == nil {
		t.Fatal("127.0.0.1 call 3 should have been rejected")
	} else if s, _ := status.FromError(err); s.Code() != codes.ResourceExhausted {
		t.Fatalf("127.0.0.1 call 3 expected ResourceExhausted, got %v", s.Code())
	}

	// Client B from 127.0.0.2: distinct source IP, fresh bucket.
	clientB, closeB := dialFrom("127.0.0.2")
	defer closeB()
	if _, err := clientB.Info(ctx, &pb.Empty{}); err != nil {
		t.Fatalf("127.0.0.2 first call should be allowed, got %v", err)
	}
}

// minimalSvc satisfies pb.LevaraServiceServer enough for the interceptor
// integration test. We only actually call Info; everything else panics so
// accidental expansion of the test is noisy rather than silent.
type minimalSvc struct {
	pb.UnimplementedLevaraServiceServer
}

func (*minimalSvc) Info(context.Context, *pb.Empty) (*pb.InfoResp, error) {
	return &pb.InfoResp{Dimension: 1}, nil
}

// fmtInt avoids pulling strconv into the test file for a single call.
func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 6)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}
