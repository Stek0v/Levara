package main

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
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

func TestRenderJSONByteIdentical(t *testing.T) {
	dir := t.TempDir()
	c := collect("rev-1", "2026-05-24T00:00:00Z")
	if err := writeJSON(c, dir); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(c, dir); err != nil {
		t.Fatal(err)
	}
	b1, err := os.ReadFile(dir + "/contract.json")
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b1) {
		t.Fatal("invalid JSON")
	}
	b2, _ := os.ReadFile(dir + "/contract.json")
	if string(b1) != string(b2) {
		t.Fatal("two writes differ")
	}
}

func TestRenderMarkdownByteIdentical(t *testing.T) {
	dir := t.TempDir()
	c := collect("rev-1", "2026-05-24T00:00:00Z")
	if err := writeMarkdown(c, dir); err != nil {
		t.Fatal(err)
	}
	b1, err := os.ReadFile(dir + "/api-contract.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMarkdown(c, dir); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(dir + "/api-contract.md")
	if string(b1) != string(b2) {
		t.Fatal("two writes differ")
	}
	if !strings.Contains(string(b1), "## REST") || !strings.Contains(string(b1), "## gRPC") {
		t.Fatal("missing sections")
	}
}
