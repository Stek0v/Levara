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
		Name:    "cognevra_grpc_duration_seconds",
		Help:    "gRPC method duration in seconds",
		Buckets: prometheus.ExponentialBuckets(0.0001, 3, 12), // 0.1ms to 53s
	}, []string{"method"})

	rpcTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "cognevra_grpc_requests_total",
		Help: "Total gRPC requests by method and status",
	}, []string{"method", "status"})

	cacheHits = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "cognevra_cache_hits_total",
		Help: "Cache hits by cache type",
	}, []string{"cache"})

	cacheMisses = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "cognevra_cache_misses_total",
		Help: "Cache misses by cache type",
	}, []string{"cache"})
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
