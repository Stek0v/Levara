package grpc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/access"
	vectorAuth "github.com/stek0v/levara/pkg/auth"
	pb "github.com/stek0v/levara/proto/pb"
	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestDocumentCognifyRPCDescriptor(t *testing.T) {
	svc := pb.File_levara_proto.Services().ByName("LevaraService")
	for _, name := range []string{"CognifyDocuments", "CognifyDocumentsStatus"} {
		t.Run(name, func(t *testing.T) {
			m := svc.Methods().ByName(protoreflect.Name(name))
			if m == nil {
				t.Fatalf("missing document-scoped RPC %s", name)
			}
			if !m.IsStreamingServer() || m.IsStreamingClient() || m.Output().Name() != "DocumentCognifyStatus" {
				t.Fatalf("invalid stream contract: %v", m)
			}
		})
	}
}

func newDocumentGRPCPolicy(t *testing.T) access.SQLPolicy {
	t.Helper()
	p := newGRPCAuthPolicy(t)
	if _, err := p.DB.Exec(`CREATE TABLE user_tenant (user_id TEXT, tenant_id TEXT);
	 INSERT INTO user_tenant VALUES ('ordinary-user','team')`); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDocumentCognifyScopedStreamAuth(t *testing.T) {
	p := newDocumentGRPCPolicy(t)
	for _, method := range []string{"CognifyDocuments", "CognifyDocumentsStatus"} {
		t.Run(method, func(t *testing.T) {
			ctx := ctxWithToken(signJWT(t, "ordinary-user", "secret", time.Hour))
			called := false
			err := StreamAuthInterceptor("secret", true, p)(nil, &authedStream{ctx: ctx},
				&grpclib.StreamServerInfo{FullMethod: "/levara.v1.LevaraService/" + method},
				func(_ any, stream grpclib.ServerStream) error {
					called = true
					actor, ok := stream.Context().Value(ctxMetadataActorKey{}).(access.MetadataActor)
					if !ok || actor.UserID != "ordinary-user" || actor.TenantID != "team" || actor.TrustedLocal || actor.Credential.Kind != "jwt" || actor.Credential.ExpiresAt <= time.Now().Unix() {
						t.Errorf("missing verified scoped identity: %+v", actor)
					}
					deadline, ok := stream.Context().Deadline()
					if !ok || time.Until(deadline) > 30*time.Minute || time.Until(deadline) < 29*time.Minute {
						t.Errorf("observer deadline = %v (present=%v)", deadline, ok)
					}
					return nil
				})
			if err != nil || !called {
				t.Fatalf("ordinary scoped user rejected: %v, called=%v", err, called)
			}
		})
	}
}

func TestDocumentCognifyForeignTenantRejected(t *testing.T) {
	p := newDocumentGRPCPolicy(t)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+signJWT(t, "ordinary-user", "secret", time.Hour), "x-tenant-id", "foreign"))
	for _, method := range []string{"CognifyDocuments", "CognifyDocumentsStatus"} {
		err := StreamAuthInterceptor("secret", true, p)(nil, &authedStream{ctx: ctx}, &grpclib.StreamServerInfo{FullMethod: "/levara.v1.LevaraService/" + method}, func(any, grpclib.ServerStream) error {
			t.Error("foreign tenant reached handler")
			return nil
		})
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("%s: %v", method, err)
		}
	}
}

func TestDocumentCognifyScopedIdentityFailures(t *testing.T) {
	p := newDocumentGRPCPolicy(t)
	for _, query := range []string{
		`INSERT INTO users VALUES ('revoked-user',false,true),('inactive-user',false,false)`,
		`INSERT INTO credential_epochs(user_id,epoch) VALUES ('revoked-user',1)`,
		`INSERT INTO auth_sessions(id,user_id,expires_at,revoked) VALUES ('revoked','ordinary-user',9999999999,true),('expired','ordinary-user',1,false),('active','ordinary-user',9999999999,false)`,
	} {
		if _, err := p.DB.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, user, session string
		epoch               int64
		want                codes.Code
	}{
		{name: "ordinary", user: "ordinary-user", want: codes.OK},
		{name: "active browser", user: "ordinary-user", session: "active", want: codes.OK},
		{name: "missing user", user: "gone", want: codes.PermissionDenied},
		{name: "inactive", user: "inactive-user", want: codes.PermissionDenied},
		{name: "old epoch", user: "revoked-user", want: codes.Unauthenticated},
		{name: "new epoch", user: "revoked-user", epoch: 1, want: codes.OK},
		{name: "revoked session", user: "ordinary-user", session: "revoked", want: codes.Unauthenticated},
		{name: "expired session", user: "ordinary-user", session: "expired", want: codes.Unauthenticated},
		{name: "missing session", user: "ordinary-user", session: "missing", want: codes.Unauthenticated},
		{name: "foreign session", user: "alice", session: "active", want: codes.Unauthenticated},
		{name: "empty subject", want: codes.Unauthenticated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := ctxWithToken(signJWTPayload(t, vectorAuth.Payload{Sub: tc.user, SessionID: tc.session, CredentialEpoch: tc.epoch, Iat: time.Now().Unix(), Exp: time.Now().Add(time.Hour).Unix()}, "secret"))
			for _, method := range []string{"CognifyDocuments", "CognifyDocumentsStatus", "IngestData"} {
				called := false
				check := func(ctx context.Context) {
					called = true
					a, ok := ctx.Value(ctxMetadataActorKey{}).(access.MetadataActor)
					if !ok || a.UserID != tc.user || a.Credential.SessionID != tc.session || a.Credential.Epoch != tc.epoch || a.TrustedLocal {
						t.Errorf("lost proof: %+v", a)
					}
				}
				var err error
				if method == "IngestData" {
					_, err = UnaryAuthInterceptor("secret", true, p)(ctx, nil, &grpclib.UnaryServerInfo{FullMethod: "/levara.v1.LevaraService/" + method}, func(ctx context.Context, _ any) (any, error) { check(ctx); return nil, nil })
				} else {
					err = StreamAuthInterceptor("secret", true, p)(nil, &authedStream{ctx: ctx}, &grpclib.StreamServerInfo{FullMethod: "/levara.v1.LevaraService/" + method}, func(_ any, ss grpclib.ServerStream) error { check(ss.Context()); return nil })
				}
				if status.Code(err) != tc.want || called != (tc.want == codes.OK) {
					t.Errorf("%s err=%v called=%v", method, err, called)
				}
			}
		})
	}
}

func TestDocumentCognifyScopedTenantValidation(t *testing.T) {
	p := newDocumentGRPCPolicy(t)
	for _, tc := range []struct {
		name, user string
		tenants    []string
		enforced   string
		want       codes.Code
		resolved   string
	}{
		{name: "default", user: "ordinary-user", want: codes.OK, resolved: "team"},
		{name: "explicit", user: "ordinary-user", tenants: []string{"team"}, want: codes.OK, resolved: "team"},
		{name: "foreign", user: "ordinary-user", tenants: []string{"foreign"}, want: codes.PermissionDenied},
		{name: "admin foreign", user: "alice", tenants: []string{"team"}, want: codes.PermissionDenied},
		{name: "empty", user: "ordinary-user", tenants: []string{""}, want: codes.InvalidArgument},
		{name: "multiple", user: "ordinary-user", tenants: []string{"team", "team"}, want: codes.InvalidArgument},
		{name: "long", user: "ordinary-user", tenants: []string{strings.Repeat("t", 257)}, want: codes.InvalidArgument},
		{name: "control", user: "ordinary-user", tenants: []string{"team\n"}, want: codes.InvalidArgument},
		{name: "whitespace", user: "ordinary-user", tenants: []string{" team"}, want: codes.InvalidArgument},
		{name: "enforced missing", user: "alice", enforced: "true", want: codes.PermissionDenied},
		{name: "enforced member", user: "ordinary-user", enforced: "1", want: codes.OK, resolved: "team"},
		{name: "unselected legacy", user: "alice", want: codes.OK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LEVARA_TENANT_ENFORCED", tc.enforced)
			md := metadata.Pairs("authorization", "Bearer "+signJWT(t, tc.user, "secret", time.Hour))
			if tc.tenants != nil {
				md.Set("x-tenant-id", tc.tenants...)
			}
			ctx := metadata.NewIncomingContext(context.Background(), md)
			called := false
			err := StreamAuthInterceptor("secret", true, p)(nil, &authedStream{ctx: ctx}, &grpclib.StreamServerInfo{FullMethod: "/levara.v1.LevaraService/CognifyDocuments"}, func(_ any, ss grpclib.ServerStream) error {
				called = true
				a := ss.Context().Value(ctxMetadataActorKey{}).(access.MetadataActor)
				if a.TenantID != tc.resolved {
					t.Errorf("tenant=%q", a.TenantID)
				}
				return nil
			})
			if status.Code(err) != tc.want || called != (tc.want == codes.OK) {
				t.Fatalf("err=%v called=%v", err, called)
			}
		})
	}
}

func TestDocumentCognifyScopedAuthUnavailableAndDeadline(t *testing.T) {
	p := newDocumentGRPCPolicy(t)
	ctx := ctxWithToken(signJWT(t, "ordinary-user", "secret", time.Hour))
	run := func(ctx context.Context, p access.SQLPolicy, required bool) error {
		return StreamAuthInterceptor("secret", required, p)(nil, &authedStream{ctx: ctx}, &grpclib.StreamServerInfo{FullMethod: "/levara.v1.LevaraService/CognifyDocuments"}, func(_ any, s grpclib.ServerStream) error {
			if required {
				t.Error("unavailable identity lookup reached handler")
			} else {
				a, ok := s.Context().Value(ctxMetadataActorKey{}).(access.MetadataActor)
				if !ok || !a.TrustedLocal {
					t.Errorf("no-auth actor=%+v", a)
				}
			}
			return nil
		})
	}
	if err := run(context.Background(), p, true); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing JWT=%v", err)
	}
	if err := run(ctx, access.SQLPolicy{}, true); status.Code(err) != codes.Unavailable {
		t.Fatalf("nil DB=%v", err)
	}
	conn, err := p.DB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	started := time.Now()
	err = run(bounded, p, true)
	cancel()
	conn.Close()
	if status.Code(err) != codes.DeadlineExceeded || time.Since(started) > time.Second {
		t.Fatalf("held pool err=%v elapsed=%v", err, time.Since(started))
	}
	if err := p.DB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, p, true); status.Code(err) != codes.Unavailable {
		t.Fatalf("closed DB=%v", err)
	}
	if err := run(context.Background(), p, false); err != nil {
		t.Fatalf("explicit dev=%v", err)
	}
	if err := run(ctx, p, false); err != nil {
		t.Fatalf("explicit dev with verified token=%v", err)
	}
}
