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
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/stek0v/levara/pkg/access"
	vectorAuth "github.com/stek0v/levara/pkg/auth"
)

// ctxUserIDKey is the context key under which an authenticated user ID
// is stored. Typed struct keeps it from colliding with any string key a
// downstream library might use.
type ctxUserIDKey struct{}
type ctxMetadataActorKey struct{}

type ctxPrivateInfoKey struct{}

// publicMethods is the allow-list of RPCs that skip auth. Keep this
// short — every entry is a potential abuse surface. Health/Info probes
// need to work before a client has a token.
var publicMethods = map[string]bool{
	"/levara.v1.LevaraService/Info":   true,
	"/levara.v2.LevaraServiceV2/Info": true,
}

// UserIDFromContext returns the authenticated user ID if the interceptor
// has stashed one, or "" if the request came through a public method.
func UserIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxUserIDKey{}).(string)
	return v
}

// UnaryAuthInterceptor restricts the global raw-storage API to active
// superusers when authentication is required. No-auth dev mode stays permissive.
func UnaryAuthInterceptor(secret string, requireAuth bool, policy access.SQLPolicy) grpclib.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpclib.UnaryServerInfo, handler grpclib.UnaryHandler) (any, error) {
		if publicMethods[info.FullMethod] {
			return handler(context.WithValue(ctx, ctxPrivateInfoKey{}, requireAuth), req)
		}
		if info.FullMethod == "/levara.v1.LevaraService/IngestData" {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			ctx, err := authenticateScopedRPC(ctx, secret, requireAuth, policy)
			if err != nil {
				return nil, err
			}
			return handler(ctx, req)
		}
		payload, ok := authFromMetadata(ctx, secret)
		if !ok {
			if requireAuth {
				return nil, status.Error(codes.Unauthenticated, "missing or invalid authorization token")
			}
			// Permissive mode: continue without uid.
			return handler(ctx, req)
		}
		if requireAuth {
			if err := authorizeGlobalStorage(ctx, payload.Sub, policy); err != nil {
				return nil, err
			}
		}
		if requireAuth && (access.ValidateCredential(ctx, policy.DB, policy.Q, payload.Sub, payload.CredentialEpoch) != nil || access.ValidateBrowserSession(ctx, policy.DB, policy.Q, payload.Sub, payload.SessionID) != nil) {
			return nil, status.Error(codes.Unauthenticated, "revoked credential")
		}
		ctx = context.WithValue(ctx, ctxUserIDKey{}, payload.Sub)
		actor := access.Actor{UserID: payload.Sub}

		ctx = context.WithValue(ctx, ctxMetadataActorKey{}, access.MetadataActor{Actor: actor, Credential: access.MetadataCredential{Kind: "jwt", SessionID: payload.SessionID, Epoch: payload.CredentialEpoch, IssuedAt: payload.Iat, ExpiresAt: payload.Exp}})
		return handler(ctx, req)
	}
}

// StreamAuthInterceptor mirrors UnaryAuthInterceptor for server-streaming
// RPCs. Wraps the stream so downstream handlers reading ss.Context() see
// the injected user_id.
func StreamAuthInterceptor(secret string, requireAuth bool, policy access.SQLPolicy) grpclib.StreamServerInterceptor {
	return func(srv any, ss grpclib.ServerStream, info *grpclib.StreamServerInfo, handler grpclib.StreamHandler) error {
		if publicMethods[info.FullMethod] {
			return handler(srv, ss)
		}
		if info.FullMethod == "/levara.v1.LevaraService/CognifyDocuments" || info.FullMethod == "/levara.v1.LevaraService/CognifyDocumentsStatus" {
			ctx, cancel := context.WithTimeout(ss.Context(), 30*time.Minute)
			defer cancel()
			ctx, err := authenticateScopedRPC(ctx, secret, requireAuth, policy)
			if err != nil {
				return err
			}
			return handler(srv, &authedStream{ServerStream: ss, ctx: ctx})
		}
		payload, ok := authFromMetadata(ss.Context(), secret)
		if !ok {
			if requireAuth {
				return status.Error(codes.Unauthenticated, "missing or invalid authorization token")
			}
			return handler(srv, ss)
		}
		if requireAuth {
			if err := authorizeGlobalStorage(ss.Context(), payload.Sub, policy); err != nil {
				return err
			}
		}
		if requireAuth && (access.ValidateCredential(ss.Context(), policy.DB, policy.Q, payload.Sub, payload.CredentialEpoch) != nil || access.ValidateBrowserSession(ss.Context(), policy.DB, policy.Q, payload.Sub, payload.SessionID) != nil) {
			return status.Error(codes.Unauthenticated, "revoked credential")
		}
		return handler(srv, &authedStream{ServerStream: ss, ctx: context.WithValue(ss.Context(), ctxUserIDKey{}, payload.Sub)})
	}
}

// authenticateScopedRPC verifies identity only. The document runner or ingestion
// coordinator owns object-level authorization and rechecks these facts under its
// SQL fence. Keep the short SQL deadline separate from the observer lifetime.
func authenticateScopedRPC(ctx context.Context, secret string, requireAuth bool, policy access.SQLPolicy) (context.Context, error) {
	payload, ok := authFromMetadata(ctx, secret)
	if !ok && requireAuth {
		return nil, status.Error(codes.Unauthenticated, "missing or invalid authorization token")
	}
	actor := access.MetadataActor{TrustedLocal: !requireAuth}
	if ok {
		actor.Actor = access.Actor{UserID: payload.Sub, AuthMethod: "jwt"}
		actor.Credential = access.MetadataCredential{Kind: "jwt", SessionID: payload.SessionID, Epoch: payload.CredentialEpoch, IssuedAt: payload.Iat, ExpiresAt: payload.Exp}
	}
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get("x-tenant-id")
	if len(values) > 1 || (len(values) == 1 && (values[0] == "" || len(values[0]) > 256 || !utf8.ValidString(values[0]) || strings.TrimSpace(values[0]) != values[0] || strings.IndexFunc(values[0], unicode.IsControl) >= 0)) {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant selector")
	}
	if len(values) == 1 {
		actor.TenantID = values[0]
	}
	if requireAuth {
		if policy.DB == nil {
			return nil, status.Error(codes.Unavailable, "metadata storage unavailable")
		}
		lookupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		active, err := policy.IsActive(lookupCtx, actor.UserID)
		if err != nil {
			if lookupCtx.Err() != nil {
				return nil, status.FromContextError(lookupCtx.Err()).Err()
			}
			return nil, status.Error(codes.Unavailable, "identity lookup unavailable")
		}
		if !active {
			return nil, status.Error(codes.PermissionDenied, "active identity required")
		}
		if access.ValidateCredential(lookupCtx, policy.DB, policy.Q, actor.UserID, payload.CredentialEpoch) != nil || access.ValidateBrowserSession(lookupCtx, policy.DB, policy.Q, actor.UserID, payload.SessionID) != nil {
			if lookupCtx.Err() != nil {
				return nil, status.FromContextError(lookupCtx.Err()).Err()
			}
			return nil, status.Error(codes.Unauthenticated, "revoked credential")
		}
		if actor.TenantID == "" {
			actor.TenantID, err = policy.DefaultTenantForUser(lookupCtx, actor.UserID)
			if err != nil {
				return nil, status.Error(codes.Unavailable, "tenant resolution unavailable")
			}
		}
		if actor.TenantID != "" {
			member, err := policy.IsTenantMember(lookupCtx, actor.UserID, actor.TenantID)
			if err != nil {
				return nil, status.Error(codes.Unavailable, "tenant resolution unavailable")
			}
			if !member {
				return nil, status.Error(codes.PermissionDenied, "tenant membership required")
			}
		} else if strings.EqualFold(strings.TrimSpace(os.Getenv("LEVARA_TENANT_ENFORCED")), "true") || os.Getenv("LEVARA_TENANT_ENFORCED") == "1" {
			return nil, status.Error(codes.PermissionDenied, "tenant membership required")
		}
	}
	ctx = context.WithValue(ctx, ctxUserIDKey{}, actor.UserID)
	return context.WithValue(ctx, ctxMetadataActorKey{}, actor), nil
}

func authorizeGlobalStorage(ctx context.Context, uid string, policy access.SQLPolicy) error {
	// Raw collection names have no ownership mapping: dataset grants cannot
	// safely authorize this API's reads, deletes, and collection-wide operations.
	superuser, err := policy.IsSuperuser(ctx, uid)
	if err != nil {
		return status.Error(codes.Internal, "authorization lookup failed")
	}
	if !superuser {
		return status.Error(codes.PermissionDenied, "active superuser required")
	}
	active, err := policy.IsActive(ctx, uid)
	if err != nil {
		return status.Error(codes.Internal, "authorization lookup failed")
	}
	if !active {
		return status.Error(codes.PermissionDenied, "active superuser required")
	}
	return nil
}

// authFromMetadata extracts and verifies the JWT. Accepts "Bearer <token>"
// or raw token in the authorization header; gRPC metadata keys are
// lower-cased on transport so we look up the lowercase form.
func authFromMetadata(ctx context.Context, secret string) (*vectorAuth.Payload, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, false
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return nil, false
	}
	token := strings.TrimSpace(vals[0])
	token = strings.TrimPrefix(token, "Bearer ")
	token = strings.TrimPrefix(token, "bearer ")
	p, ok := vectorAuth.VerifyJWT(token, secret)
	if !ok || p.Sub == "" {
		return nil, false
	}
	return p, true
}

// authedStream is a thin grpclib.ServerStream wrapper that overrides
// Context() so handlers reading it see the injected user_id.
type authedStream struct {
	grpclib.ServerStream
	ctx context.Context
}

func (s *authedStream) Context() context.Context { return s.ctx }
