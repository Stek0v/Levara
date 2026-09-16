package grpc

import (
	"context"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/access"
	pb "github.com/stek0v/levara/proto/pb"
	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

type documentStatusStream struct {
	grpclib.ServerStream
	ctx  context.Context
	send func(*pb.DocumentCognifyStatus) error
}

func (s *documentStatusStream) Context() context.Context { return s.ctx }
func (s *documentStatusStream) Send(v *pb.DocumentCognifyStatus) error {
	if s.send != nil {
		return s.send(v)
	}
	return nil
}
func validDocumentRequest() *pb.DocumentCognifyReq {
	return &pb.DocumentCognifyReq{Collection: "team_docs", Documents: []*pb.DocumentCognifySource{{DatasetId: "dataset", DocumentId: "document", SourceRevision: 7, RawContentHash: strings.Repeat("ab", 32)}}}
}
func documentTestService(t *testing.T) *Service {
	t.Helper()
	s := NewService(nil, nil, 2)
	p := newDocumentGRPCPolicy(t)
	s.SetIngestMetadata(p.DB, false, p.Q)
	return s
}

func TestDocumentCognifyValidatesEntireBatchBeforeCallbacks(t *testing.T) {
	tests := map[string]func(*pb.DocumentCognifyReq){
		"nil item": func(r *pb.DocumentCognifyReq) { r.Documents = append(r.Documents, nil) },
		"invalid last item": func(r *pb.DocumentCognifyReq) {
			r.Documents = append(r.Documents, &pb.DocumentCognifySource{DatasetId: "dataset", DocumentId: "other", SourceRevision: 1, RawContentHash: "bad"})
		},
		"empty batch": func(r *pb.DocumentCognifyReq) { r.Documents = nil },
		"too many": func(r *pb.DocumentCognifyReq) {
			for len(r.Documents) < 101 {
				r.Documents = append(r.Documents, r.Documents[0])
			}
		},
		"empty collection":          func(r *pb.DocumentCognifyReq) { r.Collection = "" },
		"traversal":                 func(r *pb.DocumentCognifyReq) { r.Collection = "../private" },
		"absolute":                  func(r *pb.DocumentCognifyReq) { r.Collection = "/private" },
		"backslash":                 func(r *pb.DocumentCognifyReq) { r.Collection = `dir\private` },
		"dot":                       func(r *pb.DocumentCognifyReq) { r.Collection = "." },
		"dotdot":                    func(r *pb.DocumentCognifyReq) { r.Collection = ".." },
		"control":                   func(r *pb.DocumentCognifyReq) { r.Collection = "safe\x00evil" },
		"whitespace":                func(r *pb.DocumentCognifyReq) { r.Collection = " safe" },
		"long collection":           func(r *pb.DocumentCognifyReq) { r.Collection = strings.Repeat("a", 257) },
		"invalid mode":              func(r *pb.DocumentCognifyReq) { r.Mode = "raw" },
		"case mode":                 func(r *pb.DocumentCognifyReq) { r.Mode = "RAG" },
		"empty dataset":             func(r *pb.DocumentCognifyReq) { r.Documents[0].DatasetId = "" },
		"empty document":            func(r *pb.DocumentCognifyReq) { r.Documents[0].DocumentId = "" },
		"invalid utf8":              func(r *pb.DocumentCognifyReq) { r.Documents[0].DocumentId = "doc\xff" },
		"zero source revision":      func(r *pb.DocumentCognifyReq) { r.Documents[0].SourceRevision = 0 },
		"negative source revision":  func(r *pb.DocumentCognifyReq) { r.Documents[0].SourceRevision = -1 },
		"negative content revision": func(r *pb.DocumentCognifyReq) { r.Documents[0].ContentRevision = -1 },
		"empty hash":                func(r *pb.DocumentCognifyReq) { r.Documents[0].RawContentHash = "" },
		"nonhex hash":               func(r *pb.DocumentCognifyReq) { r.Documents[0].RawContentHash = strings.Repeat("z", 64) },
		"short hash":                func(r *pb.DocumentCognifyReq) { r.Documents[0].RawContentHash = strings.Repeat("a", 63) },
		"conflicting source revision": func(r *pb.DocumentCognifyReq) {
			copy := proto.Clone(r.Documents[0]).(*pb.DocumentCognifySource)
			copy.SourceRevision++
			r.Documents = append(r.Documents, copy)
		},
		"conflicting content revision": func(r *pb.DocumentCognifyReq) {
			copy := proto.Clone(r.Documents[0]).(*pb.DocumentCognifySource)
			copy.ContentRevision = 1
			r.Documents = append(r.Documents, copy)
		},
		"conflicting hash": func(r *pb.DocumentCognifyReq) {
			copy := proto.Clone(r.Documents[0]).(*pb.DocumentCognifySource)
			copy.RawContentHash = strings.Repeat("c", 64)
			r.Documents = append(r.Documents, copy)
		},
	}
	s := documentTestService(t)
	s.SetDocumentCognify(func(context.Context, access.MetadataActor, *pb.DocumentCognifyReq) (string, error) {
		t.Error("invalid batch started")
		return "run", nil
	}, func(context.Context, access.MetadataActor, string, func(*pb.DocumentCognifyStatus) error) error {
		t.Error("invalid batch watched")
		return nil
	})
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			r := validDocumentRequest()
			mutate(r)
			if err := s.CognifyDocuments(r, &documentStatusStream{ctx: context.Background()}); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if err := s.CognifyDocuments(nil, nil); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("nil=%v", err)
	}
}

func TestDocumentCognifyNormalizesAndCopies(t *testing.T) {
	s := documentTestService(t)
	req := validDocumentRequest()
	req.Documents[0].RawContentHash = strings.ToUpper(req.Documents[0].RawContentHash)
	req.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
	req.Documents[0].ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
	for len(req.Documents) < 100 {
		req.Documents = append(req.Documents, proto.Clone(req.Documents[0]).(*pb.DocumentCognifySource))
	}
	original := proto.Clone(req)
	starts, watches := 0, 0
	s.SetDocumentCognify(func(ctx context.Context, a access.MetadataActor, r *pb.DocumentCognifyReq) (string, error) {
		starts++
		if !a.TrustedLocal || r.Mode != "rag" || len(r.Documents) != 1 || r.Documents[0].RawContentHash != strings.Repeat("ab", 32) || len(r.ProtoReflect().GetUnknown()) != 0 || len(r.Documents[0].ProtoReflect().GetUnknown()) != 0 {
			t.Errorf("invalid normalized request=%v actor=%+v", r, a)
		}
		r.Documents[0].DocumentId = "changed"
		return "run", nil
	}, func(_ context.Context, _ access.MetadataActor, id string, send func(*pb.DocumentCognifyStatus) error) error {
		watches++
		if id != "run" {
			t.Errorf("run=%q", id)
		}
		return send(&pb.DocumentCognifyStatus{PipelineRunId: id, Status: "COMPLETED"})
	})
	if err := s.CognifyDocuments(req, &documentStatusStream{ctx: context.Background()}); err != nil {
		t.Fatal(err)
	}
	if starts != 1 || watches != 1 || !proto.Equal(req, original) {
		t.Fatalf("starts=%d watches=%d client request mutated=%v", starts, watches, !proto.Equal(req, original))
	}
	r := validDocumentRequest()
	r.Mode = "graph"
	r.Documents = append(r.Documents, &pb.DocumentCognifySource{DatasetId: "other", DocumentId: "document", SourceRevision: 1, RawContentHash: strings.Repeat("c", 64), ContentRevision: 3})
	validated, err := validateDocumentCognify(r)
	if err != nil || validated.Mode != "graph" || len(validated.Documents) != 2 || validated.Documents[1].ContentRevision != 3 {
		t.Fatalf("distinct refs=%v err=%v", validated, err)
	}
}

func TestDocumentCognifyRequiredDependencies(t *testing.T) {
	for _, kind := range []string{"start", "status"} {
		t.Run(kind, func(t *testing.T) {
			call := func(s *Service, ctx context.Context) error {
				stream := &documentStatusStream{ctx: ctx}
				if kind == "start" {
					return s.CognifyDocuments(validDocumentRequest(), stream)
				}
				return s.CognifyDocumentsStatus(&pb.DocumentCognifyStatusReq{PipelineRunId: "run"}, stream)
			}
			s := NewService(nil, nil, 2)
			if err := call(s, context.Background()); status.Code(err) != codes.Unavailable {
				t.Fatalf("nil metadata=%v", err)
			}
			s = documentTestService(t)
			if err := call(s, context.Background()); status.Code(err) != codes.Unavailable {
				t.Fatalf("missing callback=%v", err)
			}
			s.ingestRequireAuth = true
			s.SetDocumentCognify(func(context.Context, access.MetadataActor, *pb.DocumentCognifyReq) (string, error) {
				t.Error("unverified start")
				return "", nil
			}, func(context.Context, access.MetadataActor, string, func(*pb.DocumentCognifyStatus) error) error {
				t.Error("unverified watch")
				return nil
			})
			for _, a := range []access.MetadataActor{{}, {TrustedLocal: true}, {Actor: access.Actor{UserID: "forged"}}, {Actor: access.Actor{UserID: "forged"}, Credential: access.MetadataCredential{Kind: "jwt"}, TrustedLocal: true}} {
				ctx := context.WithValue(context.Background(), ctxMetadataActorKey{}, a)
				if err := call(s, ctx); status.Code(err) != codes.Unauthenticated {
					t.Errorf("actor %+v accepted: %v", a, err)
				}
			}
			if err := call(s, context.Background()); status.Code(err) != codes.Unauthenticated {
				t.Fatalf("absent proof=%v", err)
			}
		})
	}
	s := documentTestService(t)
	for _, id := range []string{"", "../run", " run", "run\x00", strings.Repeat("x", 257)} {
		if err := s.CognifyDocumentsStatus(&pb.DocumentCognifyStatusReq{PipelineRunId: id}, nil); status.Code(err) != codes.InvalidArgument {
			t.Errorf("run=%q error=%v", id, err)
		}
	}
	if err := s.CognifyDocumentsStatus(nil, nil); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("nil status=%v", err)
	}
}

func TestDocumentCognifyCallbackErrors(t *testing.T) {
	s := documentTestService(t)
	for name, tc := range map[string]struct {
		err  error
		want codes.Code
	}{"internal": {errors.New("secret SQL/provider URL"), codes.Unavailable}, "mapped denied": {status.Error(codes.PermissionDenied, "denied"), codes.PermissionDenied}, "canceled": {context.Canceled, codes.Canceled}, "deadline": {context.DeadlineExceeded, codes.DeadlineExceeded}} {
		t.Run(name, func(t *testing.T) {
			s.SetDocumentCognify(func(context.Context, access.MetadataActor, *pb.DocumentCognifyReq) (string, error) { return "", tc.err }, func(context.Context, access.MetadataActor, string, func(*pb.DocumentCognifyStatus) error) error {
				t.Error("watch after failed start")
				return nil
			})
			err := s.CognifyDocuments(validDocumentRequest(), &documentStatusStream{ctx: context.Background()})
			if status.Code(err) != tc.want || strings.Contains(err.Error(), "secret") {
				t.Fatalf("start=%v", err)
			}
			s.SetDocumentCognify(nil, func(context.Context, access.MetadataActor, string, func(*pb.DocumentCognifyStatus) error) error {
				return tc.err
			})
			err = s.CognifyDocumentsStatus(&pb.DocumentCognifyStatusReq{PipelineRunId: "run"}, &documentStatusStream{ctx: context.Background()})
			if status.Code(err) != tc.want || strings.Contains(err.Error(), "secret") {
				t.Fatalf("watch=%v", err)
			}
		})
	}
	s.SetDocumentCognify(func(context.Context, access.MetadataActor, *pb.DocumentCognifyReq) (string, error) { return "", nil }, func(context.Context, access.MetadataActor, string, func(*pb.DocumentCognifyStatus) error) error {
		t.Error("watch invalid run")
		return nil
	})
	if err := s.CognifyDocuments(validDocumentRequest(), &documentStatusStream{ctx: context.Background()}); status.Code(err) != codes.Internal {
		t.Fatalf("invalid callback run=%v", err)
	}
}

func TestDocumentCognifyObserverDeadline(t *testing.T) {
	s := documentTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	s.SetDocumentCognify(func(ctx context.Context, _ access.MetadataActor, _ *pb.DocumentCognifyReq) (string, error) {
		d, ok := ctx.Deadline()
		if !ok || !d.Equal(deadline) {
			t.Errorf("changed parent deadline=%v", d)
		}
		return "run", nil
	}, func(ctx context.Context, _ access.MetadataActor, _ string, _ func(*pb.DocumentCognifyStatus) error) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	})
	if err := s.CognifyDocuments(validDocumentRequest(), &documentStatusStream{ctx: ctx}); status.Code(err) != codes.Canceled {
		t.Fatalf("observer=%v", err)
	}
	s.SetDocumentCognify(func(context.Context, access.MetadataActor, *pb.DocumentCognifyReq) (string, error) {
		t.Error("started canceled request")
		return "run", nil
	}, nil)
	s.documentCognifyWatch = func(context.Context, access.MetadataActor, string, func(*pb.DocumentCognifyStatus) error) error {
		t.Error("watched canceled request")
		return nil
	}
	if err := s.CognifyDocuments(validDocumentRequest(), &documentStatusStream{ctx: ctx}); status.Code(err) != codes.Canceled {
		t.Fatalf("canceled start=%v", err)
	}
}

func TestDocumentCognifyWireProgressAndStatus(t *testing.T) {
	policy := newDocumentGRPCPolicy(t)
	s := NewService(nil, nil, 2)
	s.SetIngestMetadata(policy.DB, true, policy.Q)
	proof := validDocumentRequest().Documents[0]
	proof.ContentRevision = 2
	frames := []*pb.DocumentCognifyStatus{
		{PipelineRunId: "run-id", Status: "RUNNING", Stage: "chunking", Message: "working", ChunksCreated: 3, EntitiesExtracted: 2, EdgesExtracted: 1, ElapsedMs: 8, Sources: []*pb.DocumentCognifySource{proof}, StartedAtUnixMs: 123456},
		{PipelineRunId: "run-id", Status: "COMPLETED", Stage: "complete", ChunksCreated: 4, Sources: []*pb.DocumentCognifySource{proof}, StartedAtUnixMs: 123456},
	}
	s.SetDocumentCognify(func(_ context.Context, a access.MetadataActor, r *pb.DocumentCognifyReq) (string, error) {
		if a.UserID != "ordinary-user" || a.TenantID != "team" || a.TrustedLocal || r.Mode != "rag" {
			return "", errors.New("wrong actor/request")
		}
		return "run-id", nil
	}, func(_ context.Context, a access.MetadataActor, id string, send func(*pb.DocumentCognifyStatus) error) error {
		if id != "run-id" || a.UserID != "ordinary-user" || a.TenantID != "team" {
			return errors.New("wrong watch scope")
		}
		for _, frame := range frames {
			if err := send(frame); err != nil {
				return err
			}
		}
		return nil
	})
	listener := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() { listener.Close() })
	server := grpclib.NewServer(grpclib.StreamInterceptor(StreamAuthInterceptor("secret", true, policy)))
	pb.RegisterLevaraServiceServer(server, s)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpclib.NewClient("passthrough:///document-test", grpclib.WithTransportCredentials(insecure.NewCredentials()), grpclib.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	client := pb.NewLevaraServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+signJWT(t, "ordinary-user", "secret", time.Hour)))
	for _, kind := range []string{"start", "status"} {
		t.Run(kind, func(t *testing.T) {
			var stream interface {
				Recv() (*pb.DocumentCognifyStatus, error)
			}
			var err error
			if kind == "start" {
				stream, err = client.CognifyDocuments(ctx, validDocumentRequest())
			} else {
				stream, err = client.CognifyDocumentsStatus(ctx, &pb.DocumentCognifyStatusReq{PipelineRunId: "run-id"})
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range frames {
				got, err := stream.Recv()
				if err != nil || !proto.Equal(got, want) {
					t.Fatalf("frame=%v err=%v want=%v", got, err, want)
				}
			}
			if _, err := stream.Recv(); err != io.EOF {
				t.Fatalf("terminal=%v", err)
			}
		})
	}
	if !reflect.DeepEqual(publicMethods, map[string]bool{"/levara.v1.LevaraService/Info": true, "/levara.v2.LevaraServiceV2/Info": true}) {
		t.Fatal("public RPC allowlist changed")
	}
}

func TestDocumentCognifyBlockedSendObserverDeadline(t *testing.T) {
	s := NewService(nil, nil, 2)
	transportCtx, transportCancel := context.WithCancel(context.Background())
	defer transportCancel()
	// The real transport has no deadline; only our observer does.
	ctx, cancel := context.WithTimeout(transportCtx, 60*time.Millisecond)
	defer cancel()
	sendStarted, watchDone := make(chan struct{}), make(chan struct{})
	lateSend := false
	s.SetDocumentCognify(nil, func(ctx context.Context, _ access.MetadataActor, _ string, send func(*pb.DocumentCognifyStatus) error) error {
		defer close(watchDone)
		err := send(&pb.DocumentCognifyStatus{Status: "RUNNING"})
		// Even if an adapter tries to continue after cancellation, no late frame
		// may reach the transport. This is sequential, never two concurrent sends.
		if err2 := send(&pb.DocumentCognifyStatus{Status: "COMPLETED"}); !errors.Is(err2, context.DeadlineExceeded) {
			t.Errorf("late send=%v", err2)
		}
		return err
	})
	result := make(chan error, 1)
	go func() {
		result <- s.watchDocumentCognify(ctx, access.MetadataActor{}, "run", func(*pb.DocumentCognifyStatus) error {
			select {
			case <-sendStarted:
				lateSend = true
			default:
				close(sendStarted)
			}
			<-transportCtx.Done()
			return transportCtx.Err()
		})
	}()
	select {
	case <-sendStarted:
	case <-time.After(time.Second):
		t.Fatal("Send never started")
	}
	select {
	case err := <-result:
		if status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("handler=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("observer deadline did not return handler")
	}
	select {
	case <-watchDone:
		t.Fatal("watch released before transport Send returned")
	default:
	}
	// gRPC closes its transport when the handler returns.
	transportCancel()
	select {
	case <-watchDone:
	case <-time.After(time.Second):
		t.Fatal("watcher leaked after transport interruption")
	}
	if lateSend {
		t.Fatal("late frame reached canceled transport")
	}
}

func TestDocumentCognifyObserverCancellationWinsMappedError(t *testing.T) {
	s := NewService(nil, nil, 2)
	for i := 0; i < 100; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		s.SetDocumentCognify(nil, func(context.Context, access.MetadataActor, string, func(*pb.DocumentCognifyStatus) error) error {
			defer close(finished)
			cancel()
			return status.Error(codes.NotFound, "run unavailable")
		})
		err := s.watchDocumentCognify(ctx, access.MetadataActor{}, "run", func(*pb.DocumentCognifyStatus) error { return nil })
		<-finished
		cancel()
		if status.Code(err) != codes.Canceled {
			t.Fatalf("canceled observer masked by adapter at iteration%d: %v", i, err)
		}
	}
}
