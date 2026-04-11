package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/stek0v/cognevra/internal/store"
	"github.com/stek0v/cognevra/pkg/aggregator"
	"github.com/stek0v/cognevra/pkg/chunker"
	"github.com/stek0v/cognevra/pkg/embed"
	"github.com/stek0v/cognevra/pkg/extract"
	"github.com/stek0v/cognevra/pkg/ingest"
	"github.com/stek0v/cognevra/pkg/temporal"
	"github.com/stek0v/cognevra/pkg/fileio"
	"github.com/stek0v/cognevra/pkg/graph"
	"github.com/stek0v/cognevra/pkg/bm25"
	"github.com/stek0v/cognevra/pkg/graphdb"
	"github.com/stek0v/cognevra/pkg/llmcache"
	"github.com/stek0v/cognevra/pkg/orchestrator"
	"github.com/stek0v/cognevra/pipeline"
	pb "github.com/stek0v/cognevra/proto/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Service implements the LevaraService gRPC server.
type Service struct {
	pb.UnimplementedCognevraServiceServer
	collections *store.CollectionManager
	cluster     *store.Cluster // legacy: for non-collection operations
	dim            int
	llmCache       *llmcache.Cache
	bm25Indexes    map[string]*bm25.Index
	bm25Mu         sync.RWMutex
	graphCaches    map[string]*graphdb.CachedWriter
	graphCacheMu   sync.Mutex
}

// NewService creates a gRPC service backed by CollectionManager.
func NewService(collections *store.CollectionManager, cluster *store.Cluster, dim int) *Service {
	return &Service{
		collections: collections,
		cluster:     cluster,
		dim:         dim,
		llmCache:    llmcache.New(10000, 0),
		bm25Indexes: make(map[string]*bm25.Index),
		graphCaches: make(map[string]*graphdb.CachedWriter),
	}
}

// BM25Indexes returns the shared BM25 index map for HTTP handlers.
func (s *Service) BM25Indexes() map[string]*bm25.Index {
	return s.bm25Indexes
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

	// Auto dual-index: also add to BM25 for hybrid search
	idx := s.getBM25Index(req.Collection)
	for _, r := range req.Records {
		// Extract text from metadata for BM25
		text := ""
		if r.MetadataJson != "" {
			var meta map[string]any
			if json.Unmarshal([]byte(r.MetadataJson), &meta) == nil {
				if t, ok := meta["text"].(string); ok {
					text = t
				} else if t, ok := meta["name"].(string); ok {
					text = t
				}
			}
		}
		if text != "" {
			idx.Add(r.Id, text, r.MetadataJson)
		}
	}

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

// BatchWriteGraph writes nodes and edges to Neo4j in batch via UNWIND+MERGE.
// Creates a short-lived Neo4j connection per call (caller provides credentials).
func (s *Service) BatchWriteGraph(ctx context.Context, req *pb.BatchWriteGraphReq) (*pb.BatchWriteGraphResp, error) {
	if req.Neo4JUrl == "" {
		return nil, status.Errorf(codes.InvalidArgument, "neo4j_url is required")
	}
	dbName := req.Neo4JDatabase
	if dbName == "" {
		dbName = "neo4j"
	}

	writer, err := graphdb.NewWriter(ctx, req.Neo4JUrl, req.Neo4JUser, req.Neo4JPassword, dbName)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "neo4j connect: %v", err)
	}
	defer writer.Close(ctx)

	// Convert proto nodes
	nodes := make([]graphdb.NodeRecord, len(req.Nodes))
	for i, n := range req.Nodes {
		var props map[string]any
		if n.PropertiesJson != "" {
			json.Unmarshal([]byte(n.PropertiesJson), &props)
		}
		if props == nil {
			props = map[string]any{}
		}
		nodes[i] = graphdb.NodeRecord{
			ID:         n.Id,
			Label:      n.Label,
			Properties: props,
		}
	}

	// Convert proto edges
	edges := make([]graphdb.EdgeRecord, len(req.Edges))
	for i, e := range req.Edges {
		var props map[string]any
		if e.PropertiesJson != "" {
			json.Unmarshal([]byte(e.PropertiesJson), &props)
		}
		if props == nil {
			props = map[string]any{}
		}
		edges[i] = graphdb.EdgeRecord{
			SourceID:         e.SourceId,
			TargetID:         e.TargetId,
			RelationshipName: e.RelationshipName,
			Properties:       props,
		}
	}

	result := writer.BatchWrite(ctx, nodes, edges)

	return &pb.BatchWriteGraphResp{
		NodesWritten: int32(result.NodesWritten),
		EdgesWritten: int32(result.EdgesWritten),
		Errors:       result.Errors,
	}, nil
}

// ParallelWriteDataPoints performs dedup + Neo4j write + embed + vector index
// in parallel goroutines. Replaces 8 sequential Python calls with 2 parallel phases.
func (s *Service) ParallelWriteDataPoints(ctx context.Context, req *pb.ParallelWriteReq) (*pb.ParallelWriteResp, error) {
	totalStart := time.Now()
	resp := &pb.ParallelWriteResp{}

	// --- Phase 0: Dedup ---
	dedupStart := time.Now()
	nodes := make([]graph.DedupNode, len(req.Nodes))
	for i, n := range req.Nodes {
		nodes[i] = graph.DedupNode{ID: n.Id, Name: n.Name, Description: n.Description, Type: n.Type, Text: n.Text}
	}
	edges := make([]graph.DedupEdge, len(req.Edges))
	for i, e := range req.Edges {
		edges[i] = graph.DedupEdge{SourceID: e.SourceId, TargetID: e.TargetId, RelationshipName: e.RelationshipName, EdgeText: e.EdgeText}
	}

	dedupResult := graph.Deduplicate(nodes, edges)
	resp.NodesDeduped = int32(len(req.Nodes) - len(dedupResult.Nodes))
	resp.EdgesDeduped = int32(len(req.Edges) - len(dedupResult.Edges))
	resp.DedupMs = time.Since(dedupStart).Milliseconds()

	// --- Prepare Neo4j writer (if configured) ---
	var neo4jWriter *graphdb.Writer
	if req.Neo4JUrl != "" {
		dbName := req.Neo4JDatabase
		if dbName == "" {
			dbName = "neo4j"
		}
		w, err := graphdb.NewWriter(ctx, req.Neo4JUrl, req.Neo4JUser, req.Neo4JPassword, dbName)
		if err != nil {
			resp.Errors = append(resp.Errors, fmt.Sprintf("neo4j connect: %v", err))
		} else {
			neo4jWriter = w
			defer w.Close(ctx)
		}
	}

	// --- Prepare embed client (if configured) ---
	var embedClient *embed.Client
	if req.EmbedEndpoint != "" {
		batchSize := int(req.EmbedBatchSize)
		if batchSize <= 0 {
			batchSize = 16
		}
		model := req.EmbedModel
		if model == "" {
			model = "text-embedding-3-small"
		}
		embedClient = embed.NewClient(req.EmbedEndpoint, model, batchSize, 3)
	}

	// --- Phase 1: Nodes (parallel: Neo4j + embed+index) ---
	phase1Start := time.Now()
	type phase1Result struct {
		neo4jNodes int
		neo4jErr   error
		embedCount int
		indexCount int
		collsCreated int
		embedErr   error
	}
	p1ch := make(chan phase1Result, 2)

	// Goroutine 1: Neo4j nodes
	go func() {
		r := phase1Result{}
		if neo4jWriter != nil {
			neoNodes := make([]graphdb.NodeRecord, len(dedupResult.Nodes))
			for i, n := range dedupResult.Nodes {
				props := map[string]any{"name": n.Name, "description": n.Description, "type": n.Type, "text": n.Text}
				neoNodes[i] = graphdb.NodeRecord{ID: n.ID, Label: n.Type, Properties: props}
			}
			res := neo4jWriter.BatchWrite(ctx, neoNodes, nil)
			r.neo4jNodes = res.NodesWritten
			if len(res.Errors) > 0 {
				r.neo4jErr = fmt.Errorf("%s", strings.Join(res.Errors, "; "))
			}
		}
		p1ch <- r
	}()

	// Goroutine 2: Embed + index nodes
	go func() {
		r := phase1Result{}
		if embedClient != nil && len(req.IndexGroups) > 0 {
			for _, grp := range req.IndexGroups {
				if grp.Collection == "" || len(grp.Items) == 0 {
					continue
				}
				if !s.collections.Has(grp.Collection) {
					if err := s.collections.Create(grp.Collection); err != nil {
						continue
					}
					r.collsCreated++
				}
				texts := make([]string, len(grp.Items))
				for i, item := range grp.Items {
					texts[i] = item.Text
				}
				vecs, err := embedClient.EmbedTexts(ctx, texts)
				if err != nil {
					r.embedErr = err
					continue
				}
				r.embedCount += len(vecs)
				for i, item := range grp.Items {
					if i < len(vecs) {
						meta := item.MetadataJson
						if meta == "" {
							meta = "{}"
						}
						if err := s.collections.Insert(grp.Collection, item.Id, vecs[i], meta); err == nil {
							r.indexCount++
						}
					}
				}
			}
		}
		p1ch <- r
	}()

	// Collect Phase 1 results
	var p1neo, p1embed phase1Result
	for i := 0; i < 2; i++ {
		r := <-p1ch
		if r.neo4jNodes > 0 || r.neo4jErr != nil {
			p1neo = r
		} else {
			p1embed = r
		}
	}

	resp.Neo4JNodesWritten = int32(p1neo.neo4jNodes)
	if p1neo.neo4jErr != nil {
		resp.Errors = append(resp.Errors, fmt.Sprintf("neo4j nodes: %v", p1neo.neo4jErr))
	}
	resp.VectorsEmbedded = int32(p1embed.embedCount)
	resp.VectorsIndexed = int32(p1embed.indexCount)
	resp.CollectionsCreated = int32(p1embed.collsCreated)
	if p1embed.embedErr != nil {
		resp.Errors = append(resp.Errors, fmt.Sprintf("embed nodes: %v", p1embed.embedErr))
	}
	resp.Neo4JNodesMs = time.Since(phase1Start).Milliseconds()

	// --- Phase 2: Edges (parallel: Neo4j + embed edge types) ---
	phase2Start := time.Now()
	type phase2Result struct {
		neo4jEdges int
		neo4jErr   error
		triplets   int
	}
	p2ch := make(chan phase2Result, 1)

	// Goroutine 3: Neo4j edges
	go func() {
		r := phase2Result{}
		if neo4jWriter != nil && len(dedupResult.Edges) > 0 {
			neoEdges := make([]graphdb.EdgeRecord, len(dedupResult.Edges))
			for i, e := range dedupResult.Edges {
				props := map[string]any{"edge_text": e.EdgeText}
				neoEdges[i] = graphdb.EdgeRecord{
					SourceID: e.SourceID, TargetID: e.TargetID,
					RelationshipName: e.RelationshipName, Properties: props,
				}
			}
			res := neo4jWriter.BatchWrite(ctx, nil, neoEdges)
			r.neo4jEdges = res.EdgesWritten
			if len(res.Errors) > 0 {
				r.neo4jErr = fmt.Errorf("%s", strings.Join(res.Errors, "; "))
			}
		}
		// Triplets
		if req.GenerateTriplets {
			r.triplets = len(dedupResult.Triplets)
		}
		p2ch <- r
	}()

	p2 := <-p2ch
	resp.Neo4JEdgesWritten = int32(p2.neo4jEdges)
	if p2.neo4jErr != nil {
		resp.Errors = append(resp.Errors, fmt.Sprintf("neo4j edges: %v", p2.neo4jErr))
	}
	resp.TripletsGenerated = int32(p2.triplets)
	resp.Neo4JEdgesMs = time.Since(phase2Start).Milliseconds()

	// Embed+index triplets if requested
	if req.GenerateTriplets && embedClient != nil && len(dedupResult.Triplets) > 0 {
		tripletTexts := make([]string, len(dedupResult.Triplets))
		for i, t := range dedupResult.Triplets {
			tripletTexts[i] = t.Text
		}
		if !s.collections.Has("Triplet_text") {
			s.collections.Create("Triplet_text")
		}
		if vecs, err := embedClient.EmbedTexts(ctx, tripletTexts); err == nil {
			for i, t := range dedupResult.Triplets {
				if i < len(vecs) {
					meta := fmt.Sprintf(`{"from_node_id":"%s","to_node_id":"%s"}`, t.FromNodeID, t.ToNodeID)
					s.collections.Insert("Triplet_text", t.ID, vecs[i], meta)
				}
			}
			resp.VectorsEmbedded += int32(len(vecs))
			resp.VectorsIndexed += int32(len(vecs))
		}
	}

	resp.EmbedIndexMs = time.Since(phase1Start).Milliseconds()
	resp.TotalMs = time.Since(totalStart).Milliseconds()

	return resp, nil
}

// SearchByText embeds query text and searches in-process — no Python roundtrip.
// Replaces: Python embed_data([query]) → gRPC Search → deserialize
// With: single gRPC call → Go embed HTTP → in-process HNSW search
func (s *Service) SearchByText(ctx context.Context, req *pb.SearchByTextReq) (*pb.SearchResp, error) {
	if req.Collection == "" || req.QueryText == "" {
		return nil, status.Errorf(codes.InvalidArgument, "collection and query_text required")
	}
	if req.EmbedEndpoint == "" {
		return nil, status.Errorf(codes.InvalidArgument, "embed_endpoint required")
	}

	topK := int(req.TopK)
	if topK <= 0 {
		topK = 10
	}
	model := req.EmbedModel
	if model == "" {
		model = "text-embedding-3-small"
	}

	embedClient := embed.NewClient(req.EmbedEndpoint, model, 16, 1)
	sp := pipeline.NewSearchPipeline(embedClient, s.collections, nil)

	results, err := sp.SearchByText(ctx, req.Collection, req.QueryText, topK)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "search: %v", err)
	}

	pbResults := make([]*pb.SearchResult, len(results))
	for i, r := range results {
		pbResults[i] = &pb.SearchResult{
			Id:           r.ID,
			Score:        r.Score,
			MetadataJson: string(r.Metadata),
		}
	}

	return &pb.SearchResp{Results: pbResults}, nil
}

// BatchSearchByText embeds multiple queries and searches — all in one gRPC call.
func (s *Service) BatchSearchByText(ctx context.Context, req *pb.BatchSearchByTextReq) (*pb.BatchSearchByTextResp, error) {
	if req.Collection == "" || len(req.Queries) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "collection and queries required")
	}
	if req.EmbedEndpoint == "" {
		return nil, status.Errorf(codes.InvalidArgument, "embed_endpoint required")
	}

	topK := int(req.TopK)
	if topK <= 0 {
		topK = 10
	}
	model := req.EmbedModel
	if model == "" {
		model = "text-embedding-3-small"
	}

	embedClient := embed.NewClient(req.EmbedEndpoint, model, 16, 3)
	sp := pipeline.NewSearchPipeline(embedClient, s.collections, nil)

	batchResults, err := sp.BatchSearchByText(ctx, req.Collection, req.Queries, topK)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "batch search: %v", err)
	}

	groups := make([]*pb.SearchResultGroup, len(req.Queries))
	for i, query := range req.Queries {
		results := make([]*pb.SearchResult, 0)
		if i < len(batchResults) {
			for _, r := range batchResults[i] {
				results = append(results, &pb.SearchResult{
					Id:           r.ID,
					Score:        r.Score,
					MetadataJson: string(r.Metadata),
				})
			}
		}
		groups[i] = &pb.SearchResultGroup{
			Query:   query,
			Results: results,
		}
	}

	return &pb.BatchSearchByTextResp{Results: groups}, nil
}

func (s *Service) getGraphCache(ctx context.Context, url, user, pass, db string) (*graphdb.CachedWriter, error) {
	key := url + "|" + db
	s.graphCacheMu.Lock()
	defer s.graphCacheMu.Unlock()

	if cw, ok := s.graphCaches[key]; ok {
		return cw, nil
	}

	writer, err := graphdb.NewWriter(ctx, url, user, pass, db)
	if err != nil {
		return nil, err
	}
	cw := graphdb.NewCachedWriter(writer, 1000, 5*time.Minute)
	s.graphCaches[key] = cw
	return cw, nil
}

// GraphRead executes Neo4j read queries for search-time graph projection.
// Replaces 4 separate Python→Neo4j queries with one Go gRPC call.
func (s *Service) GraphRead(ctx context.Context, req *pb.GraphReadReq) (*pb.GraphReadResp, error) {
	if req.Neo4JUrl == "" {
		return nil, status.Errorf(codes.InvalidArgument, "neo4j_url required")
	}
	dbName := req.Neo4JDatabase
	if dbName == "" {
		dbName = "neo4j"
	}

	start := time.Now()

	cw, err := s.getGraphCache(ctx, req.Neo4JUrl, req.Neo4JUser, req.Neo4JPassword, dbName)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "neo4j: %v", err)
	}

	var result graphdb.GraphReadResult

	switch req.Mode {
	case pb.GraphReadReq_FULL_GRAPH:
		result, err = cw.ReadFullGraph(ctx)
	case pb.GraphReadReq_ID_FILTERED:
		result, err = cw.ReadIDFiltered(ctx, req.NodeIds)
	case pb.GraphReadReq_NEIGHBOURS:
		if len(req.NodeIds) == 0 {
			return nil, status.Errorf(codes.InvalidArgument, "node_ids required for NEIGHBOURS mode")
		}
		result, err = cw.ReadNeighbours(ctx, req.NodeIds[0])
	case pb.GraphReadReq_SUBGRAPH:
		result, err = cw.ReadSubgraph(ctx, req.NodeLabel, req.NodeNames)
	default:
		result, err = cw.ReadFullGraph(ctx)
	}

	if err != nil {
		return nil, status.Errorf(codes.Internal, "graph read: %v", err)
	}

	// Convert to proto
	pbNodes := make([]*pb.GraphReadNode, len(result.Nodes))
	for i, n := range result.Nodes {
		propsJSON, _ := json.Marshal(n.Properties)
		pbNodes[i] = &pb.GraphReadNode{
			Id: n.ID, Label: n.Label, PropertiesJson: string(propsJSON),
		}
	}

	pbEdges := make([]*pb.GraphReadEdge, len(result.Edges))
	for i, e := range result.Edges {
		propsJSON, _ := json.Marshal(e.Properties)
		pbEdges[i] = &pb.GraphReadEdge{
			SourceId: e.SourceID, TargetId: e.TargetID,
			RelationshipType: e.RelationshipType, PropertiesJson: string(propsJSON),
		}
	}

	return &pb.GraphReadResp{
		Nodes:   pbNodes,
		Edges:   pbEdges,
		QueryMs: time.Since(start).Milliseconds(),
	}, nil
}

// GraphCompletionSearch performs the full search pipeline in one Go call:
// 1. Embed query → 2. Vector search (multi-collection) → 3. Neo4j graph read
// → 4. Triplet scoring → 5. Format context for LLM
//
// Replaces Python's TripletSearchContextProvider which does 5+ separate async calls.
func (s *Service) GraphCompletionSearch(ctx context.Context, req *pb.GraphCompletionSearchReq) (*pb.GraphCompletionSearchResp, error) {
	totalStart := time.Now()
	resp := &pb.GraphCompletionSearchResp{}

	if req.QueryText == "" || req.EmbedEndpoint == "" {
		return nil, status.Errorf(codes.InvalidArgument, "query_text and embed_endpoint required")
	}

	vectorTopK := int(req.VectorTopK)
	if vectorTopK <= 0 {
		vectorTopK = 100
	}
	tripletTopK := int(req.TripletTopK)
	if tripletTopK <= 0 {
		tripletTopK = 5
	}
	penalty := req.DistancePenalty
	if penalty <= 0 {
		penalty = graph.DefaultDistancePenalty
	}
	model := req.EmbedModel
	if model == "" {
		model = "text-embedding-3-small"
	}

	embedClient := embed.NewClient(req.EmbedEndpoint, model, 16, 1)

	// --- Step 1: Embed query ---
	embedStart := time.Now()
	queryVec, err := embedClient.EmbedSingle(ctx, req.QueryText)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "embed: %v", err)
	}
	resp.EmbedMs = time.Since(embedStart).Milliseconds()

	// --- Step 2: Vector search across collections (parallel) ---
	vsStart := time.Now()

	type vsResult struct {
		collection string
		results    []struct{ id string; score float32 }
		isEdge     bool
	}

	allCollections := make([]string, 0, len(req.NodeCollections)+1)
	allCollections = append(allCollections, req.NodeCollections...)
	if req.EdgeCollection != "" {
		allCollections = append(allCollections, req.EdgeCollection)
	}

	vsCh := make(chan vsResult, len(allCollections))
	for _, coll := range allCollections {
		coll := coll
		isEdge := coll == req.EdgeCollection
		go func() {
			r := vsResult{collection: coll, isEdge: isEdge}
			if s.collections.Has(coll) {
				searchResults, err := s.collections.Search(coll, queryVec, vectorTopK)
				if err == nil {
					for _, sr := range searchResults {
						r.results = append(r.results, struct{ id string; score float32 }{sr.ID, sr.Score})
					}
				}
			}
			vsCh <- r
		}()
	}

	// Collect vector search results
	var nodeDistEntries []graph.DistanceEntry
	var edgeDistEntries []graph.DistanceEntry
	relevantIDs := make(map[string]bool)
	totalVectorResults := 0

	for i := 0; i < len(allCollections); i++ {
		r := <-vsCh
		totalVectorResults += len(r.results)
		for _, sr := range r.results {
			if r.isEdge {
				edgeDistEntries = append(edgeDistEntries, graph.DistanceEntry{ID: sr.id, Distance: sr.score})
			} else {
				nodeDistEntries = append(nodeDistEntries, graph.DistanceEntry{ID: sr.id, Distance: sr.score})
				relevantIDs[sr.id] = true
			}
		}
	}

	resp.VectorSearchMs = time.Since(vsStart).Milliseconds()
	resp.VectorResults = int32(totalVectorResults)

	// --- Step 3: Neo4j graph read (ID-filtered for relevant nodes) ---
	graphStart := time.Now()
	var graphNodes []graph.Node
	var graphEdges []graph.Edge // reuse from pkg/graph for scoring

	if req.Neo4JUrl != "" && len(relevantIDs) > 0 {
		dbName := req.Neo4JDatabase
		if dbName == "" {
			dbName = "neo4j"
		}
		writer, err := graphdb.NewWriter(ctx, req.Neo4JUrl, req.Neo4JUser, req.Neo4JPassword, dbName)
		if err == nil {
			defer writer.Close(ctx)
			ids := make([]string, 0, len(relevantIDs))
			for id := range relevantIDs {
				ids = append(ids, id)
			}
			readResult, err := writer.ReadIDFiltered(ctx, ids)
			if err == nil {
				for _, n := range readResult.Nodes {
					name, _ := n.Properties["name"].(string)
					desc, _ := n.Properties["description"].(string)
					typ, _ := n.Properties["type"].(string)
					text, _ := n.Properties["text"].(string)
					graphNodes = append(graphNodes, graph.Node{
						ID: n.ID, Name: name, Description: desc, Type: typ, Text: text,
						Distance: penalty,
					})
				}
				for _, e := range readResult.Edges {
					relName, _ := e.Properties["relationship_name"].(string)
					if relName == "" {
						relName = e.RelationshipType
					}
					edgeText, _ := e.Properties["edge_text"].(string)
					graphEdges = append(graphEdges, graph.Edge{
						Node1ID: e.SourceID, Node2ID: e.TargetID,
						RelationshipType: relName, EdgeText: edgeText,
						EdgeTypeID: e.RelationshipType, Distance: penalty,
					})
				}
			}
		}
	}

	resp.GraphReadMs = time.Since(graphStart).Milliseconds()
	resp.GraphNodes = int32(len(graphNodes))
	resp.GraphEdges = int32(len(graphEdges))

	// --- Step 4: Build in-memory graph + score triplets ---
	scoringStart := time.Now()

	g := graph.NewGraph(penalty)
	for _, n := range graphNodes {
		g.AddNode(n.ID, n.Name, n.Description, n.Type, n.Text)
	}
	for _, e := range graphEdges {
		g.AddEdge(e.Node1ID, e.Node2ID, e.RelationshipType, e.EdgeText, e.EdgeTypeID)
	}

	g.MapNodeDistances(nodeDistEntries)
	g.MapEdgeDistances(edgeDistEntries)

	scoredResults := g.SearchTopK(tripletTopK)
	resp.ScoringMs = time.Since(scoringStart).Milliseconds()

	// --- Step 5: Format results ---
	triplets := make([]*pb.ScoredTriplet, len(scoredResults))
	for i, r := range scoredResults {
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

	resp.Triplets = triplets
	resp.FormattedContext = graph.FormatTriplets(scoredResults)
	resp.TotalMs = time.Since(totalStart).Milliseconds()

	return resp, nil
}

// LLMCacheGet checks the cache for a previously stored LLM response.
func (s *Service) LLMCacheGet(_ context.Context, req *pb.LLMCacheGetReq) (*pb.LLMCacheGetResp, error) {
	key := llmcache.Key(req.Model, req.Prompt, req.SystemPrompt, req.Temperature)
	resp, hit := s.llmCache.Get(key)
	return &pb.LLMCacheGetResp{
		Hit:      hit,
		Response: resp,
		CacheKey: key,
	}, nil
}

// LLMCachePut stores an LLM response in the cache.
func (s *Service) LLMCachePut(_ context.Context, req *pb.LLMCachePutReq) (*pb.StatusResp, error) {
	key := llmcache.Key(req.Model, req.Prompt, req.SystemPrompt, req.Temperature)
	s.llmCache.Put(key, req.Response, req.Model)
	return &pb.StatusResp{Ok: true}, nil
}

// LLMCacheStats returns cache hit/miss statistics.
func (s *Service) LLMCacheStats(_ context.Context, _ *pb.Empty) (*pb.LLMCacheStatsResp, error) {
	stats := s.llmCache.Stats()
	return &pb.LLMCacheStatsResp{
		Size:    int32(stats.Size),
		MaxSize: int32(stats.MaxSize),
		Hits:    stats.Hits,
		Misses:  stats.Misses,
		HitRate: float32(stats.HitRate),
	}, nil
}

func (s *Service) getBM25Index(collection string) *bm25.Index {
	s.bm25Mu.RLock()
	idx, ok := s.bm25Indexes[collection]
	s.bm25Mu.RUnlock()
	if ok {
		return idx
	}
	s.bm25Mu.Lock()
	defer s.bm25Mu.Unlock()
	if idx, ok = s.bm25Indexes[collection]; ok {
		return idx
	}
	idx = bm25.NewIndex()
	s.bm25Indexes[collection] = idx
	return idx
}

// BM25Index adds documents to a BM25 inverted index.
func (s *Service) BM25Index(_ context.Context, req *pb.BM25IndexReq) (*pb.StatusResp, error) {
	if req.Collection == "" {
		return nil, status.Errorf(codes.InvalidArgument, "collection required")
	}
	idx := s.getBM25Index(req.Collection)
	for _, item := range req.Items {
		idx.Add(item.Id, item.Text, item.MetadataJson)
	}
	return &pb.StatusResp{Ok: true}, nil
}

// BM25Search performs keyword/lexical search using BM25 scoring.
func (s *Service) BM25Search(_ context.Context, req *pb.BM25SearchReq) (*pb.BM25SearchResp, error) {
	if req.Collection == "" || req.Query == "" {
		return nil, status.Errorf(codes.InvalidArgument, "collection and query required")
	}
	topK := int(req.TopK)
	if topK <= 0 {
		topK = 10
	}

	s.bm25Mu.RLock()
	idx, ok := s.bm25Indexes[req.Collection]
	s.bm25Mu.RUnlock()
	if !ok {
		return &pb.BM25SearchResp{}, nil // empty collection
	}

	results := idx.Search(req.Query, topK)
	pbResults := make([]*pb.BM25Result, len(results))
	for i, r := range results {
		pbResults[i] = &pb.BM25Result{Id: r.ID, Score: r.Score, MetadataJson: r.Metadata}
	}
	return &pb.BM25SearchResp{Results: pbResults}, nil
}

// HybridSearch combines vector similarity + BM25 lexical search via Reciprocal Rank Fusion.
func (s *Service) HybridSearch(ctx context.Context, req *pb.HybridSearchReq) (*pb.HybridSearchResp, error) {
	if req.Collection == "" || req.QueryText == "" || req.EmbedEndpoint == "" {
		return nil, status.Errorf(codes.InvalidArgument, "collection, query_text, embed_endpoint required")
	}

	topK := int(req.TopK)
	if topK <= 0 {
		topK = 10
	}
	model := req.EmbedModel
	if model == "" {
		model = "text-embedding-3-small"
	}

	// Run vector search + BM25 search in parallel
	type vsOut struct {
		results []bm25.VectorResult
		err     error
	}
	type bm25Out struct {
		results []bm25.Result
	}

	vsCh := make(chan vsOut, 1)
	bmCh := make(chan bm25Out, 1)

	// Vector search goroutine
	go func() {
		embedClient := embed.NewClient(req.EmbedEndpoint, model, 16, 1)
		vec, err := embedClient.EmbedSingle(ctx, req.QueryText)
		if err != nil {
			vsCh <- vsOut{err: err}
			return
		}
		searchResults, err := s.collections.Search(req.Collection, vec, topK*2)
		if err != nil {
			vsCh <- vsOut{err: err}
			return
		}
		vr := make([]bm25.VectorResult, len(searchResults))
		for i, sr := range searchResults {
			vr[i] = bm25.VectorResult{ID: sr.ID, Score: sr.Score, Metadata: string(sr.Data)}
		}
		vsCh <- vsOut{results: vr}
	}()

	// BM25 search goroutine
	go func() {
		s.bm25Mu.RLock()
		idx, ok := s.bm25Indexes[req.Collection]
		s.bm25Mu.RUnlock()
		if !ok {
			bmCh <- bm25Out{}
			return
		}
		bmCh <- bm25Out{results: idx.Search(req.QueryText, topK*2)}
	}()

	vsResult := <-vsCh
	bmResult := <-bmCh

	if vsResult.err != nil {
		return nil, status.Errorf(codes.Internal, "vector search: %v", vsResult.err)
	}

	vw := float64(req.VectorWeight)
	bw := float64(req.Bm25Weight)

	hybridResults := bm25.HybridSearch(vsResult.results, bmResult.results, topK, vw, bw)

	pbResults := make([]*pb.HybridResult, len(hybridResults))
	for i, r := range hybridResults {
		pbResults[i] = &pb.HybridResult{
			Id:          r.ID,
			VectorScore: r.VectorScore,
			Bm25Score:   r.BM25Score,
			FusedScore:  r.FusedScore,
			VectorRank:  int32(r.VectorRank),
			Bm25Rank:    int32(r.BM25Rank),
			MetadataJson: r.Metadata,
		}
	}

	return &pb.HybridSearchResp{Results: pbResults}, nil
}

// PipelineCognify runs the full cognify pipeline with streaming progress updates.
// Stages: chunk → LLM extract → dedup → write (parallel Neo4j + vector).
func (s *Service) PipelineCognify(req *pb.PipelineCognifyReq, stream pb.CognevraService_PipelineCognifyServer) error {
	if len(req.Texts) == 0 {
		return status.Errorf(codes.InvalidArgument, "texts required")
	}

	cfg := orchestrator.Config{
		ChunkStrategy:   req.ChunkStrategy,
		MinChunkChars:   int(req.MinChunkChars),
		MaxChunkChars:   int(req.MaxChunkChars),
		LLMEndpoint:     req.LlmEndpoint,
		LLMModel:        req.LlmModel,
		SystemPrompt:    req.ExtractionSystemPrompt,
		Temperature:     req.LlmTemperature,
		LLMConcurrency:  int(req.LlmConcurrency),
		EmbedEndpoint:   req.EmbedEndpoint,
		EmbedModel:      req.EmbedModel,
		Neo4jURL:        req.Neo4JUrl,
		Neo4jUser:       req.Neo4JUser,
		Neo4jPassword:   req.Neo4JPassword,
		Neo4jDatabase:   req.Neo4JDatabase,
		Collection:      req.Collection,
		Collections:     s.collections,
		GenerateTriplets: req.GenerateTriplets,
	}

	progressCh := make(chan orchestrator.Progress, 100)

	// Run pipeline in goroutine, stream progress
	errCh := make(chan error, 1)
	go func() {
		errCh <- orchestrator.Run(stream.Context(), req.Texts, cfg, progressCh)
	}()

	// Stream progress to client
	for p := range progressCh {
		if err := stream.Send(&pb.PipelineCognifyProgress{
			Stage:             p.Stage,
			ItemsTotal:        int32(p.ItemsTotal),
			ItemsProcessed:    int32(p.ItemsProcessed),
			ChunksCreated:     int32(p.ChunksCreated),
			EntitiesExtracted: int32(p.EntitiesExtracted),
			EdgesExtracted:    int32(p.EdgesExtracted),
			NodesWritten:      int32(p.NodesWritten),
			EdgesWritten:      int32(p.EdgesWritten),
			Message:           p.Message,
			ElapsedMs:         p.ElapsedMs,
		}); err != nil {
			return err
		}
	}

	return <-errCh
}

// SemanticDedup removes near-duplicate vectors by cosine similarity.
func (s *Service) SemanticDedup(_ context.Context, req *pb.SemanticDedupReq) (*pb.SemanticDedupResp, error) {
	threshold := req.Threshold
	if threshold <= 0 {
		threshold = 0.95
	}

	vectors := make([][]float32, len(req.Vectors))
	ids := make([]string, len(req.Vectors))
	for i, v := range req.Vectors {
		vectors[i] = v.Vector
		ids[i] = v.Id
	}

	// Use LSH for large inputs (100+), brute-force for small
	var result graph.SemanticDedupResult
	if len(vectors) >= 100 {
		result = graph.SemanticDedupLSH(vectors, threshold, 12, 10)
	} else {
		result = graph.SemanticDedup(vectors, threshold)
	}

	keptIDs := make([]string, len(result.Kept))
	for i, idx := range result.Kept {
		keptIDs[i] = ids[idx]
	}
	removedIDs := make([]string, len(result.Removed))
	for i, idx := range result.Removed {
		removedIDs[i] = ids[idx]
	}

	return &pb.SemanticDedupResp{
		KeptIds:         keptIDs,
		RemovedIds:      removedIDs,
		DuplicatesFound: int32(len(result.Removed)),
	}, nil
}

// MultiQuerySearch decomposes a complex query, runs parallel searches, merges results.
func (s *Service) MultiQuerySearch(ctx context.Context, req *pb.MultiQuerySearchReq) (*pb.MultiQuerySearchResp, error) {
	if req.QueryText == "" || req.Collection == "" || req.EmbedEndpoint == "" {
		return nil, status.Errorf(codes.InvalidArgument, "query_text, collection, embed_endpoint required")
	}

	topK := int(req.TopK)
	if topK <= 0 {
		topK = 10
	}
	model := req.EmbedModel
	if model == "" {
		model = "text-embedding-3-small"
	}

	// Decompose query
	subQueries := graph.DecomposeQuery(req.QueryText)

	// Parallel search for each sub-query
	embedClient := embed.NewClient(req.EmbedEndpoint, model, 16, len(subQueries))
	sp := pipeline.NewSearchPipeline(embedClient, s.collections, nil)

	type searchOut struct {
		idx     int
		results []graph.SearchResultEntry
	}
	ch := make(chan searchOut, len(subQueries))

	for i, sq := range subQueries {
		i, sq := i, sq
		go func() {
			res, err := sp.SearchByText(ctx, req.Collection, sq, topK*2) // wider search for merge
			if err != nil {
				ch <- searchOut{idx: i}
				return
			}
			entries := make([]graph.SearchResultEntry, len(res))
			for j, r := range res {
				entries[j] = graph.SearchResultEntry{ID: r.ID, Score: r.Score, Metadata: string(r.Metadata)}
			}
			ch <- searchOut{idx: i, results: entries}
		}()
	}

	subResults := make([][]graph.SearchResultEntry, len(subQueries))
	for range subQueries {
		out := <-ch
		subResults[out.idx] = out.results
	}

	// Merge results
	merged := graph.MergeResults(subResults, topK)

	pbResults := make([]*pb.MultiQueryResult, len(merged))
	for i, r := range merged {
		pbResults[i] = &pb.MultiQueryResult{
			Id: r.ID, BestScore: r.BestScore, Appearances: int32(r.Appearances),
			FusedScore: r.FusedScore, MetadataJson: r.Metadata,
		}
	}

	return &pb.MultiQuerySearchResp{
		SubQueries:  subQueries,
		Results:     pbResults,
		TotalUnique: int32(len(merged)),
	}, nil
}

// IngestData performs fast data ingestion: hash + save + classify in one Go call.
// Replaces Python's 3x MD5 + 2x disk write with single-pass SHA256 + 1 write.
func (s *Service) IngestData(ctx context.Context, req *pb.IngestDataReq) (*pb.IngestDataResp, error) {
	start := time.Now()

	storagePath := req.StoragePath
	if storagePath == "" {
		storagePath = "data/ingested"
	}

	items := make([]ingest.Item, len(req.Items))
	for i, it := range req.Items {
		items[i] = ingest.Item{
			ID:          it.Id,
			Text:        it.Text,
			FileData:    it.FileData,
			Filename:    it.Filename,
			DatasetName: it.DatasetName,
		}
	}

	results, err := ingest.Ingest(items, storagePath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "ingest: %v", err)
	}

	pbResults := make([]*pb.IngestResult, len(results))
	for i, r := range results {
		pbResults[i] = &pb.IngestResult{
			Id:            r.ID,
			ContentHash:   r.ContentHash,
			FilePath:      r.FilePath,
			MimeType:      r.MimeType,
			Extension:     r.Extension,
			FileSize:      r.FileSize,
			Name:          r.Name,
			AlreadyExists: r.AlreadyExists,
		}
	}

	resp := &pb.IngestDataResp{
		Results: pbResults,
		TotalMs: time.Since(start).Milliseconds(),
	}

	// Optional: write metadata to PostgreSQL
	if req.PostgresDsn != "" && req.OwnerId != "" {
		mw, err := ingest.NewMetadataWriter(req.PostgresDsn)
		if err == nil {
			defer mw.Close()
			n, err := mw.WriteMetadata(ctx, results, req.OwnerId, req.DatasetId, req.DatasetName)
			if err == nil {
				resp.DbRowsWritten = int32(n)
				resp.DatasetId = req.DatasetId
			}
		}
	}

	resp.TotalMs = time.Since(start).Milliseconds()
	return resp, nil
}

// ExtractText extracts text from PDF/DOCX/TXT files in pure Go.
// Replaces Python's pypdf + unstructured loaders.
func (s *Service) ExtractText(_ context.Context, req *pb.ExtractTextReq) (*pb.ExtractTextResp, error) {
	if len(req.FileData) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "file_data required")
	}

	result, err := extract.Extract(req.FileData, req.Filename, req.MimeType)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "extract: %v", err)
	}

	resp := &pb.ExtractTextResp{
		Text:      result.Text,
		Format:    result.Format,
		Pages:     int32(result.Pages),
		ExtractMs: result.ExtractMs,
		Warnings:  result.Warnings,
	}
	if req.IncludeMarkdown {
		resp.Markdown = result.Markdown
	}
	return resp, nil
}

// TemporalSearch extracts timestamps from text and filters by date range.
// Covers Cognee's TEMPORAL search type.
func (s *Service) TemporalSearch(_ context.Context, req *pb.TemporalSearchReq) (*pb.TemporalSearchResp, error) {
	if req.Text == "" {
		return nil, status.Errorf(codes.InvalidArgument, "text required")
	}

	events := temporal.ExtractTimestamps(req.Text, time.Now())
	totalExtracted := len(events)

	// Apply date range filter
	if req.DateFrom != "" || req.DateTo != "" {
		from := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
		to := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
		if req.DateFrom != "" {
			if t, err := time.Parse("2006-01-02", req.DateFrom); err == nil {
				from = t
			}
		}
		if req.DateTo != "" {
			if t, err := time.Parse("2006-01-02", req.DateTo); err == nil {
				to = t
			}
		}
		events = temporal.FilterByRange(events, from, to)
	}

	pbEvents := make([]*pb.TemporalEvent, len(events))
	for i, e := range events {
		pbEvents[i] = &pb.TemporalEvent{
			Text:         e.Text,
			Date:         e.Date.Format("2006-01-02"),
			DateOriginal: e.DateStr,
			Confidence:   e.Confidence,
			NodeId:       e.NodeID,
		}
	}

	return &pb.TemporalSearchResp{
		Events:         pbEvents,
		TotalExtracted: int32(totalExtracted),
		InRange:        int32(len(events)),
	}, nil
}
