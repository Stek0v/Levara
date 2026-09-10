package http

import (
	"context"
	"errors"
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
	afterSave              func()
	saveCalls, failAt      int
	blockAt                int
	blocked, release, done chan struct{}
}

func (s *grpcIngestStorage) Save(ctx context.Context, key string, r io.Reader) error {
	s.saveCalls++
	call := s.saveCalls
	if call == s.failAt {
		return errors.New("injected storage failure")
	}
	if call == s.blockAt {
		close(s.blocked)
		<-s.release // Simulate a backend that ignores request cancellation.
	}
	if err := s.memStorage.Save(ctx, key, r); err != nil {
		return err
	}
	if call == s.blockAt {
		close(s.done)
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
		for name, invalid := range map[string]*pb.IngestDataReq{
			"invalid later item": {DatasetName: "grpc-owned", Items: []*pb.IngestItem{{Text: "valid"}, {}}},
			"dual payload":       {DatasetName: "grpc-owned", Items: []*pb.IngestItem{{Text: "text", FileData: []byte("file")}}},
			"mixed datasets":     {DatasetName: "grpc-owned", Items: []*pb.IngestItem{{Text: "one"}, {Text: "two", DatasetName: "other"}}},
			"unsafe source ID":   {DatasetName: "grpc-owned", Items: []*pb.IngestItem{{Id: "../escape", Text: "text"}}},
		} {
			if _, err := client.IngestData(ctx, invalid); status.Code(err) != codes.InvalidArgument || len(backend.objects) != 0 {
				t.Fatalf("%s reached storage: code=%s objects=%d", name, status.Code(err), len(backend.objects))
			}
		}

		request := &pb.IngestDataReq{Items: []*pb.IngestItem{{Text: "Консольный закрытый документ", Filename: "документ.txt"}}, DatasetName: "grpc-owned", OwnerId: "foreign", StoragePath: "/must-not-write-here"}
		response, err := client.IngestData(ctx, request)
		if err != nil || len(response.GetResults()) != 1 || response.DbRowsWritten != 1 || response.Results[0].FilePath != "" {
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
		idOnly, err := client.IngestData(ctx, &pb.IngestDataReq{DatasetId: response.DatasetId, Items: []*pb.IngestItem{{Text: "dataset selected only by ID"}}})
		if err != nil || idOnly.DatasetId != response.DatasetId {
			t.Fatalf("ID-only dataset selection: response=%+v err=%v", idOnly, err)
		}
		itemNamed, err := client.IngestData(ctx, &pb.IngestDataReq{Items: []*pb.IngestItem{{Text: "item-level compatible dataset", DatasetName: "grpc-item-named"}}})
		if err != nil || itemNamed.DatasetId == "" {
			t.Fatalf("item-level dataset selection: response=%+v err=%v", itemNamed, err)
		}
		objects = len(backend.objects)
		if _, err := client.IngestData(ctx, &pb.IngestDataReq{DatasetId: response.DatasetId, DatasetName: "wrong-name", Items: []*pb.IngestItem{{Text: "ID/name conflict"}}}); status.Code(err) != codes.FailedPrecondition || len(backend.objects) != objects {
			t.Fatalf("ID/name conflict reached storage: code=%s objects=%d/%d", status.Code(err), len(backend.objects), objects)
		}
		if _, err := client.IngestData(ctx, &pb.IngestDataReq{DatasetId: "missing-dataset", Items: []*pb.IngestItem{{Text: "missing"}}}); status.Code(err) != codes.InvalidArgument || len(backend.objects) != objects {
			t.Fatalf("missing ID reached storage: code=%s objects=%d/%d", status.Code(err), len(backend.objects), objects)
		}
		batch, err := client.IngestData(ctx, &pb.IngestDataReq{DatasetId: response.DatasetId, Items: []*pb.IngestItem{{Text: "batch text"}, {FileData: []byte("batch file bytes"), Filename: "batch.bin"}}})
		if err != nil || len(batch.GetResults()) != 2 {
			t.Fatalf("batch ingest: response=%+v err=%v", batch, err)
		}
		objects = len(backend.objects)
		duplicate, err := client.IngestData(ctx, &pb.IngestDataReq{DatasetId: response.DatasetId, Items: []*pb.IngestItem{{Text: "duplicate in one request"}, {Text: "duplicate in one request"}}})
		if err != nil || len(duplicate.GetResults()) != 2 || duplicate.DbRowsWritten != 2 || len(backend.objects) != objects+1 {
			t.Fatalf("duplicate batch: response=%+v err=%v objects=%d/%d", duplicate, err, len(backend.objects), objects+1)
		}

		rowCount := func(query string, args ...any) int {
			t.Helper()
			var n int
			if err := f.db.QueryRow(Q(query), args...).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		objects, rows := len(backend.objects), rowCount("SELECT COUNT(*) FROM data")
		backend.failAt = backend.saveCalls + 2
		failed := &pb.IngestDataReq{DatasetId: response.DatasetId, Items: []*pb.IngestItem{{Text: "partial first"}, {Text: "partial second"}}}
		if _, err := client.IngestData(ctx, failed); status.Code(err) != codes.Unavailable || len(backend.objects) != objects || rowCount("SELECT COUNT(*) FROM data") != rows || rowCount("SELECT COUNT(*) FROM ingest_pending_uploads") != 0 {
			t.Fatalf("partial backend failure leaked state: code=%s objects=%d/%d rows=%d/%d pending=%d", status.Code(err), len(backend.objects), objects, rowCount("SELECT COUNT(*) FROM data"), rows, rowCount("SELECT COUNT(*) FROM ingest_pending_uploads"))
		}
		backend.failAt = 0

		backend.blockAt = backend.saveCalls + 1
		backend.blocked, backend.release, backend.done = make(chan struct{}), make(chan struct{}), make(chan struct{})
		lateCtx, lateCancel := context.WithCancel(metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+createJWT("owner", "owner@test.invalid", "grpc-ingest-test")))
		lateErr := make(chan error, 1)
		go func() {
			_, err := client.IngestData(lateCtx, &pb.IngestDataReq{DatasetId: response.DatasetId, Items: []*pb.IngestItem{{Text: "late canceled worker"}}})
			lateErr <- err
		}()
		select {
		case <-backend.blocked:
		case <-time.After(2 * time.Second):
			t.Fatal("late backend was not reached")
		}
		lateCancel()
		if err := <-lateErr; status.Code(err) != codes.Canceled {
			t.Fatalf("canceled client status=%s err=%v", status.Code(err), err)
		}
		close(backend.release)
		select {
		case <-backend.done:
		case <-time.After(2 * time.Second):
			t.Fatal("late backend did not return")
		}
		deadline := time.Now().Add(2 * time.Second)
		for rowCount("SELECT COUNT(*) FROM ingest_pending_uploads") != 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if len(backend.objects) != objects || rowCount("SELECT COUNT(*) FROM data") != rows || rowCount("SELECT COUNT(*) FROM ingest_pending_uploads") != 0 {
			t.Fatalf("late canceled worker published: objects=%d/%d rows=%d/%d pending=%d", len(backend.objects), objects, rowCount("SELECT COUNT(*) FROM data"), rows, rowCount("SELECT COUNT(*) FROM ingest_pending_uploads"))
		}
		backend.blockAt = 0

		foreignTenant := metadata.AppendToOutgoingContext(ctx, "x-tenant-id", "b")
		objects = len(backend.objects)
		if _, err := client.IngestData(foreignTenant, request); status.Code(err) != codes.PermissionDenied || len(backend.objects) != objects {
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
		if err := f.db.QueryRow(Q("SELECT COUNT(*) FROM dataset_data WHERE dataset_id=$1"), response.DatasetId).Scan(&count); err != nil || count != 6 {
			t.Fatalf("revoked metadata published %d %v", count, err)
		}
	})
}
