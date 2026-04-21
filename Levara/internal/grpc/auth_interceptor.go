// auth_interceptor.go — JWT auth for gRPC unary + stream RPCs (T19).
//
// Clients pass the same JWT they use against the HTTP API in the
// `authorization` metadata header. Tokens are verified against the
// shared secret via pkg/auth.VerifyJWT — the HTTP sign path
// (internal/http/auth.go createJWT) and this verify path share a single
// implementation so there's no drift risk.
//
// Public methods are whitelisted so healthchecks and Info probes don't
// need a token.
package grpc

import (
	"context"
	"strings"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	vectorAuth "github.com/stek0v/cognevra/pkg/auth"
)

// ctxUserIDKey is the context key under which an authenticated user ID
// is stored. Typed struct keeps it from colliding with any string key a
// downstream library might use.
type ctxUserIDKey struct{}

// publicMethods is the allow-list of RPCs that skip auth. Keep this
// short — every entry is a potential abuse surface. Health/Info probes
// need to work before a client has a token.
var publicMethods = map[string]bool{
	"/cognevra.v1.CognevraService/Info": true,
}

// UserIDFromContext returns the authenticated user ID if the interceptor
// has stashed one, or "" if the request came through a public method.
func UserIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxUserIDKey{}).(string)
	return v
}

// UnaryAuthInterceptor verifies JWT on every non-whitelisted unary RPC
// and injects the user_id into the context. requireAuth=false makes the
// interceptor permissive — useful for dev deployments where clients
// don't set a token yet; it logs missing/invalid tokens as "anon" rather
// than rejecting.
func UnaryAuthInterceptor(secret string, requireAuth bool) grpclib.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpclib.UnaryServerInfo, handler grpclib.UnaryHandler) (any, error) {
		if publicMethods[info.FullMethod] {
			return handler(ctx, req)
		}
		uid, ok := authFromMetadata(ctx, secret)
		if !ok {
			if requireAuth {
				return nil, status.Error(codes.Unauthenticated, "missing or invalid authorization token")
			}
			// Permissive mode: continue without uid.
			return handler(ctx, req)
		}
		ctx = context.WithValue(ctx, ctxUserIDKey{}, uid)
		return handler(ctx, req)
	}
}

// StreamAuthInterceptor mirrors UnaryAuthInterceptor for server-streaming
// RPCs. Wraps the stream so downstream handlers reading ss.Context() see
// the injected user_id.
func StreamAuthInterceptor(secret string, requireAuth bool) grpclib.StreamServerInterceptor {
	return func(srv any, ss grpclib.ServerStream, info *grpclib.StreamServerInfo, handler grpclib.StreamHandler) error {
		if publicMethods[info.FullMethod] {
			return handler(srv, ss)
		}
		uid, ok := authFromMetadata(ss.Context(), secret)
		if !ok {
			if requireAuth {
				return status.Error(codes.Unauthenticated, "missing or invalid authorization token")
			}
			return handler(srv, ss)
		}
		return handler(srv, &authedStream{ServerStream: ss, ctx: context.WithValue(ss.Context(), ctxUserIDKey{}, uid)})
	}
}

// authFromMetadata extracts and verifies the JWT. Accepts "Bearer <token>"
// or raw token in the authorization header; gRPC metadata keys are
// lower-cased on transport so we look up the lowercase form.
func authFromMetadata(ctx context.Context, secret string) (string, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return "", false
	}
	token := strings.TrimSpace(vals[0])
	token = strings.TrimPrefix(token, "Bearer ")
	token = strings.TrimPrefix(token, "bearer ")
	p, ok := vectorAuth.VerifyJWT(token, secret)
	if !ok {
		return "", false
	}
	return p.Sub, true
}

// authedStream is a thin grpclib.ServerStream wrapper that overrides
// Context() so handlers reading it see the injected user_id.
type authedStream struct {
	grpclib.ServerStream
	ctx context.Context
}

func (s *authedStream) Context() context.Context { return s.ctx }
