package grpc

import (
	"sort"
	"testing"

	"github.com/stek0v/levara/internal/contract"
)

func TestGRPCInventoryCoversV1Critical(t *testing.T) {
	inv := GRPCInventory()
	if !sort.SliceIsSorted(inv, func(i, j int) bool {
		if inv[i].Service != inv[j].Service {
			return inv[i].Service < inv[j].Service
		}
		return inv[i].Method < inv[j].Method
	}) {
		t.Fatal("inventory not sorted")
	}
	got := map[string]contract.GRPCMethod{}
	for _, m := range inv {
		got[m.Service+"/"+m.Method] = m
	}
	for _, key := range []string{
		"levara.v1.LevaraService/Search",
		"levara.v1.LevaraService/BatchInsert",
		"levara.v1.LevaraService/PipelineCognify",
	} {
		if _, ok := got[key]; !ok {
			t.Fatalf("missing %s", key)
		}
	}
	t.Logf("total methods inventoried: %d", len(inv))
}
