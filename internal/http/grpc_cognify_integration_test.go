package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	vectorgrpc "github.com/stek0v/levara/internal/grpc"
	"github.com/stek0v/levara/internal/store"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/bm25"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/llm/mock"
	"github.com/stek0v/levara/pkg/runreg"
	pb "github.com/stek0v/levara/proto/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type grpcCognifyStorage struct {
	*memStorage
	loads atomic.Int32
}

func (s *grpcCognifyStorage) Load(ctx context.Context, key string) (io.ReadCloser, error) {
	s.loads.Add(1)
	return s.memStorage.Load(ctx, key)
}

func grpcCognifySource(t *testing.T, f *documentHTTPFixture, backend *grpcCognifyStorage, dataset, document, text string) *pb.DocumentCognifySource {
	t.Helper()
	if err := backend.Save(context.Background(), document, bytes.NewBufferString(text)); err != nil {
		t.Fatal(err)
	}
	f.exec("UPDATE data SET raw_data_location=$1,raw_content_hash=$2,data_size=$3 WHERE id=$4", "storage://"+document, fmt.Sprintf("%x", sha256.Sum256([]byte(text))), len(text), document)
	revision, hash, err := f.p.SourceVersion(context.Background(), accesspkg.DocumentRef{DatasetID: dataset, DataID: document})
	if err != nil {
		t.Fatal(err)
	}
	return &pb.DocumentCognifySource{DatasetId: dataset, DocumentId: document, SourceRevision: revision, RawContentHash: hash}
}

func grpcCognifyClient(t *testing.T, f *documentHTTPFixture, cfg APIConfig, beforeStart func(accesspkg.MetadataActor)) pb.LevaraServiceClient {
	t.Helper()
	svc := vectorgrpc.NewService(cfg.Collections, nil, 2)
	svc.SetIngestMetadata(f.db, true, Q)
	svc.SetDocumentCognify(func(ctx context.Context, actor accesspkg.MetadataActor, req *pb.DocumentCognifyReq) (string, error) {
		if beforeStart != nil {
			beforeStart(actor)
		}
		return StartGRPCDocumentCognify(ctx, cfg, actor, req)
	}, func(ctx context.Context, actor accesspkg.MetadataActor, runID string, send func(*pb.DocumentCognifyStatus) error) error {
		return WatchGRPCDocumentCognify(ctx, cfg, actor, runID, send)
	})
	server := grpc.NewServer(grpc.UnaryInterceptor(vectorgrpc.UnaryAuthInterceptor("grpc-cognify-test", true, f.p)), grpc.StreamInterceptor(vectorgrpc.StreamAuthInterceptor("grpc-cognify-test", true, f.p)))
	pb.RegisterLevaraServiceServer(server, svc)
	listener := bufconn.Listen(1 << 20)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return pb.NewLevaraServiceClient(conn)
}

func grpcCognifyActor(t *testing.T, user string) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+createJWT(user, user+"@test.invalid", "grpc-cognify-test"))
}

func grpcCognifyTerminal(t *testing.T, recv func() (*pb.DocumentCognifyStatus, error)) *pb.DocumentCognifyStatus {
	t.Helper()
	var last *pb.DocumentCognifyStatus
	for {
		frame, err := recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		last = frame
	}
	if last == nil || last.Status == "RUNNING" {
		t.Fatalf("no terminal frame: %+v", last)
	}
	return last
}

func TestGRPCDocumentCognifySourcesAndStatus(t *testing.T) {
	t.Setenv("LLM_ENDPOINT", "")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1) // Exercise both SQL dialects without nested pool reads.
		backend := &grpcCognifyStorage{memStorage: newMemStorage()}
		blob := grpcCognifySource(t, f, backend, "alpha", "blob", "Immutable gRPC document source belongs to its exact dataset and retains independent publication and generation facts.")
		visible := grpcCognifySource(t, f, backend, "alpha", "visible", "Second gRPC document source produces its own chunks and lineage, independently from an equal-content alias in another dataset.")
		cm, err := store.NewCollectionManager(2, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cm.Close() })
		var embedCalls atomic.Int32
		endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			embedCalls.Add(1)
			var req struct {
				Input []string `json:"input"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				http.Error(w, "invalid", 400)
				return
			}
			rows := make([]any, len(req.Input))
			for i := range rows {
				rows[i] = map[string]any{"index": i, "embedding": []float32{1, 0}}
				if req.Input[i] == "Cat" {
					rows[i] = map[string]any{"index": i, "embedding": []float32{0, 1}}
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"data": rows})
		}))
		t.Cleanup(endpoint.Close)
		cfg := f.cfg
		cfg.RequireAuth, cfg.Runs, cfg.FileStorage, cfg.Collections, cfg.BM25Indexes, cfg.EmbedEndpoint = true, runreg.New(), backend, cm, bm25.NewIndexRegistry(), endpoint.URL
		client := grpcCognifyClient(t, f, cfg, func(actor accesspkg.MetadataActor) {
			if actor.UserID == "peer" {
				f.exec("INSERT INTO credential_epochs(user_id,epoch,revoked_before) VALUES('peer',1,0)")
			}
		})
		for _, noIndex := range []bool{false, true} {
			badCfg := cfg
			if noIndex {
				badCfg.Collections = nil
			} else {
				badCfg.EmbedEndpoint = ""
				badCfg.EmbedClient = embed.NewClient(endpoint.URL, "fixture", 1, 1)
			}
			badClient := grpcCognifyClient(t, f, badCfg, nil)
			unavailable, err := badClient.CognifyDocuments(grpcCognifyActor(t, "owner"), &pb.DocumentCognifyReq{Documents: []*pb.DocumentCognifySource{blob}, Collection: "docs"})
			if err == nil {
				_, err = unavailable.Recv()
			}
			if status.Code(err) != codes.Unavailable || backend.loads.Load() != 0 || embedCalls.Load() != 0 || len(cfg.Runs.Snapshot()) != 0 {
				t.Fatalf("missing vector dependency: %v", err)
			}
			var claims int
			if err := f.db.QueryRow("SELECT COUNT(*) FROM document_pipeline_statuses").Scan(&claims); err != nil || claims != 0 {
				t.Fatalf("unavailable claims=%d err=%v", claims, err)
			}
		}
		badHash := *blob
		badHash.RawContentHash = strings.Repeat("0", 64)
		stale := *blob
		stale.SourceRevision++
		staleContent := *blob
		staleContent.ContentRevision = f.r.ContentRevision + 1
		cases := []struct {
			name, user string
			docs       []*pb.DocumentCognifySource
			code       codes.Code
		}{
			{"viewer", "viewer", []*pb.DocumentCognifySource{blob}, codes.PermissionDenied},
			{"foreign", "foreign", []*pb.DocumentCognifySource{blob}, codes.PermissionDenied},
			{"inactive", "inactive", []*pb.DocumentCognifySource{blob}, codes.PermissionDenied},
			{"bad hash later", "owner", []*pb.DocumentCognifySource{visible, &badHash}, codes.FailedPrecondition},
			{"stale source", "owner", []*pb.DocumentCognifySource{&stale}, codes.FailedPrecondition},
			{"stale content", "owner", []*pb.DocumentCognifySource{&staleContent}, codes.FailedPrecondition},
			{"missing later", "owner", []*pb.DocumentCognifySource{visible, {DatasetId: "alpha", DocumentId: "missing", SourceRevision: 1, RawContentHash: blob.RawContentHash}}, codes.NotFound},
			{"revoked after interceptor", "peer", []*pb.DocumentCognifySource{blob}, codes.Unauthenticated},
		}
		for _, tc := range cases {
			stream, err := client.CognifyDocuments(grpcCognifyActor(t, tc.user), &pb.DocumentCognifyReq{Documents: tc.docs, Collection: "docs", Mode: "rag"})
			if err == nil {
				_, err = stream.Recv()
			}
			if status.Code(err) != tc.code {
				t.Fatalf("%s: code=%s err=%v", tc.name, status.Code(err), err)
			}
			var claims int
			if err := f.db.QueryRow("SELECT COUNT(*) FROM document_pipeline_statuses").Scan(&claims); err != nil {
				t.Fatal(err)
			}
			if backend.loads.Load() != 0 || embedCalls.Load() != 0 || claims != 0 || len(cfg.Runs.Snapshot()) != 0 {
				t.Fatalf("%s caused effects: loads=%d calls=%d claims=%d", tc.name, backend.loads.Load(), embedCalls.Load(), claims)
			}
		}
		unconfigured, err := client.CognifyDocuments(grpcCognifyActor(t, "owner"), &pb.DocumentCognifyReq{Documents: []*pb.DocumentCognifySource{blob}, Collection: "docs", Mode: "graph"})
		if err == nil {
			_, err = unconfigured.Recv()
		}
		if status.Code(err) != codes.Unavailable || backend.loads.Load() != 0 {
			t.Fatalf("unconfigured graph: %v loads=%d", err, backend.loads.Load())
		}
		f.exec("UPDATE data SET raw_data_location='storage://missing' WHERE id='blob'")
		missing, err := client.CognifyDocuments(grpcCognifyActor(t, "owner"), &pb.DocumentCognifyReq{Documents: []*pb.DocumentCognifySource{blob}, Collection: "docs"})
		if err == nil {
			_, err = missing.Recv()
		}
		var missingClaims int
		if queryErr := f.db.QueryRow("SELECT COUNT(*) FROM document_pipeline_statuses").Scan(&missingClaims); queryErr != nil {
			t.Fatal(queryErr)
		}
		if status.Code(err) != codes.Unavailable || missingClaims != 0 || len(cfg.Runs.Snapshot()) != 0 || embedCalls.Load() != 0 {
			t.Fatalf("missing raw: %v claims=%d", err, missingClaims)
		}
		f.exec("UPDATE data SET raw_data_location='storage://blob' WHERE id='blob'")
		backend.loads.Store(0)
		alias := *blob
		alias.DatasetId = "beta"
		stream, err := client.CognifyDocuments(grpcCognifyActor(t, "owner"), &pb.DocumentCognifyReq{Documents: []*pb.DocumentCognifySource{blob, blob, &alias, visible}, Collection: "docs"})
		if err != nil {
			t.Fatal(err)
		}
		terminal := grpcCognifyTerminal(t, stream.Recv)
		if terminal.Status != "COMPLETED" || terminal.PipelineRunId == "" || len(terminal.Sources) != 3 || terminal.ChunksCreated <= 0 || backend.loads.Load() != 3 || embedCalls.Load() != 3 {
			t.Fatalf("terminal=%+v loads=%d calls=%d", terminal, backend.loads.Load(), embedCalls.Load())
		}
		ids, _, records, err := cm.AllRecords("docs")
		if err != nil || len(ids) != 3 {
			t.Fatalf("records=%d err=%v", len(ids), err)
		}
		for _, record := range records {
			proof, err := decodeSearchDocumentSource(string(record))
			if err != nil || proof.Generation == "" || proof.DocumentID == "" || proof.Collection != "docs" {
				t.Fatalf("record=%s err=%v", record, err)
			}
			var lineage, state, attempt string
			err = f.db.QueryRow(Q(`SELECT p.sources_json,s.pipeline_state,s.attempt_id FROM document_index_publications p JOIN document_pipeline_statuses s ON s.dataset_id=p.dataset_id AND s.data_id=p.data_id AND s.collection_name=p.collection_name WHERE p.dataset_id=$1 AND p.data_id=$2 AND p.collection_name='docs' AND p.generation=$3`), proof.DatasetID, proof.DocumentID, proof.Generation).Scan(&lineage, &state, &attempt)
			var sources []searchDocumentSource
			if err != nil || json.Unmarshal([]byte(lineage), &sources) != nil || len(sources) != 1 || sources[0].DatasetID != proof.DatasetID || sources[0].DocumentID != proof.DocumentID || state != "COMPLETED" || attempt != terminal.PipelineRunId {
				t.Fatalf("lineage=%s state=%s attempt=%s err=%v", lineage, state, attempt, err)
			}
		}
		for _, user := range []string{"viewer", "foreign"} {
			watch, err := client.CognifyDocumentsStatus(grpcCognifyActor(t, user), &pb.DocumentCognifyStatusReq{PipelineRunId: terminal.PipelineRunId})
			if err == nil {
				_, err = watch.Recv()
			}
			if status.Code(err) != codes.NotFound {
				t.Fatalf("%s status leaked: %v", user, err)
			}
		}
		watch, err := client.CognifyDocumentsStatus(grpcCognifyActor(t, "owner"), &pb.DocumentCognifyStatusReq{PipelineRunId: terminal.PipelineRunId})
		if err != nil || grpcCognifyTerminal(t, watch.Recv).PipelineRunId != terminal.PipelineRunId {
			t.Fatalf("owner status: %v", err)
		}
		group, err := f.p.CreateGroup(context.Background(), f.owner, "a", "Document editors")
		if err != nil {
			t.Fatal(err)
		}
		group, err = f.p.ReplaceGroupMembers(context.Background(), f.owner, group.ID, group.Revision, []string{"viewer"})
		if err != nil {
			t.Fatal(err)
		}
		f.r, err = f.p.GrantDocument(context.Background(), f.owner, f.r.DocumentRef, f.r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentGroup, ID: group.ID}, accesspkg.RoleEditor)
		if err != nil {
			t.Fatal(err)
		}
		provider := mock.New().OnAny().Reply(`{"nodes":[{"id":"alice","name":"Alice","type":"Person"},{"id":"cat","name":"Cat","type":"Animal"}],"edges":[{"source":"alice","target":"cat","relationship":"owns","edge_text":"Alice owns Cat"}]}`)
		graphCfg := cfg
		graphCfg.LLMProvider = provider.Provider()
		graphClient := grpcCognifyClient(t, f, graphCfg, nil)
		loadsBeforeBatch := backend.loads.Load()
		deniedBatch, err := graphClient.CognifyDocuments(grpcCognifyActor(t, "viewer"), &pb.DocumentCognifyReq{Documents: []*pb.DocumentCognifySource{blob, visible}, Collection: "deniedbatch"})
		if err == nil {
			_, err = deniedBatch.Recv()
		}
		var deniedClaims int
		if queryErr := f.db.QueryRow("SELECT COUNT(*) FROM document_pipeline_statuses WHERE collection_name='deniedbatch'").Scan(&deniedClaims); queryErr != nil {
			t.Fatal(queryErr)
		}
		if status.Code(err) != codes.PermissionDenied || backend.loads.Load() != loadsBeforeBatch || deniedClaims != 0 {
			t.Fatalf("denied second: %v loads=%d claims=%d", err, backend.loads.Load()-loadsBeforeBatch, deniedClaims)
		}
		graphStream, err := graphClient.CognifyDocuments(grpcCognifyActor(t, "viewer"), &pb.DocumentCognifyReq{Documents: []*pb.DocumentCognifySource{blob}, Collection: "graphdocs", Mode: "graph"})
		if err != nil {
			t.Fatal(err)
		}
		graphTerminal := grpcCognifyTerminal(t, graphStream.Recv)
		if graphTerminal.Status != "COMPLETED" || graphTerminal.EntitiesExtracted != 2 || graphTerminal.EdgesExtracted != 1 || len(provider.Calls()) != 1 {
			t.Fatalf("graph=%+v calls=%d", graphTerminal, len(provider.Calls()))
		}
		var graphRows int
		if err := f.db.QueryRow(Q(`SELECT COUNT(*) FROM graph_nodes WHERE dataset_id='alpha'`)).Scan(&graphRows); err != nil || graphRows != 2 {
			t.Fatalf("graph rows=%d err=%v", graphRows, err)
		}
		if _, err := f.p.ReplaceGroupMembers(context.Background(), f.owner, group.ID, group.Revision, nil); err != nil {
			t.Fatal(err)
		}
		groupWatch, err := graphClient.CognifyDocumentsStatus(grpcCognifyActor(t, "viewer"), &pb.DocumentCognifyStatusReq{PipelineRunId: graphTerminal.PipelineRunId})
		if err == nil {
			_, err = groupWatch.Recv()
		}
		if status.Code(err) != codes.NotFound {
			t.Fatalf("removed group status=%v", err)
		}
		loadsBefore := backend.loads.Load()
		denied, err := graphClient.CognifyDocuments(grpcCognifyActor(t, "viewer"), &pb.DocumentCognifyReq{Documents: []*pb.DocumentCognifySource{blob}, Collection: "denied"})
		if err == nil {
			_, err = denied.Recv()
		}
		if status.Code(err) != codes.PermissionDenied || backend.loads.Load() != loadsBefore {
			t.Fatalf("removed group start=%v", err)
		}
		f.exec("UPDATE data SET raw_content_hash=$1 WHERE id='blob'", strings.Repeat("f", 64))
		staleWatch, err := client.CognifyDocumentsStatus(grpcCognifyActor(t, "owner"), &pb.DocumentCognifyStatusReq{PipelineRunId: terminal.PipelineRunId})
		if err == nil {
			_, err = staleWatch.Recv()
		}
		if status.Code(err) != codes.NotFound {
			t.Fatalf("replaced source status=%v", err)
		}
	})
}

func TestGRPCDocumentCognifyPartialFailureAndDetachedObserver(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		backend := &grpcCognifyStorage{memStorage: newMemStorage()}
		first := grpcCognifySource(t, f, backend, "alpha", "blob", "First source finishes and keeps its successful publication when the following gRPC batch item fails processing.")
		second := grpcCognifySource(t, f, backend, "alpha", "visible", "fail-second source must get an exact FAILED status and must never claim a completed document generation.")
		cm, err := store.NewCollectionManager(2, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cm.Close() })
		entered, release := make(chan struct{}), make(chan struct{})
		var releaseOnce sync.Once
		releaseEmbed := func() { releaseOnce.Do(func() { close(release) }) }
		var calls atomic.Int32
		endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Input []string `json:"input"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				http.Error(w, "invalid", 400)
				return
			}
			if calls.Add(1) == 1 {
				close(entered)
				<-release
			}
			for _, text := range req.Input {
				if strings.Contains(text, "fail-second") {
					http.Error(w, "backend private details must not escape", 503)
					return
				}
			}
			rows := make([]any, len(req.Input))
			for i := range rows {
				rows[i] = map[string]any{"index": i, "embedding": []float32{1, 0}}
			}
			json.NewEncoder(w).Encode(map[string]any{"data": rows})
		}))
		t.Cleanup(endpoint.Close)
		t.Cleanup(releaseEmbed)
		cfg := f.cfg
		cfg.RequireAuth, cfg.Runs, cfg.FileStorage, cfg.Collections, cfg.EmbedEndpoint = true, runreg.New(), backend, cm, endpoint.URL
		client := grpcCognifyClient(t, f, cfg, nil)
		ctx, cancel := context.WithCancel(grpcCognifyActor(t, "owner"))
		stream, err := client.CognifyDocuments(ctx, &pb.DocumentCognifyReq{Documents: []*pb.DocumentCognifySource{first, second}, Collection: "partial", Mode: "rag"})
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("embed did not begin")
		}
		accepted := cfg.Runs.Snapshot()
		if len(accepted) != 1 {
			t.Fatalf("accepted runs=%d", len(accepted))
		}
		cancel()
		for err == nil {
			_, err = stream.Recv() // A frame buffered before cancel may still arrive.
		}
		if status.Code(err) != codes.Canceled {
			t.Fatalf("observer cancel=%v", err)
		}
		releaseEmbed()
		watch, err := client.CognifyDocumentsStatus(grpcCognifyActor(t, "owner"), &pb.DocumentCognifyStatusReq{PipelineRunId: accepted[0].RunID})
		if err != nil {
			t.Fatal(err)
		}
		terminal := grpcCognifyTerminal(t, watch.Recv)
		if terminal.Status != "FAILED" || terminal.Message != "document processing failed" {
			t.Fatalf("terminal=%+v", terminal)
		}
		for i, doc := range []*pb.DocumentCognifySource{first, second} {
			var state string
			err := f.db.QueryRow(Q("SELECT pipeline_state FROM document_pipeline_statuses WHERE dataset_id=$1 AND data_id=$2 AND collection_name='partial'"), doc.DatasetId, doc.DocumentId).Scan(&state)
			if err != nil || state != []string{"COMPLETED", "FAILED"}[i] {
				t.Fatalf("state[%d]=%s err=%v", i, state, err)
			}
		}
		var publications int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM document_index_publications WHERE collection_name='partial'").Scan(&publications); err != nil || publications != 1 {
			t.Fatalf("publications=%d err=%v", publications, err)
		}
	})
}

func TestGRPCDocumentCognifyTenantRevokedAfterInterceptor(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		backend := &grpcCognifyStorage{memStorage: newMemStorage()}
		source := grpcCognifySource(t, f, backend, "alpha", "visible", "Legacy source still requires live selected tenant membership before any raw bytes are transferred for processing.")
		f.exec("UPDATE users SET is_superuser=true WHERE id='owner'")
		cm, err := store.NewCollectionManager(2, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cm.Close() })
		cfg := f.cfg
		cfg.RequireAuth, cfg.Runs, cfg.FileStorage, cfg.EmbedEndpoint, cfg.Collections = true, runreg.New(), backend, "http://127.0.0.1:1", cm
		client := grpcCognifyClient(t, f, cfg, func(accesspkg.MetadataActor) {
			f.exec("DELETE FROM user_tenant WHERE user_id='owner' AND tenant_id='a'")
		})
		ctx := metadata.AppendToOutgoingContext(grpcCognifyActor(t, "owner"), "x-tenant-id", "a")
		stream, err := client.CognifyDocuments(ctx, &pb.DocumentCognifyReq{Documents: []*pb.DocumentCognifySource{source}, Collection: "docs"})
		if err == nil {
			_, err = stream.Recv()
		}
		var claims int
		if queryErr := f.db.QueryRow("SELECT COUNT(*) FROM document_pipeline_statuses").Scan(&claims); queryErr != nil {
			t.Fatal(queryErr)
		}
		if status.Code(err) != codes.PermissionDenied || backend.loads.Load() != 0 || claims != 0 || len(cfg.Runs.Snapshot()) != 0 {
			t.Fatalf("tenant revoke: %v loads=%d claims=%d", err, backend.loads.Load(), claims)
		}
	})
}
