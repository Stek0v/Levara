// service_v2.go — thin v2 façade over the v1 gRPC service (T10).
//
// The v2 wire contract (proto/levara_v2.proto) introduces a typed
// ErrorDetail and keeps Add/Save/Create as deprecated aliases for Insert
// so existing clients don't have to rename their generated calls on day
// one. Rather than reimplement every handler, ServiceV2 composes the v1
// Service and rewrites the inputs/outputs at the boundary. This keeps
// exactly one implementation of the actual storage logic.
package grpc

import (
	"context"

	pbv1 "github.com/stek0v/levara/proto/pb"
	pbv2 "github.com/stek0v/levara/proto/pb/v2"
)

// ServiceV2 implements pbv2.LevaraServiceV2Server by delegating to an
// embedded v1 Service. The v1 Service stays the source of truth for
// storage + auth; v2 only changes the wire shape.
type ServiceV2 struct {
	pbv2.UnimplementedLevaraServiceV2Server
	v1 *Service
}

// NewServiceV2 wraps an existing v1 Service. Callers register the
// returned value via pbv2.RegisterLevaraServiceV2Server on the same
// grpc.Server used for v1 — gRPC dispatches by fully qualified method
// name so both contracts coexist.
func NewServiceV2(v1 *Service) *ServiceV2 {
	return &ServiceV2{v1: v1}
}

// Insert — canonical v2 write. Delegates to the v1 handler and
// translates the plain error string into ErrorDetail.
func (s *ServiceV2) Insert(ctx context.Context, req *pbv2.InsertReq) (*pbv2.InsertResp, error) {
	v1Resp, err := s.v1.Insert(ctx, &pbv1.InsertReq{
		Collection:   req.GetCollection(),
		Id:           req.GetId(),
		Vector:       req.GetVector(),
		MetadataJson: string(req.GetMetadataJson()),
	})
	return insertRespFromV1(v1Resp, err), nil
}

// Add / Save / Create — deprecated aliases for Insert. They exist so
// generated v1 clients that named their call sites around "add" /
// "create" can migrate to v2 without renaming every caller in the same
// PR. Remove in v3 (expected 6 months after v2 GA).
func (s *ServiceV2) Add(ctx context.Context, req *pbv2.InsertReq) (*pbv2.InsertResp, error) {
	return s.Insert(ctx, req)
}
func (s *ServiceV2) Save(ctx context.Context, req *pbv2.InsertReq) (*pbv2.InsertResp, error) {
	return s.Insert(ctx, req)
}
func (s *ServiceV2) Create(ctx context.Context, req *pbv2.InsertReq) (*pbv2.InsertResp, error) {
	return s.Insert(ctx, req)
}

// BatchInsert translates the v1 string-error surface into per-item
// ErrorDetail entries so clients can programmatically recover from
// partial failures.
func (s *ServiceV2) BatchInsert(ctx context.Context, req *pbv2.BatchInsertReq) (*pbv2.BatchInsertResp, error) {
	// v1 calls the items "records" in its BatchInsertReq — the v2 proto
	// renames to "items" for clarity (an Insert *is* an item, not a
	// record). Translate at the boundary.
	records := make([]*pbv1.InsertRecord, 0, len(req.GetItems()))
	for _, it := range req.GetItems() {
		records = append(records, &pbv1.InsertRecord{
			Id:           it.GetId(),
			Vector:       it.GetVector(),
			MetadataJson: string(it.GetMetadataJson()),
		})
	}
	v1Resp, err := s.v1.BatchInsert(ctx, &pbv1.BatchInsertReq{
		Collection: req.GetCollection(),
		Records:    records,
	})
	if err != nil {
		return &pbv2.BatchInsertResp{Error: errFromV1(err)}, nil
	}
	return &pbv2.BatchInsertResp{
		Inserted: v1Resp.GetInserted(),
		Failed:   v1Resp.GetFailed(),
		// v1 batch response doesn't carry per-item errors in this build;
		// failures array is best-effort empty on success path. A future
		// v1 upgrade could populate it and the v2 mapping follows.
	}, nil
}

func (s *ServiceV2) Delete(ctx context.Context, req *pbv2.DeleteReq) (*pbv2.DeleteResp, error) {
	v1Resp, err := s.v1.Delete(ctx, &pbv1.DeleteReq{
		Collection: req.GetCollection(),
		Ids:        req.GetIds(),
	})
	if err != nil {
		return &pbv2.DeleteResp{Error: errFromV1(err)}, nil
	}
	return &pbv2.DeleteResp{
		Deleted: v1Resp.GetDeleted(),
		Failed:  v1Resp.GetFailed(),
	}, nil
}

func (s *ServiceV2) Search(ctx context.Context, req *pbv2.SearchReq) (*pbv2.SearchResp, error) {
	v1Resp, err := s.v1.Search(ctx, &pbv1.SearchReq{
		Collection: req.GetCollection(),
		Vector:     req.GetVector(),
		TopK:       req.GetTopK(),
	})
	if err != nil {
		return &pbv2.SearchResp{Error: errFromV1(err)}, nil
	}
	results := make([]*pbv2.SearchResult, 0, len(v1Resp.GetResults()))
	for _, r := range v1Resp.GetResults() {
		results = append(results, &pbv2.SearchResult{
			Id:           r.GetId(),
			Score:        r.GetScore(),
			MetadataJson: []byte(r.GetMetadataJson()),
		})
	}
	return &pbv2.SearchResp{Results: results}, nil
}

func (s *ServiceV2) Info(ctx context.Context, _ *pbv2.InfoReq) (*pbv2.InfoResp, error) {
	v1Resp, err := s.v1.Info(ctx, &pbv1.Empty{})
	if err != nil {
		return &pbv2.InfoResp{}, err
	}
	return &pbv2.InfoResp{
		Dimension: v1Resp.GetDimension(),
		Shards:    v1Resp.GetShards(),
		Version:   "v2",
	}, nil
}

// insertRespFromV1 maps v1's (StatusResp, error) shape into v2's
// (InsertResp with typed error) shape.
func insertRespFromV1(v1 *pbv1.StatusResp, err error) *pbv2.InsertResp {
	if err != nil {
		return &pbv2.InsertResp{Ok: false, Error: errFromV1(err)}
	}
	return &pbv2.InsertResp{Ok: v1.GetOk(), Error: nil}
}

// errFromV1 wraps a Go error into ErrorDetail. gRPC status codes live
// outside this envelope; callers see both (ErrorDetail for parseable
// categorisation, status.Status for transport-level handling).
func errFromV1(err error) *pbv2.ErrorDetail {
	if err == nil {
		return nil
	}
	return &pbv2.ErrorDetail{
		Code:    1,
		Message: err.Error(),
	}
}
