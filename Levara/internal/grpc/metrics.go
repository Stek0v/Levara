package grpc

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	grpclib "google.golang.org/grpc"
)

var (
	rpcDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "levara_grpc_duration_seconds",
		Help:    "gRPC method duration in seconds",
		Buckets: prometheus.ExponentialBuckets(0.0001, 3, 12), // 0.1ms to 53s
	}, []string{"method"})

	rpcTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "levara_grpc_requests_total",
		Help: "Total gRPC requests by method and status",
	}, []string{"method", "status"})
)

// MetricsUnaryInterceptor adds Prometheus metrics to all unary gRPC calls.
func MetricsUnaryInterceptor() grpclib.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpclib.UnaryServerInfo, handler grpclib.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		duration := time.Since(start).Seconds()

		method := info.FullMethod
		status := "ok"
		if err != nil {
			status = "error"
		}

		rpcDuration.WithLabelValues(method).Observe(duration)
		rpcTotal.WithLabelValues(method, status).Inc()

		return resp, err
	}
}

// MetricsStreamInterceptor mirrors the unary version for server-streaming
// RPCs (PipelineCognify and any future stream we add). Without it, stream
// methods produced no histogram data and dashboards showed a hole in
// gRPC latency wherever streams were the dominant traffic — A.3 from the
// 20.04 review.
//
// The duration measured is the full stream lifetime (open → close), which
// is the right thing for "how long did the consumer stay connected" but
// will look long for live progress streams. Pair with a per-message
// counter if you need throughput visibility — that's a separate metric
// the handler itself owns.
func MetricsStreamInterceptor() grpclib.StreamServerInterceptor {
	return func(srv any, ss grpclib.ServerStream, info *grpclib.StreamServerInfo, handler grpclib.StreamHandler) error {
		start := time.Now()
		err := handler(srv, ss)
		duration := time.Since(start).Seconds()

		method := info.FullMethod
		status := "ok"
		if err != nil {
			status = "error"
		}
		rpcDuration.WithLabelValues(method).Observe(duration)
		rpcTotal.WithLabelValues(method, status).Inc()
		return err
	}
}
