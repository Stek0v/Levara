package http

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	vectorgrpc "github.com/stek0v/levara/internal/grpc"
	"github.com/stek0v/levara/pkg/ingest"
	pb "github.com/stek0v/levara/proto/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type grpcIngestStorage struct {
	*memStorage
	afterSave func()
}

func (s *grpcIngestStorage) Save(ctx context.Context, key string, r io.Reader) error {
	if err := s.memStorage.Save(ctx, key, r); err != nil {
		return err
	}
	if s.afterSave != nil {
		s.afterSave()
	}
	return nil
}

func TestGRPCIngestUsesServerStorageIdentityAndLiveMetadata(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ingest.SetSQLiteMode(GetDBProvider() == DBSQLite)
		t.Cleanup(func() { ingest.SetSQLiteMode(false) })
		backend := &grpcIngestStorage{memStorage: newMemStorage()}
		path := filepath.Join(t.TempDir(), "no-local-plaintext")
		svc := vectorgrpc.NewService(nil, nil, 2)
		svc.SetIngestStorage(path, backend)
		svc.SetIngestMetadata(f.db, true, Q)
		server := grpc.NewServer(grpc.UnaryInterceptor(vectorgrpc.UnaryAuthInterceptor("grpc-ingest-test", true, f.p)))
		pb.RegisterLevaraServiceServer(server, svc)
		listener := bufconn.Listen(1 << 20)
		go server.Serve(listener)
		defer server.Stop()
		conn, err := grpc.NewClient("passthrough:///test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		client := pb.NewLevaraServiceClient(conn)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+createJWT("owner", "owner@test.invalid", "grpc-ingest-test"))
		request := &pb.IngestDataReq{Items: []*pb.IngestItem{{Text: "Консольный закрытый документ", Filename: "документ.txt"}}, DatasetName: "grpc-owned", OwnerId: "foreign", StoragePath: "/must-not-write-here"}
		response, err := client.IngestData(ctx, request)
		if err != nil || len(response.GetResults()) != 1 || response.DbRowsWritten != 1 {
			t.Fatalf("gRPC ingest %+v %v", response, err)
		}
		var owner, location string
		if err := f.db.QueryRow(Q("SELECT owner_id,raw_data_location FROM data WHERE id=$1"), response.Results[0].Id).Scan(&owner, &location); err != nil {
			t.Fatal(err)
		}
		if owner != "owner" || !strings.HasPrefix(location, "storage://") {
			t.Fatalf("untrusted storage/owner %q %q", owner, location)
		}
		if string(backend.objects[strings.TrimPrefix(location, "storage://")]) != request.Items[0].Text {
			t.Fatal("backend bytes mismatch")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("gRPC staged local plaintext")
		}
		request.PostgresDsn = "postgres://must-not-connect.invalid/private"
		if _, err := client.IngestData(ctx, request); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("request DSN override %v", err)
		}
		request.PostgresDsn = ""
		objects := len(backend.objects)
		repeat, err := client.IngestData(ctx, request)
		if err != nil || len(repeat.GetResults()) != 1 || !repeat.Results[0].AlreadyExists || len(backend.objects) != objects {
			t.Fatalf("repeat wrote objects: response=%+v err=%v objects=%d/%d", repeat, err, len(backend.objects), objects)
		}
		foreignTenant := metadata.AppendToOutgoingContext(ctx, "x-tenant-id", "b")
		if _, err := client.IngestData(foreignTenant, request); status.Code(err) != codes.FailedPrecondition || len(backend.objects) != objects {
			t.Fatalf("foreign tenant upload reached storage: %v", err)
		}
		request.Items[0].Text = "revocation serialized around publication"
		revoked := make(chan error, 1)
		backend.afterSave = func() {
			go func() {
				_, err := f.db.ExecContext(ctx, "UPDATE users SET is_active=false WHERE id='owner'")
				revoked <- err
			}()
			select {
			case err := <-revoked:
				t.Errorf("revocation crossed active publication fence: %v", err)
				revoked <- err
			case <-time.After(50 * time.Millisecond):
			}
		}
		if _, err := client.IngestData(ctx, request); err != nil {
			t.Fatalf("authorized publication lost race before revoke: %v", err)
		}
		if err := <-revoked; err != nil {
			t.Fatal(err)
		}
		backend.afterSave = nil
		objects = len(backend.objects)
		request.Items[0].Text = "must not save after revoke"
		if _, err := client.IngestData(ctx, request); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("revoked request accepted: %v", err)
		}
		if len(backend.objects) != objects {
			t.Fatal("revoked request saved bytes")
		}
		var count int
		if err := f.db.QueryRow(Q("SELECT COUNT(*) FROM dataset_data WHERE dataset_id=$1"), response.DatasetId).Scan(&count); err != nil || count != 2 {
			t.Fatalf("revoked metadata published %d %v", count, err)
		}
	})
}
