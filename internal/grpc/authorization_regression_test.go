package grpc

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stek0v/levara/internal/store"
	pb "github.com/stek0v/levara/proto/pb"
	pbv2 "github.com/stek0v/levara/proto/pb/v2"
	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestGRPCAuthenticatedUserCannotAccessGlobalCollections(t *testing.T) {
	const secret = "isolated-regression-secret"
	collections, err := store.NewCollectionManager(2, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { collections.Close() })
	if err := collections.Insert("private", "secret", []float32{1, 0}, map[string]any{"owner_id": "victim", "text": "private contents"}); err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() { lis.Close() })
	policy := newGRPCAuthPolicy(t)
	srv := grpclib.NewServer(
		grpclib.UnaryInterceptor(UnaryAuthInterceptor(secret, true, policy)),
		grpclib.StreamInterceptor(StreamAuthInterceptor(secret, true, policy)),
	)
	service := NewService(collections, store.NewCluster(nil), 2)
	pb.RegisterLevaraServiceServer(srv, service)
	pbv2.RegisterLevaraServiceV2Server(srv, NewServiceV2(service))
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpclib.NewClient("passthrough:///regression",
		grpclib.WithTransportCredentials(insecure.NewCredentials()),
		grpclib.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+signJWT(t, "ordinary-user", secret, time.Hour)))
	v1, v2 := pb.NewLevaraServiceClient(conn), pbv2.NewLevaraServiceV2Client(conn)
	t.Run("public info", func(t *testing.T) {
		resp, err := v1.Info(context.Background(), &pb.Empty{})
		if err != nil || resp.GetStatus() != "ready" {
			t.Fatalf("public Info unavailable: response=%v error=%v", resp, err)
		}
		if len(resp.GetCollections()) != 0 {
			t.Errorf("public Info exposes collections: %v", resp.GetCollections())
		}
		if info, err := v2.Info(context.Background(), &pbv2.InfoReq{}); err != nil || info.GetVersion() != "v2" {
			t.Errorf("public v2 Info unavailable: response=%v error=%v", info, err)
		}
	})
	tests := []struct {
		name string
		call func() (any, error)
	}{
		{"v1 list", func() (any, error) { return v1.ListCollections(ctx, &pb.Empty{}) }},
		{"v1 read", func() (any, error) {
			return v1.GetByID(ctx, &pb.GetByIDReq{Collection: "private", Ids: []string{"secret"}})
		}},
		{"v2 delete", func() (any, error) {
			return v2.Delete(ctx, &pbv2.DeleteReq{Collection: "private", Ids: []string{"secret"}})
		}},
		{"v1 drop", func() (any, error) { return v1.DropCollection(ctx, &pb.DropCollectionReq{Name: "private"}) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := tt.call()
			if status.Code(err) != codes.PermissionDenied {
				t.Errorf("ordinary-user RPC = %v, response=%v; want PermissionDenied", err, resp)
			}
		})
	}
	if !collections.Has("private") {
		t.Fatal("ordinary user deleted the private collection")
	}
	db, err := collections.Get("private")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, found := db.Get("secret"); !found {
		t.Fatal("ordinary user deleted the private record")
	}
	adminCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+signJWT(t, "alice", secret, time.Hour)))
	if resp, err := v1.GetByID(adminCtx, &pb.GetByIDReq{Collection: "private", Ids: []string{"secret"}}); err != nil || len(resp.GetRecords()) != 1 || !resp.GetRecords()[0].GetFound() {
		t.Fatalf("superuser v1 read: response=%v error=%v", resp, err)
	}
	if resp, err := v2.Insert(adminCtx, &pbv2.InsertReq{Collection: "private", Id: "admin-record", Vector: []float32{0, 1}}); err != nil || !resp.GetOk() {
		t.Fatalf("superuser v2 insert: response=%v error=%v", resp, err)
	}
}
