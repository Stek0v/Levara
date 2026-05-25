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

func TestValidateDetectsDrift(t *testing.T) {
	dir := t.TempDir()
	c := collect("rev-1", "2026-05-24T00:00:00Z")
	// Seed an AGENTS.md with markers so writeAll succeeds even though we don't read it back.
	if err := os.WriteFile(dir+"/AGENTS.md",
		[]byte("<!-- BEGIN: contract-mcp -->\n<!-- END: contract-mcp -->\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeAll(c, dir, dir); err != nil {
		t.Fatal(err)
	}

	if err := validate(c, dir, dir); err != nil {
		t.Fatalf("validate clean: %v", err)
	}

	// Mutate disk: drift introduced.
	if err := os.WriteFile(dir+"/contract.json", []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validate(c, dir, dir); err == nil {
		t.Fatal("validate did not detect drift")
	}
}

func TestRewriteAgentsMD(t *testing.T) {
	dir := t.TempDir()
	src := "# Title\n\n## MCP Tools\n\n<!-- BEGIN: contract-mcp -->\nstale\n<!-- END: contract-mcp -->\n"
	path := dir + "/AGENTS.md"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	c := collect("rev-1", "2026-05-24T00:00:00Z")
	if err := rewriteAgentsMD(c, dir); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	s := string(got)
	if !strings.Contains(s, "# Title") {
		t.Fatal("clobbered preamble")
	}
	if strings.Contains(s, "stale") {
		t.Fatal("did not replace stale content")
	}
	if !strings.Contains(s, "| search |") {
		t.Fatal("did not insert MCP table")
	}
}
