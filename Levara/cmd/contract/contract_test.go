package main

import (
	"sort"
	"testing"
)

func TestCollectIsDeterministic(t *testing.T) {
	a := collect("fixed-rev", "2026-05-24T00:00:00Z")
	b := collect("fixed-rev", "2026-05-24T00:00:00Z")
	if a.GitRev != b.GitRev || a.GeneratedAt != b.GeneratedAt {
		t.Fatal("metadata not stable")
	}
	if !sort.SliceIsSorted(a.REST, func(i, j int) bool {
		if a.REST[i].Path != a.REST[j].Path {
			return a.REST[i].Path < a.REST[j].Path
		}
		return a.REST[i].Method < a.REST[j].Method
	}) {
		t.Fatal("REST not sorted")
	}
	if len(a.REST) == 0 || len(a.GRPC) == 0 || len(a.MCP) == 0 || len(a.Schema) == 0 {
		t.Fatalf("empty surface: rest=%d grpc=%d mcp=%d schema=%d",
			len(a.REST), len(a.GRPC), len(a.MCP), len(a.Schema))
	}
	t.Logf("counts: rest=%d grpc=%d mcp=%d schema=%d",
		len(a.REST), len(a.GRPC), len(a.MCP), len(a.Schema))
}
