package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rupamthxt/cognevra/internal/store"
	"github.com/rupamthxt/cognevra/pkg/chunker"
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
		chunks = chunker.ChunkByParagraphSimple(req.Text, minChars)
	case "sentence":
		chunks = chunker.ChunkBySentence(req.Text, minChars, maxChars)
	default: // "merged" or empty
		chunks = chunker.ChunkByParagraphMerged(req.Text, minChars, maxChars)
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
