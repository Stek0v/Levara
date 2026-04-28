package grpc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	vectorAuth "github.com/stek0v/levara/pkg/auth"
)

// signJWT produces a valid HS256 token with the given sub/secret/ttl.
// Duplicates the internal/http.createJWT logic to avoid a cross-package
// import from a test file.
func signJWT(t *testing.T, sub, secret string, ttl time.Duration) string {
	t.Helper()
	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	payload := vectorAuth.Payload{
		Sub: sub,
		Exp: time.Now().Add(ttl).Unix(),
		Iat: time.Now().Unix(),
	}
	hJSON, _ := json.Marshal(header)
	pJSON, _ := json.Marshal(payload)
	hEnc := base64.RawURLEncoding.EncodeToString(hJSON)
	pEnc := base64.RawURLEncoding.EncodeToString(pJSON)
	sigInput := hEnc + "." + pEnc
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(sigInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return sigInput + "." + sig
}

func ctxWithToken(token string) context.Context {
	return metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("authorization", "Bearer "+token),
	)
}

func TestUnaryAuthInterceptor_ValidTokenInjectsUserID(t *testing.T) {
	secret := "s3cret"
	tok := signJWT(t, "alice", secret, time.Hour)
	ctx := ctxWithToken(tok)

	interceptor := UnaryAuthInterceptor(secret, true)
	info := &grpclib.UnaryServerInfo{FullMethod: "/levara.v1.LevaraService/Search"}

	var gotUID string
	_, err := interceptor(ctx, nil, info, func(ctx context.Context, req any) (any, error) {
		gotUID = UserIDFromContext(ctx)
		return nil, nil
	})
	if err != nil {
		t.Fatalf("valid token unexpected err: %v", err)
	}
	if gotUID != "alice" {
		t.Errorf("user_id = %q, want alice", gotUID)
	}
}

func TestUnaryAuthInterceptor_MissingTokenRejected(t *testing.T) {
	interceptor := UnaryAuthInterceptor("s3cret", true)
	info := &grpclib.UnaryServerInfo{FullMethod: "/levara.v1.LevaraService/Search"}

	_, err := interceptor(context.Background(), nil, info, func(context.Context, any) (any, error) {
		t.Fatal("handler should not have been called")
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected Unauthenticated, got nil")
	}
	if s, _ := status.FromError(err); s.Code() != codes.Unauthenticated {
		t.Errorf("code = %v, want Unauthenticated", s.Code())
	}
}

func TestUnaryAuthInterceptor_WhitelistedMethodBypassesAuth(t *testing.T) {
	interceptor := UnaryAuthInterceptor("s3cret", true)
	info := &grpclib.UnaryServerInfo{FullMethod: "/levara.v1.LevaraService/Info"}

	called := false
	_, err := interceptor(context.Background(), nil, info, func(context.Context, any) (any, error) {
		called = true
		return nil, nil
	})
	if err != nil {
		t.Fatalf("whitelisted method returned err: %v", err)
	}
	if !called {
		t.Fatal("whitelisted handler was not called")
	}
}

func TestUnaryAuthInterceptor_ExpiredTokenRejected(t *testing.T) {
	secret := "s3cret"
	tok := signJWT(t, "alice", secret, -time.Hour) // already expired
	ctx := ctxWithToken(tok)

	interceptor := UnaryAuthInterceptor(secret, true)
	info := &grpclib.UnaryServerInfo{FullMethod: "/levara.v1.LevaraService/Search"}
	_, err := interceptor(ctx, nil, info, func(context.Context, any) (any, error) {
		t.Fatal("expired token should not reach handler")
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected rejection on expired token")
	}
}

func TestUnaryAuthInterceptor_WrongSecretRejected(t *testing.T) {
	tok := signJWT(t, "alice", "right-secret", time.Hour)
	ctx := ctxWithToken(tok)

	interceptor := UnaryAuthInterceptor("wrong-secret", true)
	info := &grpclib.UnaryServerInfo{FullMethod: "/levara.v1.LevaraService/Search"}
	_, err := interceptor(ctx, nil, info, func(context.Context, any) (any, error) {
		t.Fatal("wrong-secret token should not reach handler")
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected rejection on wrong secret")
	}
}

// Permissive mode: missing token doesn't reject — useful for dev where
// some clients haven't been upgraded to send tokens yet.
func TestUnaryAuthInterceptor_PermissiveModeAllowsAnon(t *testing.T) {
	interceptor := UnaryAuthInterceptor("s3cret", false)
	info := &grpclib.UnaryServerInfo{FullMethod: "/levara.v1.LevaraService/Search"}

	called := false
	var gotUID string
	_, err := interceptor(context.Background(), nil, info, func(ctx context.Context, req any) (any, error) {
		called = true
		gotUID = UserIDFromContext(ctx)
		return nil, nil
	})
	if err != nil {
		t.Fatalf("permissive mode rejected: %v", err)
	}
	if !called {
		t.Fatal("handler was not called in permissive mode")
	}
	if gotUID != "" {
		t.Errorf("anonymous call should have empty user_id, got %q", gotUID)
	}
}
