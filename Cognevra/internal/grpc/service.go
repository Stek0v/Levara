package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/rupamthxt/cognevra/internal/store"
	"github.com/rupamthxt/cognevra/pkg/aggregator"
	"github.com/rupamthxt/cognevra/pkg/chunker"
	"github.com/rupamthxt/cognevra/pkg/embed"
	"github.com/rupamthxt/cognevra/pkg/fileio"
	"github.com/rupamthxt/cognevra/pkg/graph"
	pb "github.com/rupamthxt/cognevra/proto/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Service implements the CognevraService gRPC server.
type Service struct {
	pb.UnimplementedCognevraServiceServer
	collections *store.CollectionManager
	cluster     *store.Cluster // legacy: for non-collection operations
	dim         int
}

// NewService creates a gRPC service backed by CollectionManager.
func NewService(collections *store.CollectionManager, cluster *store.Cluster, dim int) *Service {
	return &Service{
		collections: collections,
		cluster:     cluster,
		dim:         dim,
	}
}

func (s *Service) CreateCollection(_ context.Context, req *pb.CreateCollectionReq) (*pb.StatusResp, error) {
	if req.Name == "" {
		return &pb.StatusResp{Ok: false, Error: "name is required"}, nil
	}
	if err := s.collections.Create(req.Name); err != nil {
		return &pb.StatusResp{Ok: false, Error: err.Error()}, nil
	}
	return &pb.StatusResp{Ok: true}, nil
}

func (s *Service) DropCollection(_ context.Context, req *pb.DropCollectionReq) (*pb.StatusResp, error) {
	if err := s.collections.Drop(req.Name); err != nil {
		return &pb.StatusResp{Ok: false, Error: err.Error()}, nil
	}
	return &pb.StatusResp{Ok: true}, nil
}

func (s *Service) ListCollections(_ context.Context, _ *pb.Empty) (*pb.ListCollectionsResp, error) {
	return &pb.ListCollectionsResp{Collections: s.collections.List()}, nil
}

func (s *Service) HasCollection(_ context.Context, req *pb.HasCollectionReq) (*pb.HasCollectionResp, error) {
	return &pb.HasCollectionResp{Exists: s.collections.Has(req.Name)}, nil
}

func (s *Service) Insert(_ context.Context, req *pb.InsertReq) (*pb.StatusResp, error) {
	if req.Collection == "" || req.Id == "" || len(req.Vector) == 0 {
		return &pb.StatusResp{Ok: false, Error: "collection, id, and vector are required"}, nil
	}

	var meta map[string]any
	if req.MetadataJson != "" {
		if err := json.Unmarshal([]byte(req.MetadataJson), &meta); err != nil {
			return &pb.StatusResp{Ok: false, Error: fmt.Sprintf("invalid metadata JSON: %v", err)}, nil
		}
	}

	if err := s.collections.Insert(req.Collection, req.Id, req.Vector, meta); err != nil {
		return &pb.StatusResp{Ok: false, Error: err.Error()}, nil
	}
	return &pb.StatusResp{Ok: true}, nil
}

func (s *Service) BatchInsert(_ context.Context, req *pb.BatchInsertReq) (*pb.BatchInsertResp, error) {
	if req.Collection == "" || len(req.Records) == 0 {
		return &pb.BatchInsertResp{Failed: int32(len(req.Records)), Errors: []string{"collection and records are required"}}, nil
	}

	items := make([]store.BatchItem, 0, len(req.Records))
	for _, r := range req.Records {
		var meta map[string]any
		if r.MetadataJson != "" {
			json.Unmarshal([]byte(r.MetadataJson), &meta)
		}
		items = append(items, store.BatchItem{
			ID:     r.Id,
			Vector: r.Vector,
			Data:   meta,
		})
	}

	errs := s.collections.BatchInsert(req.Collection, items)

	resp := &pb.BatchInsertResp{
		Inserted: int32(len(items) - len(errs)),
		Failed:   int32(len(errs)),
	}
	for _, e := range errs {
		resp.Errors = append(resp.Errors, e.Error())
	}
	return resp, nil
}

func (s *Service) Delete(_ context.Context, req *pb.DeleteReq) (*pb.DeleteResp, error) {
	if req.Collection == "" || len(req.Ids) == 0 {
		return &pb.DeleteResp{Failed: int32(len(req.Ids)), Errors: []string{"collection and ids are required"}}, nil
	}

	errs := s.collections.BatchDelete(req.Collection, req.Ids)

	resp := &pb.DeleteResp{
		Deleted: int32(len(req.Ids) - len(errs)),
		Failed:  int32(len(errs)),
	}
	for _, e := range errs {
		resp.Errors = append(resp.Errors, e.Error())
	}
	return resp, nil
}

func (s *Service) Search(_ context.Context, req *pb.SearchReq) (*pb.SearchResp, error) {
	if req.Collection == "" || len(req.Vector) == 0 {
		return nil, status.Error(codes.InvalidArgument, "collection and vector are required")
	}

	topK := int(req.TopK)
	if topK <= 0 {
		topK = 10
	}

	results, err := s.collections.Search(req.Collection, req.Vector, topK)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "collection %q: %v", req.Collection, err)
	}

	pbResults := make([]*pb.SearchResult, 0, len(results))
	for _, r := range results {
		pbResults = append(pbResults, &pb.SearchResult{
			Id:           r.ID,
			Score:        r.Score,
			MetadataJson: string(r.Data),
		})
	}

	return &pb.SearchResp{Results: pbResults}, nil
}

func (s *Service) GetByID(_ context.Context, req *pb.GetByIDReq) (*pb.GetByIDResp, error) {
	if req.Collection == "" || len(req.Ids) == 0 {
		return &pb.GetByIDResp{}, nil
	}

	db, err := s.collections.Get(req.Collection)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "collection %q not found", req.Collection)
	}

	records := make([]*pb.RecordEntry, 0, len(req.Ids))
	for _, id := range req.Ids {
		_, meta, found := db.Get(id)
		entry := &pb.RecordEntry{Id: id, Found: found}
		if found && meta != nil {
			entry.MetadataJson = string(meta)
		}
		records = append(records, entry)
	}

	return &pb.GetByIDResp{Records: records}, nil
}

func (s *Service) ChunkText(_ context.Context, req *pb.ChunkTextReq) (*pb.ChunkTextResp, error) {
	minChars := int(req.MinChunkChars)
	if minChars <= 0 {
		minChars = chunker.DefaultMinChunkChars
	}
	maxChars := int(req.MaxChunkChars)
	if maxChars <= 0 {
		maxChars = chunker.DefaultMaxChunkChars
	}

	var chunks []chunker.Chunk
	switch req.Strategy {
	case "paragraph":
		chunks = chunker.ChunkByParagraphSimple(req.Text, minChars, req.DocumentId)
	case "sentence":
		chunks = chunker.ChunkBySentence(req.Text, minChars, maxChars, req.DocumentId)
	default: // "merged" or empty
		chunks = chunker.ChunkByParagraphMerged(req.Text, minChars, maxChars, req.DocumentId)
	}

	pbChunks := make([]*pb.TextChunk, len(chunks))
	for i, c := range chunks {
		pbChunks[i] = &pb.TextChunk{
			Id:         c.ID,
			Text:       c.Text,
			Chapter:    int32(c.Chapter),
			ChunkIndex: int32(c.ChunkIndex),
			CutType:    c.CutType,
		}
	}
	return &pb.ChunkTextResp{Chunks: pbChunks}, nil
}

func (s *Service) Info(_ context.Context, _ *pb.Empty) (*pb.InfoResp, error) {
	return &pb.InfoResp{
		Dimension:   int32(s.dim),
		Shards:      int32(s.cluster.NumShards()),
		Status:      "ready",
		Collections: s.collections.List(),
	}, nil
}

func (s *Service) ProcessTriplets(_ context.Context, req *pb.ProcessTripletsReq) (*pb.ProcessTripletsResp, error) {
	// Build node map
	nodeMap := make(map[string]string, len(req.Nodes)) // id → text
	for _, n := range req.Nodes {
		if _, exists := nodeMap[n.Id]; !exists {
			nodeMap[n.Id] = n.Text
		}
	}

	seenIDs := make(map[string]bool)
	var triplets []*pb.TripletResult
	skipped := 0

	for _, edge := range req.Edges {
		sourceText, sourceOK := nodeMap[edge.SourceId]
		targetText, targetOK := nodeMap[edge.TargetId]
		if !sourceOK || !targetOK {
			skipped++
			continue
		}
		if edge.RelationshipName == "" {
			skipped++
			continue
		}

		// Relationship text: edge_text or fallback to relationship_name
		relText := edge.EdgeText
		if relText == "" {
			relText = edge.RelationshipName
		}

		// Generate dedup key
		dedupInput := edge.SourceId + edge.RelationshipName + edge.TargetId
		dedupInput = strings.ToLower(dedupInput)
		dedupInput = strings.ReplaceAll(dedupInput, " ", "_")
		dedupInput = strings.ReplaceAll(dedupInput, "'", "")
		tripletID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(dedupInput)).String()

		if seenIDs[tripletID] {
			skipped++
			continue
		}
		seenIDs[tripletID] = true

		// Format embeddable text
		embeddableText := strings.TrimSpace(
			fmt.Sprintf("%s -› %s-›%s", sourceText, relText, targetText),
		)

		triplets = append(triplets, &pb.TripletResult{
			Id:         tripletID,
			FromNodeId: edge.SourceId,
			ToNodeId:   edge.TargetId,
			Text:       embeddableText,
		})
	}

	return &pb.ProcessTripletsResp{
		Triplets: triplets,
		Created:  int32(len(triplets)),
		Skipped:  int32(skipped),
	}, nil
}

func (s *Service) HashFiles(_ context.Context, req *pb.HashFilesReq) (*pb.HashFilesResp, error) {
	maxConcurrent := int(req.MaxConcurrent)
	results := fileio.HashFiles(req.FilePaths, maxConcurrent)

	pbResults := make([]*pb.FileHash, len(results))
	for i, r := range results {
		pbResults[i] = &pb.FileHash{
			FilePath: r.FilePath,
			Sha256:   r.SHA256,
			FileSize: r.FileSize,
			MimeType: r.MimeType,
			Error:    r.Error,
		}
	}
	return &pb.HashFilesResp{Results: pbResults}, nil
}

func (s *Service) ListDirectory(_ context.Context, req *pb.ListDirectoryReq) (*pb.ListDirectoryResp, error) {
	files := fileio.ListDirectory(req.RootPath, req.Recursive, req.Extensions)
	return &pb.ListDirectoryResp{
		FilePaths: files,
		Total:     int32(len(files)),
	}, nil
}

func (s *Service) AggregateSearch(_ context.Context, req *pb.AggregateSearchReq) (*pb.AggregateSearchResp, error) {
	edges := make([]aggregator.ScoredEdge, len(req.Edges))
	for i, e := range req.Edges {
		edges[i] = aggregator.ScoredEdge{
			SourceID: e.SourceId, SourceName: e.SourceName, SourceText: e.SourceText,
			SourceDist:       e.SourceDistance,
			TargetID:         e.TargetId,
			TargetName:       e.TargetName,
			TargetText:       e.TargetText,
			TargetDist:       e.TargetDistance,
			RelationshipName: e.RelationshipName,
			EdgeDist:         e.EdgeDistance,
		}
	}

	result := aggregator.Aggregate(edges, int(req.TopK))

	resp := &pb.AggregateSearchResp{
		FormattedContext: result.FormattedContext,
		UniqueNodes:      int32(result.UniqueNodeCount),
	}
	for _, r := range result.RankedEdges {
		resp.RankedEdges = append(resp.RankedEdges, &pb.RankedEdge{
			SourceId: r.SourceID, SourceName: r.SourceName,
			TargetId: r.TargetID, TargetName: r.TargetName,
			RelationshipName: r.RelationshipName,
			Score:            r.Score,
		})
	}
	return resp, nil
}

func (s *Service) Compact(_ context.Context, _ *pb.Empty) (*pb.CompactResp, error) {
	err := s.collections.Checkpoint()
	resp := &pb.CompactResp{CollectionsCompacted: int32(len(s.collections.List()))}
	if err != nil {
		resp.Error = err.Error()
	}
	return resp, nil
}

// SearchTriplets performs in-memory triplet search scoring on a pre-loaded graph.
// The caller supplies the graph structure + vector distances from DB search;
// Go does the scoring and top-k selection (100-400x faster than Python).
func (s *Service) SearchTriplets(_ context.Context, req *pb.SearchTripletsReq) (*pb.SearchTripletsResp, error) {
	penalty := req.DistancePenalty
	if penalty <= 0 {
		penalty = graph.DefaultDistancePenalty
	}
	topK := int(req.TopK)
	if topK <= 0 {
		topK = 5
	}

	// Build in-memory graph
	g := graph.NewGraph(penalty)

	for _, n := range req.Nodes {
		g.AddNode(n.Id, n.Name, n.Description, n.Type, n.Text)
	}
	for _, e := range req.Edges {
		g.AddEdge(e.Node1Id, e.Node2Id, e.RelationshipType, e.EdgeText, e.EdgeTypeId)
	}

	// Map node distances (from multiple collections)
	for _, coll := range req.NodeDistances {
		entries := make([]graph.DistanceEntry, len(coll.Entries))
		for i, e := range coll.Entries {
			entries[i] = graph.DistanceEntry{ID: e.Id, Distance: e.Distance}
		}
		g.MapNodeDistances(entries)
	}

	// Map edge distances
	if len(req.EdgeDistances) > 0 {
		entries := make([]graph.DistanceEntry, len(req.EdgeDistances))
		for i, e := range req.EdgeDistances {
			entries[i] = graph.DistanceEntry{ID: e.Id, Distance: e.Distance}
		}
		g.MapEdgeDistances(entries)
	}

	// Score and rank
	results := g.SearchTopK(topK)

	// Convert to proto
	triplets := make([]*pb.ScoredTriplet, len(results))
	for i, r := range results {
		triplets[i] = &pb.ScoredTriplet{
			Node1Id:          r.Node1.ID,
			Node1Name:        r.Node1.Name,
			Node1Description: r.Node1.Description,
			Node2Id:          r.Node2.ID,
			Node2Name:        r.Node2.Name,
			Node2Description: r.Node2.Description,
			RelationshipType: r.Edge.RelationshipType,
			EdgeText:         r.Edge.EdgeText,
			Score:            r.Score,
		}
	}

	formatted := graph.FormatTriplets(results)

	return &pb.SearchTripletsResp{
		Triplets:         triplets,
		FormattedContext: formatted,
	}, nil
}

// DeduplicateGraph removes duplicate nodes/edges and generates triplets.
// Mirrors Cognee's deduplicate_nodes_and_edges + _create_triplets_from_graph.
func (s *Service) DeduplicateGraph(_ context.Context, req *pb.DeduplicateGraphReq) (*pb.DeduplicateGraphResp, error) {
	nodes := make([]graph.DedupNode, len(req.Nodes))
	for i, n := range req.Nodes {
		nodes[i] = graph.DedupNode{
			ID: n.Id, Name: n.Name, Description: n.Description,
			Type: n.Type, Text: n.Text,
		}
	}

	edges := make([]graph.DedupEdge, len(req.Edges))
	for i, e := range req.Edges {
		edges[i] = graph.DedupEdge{
			SourceID: e.SourceId, TargetID: e.TargetId,
			RelationshipName: e.RelationshipName, EdgeText: e.EdgeText,
		}
	}

	result := graph.Deduplicate(nodes, edges)

	// Convert back to proto
	pbNodes := make([]*pb.DedupNodeMsg, len(result.Nodes))
	for i, n := range result.Nodes {
		pbNodes[i] = &pb.DedupNodeMsg{
			Id: n.ID, Name: n.Name, Description: n.Description,
			Type: n.Type, Text: n.Text,
		}
	}

	pbEdges := make([]*pb.DedupEdgeMsg, len(result.Edges))
	for i, e := range result.Edges {
		pbEdges[i] = &pb.DedupEdgeMsg{
			SourceId: e.SourceID, TargetId: e.TargetID,
			RelationshipName: e.RelationshipName, EdgeText: e.EdgeText,
		}
	}

	pbTriplets := make([]*pb.TripletResult, len(result.Triplets))
	for i, t := range result.Triplets {
		pbTriplets[i] = &pb.TripletResult{
			Id: t.ID, FromNodeId: t.FromNodeID,
			ToNodeId: t.ToNodeID, Text: t.Text,
		}
	}

	return &pb.DeduplicateGraphResp{
		Nodes:        pbNodes,
		Edges:        pbEdges,
		Triplets:     pbTriplets,
		NodesRemoved: int32(len(req.Nodes) - len(result.Nodes)),
		EdgesRemoved: int32(len(req.Edges) - len(result.Edges)),
	}, nil
}

// BatchEmbedAndIndex embeds texts and inserts vectors into collections in one call.
// Replaces Python's index_data_points asyncio.gather pattern with Go goroutines.
func (s *Service) BatchEmbedAndIndex(ctx context.Context, req *pb.BatchEmbedAndIndexReq) (*pb.BatchEmbedAndIndexResp, error) {
	if req.EmbedEndpoint == "" {
		return nil, status.Errorf(codes.InvalidArgument, "embed_endpoint is required")
	}
	batchSize := int(req.BatchSize)
	if batchSize <= 0 {
		batchSize = 16
	}
	concurrency := int(req.Concurrency)
	if concurrency <= 0 {
		concurrency = 3
	}
	model := req.EmbedModel
	if model == "" {
		model = "text-embedding-3-small"
	}

	embedClient := embed.NewClient(req.EmbedEndpoint, model, batchSize, concurrency)

	var totalEmbedded, totalIndexed, collectionsCreated int32
	var errs []string

	for _, group := range req.Groups {
		if group.Collection == "" || len(group.Items) == 0 {
			continue
		}

		// Create collection if needed
		if !s.collections.Has(group.Collection) {
			if err := s.collections.Create(group.Collection); err != nil {
				errs = append(errs, fmt.Sprintf("create %s: %v", group.Collection, err))
				continue
			}
			collectionsCreated++
		}

		// Extract texts for embedding
		texts := make([]string, len(group.Items))
		for i, item := range group.Items {
			texts[i] = item.Text
		}

		// Embed all texts (batched + concurrent internally)
		vectors, err := embedClient.EmbedTexts(ctx, texts)
		if err != nil {
			errs = append(errs, fmt.Sprintf("embed %s: %v", group.Collection, err))
			continue
		}
		totalEmbedded += int32(len(vectors))

		// Batch insert into collection
		for i, item := range group.Items {
			if i >= len(vectors) {
				break
			}
			meta := item.MetadataJson
			if meta == "" {
				meta = "{}"
			}
			if err := s.collections.Insert(group.Collection, item.Id, vectors[i], meta); err != nil {
				errs = append(errs, fmt.Sprintf("insert %s/%s: %v", group.Collection, item.Id, err))
				continue
			}
			totalIndexed++
		}
	}

	return &pb.BatchEmbedAndIndexResp{
		TotalEmbedded:      totalEmbedded,
		TotalIndexed:       totalIndexed,
		CollectionsCreated: collectionsCreated,
		Errors:             errs,
	}, nil
}
