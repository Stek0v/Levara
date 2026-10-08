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
	"encoding/json"
	"errors"
	"sort"

	"github.com/stek0v/levara/internal/store"
	pbv1 "github.com/stek0v/levara/proto/pb"
	pbv2 "github.com/stek0v/levara/proto/pb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var errV2InvalidArgument = errors.New("invalid argument")

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

// Insert — canonical v2 write. Delegates to the shared v1 implementation and
// translates its typed failure into ErrorDetail.
func (s *ServiceV2) Insert(ctx context.Context, req *pbv2.InsertReq) (*pbv2.InsertResp, error) {
	if req.GetCollection() == "" || req.GetId() == "" || len(req.GetVector()) == 0 {
		return &pbv2.InsertResp{Error: errorDetailWithCode(pbv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "collection, id, and vector are required")}, nil
	}
	if len(req.GetMetadataJson()) > 0 {
		var metadata map[string]any
		if err := json.Unmarshal(req.GetMetadataJson(), &metadata); err != nil {
			return &pbv2.InsertResp{Error: errorDetailWithCode(pbv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "invalid metadata JSON")}, nil
		}
	}
	v1Resp, cause := s.v1.insert(&pbv1.InsertReq{
		Collection:   req.GetCollection(),
		Id:           req.GetId(),
		Vector:       req.GetVector(),
		MetadataJson: string(req.GetMetadataJson()),
	})
	if cause != nil {
		return &pbv2.InsertResp{Error: errorDetailFromError(cause)}, nil
	}
	return insertRespFromV1(v1Resp), nil
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
	if req.GetCollection() == "" || len(req.GetItems()) == 0 {
		return &pbv2.BatchInsertResp{Failed: int32(len(req.GetItems())), Error: errorDetailWithCode(pbv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "collection and items are required")}, nil
	}
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
	v1Req := &pbv1.BatchInsertReq{
		Collection: req.GetCollection(),
		Records:    records,
	}
	v1Resp, errs := s.v1.batchInsert(v1Req)
	resp := &pbv2.BatchInsertResp{Inserted: v1Resp.GetInserted(), Failed: v1Resp.GetFailed()}
	if len(errs) == 0 && len(v1Resp.GetErrors()) > 0 {
		resp.Error = errorDetail(v1Resp.GetErrors()[0])
		return resp, nil
	}
	for _, failure := range orderedBatchErrors(errs) {
		resp.Failures = append(resp.Failures, &pbv2.BatchInsertFailure{Id: failure.ID, Index: int32(failure.Index), Error: errorDetailFromError(failure.Err)})
	}
	return resp, nil
}

func (s *ServiceV2) Delete(ctx context.Context, req *pbv2.DeleteReq) (*pbv2.DeleteResp, error) {
	if req.GetCollection() == "" || len(req.GetIds()) == 0 {
		return &pbv2.DeleteResp{Failed: int32(len(req.GetIds())), Error: errorDetailWithCode(pbv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "collection and ids are required")}, nil
	}
	v1Req := &pbv1.DeleteReq{
		Collection: req.GetCollection(),
		Ids:        req.GetIds(),
	}
	v1Resp, errs := s.v1.batchDelete(v1Req)
	resp := &pbv2.DeleteResp{Deleted: v1Resp.GetDeleted(), Failed: v1Resp.GetFailed()}
	if len(errs) == 0 && len(v1Resp.GetErrors()) > 0 {
		resp.Error = errorDetail(v1Resp.GetErrors()[0])
		return resp, nil
	}
	for _, failure := range orderedBatchErrors(errs) {
		resp.Failures = append(resp.Failures, &pbv2.DeleteFailure{Id: failure.ID, Index: int32(failure.Index), Error: errorDetailFromError(failure.Err)})
	}
	return resp, nil
}

func (s *ServiceV2) Search(ctx context.Context, req *pbv2.SearchReq) (*pbv2.SearchResp, error) {
	v1Resp, err := s.v1.Search(ctx, &pbv1.SearchReq{
		Collection: req.GetCollection(),
		Vector:     req.GetVector(),
		TopK:       req.GetTopK(),
	})
	if err != nil {
		return nil, err
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
func insertRespFromV1(v1 *pbv1.StatusResp) *pbv2.InsertResp {
	if v1.GetError() != "" {
		return &pbv2.InsertResp{Ok: false, Error: errorDetail(v1.GetError())}
	}
	return &pbv2.InsertResp{Ok: v1.GetOk()}
}

// errFromV1 wraps a Go error into ErrorDetail. gRPC status codes live
// outside this envelope; callers see both (ErrorDetail for parseable
// categorisation, status.Status for transport-level handling).
func errFromV1(err error) *pbv2.ErrorDetail {
	if err == nil {
		return nil
	}
	return errorDetailFromError(err)
}

func errorDetail(message string) *pbv2.ErrorDetail {
	return errorDetailWithCode(pbv2.ErrorCode_ERROR_CODE_LEGACY, message)
}

func errorDetailWithCode(code pbv2.ErrorCode, message string) *pbv2.ErrorDetail {
	return &pbv2.ErrorDetail{Code: code, Message: message}
}

func errorDetailFromError(err error) *pbv2.ErrorDetail {
	if err == nil {
		return nil
	}
	code := pbv2.ErrorCode_ERROR_CODE_LEGACY
	switch {
	case errors.Is(err, errV2InvalidArgument), errors.Is(err, store.ErrDimMismatch):
		code = pbv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
	case errors.Is(err, store.ErrCollectionNotFound), errors.Is(err, store.ErrRecordNotFound):
		code = pbv2.ErrorCode_ERROR_CODE_NOT_FOUND
	case errors.Is(err, store.ErrEmbeddingContractMismatch):
		code = pbv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
	default:
		if grpcStatus, ok := status.FromError(err); ok {
			switch grpcStatus.Code() {
			case codes.InvalidArgument:
				code = pbv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
			case codes.NotFound:
				code = pbv2.ErrorCode_ERROR_CODE_NOT_FOUND
			case codes.FailedPrecondition:
				code = pbv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
			case codes.Unavailable:
				code = pbv2.ErrorCode_ERROR_CODE_UNAVAILABLE
			case codes.Internal:
				code = pbv2.ErrorCode_ERROR_CODE_INTERNAL
			}
		}
	}
	return errorDetailWithCode(code, err.Error())
}

func orderedBatchErrors(errs []error) []*store.BatchError {
	failures := make([]*store.BatchError, 0, len(errs))
	for _, err := range errs {
		var failure *store.BatchError
		if errors.As(err, &failure) {
			failures = append(failures, failure)
		}
	}
	sort.SliceStable(failures, func(i, j int) bool { return failures[i].Index < failures[j].Index })
	return failures
}
