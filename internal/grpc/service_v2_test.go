package grpc

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stek0v/levara/internal/store"
	pbv2 "github.com/stek0v/levara/proto/pb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func TestServiceV2_PreservesErrors(t *testing.T) {
	svc, cleanup := v2TestService(t, 2)
	defer cleanup()
	ctx := context.Background()

	for name, call := range map[string]func(context.Context, *pbv2.InsertReq) (*pbv2.InsertResp, error){
		"insert": svc.Insert, "add": svc.Add, "save": svc.Save, "create": svc.Create,
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := call(ctx, &pbv2.InsertReq{})
			if err != nil || resp.GetOk() || resp.GetError().GetMessage() == "" {
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
	if failure.GetIndex() != 1 || failure.GetId() != "bad" || failure.GetError().GetMessage() == "" {
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
