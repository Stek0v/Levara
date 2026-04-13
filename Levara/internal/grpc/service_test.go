package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stek0v/cognevra/internal/store"
	pb "github.com/stek0v/cognevra/proto/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func randomVec(dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = rand.Float32()*2 - 1
	}
	return v
}

// startTestServer starts a gRPC server on a random port and returns client + cleanup.
func startTestServer(t *testing.T, dim int) (pb.CognevraServiceClient, func()) {
	t.Helper()

	dir, _ := os.MkdirTemp("", "levara-grpc-test-*")

	colMgr, err := store.NewCollectionManager(dim, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}

	// Create a dummy cluster (single shard) for Info
	dbPath := fmt.Sprintf("%s/dummy/shard_0/meta.bin", dir)
	os.MkdirAll(fmt.Sprintf("%s/dummy/shard_0", dir), 0755)
	db, _ := store.NewLevara(dim, dbPath)
	cluster := store.NewCluster([]store.ShardHandler{
		&dummyShard{db: db},
	})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := grpc.NewServer()
	pb.RegisterCognevraServiceServer(srv, NewService(colMgr, cluster, dim))
	go srv.Serve(lis)

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	client := pb.NewCognevraServiceClient(conn)

	cleanup := func() {
		conn.Close()
		srv.GracefulStop()
		colMgr.Close()
		db.Close()
		os.RemoveAll(dir)
	}

	return client, cleanup
}

// dummyShard implements store.ShardHandler for testing.
type dummyShard struct {
	db *store.Levara
}

func (d *dummyShard) Insert(id string, vec []float32, data interface{}) error {
	return d.db.Insert(id, vec, data)
}
func (d *dummyShard) BatchInsert(records []store.BatchItem) []error {
	return d.db.BatchInsert(records)
}
func (d *dummyShard) Search(query []float32, topK int) []store.VectroRecord {
	return d.db.Search(query, topK)
}
func (d *dummyShard) Delete(id string) error { return d.db.Delete(id) }
func (d *dummyShard) BatchDelete(ids []string) []error {
	return d.db.BatchDelete(ids)
}

func TestGRPCCollectionCRUD(t *testing.T) {
	client, cleanup := startTestServer(t, 64)
	defer cleanup()
	ctx := context.Background()

	// Create
	resp, err := client.CreateCollection(ctx, &pb.CreateCollectionReq{Name: "test"})
	if err != nil || !resp.Ok {
		t.Fatalf("CreateCollection: err=%v resp=%v", err, resp)
	}

	// List
	listResp, err := client.ListCollections(ctx, &pb.Empty{})
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(listResp.Collections) != 1 || listResp.Collections[0] != "test" {
		t.Fatalf("List: got %v", listResp.Collections)
	}

	// Drop
	resp, err = client.DropCollection(ctx, &pb.DropCollectionReq{Name: "test"})
	if err != nil || !resp.Ok {
		t.Fatalf("DropCollection: err=%v resp=%v", err, resp)
	}

	// Verify empty
	listResp, _ = client.ListCollections(ctx, &pb.Empty{})
	if len(listResp.Collections) != 0 {
		t.Fatalf("List after drop: got %v", listResp.Collections)
	}
}

func TestGRPCInsertSearch(t *testing.T) {
	client, cleanup := startTestServer(t, 64)
	defer cleanup()
	ctx := context.Background()

	// Create collection
	client.CreateCollection(ctx, &pb.CreateCollectionReq{Name: "books"})

	// Insert
	vec := randomVec(64)
	resp, err := client.Insert(ctx, &pb.InsertReq{
		Collection:   "books",
		Id:           "book-1",
		Vector:       vec,
		MetadataJson: `{"title":"Test Book"}`,
	})
	if err != nil || !resp.Ok {
		t.Fatalf("Insert: err=%v resp=%v", err, resp)
	}

	// Wait for HNSW indexer
	time.Sleep(100 * time.Millisecond)

	// Search
	searchResp, err := client.Search(ctx, &pb.SearchReq{
		Collection: "books",
		Vector:     vec,
		TopK:       5,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(searchResp.Results) == 0 {
		t.Fatal("Search returned no results")
	}
	if searchResp.Results[0].Id != "book-1" {
		t.Fatalf("Search top result: got %q, want book-1", searchResp.Results[0].Id)
	}
}

func TestGRPCBatchInsertAndDelete(t *testing.T) {
	client, cleanup := startTestServer(t, 64)
	defer cleanup()
	ctx := context.Background()

	client.CreateCollection(ctx, &pb.CreateCollectionReq{Name: "items"})

	// Batch insert
	records := make([]*pb.InsertRecord, 20)
	for i := range records {
		records[i] = &pb.InsertRecord{
			Id:           fmt.Sprintf("item-%d", i),
			Vector:       randomVec(64),
			MetadataJson: fmt.Sprintf(`{"index":%d}`, i),
		}
	}

	batchResp, err := client.BatchInsert(ctx, &pb.BatchInsertReq{
		Collection: "items",
		Records:    records,
	})
	if err != nil {
		t.Fatalf("BatchInsert: %v", err)
	}
	if batchResp.Inserted != 20 {
		t.Fatalf("Inserted: got %d, want 20", batchResp.Inserted)
	}

	// Delete some
	delResp, err := client.Delete(ctx, &pb.DeleteReq{
		Collection: "items",
		Ids:        []string{"item-0", "item-5", "item-10"},
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if delResp.Deleted != 3 {
		t.Fatalf("Deleted: got %d, want 3", delResp.Deleted)
	}
}

func TestGRPCInfo(t *testing.T) {
	client, cleanup := startTestServer(t, 64)
	defer cleanup()
	ctx := context.Background()

	info, err := client.Info(ctx, &pb.Empty{})
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Dimension != 64 {
		t.Fatalf("Dimension: got %d, want 64", info.Dimension)
	}
	if info.Status != "ready" {
		t.Fatalf("Status: got %q, want ready", info.Status)
	}
}

func TestHasCollection(t *testing.T) {
	client, cleanup := startTestServer(t, 4)
	defer cleanup()
	ctx := context.Background()

	// Non-existent collection
	resp, err := client.HasCollection(ctx, &pb.HasCollectionReq{Name: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Exists {
		t.Error("expected false for non-existent collection")
	}

	// Create and verify
	client.CreateCollection(ctx, &pb.CreateCollectionReq{Name: "test"})
	resp, err = client.HasCollection(ctx, &pb.HasCollectionReq{Name: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Exists {
		t.Error("expected true after creation")
	}

	// Drop and verify gone
	client.DropCollection(ctx, &pb.DropCollectionReq{Name: "test"})
	resp, err = client.HasCollection(ctx, &pb.HasCollectionReq{Name: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Exists {
		t.Error("expected false after drop")
	}
}

func TestGetByID(t *testing.T) {
	client, cleanup := startTestServer(t, 4)
	defer cleanup()
	ctx := context.Background()

	client.CreateCollection(ctx, &pb.CreateCollectionReq{Name: "books"})

	meta := `{"text":"hello world","chapter":1}`
	client.Insert(ctx, &pb.InsertReq{
		Collection:   "books",
		Id:           "rec-1",
		Vector:       []float32{0.1, 0.2, 0.3, 0.4},
		MetadataJson: meta,
	})

	resp, err := client.GetByID(ctx, &pb.GetByIDReq{
		Collection: "books",
		Ids:        []string{"rec-1", "rec-missing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(resp.Records))
	}
	if !resp.Records[0].Found {
		t.Error("rec-1 should be found")
	}
	// Compare JSON semantically — marshal reorders map keys.
	var wantMeta, gotMeta map[string]any
	json.Unmarshal([]byte(meta), &wantMeta)
	json.Unmarshal([]byte(resp.Records[0].MetadataJson), &gotMeta)
	if !reflect.DeepEqual(wantMeta, gotMeta) {
		t.Errorf("metadata mismatch: got %s, want %s", resp.Records[0].MetadataJson, meta)
	}
	if resp.Records[1].Found {
		t.Error("rec-missing should not be found")
	}

	// Missing collection -> gRPC NotFound error
	_, err = client.GetByID(ctx, &pb.GetByIDReq{
		Collection: "nonexistent",
		Ids:        []string{"x"},
	})
	if err == nil {
		t.Error("expected error for missing collection")
	}
}

func TestSearchErrorHandling(t *testing.T) {
	client, cleanup := startTestServer(t, 4)
	defer cleanup()
	ctx := context.Background()

	// Search against a collection that doesn't exist
	_, err := client.Search(ctx, &pb.SearchReq{
		Collection: "nonexistent",
		Vector:     []float32{0.1, 0.2, 0.3, 0.4},
		TopK:       5,
	})
	if err == nil {
		t.Error("expected error for missing collection")
	}

	// Search with empty collection name
	_, err = client.Search(ctx, &pb.SearchReq{
		Collection: "",
		Vector:     []float32{0.1, 0.2, 0.3, 0.4},
		TopK:       5,
	})
	if err == nil {
		t.Error("expected error for empty collection")
	}
}

// setupService creates a Service instance directly (without gRPC transport) for unit tests.
func setupService(t *testing.T) *Service {
	t.Helper()
	dir, _ := os.MkdirTemp("", "levara-svc-test-*")
	t.Cleanup(func() { os.RemoveAll(dir) })

	colMgr, err := store.NewCollectionManager(64, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}
	t.Cleanup(func() { colMgr.Close() })

	os.MkdirAll(fmt.Sprintf("%s/dummy/shard_0", dir), 0755)
	db, _ := store.NewLevara(64, fmt.Sprintf("%s/dummy/shard_0/meta.bin", dir))
	t.Cleanup(func() { db.Close() })
	cluster := store.NewCluster([]store.ShardHandler{&dummyShard{db: db}})

	return NewService(colMgr, cluster, 64)
}

func TestProcessTriplets(t *testing.T) {
	svc := setupService(t)
	ctx := context.Background()

	resp, err := svc.ProcessTriplets(ctx, &pb.ProcessTripletsReq{
		Nodes: []*pb.GraphNode{
			{Id: "n1", Text: "Alice"},
			{Id: "n2", Text: "Bob"},
			{Id: "n3", Text: "Charlie"},
		},
		Edges: []*pb.GraphEdge{
			{SourceId: "n1", TargetId: "n2", RelationshipName: "knows", EdgeText: "is friends with"},
			{SourceId: "n2", TargetId: "n3", RelationshipName: "works_with"},
			{SourceId: "n1", TargetId: "n2", RelationshipName: "knows"}, // duplicate
			{SourceId: "n1", TargetId: "n999", RelationshipName: "missing"}, // missing node
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Created != 2 {
		t.Errorf("expected 2 triplets, got %d", resp.Created)
	}
	if resp.Skipped != 2 {
		t.Errorf("expected 2 skipped, got %d", resp.Skipped)
	}
	// Check first triplet text format
	if !strings.Contains(resp.Triplets[0].Text, "Alice") || !strings.Contains(resp.Triplets[0].Text, "is friends with") {
		t.Errorf("unexpected triplet text: %s", resp.Triplets[0].Text)
	}
	// Check dedup: edge_text preferred over relationship_name
	if !strings.Contains(resp.Triplets[0].Text, "is friends with") {
		t.Errorf("edge_text should be preferred: %s", resp.Triplets[0].Text)
	}
	// Check second triplet uses relationship_name as fallback
	if !strings.Contains(resp.Triplets[1].Text, "works_with") {
		t.Errorf("should fallback to relationship_name: %s", resp.Triplets[1].Text)
	}
}

func BenchmarkGRPCSearch(b *testing.B) {
	dir, _ := os.MkdirTemp("", "levara-grpc-bench-*")
	defer os.RemoveAll(dir)

	dim := 64
	colMgr, _ := store.NewCollectionManager(dim, dir)
	defer colMgr.Close()

	os.MkdirAll(fmt.Sprintf("%s/dummy/shard_0", dir), 0755)
	db, _ := store.NewLevara(dim, fmt.Sprintf("%s/dummy/shard_0/meta.bin", dir))
	defer db.Close()
	cluster := store.NewCluster([]store.ShardHandler{&dummyShard{db: db}})

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	srv := grpc.NewServer()
	pb.RegisterCognevraServiceServer(srv, NewService(colMgr, cluster, dim))
	go srv.Serve(lis)
	defer srv.GracefulStop()

	conn, _ := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	defer conn.Close()
	client := pb.NewCognevraServiceClient(conn)
	ctx := context.Background()

	// Setup: create collection + insert 500 vectors
	client.CreateCollection(ctx, &pb.CreateCollectionReq{Name: "bench"})
	for i := 0; i < 500; i++ {
		client.Insert(ctx, &pb.InsertReq{
			Collection:   "bench",
			Id:           fmt.Sprintf("v-%d", i),
			Vector:       randomVec(dim),
			MetadataJson: fmt.Sprintf(`{"i":%d}`, i),
		})
	}
	time.Sleep(500 * time.Millisecond) // let HNSW index

	queryVec := randomVec(dim)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client.Search(ctx, &pb.SearchReq{
			Collection: "bench",
			Vector:     queryVec,
			TopK:       10,
		})
	}
}
