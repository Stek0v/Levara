package grpc

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stek0v/levara/internal/store"
	pbv2 "github.com/stek0v/levara/proto/pb/v2"
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
