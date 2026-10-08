package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/embcontract"
	pbv1 "github.com/stek0v/levara/proto/pb"
	pbv2 "github.com/stek0v/levara/proto/pb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// v2TestService spins up a real v1 Service (in-memory store + cluster)
// and wraps it with ServiceV2. Used by the alias tests below.
func v2TestService(t *testing.T, dim int) (*ServiceV2, func()) {
	t.Helper()
	dir, _ := os.MkdirTemp("", "levara-v2-test-*")
	cm, err := store.NewCollectionManager(dim, dir)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatalf("NewCollectionManager: %v", err)
	}
	dbPath := fmt.Sprintf("%s/shard_0/meta.bin", dir)
	os.MkdirAll(fmt.Sprintf("%s/shard_0", dir), 0755)
	db, _ := store.NewLevara(dim, dbPath)
	cluster := store.NewCluster([]store.ShardHandler{&dummyShard{db: db}})
	v1 := NewService(cm, cluster, dim)
	v2 := NewServiceV2(v1)

	return v2, func() {
		cm.Close()
		db.Close()
		os.RemoveAll(dir)
	}
}

func TestServiceV2_ErrorTaxonomy(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want pbv2.ErrorCode
	}{
		{"legacy", errors.New("legacy"), pbv2.ErrorCode_ERROR_CODE_LEGACY},
		{"invalid", status.Error(codes.InvalidArgument, "invalid"), pbv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT},
		{"not-found", status.Error(codes.NotFound, "missing"), pbv2.ErrorCode_ERROR_CODE_NOT_FOUND},
		{"precondition", status.Error(codes.FailedPrecondition, "changed"), pbv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION},
		{"unavailable", status.Error(codes.Unavailable, "down"), pbv2.ErrorCode_ERROR_CODE_UNAVAILABLE},
		{"internal", status.Error(codes.Internal, "broken"), pbv2.ErrorCode_ERROR_CODE_INTERNAL},
		{"dimension", store.ErrDimMismatch, pbv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT},
		{"collection-not-found", store.ErrCollectionNotFound, pbv2.ErrorCode_ERROR_CODE_NOT_FOUND},
		{"record-not-found", store.ErrRecordNotFound, pbv2.ErrorCode_ERROR_CODE_NOT_FOUND},
		{"embedding-contract", store.ErrEmbeddingContractMismatch, pbv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := errorDetailFromError(tc.err).GetCode(); got != tc.want {
				t.Fatalf("code=%v want=%v", got, tc.want)
			}
		})
	}

	// The old int32 code=1 has the same varint wire encoding and now decodes as LEGACY.
	var legacy pbv2.ErrorDetail
	if err := proto.Unmarshal([]byte{0x08, 0x01, 0x12, 0x03, 'o', 'l', 'd'}, &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.GetCode() != pbv2.ErrorCode_ERROR_CODE_LEGACY || legacy.GetMessage() != "old" {
		t.Fatalf("legacy wire=%+v", &legacy)
	}
}

func TestServiceV2_InsertReportsTypedStorageErrors(t *testing.T) {
	svc, cleanup := v2TestService(t, 2)
	defer cleanup()
	ctx := context.Background()

	if resp, err := svc.Insert(ctx, &pbv2.InsertReq{Collection: "c", Id: "seed", Vector: []float32{1, 0}}); err != nil || !resp.GetOk() {
		t.Fatalf("seed insert: err=%v resp=%+v", err, resp)
	}
	for name, call := range map[string]func(context.Context, *pbv2.InsertReq) (*pbv2.InsertResp, error){
		"insert": svc.Insert, "add": svc.Add, "save": svc.Save, "create": svc.Create,
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := call(ctx, &pbv2.InsertReq{Collection: "c", Id: name, Vector: []float32{1}})
			if err != nil || resp.GetError().GetCode() != pbv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
				t.Fatalf("err=%v resp=%+v", err, resp)
			}
		})
	}

	v1 := embcontract.Contract{Encoder: "v1", Tokenizer: "tok", Pooling: "mean", Normalization: "l2", Dim: 2, Metric: "cosine"}
	v2 := embcontract.Contract{Encoder: "v2", Tokenizer: "tok", Pooling: "mean", Normalization: "l2", Dim: 2, Metric: "cosine"}
	svc.v1.collections.SetDefaultEmbeddingContract(v1)
	if err := svc.v1.collections.CreateWithDim("contract", 2, "v1", "cosine"); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(embcontract.StampMetadata(nil, v2))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := svc.Insert(ctx, &pbv2.InsertReq{Collection: "contract", Id: "wrong", Vector: []float32{1, 0}, MetadataJson: metadata})
	if err != nil || resp.GetError().GetCode() != pbv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION {
		t.Fatalf("contract mismatch: err=%v resp=%+v", err, resp)
	}
}

func TestServiceV2_DeleteReportsNotFound(t *testing.T) {
	svc, cleanup := v2TestService(t, 2)
	defer cleanup()
	ctx := context.Background()

	missingCollection, err := svc.Delete(ctx, &pbv2.DeleteReq{Collection: "missing", Ids: []string{"id"}})
	if err != nil || missingCollection.GetFailures()[0].GetError().GetCode() != pbv2.ErrorCode_ERROR_CODE_NOT_FOUND {
		t.Fatalf("missing collection: err=%v resp=%+v", err, missingCollection)
	}
	if resp, err := svc.Insert(ctx, &pbv2.InsertReq{Collection: "c", Id: "seed", Vector: []float32{1, 0}}); err != nil || !resp.GetOk() {
		t.Fatalf("seed insert: err=%v resp=%+v", err, resp)
	}
	missingRecord, err := svc.Delete(ctx, &pbv2.DeleteReq{Collection: "c", Ids: []string{"missing"}})
	if err != nil || missingRecord.GetFailures()[0].GetError().GetCode() != pbv2.ErrorCode_ERROR_CODE_NOT_FOUND {
		t.Fatalf("missing record: err=%v resp=%+v", err, missingRecord)
	}
}

func TestServiceV1_ErrorTextRemainsCompatible(t *testing.T) {
	svc, cleanup := v2TestService(t, 2)
	defer cleanup()
	ctx := context.Background()
	if resp, err := svc.v1.Insert(ctx, &pbv1.InsertReq{Collection: "c", Id: "seed", Vector: []float32{1, 0}}); err != nil || !resp.GetOk() {
		t.Fatalf("seed insert: err=%v resp=%+v", err, resp)
	}

	inserted, err := svc.v1.Insert(ctx, &pbv1.InsertReq{Collection: "c", Id: "bad", Vector: []float32{1}})
	if err != nil || inserted.GetError() != `dimension mismatch: vector dim=1, collection "c" expects dim=2 (model=)` {
		t.Fatalf("insert error changed: err=%v resp=%+v", err, inserted)
	}
	deleted, err := svc.v1.Delete(ctx, &pbv1.DeleteReq{Collection: "missing", Ids: []string{"id"}})
	if err != nil || len(deleted.GetErrors()) != 1 || deleted.GetErrors()[0] != `collection "missing" not found` {
		t.Fatalf("missing collection error changed: err=%v resp=%+v", err, deleted)
	}
	deleted, err = svc.v1.Delete(ctx, &pbv1.DeleteReq{Collection: "c", Ids: []string{"missing"}})
	if err != nil || len(deleted.GetErrors()) != 1 || deleted.GetErrors()[0] != `record "missing" not found` {
		t.Fatalf("missing record error changed: err=%v resp=%+v", err, deleted)
	}
}

func TestServiceV2_BatchInsertRejectsMalformedMetadata(t *testing.T) {
	svc, cleanup := v2TestService(t, 2)
	defer cleanup()
	resp, err := svc.BatchInsert(context.Background(), &pbv2.BatchInsertReq{Collection: "c", Items: []*pbv2.InsertItem{
		{Id: "good", Vector: []float32{1, 0}, MetadataJson: []byte(`{"text":"ok"}`)},
		{Id: "bad", Vector: []float32{0, 1}, MetadataJson: []byte(`{`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetInserted() != 1 || resp.GetFailed() != 1 || len(resp.GetFailures()) != 1 {
		t.Fatalf("response=%+v", resp)
	}
	failure := resp.GetFailures()[0]
	if failure.GetId() != "bad" || failure.GetIndex() != 1 || failure.GetError().GetCode() != pbv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
		t.Fatalf("failure=%+v", failure)
	}
	if svc.v1.collections.HasRecord("c", "bad") || !svc.v1.collections.HasRecord("c", "good") {
		t.Fatal("malformed metadata item reached storage or valid sibling was lost")
	}
}

func TestServiceV2_BatchInsertCombinesValidationAndStorageFailures(t *testing.T) {
	svc, cleanup := v2TestService(t, 2)
	defer cleanup()
	resp, err := svc.BatchInsert(context.Background(), &pbv2.BatchInsertReq{Collection: "c", Items: []*pbv2.InsertItem{
		{Id: "json", Vector: []float32{1, 0}, MetadataJson: []byte(`{`)},
		{Id: "dim", Vector: []float32{1}},
		{Id: "good", Vector: []float32{0, 1}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetInserted() != 1 || resp.GetFailed() != 2 || len(resp.GetFailures()) != 2 {
		t.Fatalf("response=%+v", resp)
	}
	for i, wantID := range []string{"json", "dim"} {
		failure := resp.GetFailures()[i]
		if failure.GetIndex() != int32(i) || failure.GetId() != wantID || failure.GetError().GetCode() != pbv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
			t.Fatalf("failure[%d]=%+v", i, failure)
		}
	}
	if !svc.v1.collections.HasRecord("c", "good") {
		t.Fatal("valid sibling was not inserted")
	}
}

func TestServiceV2_PreservesErrors(t *testing.T) {
	svc, cleanup := v2TestService(t, 2)
	defer cleanup()
	ctx := context.Background()

	for name, call := range map[string]func(context.Context, *pbv2.InsertReq) (*pbv2.InsertResp, error){
		"insert": svc.Insert, "add": svc.Add, "save": svc.Save, "create": svc.Create,
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := call(ctx, &pbv2.InsertReq{})
			if err != nil || resp.GetOk() || resp.GetError().GetMessage() == "" || resp.GetError().GetCode() != pbv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
				t.Fatalf("err=%v resp=%+v", err, resp)
			}
		})
	}

	if _, err := svc.Search(ctx, &pbv2.SearchReq{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Search status = %v, want InvalidArgument", status.Code(err))
	}
}

func TestServiceV2_BatchFailuresKeepOriginalIdentity(t *testing.T) {
	svc, cleanup := v2TestService(t, 2)
	defer cleanup()
	ctx := context.Background()

	inserted, err := svc.BatchInsert(ctx, &pbv2.BatchInsertReq{Collection: "c", Items: []*pbv2.InsertItem{
		{Id: "a", Vector: []float32{1, 0}},
		{Id: "bad", Vector: []float32{1}},
		{Id: "c", Vector: []float32{0, 1}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if inserted.GetInserted() != 2 || inserted.GetFailed() != 1 || len(inserted.GetFailures()) != 1 {
		t.Fatalf("unexpected batch result: %+v", inserted)
	}
	failure := inserted.GetFailures()[0]
	if failure.GetIndex() != 1 || failure.GetId() != "bad" || failure.GetError().GetMessage() == "" || failure.GetError().GetCode() != pbv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
		t.Fatalf("failure lost identity: %+v", failure)
	}

	deleted, err := svc.Delete(ctx, &pbv2.DeleteReq{Collection: "c", Ids: []string{"a", "missing", "c"}})
	if err != nil {
		t.Fatal(err)
	}
	if deleted.GetDeleted() != 2 || deleted.GetFailed() != 1 || len(deleted.GetFailures()) != 1 {
		t.Fatalf("unexpected delete result: %+v", deleted)
	}
	deleteFailure := deleted.GetFailures()[0]
	if deleteFailure.GetIndex() != 1 || deleteFailure.GetId() != "missing" || deleteFailure.GetError().GetMessage() == "" {
		t.Fatalf("delete failure lost identity: %+v", deleteFailure)
	}
}

// T10: the three deprecated aliases (Add/Save/Create) must round-trip
// identically to Insert. This test locks in the aliasing contract so a
// future maintainer can't accidentally break one-but-not-the-others.
func TestServiceV2_AliasesDelegateToInsert(t *testing.T) {
	svc, cleanup := v2TestService(t, 2)
	defer cleanup()

	ctx := context.Background()
	_, err := svc.Insert(ctx, &pbv2.InsertReq{Collection: "c", Id: "i1", Vector: []float32{1, 0}})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	addResp, err := svc.Add(ctx, &pbv2.InsertReq{Collection: "c", Id: "i2", Vector: []float32{0, 1}})
	if err != nil || !addResp.GetOk() {
		t.Errorf("Add alias failed: err=%v resp=%+v", err, addResp)
	}
	saveResp, err := svc.Save(ctx, &pbv2.InsertReq{Collection: "c", Id: "i3", Vector: []float32{1, 1}})
	if err != nil || !saveResp.GetOk() {
		t.Errorf("Save alias failed: err=%v resp=%+v", err, saveResp)
	}
	createResp, err := svc.Create(ctx, &pbv2.InsertReq{Collection: "c", Id: "i4", Vector: []float32{0.5, 0.5}})
	if err != nil || !createResp.GetOk() {
		t.Errorf("Create alias failed: err=%v resp=%+v", err, createResp)
	}
}

// Info is whitelisted from auth — verify the v2 wrapper still works.
func TestServiceV2_Info(t *testing.T) {
	svc, cleanup := v2TestService(t, 4)
	defer cleanup()

	resp, err := svc.Info(context.Background(), &pbv2.InfoReq{})
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if resp.GetDimension() != 4 {
		t.Errorf("Dimension = %d, want 4", resp.GetDimension())
	}
	if resp.GetVersion() != "v2" {
		t.Errorf("Version = %q, want v2", resp.GetVersion())
	}
}
