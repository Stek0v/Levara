// ratelimit.go — per-peer gRPC rate limiter (T2).
//
// Uses golang.org/x/time/rate token buckets keyed by peer.Addr. Buckets are
// kept in a map with a coarse LRU-by-timestamp eviction so long-lived connections
// don't bloat memory; the eviction runs inline on every request and is O(1)
// amortised. In-memory is acceptable for single-node deployments (D2).
//
// Rejections are logged in levara_rate_limit_rejected_total{channel="grpc",bucket="peer"}.
package grpc

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/stek0v/cognevra/internal/metrics"
)

// peerLimiters maps a peer address string to a token-bucket limiter.
// Access is serialised via mu; a 30-minute inactivity eviction keeps it bounded.
type peerLimiters struct {
	mu        sync.Mutex
	buckets   map[string]*bucket
	rps       rate.Limit
	burst     int
	idleTTL   time.Duration
	lastPrune time.Time
}

type bucket struct {
	lim       *rate.Limiter
	lastSeen  time.Time
}

// NewPeerLimiters constructs a peer-keyed limiter pool. rpm is the steady-state
// allowance (requests per minute); burst controls short-lived bursts beyond rps.
// idleTTL evicts buckets that haven't been used recently.
func NewPeerLimiters(rpm, burst int, idleTTL time.Duration) *peerLimiters {
	if burst < 1 {
		burst = 1
	}
	return &peerLimiters{
		buckets: make(map[string]*bucket),
		rps:     rate.Limit(float64(rpm) / 60.0),
		burst:   burst,
		idleTTL: idleTTL,
	}
}

// get returns the bucket for addr, creating one on first sight. Opportunistically
// prunes idle buckets while the map mutex is already held (at most once every
// idleTTL/2 to avoid O(N) walk on every call).
func (p *peerLimiters) get(addr string) *rate.Limiter {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.idleTTL > 0 && now.Sub(p.lastPrune) > p.idleTTL/2 {
		cutoff := now.Add(-p.idleTTL)
		for k, b := range p.buckets {
			if b.lastSeen.Before(cutoff) {
				delete(p.buckets, k)
			}
		}
		p.lastPrune = now
	}
	b, ok := p.buckets[addr]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(p.rps, p.burst)}
		p.buckets[addr] = b
	}
	b.lastSeen = now
	return b.lim
}

// UnaryRateLimitInterceptor rejects unary RPCs that exceed the per-peer bucket.
// Returns codes.ResourceExhausted — the standard gRPC mapping for rate limits.
func UnaryRateLimitInterceptor(pl *peerLimiters) grpclib.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpclib.UnaryServerInfo, handler grpclib.UnaryHandler) (any, error) {
		if !pl.allow(ctx) {
			metrics.RateLimitRejected.WithLabelValues("grpc", "peer").Inc()
			return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
		}
		return handler(ctx, req)
	}
}

// StreamRateLimitInterceptor: one token per stream open. Per-frame throttling
// would starve legitimate streaming RPCs (e.g. PipelineCognify progress) so we
// admit the stream and let downstream consumers pace themselves.
func StreamRateLimitInterceptor(pl *peerLimiters) grpclib.StreamServerInterceptor {
	return func(srv any, ss grpclib.ServerStream, info *grpclib.StreamServerInfo, handler grpclib.StreamHandler) error {
		if !pl.allow(ss.Context()) {
			metrics.RateLimitRejected.WithLabelValues("grpc", "peer").Inc()
			return status.Error(codes.ResourceExhausted, "rate limit exceeded")
		}
		return handler(srv, ss)
	}
}

func (p *peerLimiters) allow(ctx context.Context) bool {
	addr := "unknown"
	if pr, ok := peer.FromContext(ctx); ok && pr.Addr != nil {
		addr = pr.Addr.String()
	}
	return p.get(addr).Allow()
}
