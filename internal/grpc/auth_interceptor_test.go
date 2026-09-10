package grpc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stek0v/levara/pkg/access"
	vectorAuth "github.com/stek0v/levara/pkg/auth"
)

// signJWT produces a valid HS256 token with the given sub/secret/ttl.
// Duplicates the internal/http.createJWT logic to avoid a cross-package
// import from a test file.
func signJWT(t *testing.T, sub, secret string, ttl time.Duration) string {
	t.Helper()
	payload := vectorAuth.Payload{
		Sub: sub,
		Exp: time.Now().Add(ttl).Unix(),
		Iat: time.Now().Unix(),
	}
	return signJWTPayload(t, payload, secret)
}

func signJWTPayload(t *testing.T, payload vectorAuth.Payload, secret string) string {
	t.Helper()
	header := map[string]string{"alg": "HS256", "typ": "JWT"}
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

	interceptor := UnaryAuthInterceptor(secret, true, newGRPCAuthPolicy(t))
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
	interceptor := UnaryAuthInterceptor("s3cret", true, access.SQLPolicy{})
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
	interceptor := UnaryAuthInterceptor("s3cret", true, access.SQLPolicy{})
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

	interceptor := UnaryAuthInterceptor(secret, true, access.SQLPolicy{})
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

	interceptor := UnaryAuthInterceptor("wrong-secret", true, access.SQLPolicy{})
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
	interceptor := UnaryAuthInterceptor("s3cret", false, access.SQLPolicy{})
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

func newGRPCAuthPolicy(t *testing.T) access.SQLPolicy {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, is_superuser BOOLEAN, is_active BOOLEAN);
		INSERT INTO users VALUES ('alice', true, true), ('ordinary-user', false, true), ('disabled-superuser', true, false)`); err != nil {
		t.Fatal(err)
	}
	policy := access.SQLPolicy{DB: db, Q: func(q string) string {
		q = strings.ReplaceAll(q, "$1", "?")
		q = strings.ReplaceAll(q, "$2", "?")
		return strings.ReplaceAll(q, "$3", "?")
	}}
	if err := access.EnsureIdentitySchema(context.Background(), db, policy.Q); err != nil {
		t.Fatal(err)
	}
	if err := access.EnsureBrowserSessionSchema(context.Background(), db, policy.Q); err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestAuthInterceptorsGlobalStoragePolicy(t *testing.T) {
	policy := newGRPCAuthPolicy(t)
	brokenPolicy := newGRPCAuthPolicy(t)
	if err := brokenPolicy.DB.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, user  string
		requireAuth bool
		policy      access.SQLPolicy
		want        codes.Code
	}{
		{"active superuser", "alice", true, policy, codes.OK},
		{"ordinary user", "ordinary-user", true, policy, codes.PermissionDenied},
		{"inactive superuser", "disabled-superuser", true, policy, codes.PermissionDenied},
		{"missing user", "deleted-user", true, policy, codes.PermissionDenied},
		{"empty subject", "", true, policy, codes.Unauthenticated},
		{"nil database", "alice", true, access.SQLPolicy{}, codes.PermissionDenied},
		{"database failure", "alice", true, brokenPolicy, codes.Internal},
		{"dev anonymous", "", false, access.SQLPolicy{}, codes.OK},
		{"dev ordinary user", "ordinary-user", false, brokenPolicy, codes.OK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := ctxWithToken(signJWT(t, tc.user, "secret", time.Hour))
			called := false
			_, err := UnaryAuthInterceptor("secret", tc.requireAuth, tc.policy)(ctx, nil,
				&grpclib.UnaryServerInfo{FullMethod: "/levara.v2.LevaraServiceV2/Insert"},
				func(ctx context.Context, _ any) (any, error) {
					called = true
					if UserIDFromContext(ctx) != tc.user {
						t.Errorf("unary user=%q", UserIDFromContext(ctx))
					}
					return nil, nil
				})
			if status.Code(err) != tc.want || called != (tc.want == codes.OK) {
				t.Fatalf("unary: error=%v called=%v, want %s", err, called, tc.want)
			}
			called = false
			err = StreamAuthInterceptor("secret", tc.requireAuth, tc.policy)(nil, &authedStream{ctx: ctx},
				&grpclib.StreamServerInfo{FullMethod: "/levara.v1.LevaraService/PipelineCognify"},
				func(_ any, stream grpclib.ServerStream) error {
					called = true
					if UserIDFromContext(stream.Context()) != tc.user {
						t.Errorf("stream user=%q", UserIDFromContext(stream.Context()))
					}
					return nil
				})
			if status.Code(err) != tc.want || called != (tc.want == codes.OK) {
				t.Fatalf("stream: error=%v called=%v, want %s", err, called, tc.want)
			}
		})
	}
}

func TestGRPCRejectsRevokedCredentialEpoch(t *testing.T) {
	p := newGRPCAuthPolicy(t)
	if err := access.EnsureIdentitySchema(context.Background(), p.DB, p.Q); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DB.Exec(`INSERT INTO credential_epochs(user_id,epoch) VALUES ('alice',1)`); err != nil {
		t.Fatal(err)
	}
	ctx := ctxWithToken(signJWT(t, "alice", "secret", time.Hour))
	_, err := UnaryAuthInterceptor("secret", true, p)(ctx, nil, &grpclib.UnaryServerInfo{FullMethod: "/levara.v1.LevaraService/Search"}, func(context.Context, any) (any, error) { t.Error("revoked JWT reached unary handler"); return nil, nil })
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("revoked epoch unary=%v", err)
	}
}

func TestGRPCBrowserSessionLifecycle(t *testing.T) {
	p := newGRPCAuthPolicy(t)
	if _, err := p.DB.Exec(`INSERT INTO credential_epochs(user_id,epoch) VALUES ('alice',3)`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO auth_sessions(id,user_id,expires_at,revoked) VALUES ('active','alice',9999999999,false)`,
		`INSERT INTO auth_sessions(id,user_id,expires_at,revoked) VALUES ('revoked','alice',9999999999,true)`,
		`INSERT INTO auth_sessions(id,user_id,expires_at,revoked) VALUES ('expired','alice',1,false)`,
	} {
		if _, err := p.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, sid string
		epoch     int64
		want      codes.Code
	}{
		{"active", "active", 3, codes.OK}, {"programmatic", "", 3, codes.OK}, {"revoked", "revoked", 3, codes.Unauthenticated}, {"expired", "expired", 3, codes.Unauthenticated}, {"unknown", "unknown", 3, codes.Unauthenticated}, {"old epoch", "active", 0, codes.Unauthenticated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := ctxWithToken(signJWTPayload(t, vectorAuth.Payload{Sub: "alice", Exp: time.Now().Add(time.Hour).Unix(), CredentialEpoch: tc.epoch, SessionID: tc.sid}, "secret"))
			called := false
			_, err := UnaryAuthInterceptor("secret", true, p)(ctx, nil, &grpclib.UnaryServerInfo{FullMethod: "/levara.v2.LevaraServiceV2/Search"}, func(context.Context, any) (any, error) { called = true; return nil, nil })
			if status.Code(err) != tc.want || called != (tc.want == codes.OK) {
				t.Fatalf("unary=%v called=%v", err, called)
			}
			called = false
			err = StreamAuthInterceptor("secret", true, p)(nil, &authedStream{ctx: ctx}, &grpclib.StreamServerInfo{FullMethod: "/levara.v1.LevaraService/PipelineCognify"}, func(any, grpclib.ServerStream) error { called = true; return nil })
			if status.Code(err) != tc.want || called != (tc.want == codes.OK) {
				t.Fatalf("stream=%v called=%v", err, called)
			}
		})
	}
}
