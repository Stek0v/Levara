package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	grpclib "google.golang.org/grpc"
)

type fakeServerStream struct {
	grpclib.ServerStream
}

func (fakeServerStream) Context() context.Context { return context.Background() }

// TestMetricsUnaryInterceptorCountsByMethodAndStatus pins the per-method
// accounting the Р4 freeze decision relies on: levara_grpc_requests_total
// must distinguish methods (so frozen raw surface shows up separately) and
// ok/error status (so a noisy failing client is visible as traffic, not lost).
func TestMetricsUnaryInterceptorCountsByMethodAndStatus(t *testing.T) {
	const method = "/levara.v1.LevaraService/MetricsTestUnary"
	okCounter := rpcTotal.WithLabelValues(method, "ok")
	errCounter := rpcTotal.WithLabelValues(method, "error")
	okBefore := testutil.ToFloat64(okCounter)
	errBefore := testutil.ToFloat64(errCounter)
	seriesBefore := testutil.CollectAndCount(rpcDuration)

	interceptor := MetricsUnaryInterceptor()
	info := &grpclib.UnaryServerInfo{FullMethod: method}

	if _, err := interceptor(context.Background(), nil, info, func(context.Context, any) (any, error) {
		return "resp", nil
	}); err != nil {
		t.Fatalf("ok path returned error: %v", err)
	}
	if got := testutil.ToFloat64(okCounter); got != okBefore+1 {
		t.Fatalf("ok counter = %v, want %v", got, okBefore+1)
	}
	if got := testutil.ToFloat64(errCounter); got != errBefore {
		t.Fatalf("error counter = %v, want unchanged %v", got, errBefore)
	}
	if got := testutil.CollectAndCount(rpcDuration); got != seriesBefore+1 {
		t.Fatalf("duration series = %d, want %d (one new method)", got, seriesBefore+1)
	}

	if _, err := interceptor(context.Background(), nil, info, func(context.Context, any) (any, error) {
		return nil, errors.New("boom")
	}); err == nil {
		t.Fatal("error path returned nil error")
	}
	if got := testutil.ToFloat64(errCounter); got != errBefore+1 {
		t.Fatalf("error counter = %v, want %v", got, errBefore+1)
	}
}

func TestMetricsStreamInterceptorCountsByMethodAndStatus(t *testing.T) {
	const method = "/levara.v1.LevaraService/MetricsTestStream"
	okCounter := rpcTotal.WithLabelValues(method, "ok")
	errCounter := rpcTotal.WithLabelValues(method, "error")
	okBefore := testutil.ToFloat64(okCounter)
	errBefore := testutil.ToFloat64(errCounter)

	interceptor := MetricsStreamInterceptor()
	info := &grpclib.StreamServerInfo{FullMethod: method}
	ss := fakeServerStream{}

	if err := interceptor(nil, ss, info, func(any, grpclib.ServerStream) error { return nil }); err != nil {
		t.Fatalf("ok path returned error: %v", err)
	}
	if got := testutil.ToFloat64(okCounter); got != okBefore+1 {
		t.Fatalf("ok counter = %v, want %v", got, okBefore+1)
	}

	if err := interceptor(nil, ss, info, func(any, grpclib.ServerStream) error { return errors.New("boom") }); err == nil {
		t.Fatal("error path returned nil error")
	}
	if got := testutil.ToFloat64(errCounter); got != errBefore+1 {
		t.Fatalf("error counter = %v, want %v", got, errBefore+1)
	}
}
