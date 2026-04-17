// service_contract_test.go — FIX-13: contract tests for the gRPC RPCs that
// existing service_test.go leaves uncovered. The existing 8 tests cover
// collection CRUD / Insert+Search / BatchInsert+Delete / Info / HasCollection
// / GetByID / Search error handling / ProcessTriplets. That left most of the
// surface (~40 RPCs) without any contract coverage.
//
// These tests are symmetric to the HTTP wave tests: they exercise each RPC
// through the real gRPC server (startTestServer) and verify wire-level
// behaviour — status codes, pass-through to the underlying package, and
// that field names line up with the proto. They intentionally stay pure
// (no embed-server, no graphdb, no LLM) so they run in under a second.
package grpc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/stek0v/cognevra/proto/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ── ChunkText ──

func TestChunkText_Strategies(t *testing.T) {
	client, cleanup := startTestServer(t, 4)
	defer cleanup()

	ctx := context.Background()
	text := "First paragraph has some content that should survive. " +
		"It continues here to fill out the minimum chunk size.\n\n" +
		"Second paragraph begins. This one also has several sentences. " +
		"Something else to talk about. A third point to round it off.\n\n" +
		"Third paragraph. Short but distinct."

	cases := []struct {
		strategy    string
		maxChars    int32
		wantAtLeast int
	}{
		{"merged", 500, 1},
		{"paragraph", 500, 1},
		// Sentence strategy packs sentences up to maxChunkChars — use a
		// tight cap so the text produces multiple chunks.
		{"sentence", 80, 2},
		{"", 500, 1}, // default → merged
	}

	for _, tc := range cases {
		t.Run("strategy="+tc.strategy, func(t *testing.T) {
			resp, err := client.ChunkText(ctx, &pb.ChunkTextReq{
				Text:          text,
				Strategy:      tc.strategy,
				MinChunkChars: 20,
				MaxChunkChars: tc.maxChars,
				DocumentId:    "doc-1",
			})
			if err != nil {
				t.Fatalf("ChunkText: %v", err)
			}
			if len(resp.Chunks) < tc.wantAtLeast {
				t.Errorf("strategy=%q produced %d chunks, want >= %d",
					tc.strategy, len(resp.Chunks), tc.wantAtLeast)
			}
			for _, c := range resp.Chunks {
				if c.Id == "" || c.Text == "" {
					t.Errorf("empty chunk field: id=%q text=%q", c.Id, c.Text)
				}
			}
		})
	}
}

// ── HashFiles / ListDirectory ──

// Write two tempfiles with known content, ask gRPC to hash them, verify
// both come back with non-empty SHA and the right file size.
func TestHashFiles_RoundTrip(t *testing.T) {
	client, cleanup := startTestServer(t, 4)
	defer cleanup()

	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.txt")
	pathB := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(pathA, []byte("alpha"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathB, []byte("beta-beta"), 0644); err != nil {
		t.Fatal(err)
	}

	resp, err := client.HashFiles(context.Background(), &pb.HashFilesReq{
		FilePaths:     []string{pathA, pathB},
		MaxConcurrent: 2,
	})
	if err != nil {
		t.Fatalf("HashFiles: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("Results len=%d, want 2", len(resp.Results))
	}
	for _, r := range resp.Results {
		if r.Sha256 == "" {
			t.Errorf("%s: empty SHA256", r.FilePath)
		}
		if r.FileSize <= 0 {
			t.Errorf("%s: size=%d", r.FilePath, r.FileSize)
		}
	}
}

// ListDirectory with extension filter must return only the matching files
// (and respect the recursive flag). We put files in two levels and check
// both the non-recursive and recursive forms.
func TestListDirectory_ExtensionFilter(t *testing.T) {
	client, cleanup := startTestServer(t, 4)
	defer cleanup()

	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0755)
	os.WriteFile(filepath.Join(dir, "top.txt"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "skip.md"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(sub, "deep.txt"), []byte("x"), 0644)

	resp, err := client.ListDirectory(context.Background(), &pb.ListDirectoryReq{
		RootPath:   dir,
		Recursive:  false,
		Extensions: []string{".txt"},
	})
	if err != nil {
		t.Fatalf("ListDirectory: %v", err)
	}
	if len(resp.FilePaths) != 1 {
		t.Errorf("non-recursive .txt = %v, want 1 (top.txt)", resp.FilePaths)
	}

	resp, err = client.ListDirectory(context.Background(), &pb.ListDirectoryReq{
		RootPath:   dir,
		Recursive:  true,
		Extensions: []string{".txt"},
	})
	if err != nil {
		t.Fatalf("ListDirectory recursive: %v", err)
	}
	if len(resp.FilePaths) != 2 {
		t.Errorf("recursive .txt = %v, want 2 (top.txt + sub/deep.txt)", resp.FilePaths)
	}
}

// ── Compact ──

// Compact must count collections without erroring on the default state
// (one collection created via CreateCollection).
func TestCompact_ReturnsCollectionCount(t *testing.T) {
	client, cleanup := startTestServer(t, 4)
	defer cleanup()

	_, err := client.CreateCollection(context.Background(), &pb.CreateCollectionReq{
		Name: "c1",
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	resp, err := client.Compact(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if resp.CollectionsCompacted < 1 {
		t.Errorf("CollectionsCompacted = %d, want >= 1", resp.CollectionsCompacted)
	}
}

// ── AggregateSearch ──

// AggregateSearch wraps pkg/aggregator. We feed two edges and verify both
// the RankedEdges slice and FormattedContext come back populated.
func TestAggregateSearch_FormatsContext(t *testing.T) {
	client, cleanup := startTestServer(t, 4)
	defer cleanup()

	edges := []*pb.ScoredEdge{
		{
			SourceId: "n1", SourceName: "Alice", SourceText: "Alice is an engineer",
			SourceDistance: 0.1,
			TargetId: "n2", TargetName: "Bob", TargetText: "Bob is a manager",
			TargetDistance:   0.2,
			RelationshipName: "KNOWS",
			EdgeDistance:     0.15,
		},
		{
			SourceId: "n2", SourceName: "Bob",
			SourceDistance: 0.3,
			TargetId: "n3", TargetName: "Carol",
			TargetDistance:   0.25,
			RelationshipName: "MENTORS",
			EdgeDistance:     0.2,
		},
	}
	resp, err := client.AggregateSearch(context.Background(), &pb.AggregateSearchReq{
		Edges: edges,
		TopK:  5,
	})
	if err != nil {
		t.Fatalf("AggregateSearch: %v", err)
	}
	if len(resp.RankedEdges) == 0 {
		t.Errorf("RankedEdges empty; expected ranked output")
	}
	if resp.FormattedContext == "" {
		t.Errorf("FormattedContext empty")
	}
	if resp.UniqueNodes < 2 {
		t.Errorf("UniqueNodes = %d, want >= 2", resp.UniqueNodes)
	}
}

// ── LLMCache ──

// Put → Get round-trip with identical keys must Hit. Different temperature
// with same prompt/model/system must Miss (cache key folds temperature).
func TestLLMCache_PutGetAndKeying(t *testing.T) {
	client, cleanup := startTestServer(t, 4)
	defer cleanup()

	ctx := context.Background()
	putReq := &pb.LLMCachePutReq{
		Model: "gemma3:4b", Prompt: "say hi", SystemPrompt: "be brief",
		Temperature: 0.7, Response: "Hi.",
	}
	if _, err := client.LLMCachePut(ctx, putReq); err != nil {
		t.Fatalf("LLMCachePut: %v", err)
	}

	// Exact match → Hit.
	got, err := client.LLMCacheGet(ctx, &pb.LLMCacheGetReq{
		Model: "gemma3:4b", Prompt: "say hi", SystemPrompt: "be brief",
		Temperature: 0.7,
	})
	if err != nil {
		t.Fatalf("LLMCacheGet: %v", err)
	}
	if !got.Hit {
		t.Error("exact-match LLMCacheGet Hit=false, want true")
	}
	if got.Response != "Hi." {
		t.Errorf("Response = %q, want %q", got.Response, "Hi.")
	}

	// Different temperature → Miss.
	got, err = client.LLMCacheGet(ctx, &pb.LLMCacheGetReq{
		Model: "gemma3:4b", Prompt: "say hi", SystemPrompt: "be brief",
		Temperature: 0.9,
	})
	if err != nil {
		t.Fatalf("LLMCacheGet (diff temp): %v", err)
	}
	if got.Hit {
		t.Error("different-temp Hit=true, want false (temp must fold into key)")
	}

	// Stats counts both lookups.
	stats, err := client.LLMCacheStats(ctx, &pb.Empty{})
	if err != nil {
		t.Fatalf("LLMCacheStats: %v", err)
	}
	if stats.Size < 1 {
		t.Errorf("Size = %d, want >= 1", stats.Size)
	}
	if stats.Hits < 1 || stats.Misses < 1 {
		t.Errorf("Hits=%d Misses=%d, want >= 1 each", stats.Hits, stats.Misses)
	}
}

// ── BM25Index + BM25Search ──

// Index three docs, query a term that only appears in one — must come back
// ranked first. Empty collection on a different name returns empty (not
// an error).
func TestBM25_IndexThenSearch(t *testing.T) {
	client, cleanup := startTestServer(t, 4)
	defer cleanup()

	ctx := context.Background()
	_, err := client.BM25Index(ctx, &pb.BM25IndexReq{
		Collection: "docs",
		Items: []*pb.IndexItem{
			{Id: "d1", Text: "the cat sat on the mat"},
			{Id: "d2", Text: "a dog chased a squirrel up the tree"},
			{Id: "d3", Text: "quick brown foxes jump over lazy hounds"},
		},
	})
	if err != nil {
		t.Fatalf("BM25Index: %v", err)
	}

	resp, err := client.BM25Search(ctx, &pb.BM25SearchReq{
		Collection: "docs", Query: "squirrel", TopK: 3,
	})
	if err != nil {
		t.Fatalf("BM25Search: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatalf("expected BM25 hit for 'squirrel'")
	}
	if resp.Results[0].Id != "d2" {
		t.Errorf("top hit id = %q, want d2", resp.Results[0].Id)
	}

	// Search on non-existent collection is empty, not an error.
	resp, err = client.BM25Search(ctx, &pb.BM25SearchReq{
		Collection: "nonexistent", Query: "anything", TopK: 5,
	})
	if err != nil {
		t.Fatalf("BM25Search on missing collection: %v", err)
	}
	if len(resp.Results) != 0 {
		t.Errorf("non-existent collection returned %d results, want 0", len(resp.Results))
	}

	// Empty collection + query fields → InvalidArgument.
	_, err = client.BM25Search(ctx, &pb.BM25SearchReq{Collection: "", Query: ""})
	if err == nil {
		t.Fatal("BM25Search with empty collection+query should error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", st.Code())
	}
}

// ── SearchTriplets ──

// Feed a tiny graph A-KNOWS->B-WORKS_AT->C with distances that make the
// A→B edge the best-scoring one, verify it comes out on top.
func TestSearchTriplets_Scoring(t *testing.T) {
	client, cleanup := startTestServer(t, 4)
	defer cleanup()

	resp, err := client.SearchTriplets(context.Background(), &pb.SearchTripletsReq{
		Nodes: []*pb.TripletNode{
			{Id: "a", Name: "Alice", Description: "engineer", Type: "Person", Text: "Alice"},
			{Id: "b", Name: "Bob", Description: "manager", Type: "Person", Text: "Bob"},
			{Id: "c", Name: "Acme", Description: "the firm", Type: "Company", Text: "Acme"},
		},
		Edges: []*pb.TripletEdge{
			{Node1Id: "a", Node2Id: "b", RelationshipType: "KNOWS", EdgeText: "a knows b", EdgeTypeId: "et1"},
			{Node1Id: "b", Node2Id: "c", RelationshipType: "WORKS_AT", EdgeText: "b works at c", EdgeTypeId: "et2"},
		},
		NodeDistances: []*pb.CollectionDistances{
			{
				CollectionName: "entities",
				Entries: []*pb.DistanceEntry{
					{Id: "a", Distance: 0.10},
					{Id: "b", Distance: 0.15},
					{Id: "c", Distance: 0.80}, // far
				},
			},
		},
		EdgeDistances: []*pb.DistanceEntry{
			{Id: "et1", Distance: 0.05}, // close
			{Id: "et2", Distance: 0.90}, // far
		},
		TopK: 2,
	})
	if err != nil {
		t.Fatalf("SearchTriplets: %v", err)
	}
	if len(resp.Triplets) == 0 {
		t.Fatal("no triplets returned")
	}
	top := resp.Triplets[0]
	// The A-KNOWS-B edge must win: its nodes are close and edge is close.
	if top.RelationshipType != "KNOWS" {
		t.Errorf("top triplet rel = %q, want KNOWS (closer distances)", top.RelationshipType)
	}
	if resp.FormattedContext == "" {
		t.Errorf("FormattedContext empty")
	}
}

// ── ExtractText ──

// Plain-text file: no external deps (no tesseract, no pdf lib). Verifies
// the proto round-trip plus the InvalidArgument guard on empty input.
func TestExtractText_PlainText(t *testing.T) {
	client, cleanup := startTestServer(t, 4)
	defer cleanup()

	content := "Hello world.\nThis is plain text."
	resp, err := client.ExtractText(context.Background(), &pb.ExtractTextReq{
		FileData: []byte(content),
		Filename: "note.txt",
		MimeType: "text/plain",
	})
	if err != nil {
		t.Fatalf("ExtractText: %v", err)
	}
	if !strings.Contains(resp.Text, "Hello world") {
		t.Errorf("extracted text missing content: %q", resp.Text)
	}
	if resp.Format == "" {
		t.Errorf("Format empty, want non-empty (e.g. 'text')")
	}

	// Empty file_data must surface as InvalidArgument.
	_, err = client.ExtractText(context.Background(), &pb.ExtractTextReq{
		FileData: nil, Filename: "empty.txt", MimeType: "text/plain",
	})
	if err == nil {
		t.Fatal("empty file_data should error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", st.Code())
	}
}
