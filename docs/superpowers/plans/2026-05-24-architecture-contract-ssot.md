# Architecture Contract SSOT Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `cmd/contract` — a single Go binary that walks the four canonical inventories (REST, gRPC, MCP, DB schema) and regenerates `docs/api-contract.md`, `docs/contract.json`, the MCP section of `AGENTS.md`, and validates `docs/deployment-matrix.md` references, gated by `make contract` and a CI drift check.

**Architecture:** Shared types live in `internal/contract`. Each surface owns an `Inventory()` function returning canonical typed records (REST already has one; gRPC and MCP get new ones; Schema is migrated to the shared types). `cmd/contract` composes them, renders deterministic artefacts (sorted keys, git-commit timestamp, atomic write-tmp+rename), and a `validate` subcommand fails CI on drift. Existing `architecture_contract_test.go` files become test-only consumers of the same inventories.

**Tech Stack:** Go 1.26, module `github.com/stek0v/levara`, gofiber/fiber v2 (REST), google.golang.org/protobuf (gRPC descriptors), the existing MCP `ToolDescriptors()` registry, Makefile-driven CI.

---

## File Structure

**New:**
- `Levara/internal/contract/types.go` — shared `Status`, `Contract`, `RESTRoute`, `GRPCMethod`, `MCPTool`, `SchemaObject`
- `Levara/internal/contract/types_test.go` — round-trip JSON + sort stability
- `Levara/internal/grpc/inventory.go` — `GRPCInventory() []contract.GRPCMethod` walking `pb.File_levara_proto` + `pb.File_levara_v2_proto`
- `Levara/internal/grpc/inventory_test.go` — descriptor coverage assertions
- `Levara/cmd/contract/main.go` — CLI entrypoint (`generate` / `validate`)
- `Levara/cmd/contract/collect.go` — composes inventories into `contract.Contract`
- `Levara/cmd/contract/render_md.go` — writes `docs/api-contract.md`
- `Levara/cmd/contract/render_json.go` — writes `docs/contract.json`
- `Levara/cmd/contract/agents_md.go` — regenerates the MCP section between markers in `AGENTS.md`
- `Levara/cmd/contract/validate.go` — drift + deployment-matrix link validation
- `Levara/cmd/contract/contract_test.go` — determinism (two runs produce byte-identical output)

**Modified:**
- `Levara/internal/http/routes.go` — switch type aliases to `contract.RESTRoute` / `contract.Status`
- `Levara/internal/http/schema_inventory.go` — return `[]contract.SchemaObject`
- `Levara/internal/http/architecture_contract_test.go` — adjust to shared types
- `Levara/pkg/mcp/tools.go` (or wherever `ToolDescriptor` is defined) — extend with `Group` and `Status` fields
- `Levara/pkg/mcp/architecture_contract_test.go` — add `MCPInventory()` coverage test
- `Levara/Makefile` (create if absent) — `contract`, `contract-check` targets
- `AGENTS.md` (repo root) — insert `<!-- BEGIN: contract-mcp -->` / `<!-- END: contract-mcp -->` markers (one-time bootstrap)

---

## Task 1: Bootstrap `internal/contract` package with shared types

**Files:**
- Create: `Levara/internal/contract/types.go`
- Create: `Levara/internal/contract/types_test.go`

- [ ] **Step 1: Write the failing test**

`Levara/internal/contract/types_test.go`:

```go
package contract

import (
	"encoding/json"
	"sort"
	"testing"
)

func TestStatusValues(t *testing.T) {
	for _, s := range []Status{StatusCanonical, StatusLegacy, StatusAlias, StatusOps, StatusDeprecated} {
		if s == "" {
			t.Fatalf("empty Status constant")
		}
	}
}

func TestContractJSONRoundTrip(t *testing.T) {
	in := Contract{
		GeneratedAt: "2026-05-24T00:00:00Z",
		GitRev:      "deadbeef",
		REST:        []RESTRoute{{Method: "GET", Path: "/health", Status: StatusOps, Group: "ops"}},
		GRPC:        []GRPCMethod{{Service: "levara.v1.LevaraService", Method: "Search", Status: StatusCanonical}},
		MCP:         []MCPTool{{Name: "search", Status: StatusCanonical, Group: "search"}},
		Schema:      []SchemaObject{{Provider: "postgres", Kind: "table", Name: "users"}},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Contract
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.REST[0].Path != "/health" || out.GRPC[0].Method != "Search" {
		t.Fatalf("round-trip lost fields: %+v", out)
	}
}

func TestRESTRouteSortStable(t *testing.T) {
	rs := []RESTRoute{
		{Method: "POST", Path: "/a"},
		{Method: "GET", Path: "/a"},
		{Method: "GET", Path: "/b"},
	}
	sort.Sort(ByRESTRoute(rs))
	if rs[0].Method != "GET" || rs[0].Path != "/a" {
		t.Fatalf("wrong order: %+v", rs)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd Levara && go test ./internal/contract/...`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Write the package**

`Levara/internal/contract/types.go`:

```go
// Package contract holds the shared types produced by every inventory
// (REST, gRPC, MCP, DB schema). cmd/contract composes them into the
// canonical artefacts under docs/.
package contract

type Status string

const (
	StatusCanonical  Status = "canonical"
	StatusLegacy     Status = "legacy_compat"
	StatusAlias      Status = "alias"
	StatusOps        Status = "ops"
	StatusDeprecated Status = "deprecated"
)

type Contract struct {
	GeneratedAt string         `json:"generated_at"`
	GitRev      string         `json:"git_rev"`
	REST        []RESTRoute    `json:"rest"`
	GRPC        []GRPCMethod   `json:"grpc"`
	MCP         []MCPTool      `json:"mcp"`
	Schema      []SchemaObject `json:"schema"`
}

type RESTRoute struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Status Status `json:"status"`
	Group  string `json:"group,omitempty"`
	Note   string `json:"note,omitempty"`
}

type GRPCMethod struct {
	Service string `json:"service"`
	Method  string `json:"method"`
	Status  Status `json:"status"`
	Note    string `json:"note,omitempty"`
}

type MCPTool struct {
	Name   string `json:"name"`
	Group  string `json:"group,omitempty"`
	Status Status `json:"status"`
	Note   string `json:"note,omitempty"`
}

type SchemaObject struct {
	Provider string `json:"provider"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Note     string `json:"note,omitempty"`
}

type ByRESTRoute []RESTRoute

func (s ByRESTRoute) Len() int      { return len(s) }
func (s ByRESTRoute) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s ByRESTRoute) Less(i, j int) bool {
	if s[i].Path != s[j].Path {
		return s[i].Path < s[j].Path
	}
	return s[i].Method < s[j].Method
}

type ByGRPCMethod []GRPCMethod

func (s ByGRPCMethod) Len() int      { return len(s) }
func (s ByGRPCMethod) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s ByGRPCMethod) Less(i, j int) bool {
	if s[i].Service != s[j].Service {
		return s[i].Service < s[j].Service
	}
	return s[i].Method < s[j].Method
}

type ByMCPTool []MCPTool

func (s ByMCPTool) Len() int           { return len(s) }
func (s ByMCPTool) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s ByMCPTool) Less(i, j int) bool { return s[i].Name < s[j].Name }

type BySchemaObject []SchemaObject

func (s BySchemaObject) Len() int      { return len(s) }
func (s BySchemaObject) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s BySchemaObject) Less(i, j int) bool {
	if s[i].Provider != s[j].Provider {
		return s[i].Provider < s[j].Provider
	}
	if s[i].Kind != s[j].Kind {
		return s[i].Kind < s[j].Kind
	}
	return s[i].Name < s[j].Name
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd Levara && go test ./internal/contract/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add Levara/internal/contract/
git commit -m "contract: add shared types package for SSOT inventories"
```

---

## Task 2: Migrate REST inventory to shared types

**Files:**
- Modify: `Levara/internal/http/routes.go`
- Modify: `Levara/internal/http/architecture_contract_test.go`

- [ ] **Step 1: Inspect existing routes.go**

Run: `grep -n "type RouteSpec\|type APIStatus\|^const\|^var\|RESTRouteInventory" Levara/internal/http/routes.go`
Confirm the existing names so the alias edit keeps callers compiling.

- [ ] **Step 2: Replace local types with aliases to `contract`**

In `Levara/internal/http/routes.go`, replace the existing `RouteSpec` struct and `APIStatus` enum with type aliases:

```go
package http

import "github.com/stek0v/levara/internal/contract"

type RouteSpec = contract.RESTRoute
type APIStatus = contract.Status

const (
	APICanonical  = contract.StatusCanonical
	APILegacy     = contract.StatusLegacy
	APIAlias      = contract.StatusAlias
	APIOps        = contract.StatusOps
	APIDeprecated = contract.StatusDeprecated
)
```

Keep `RESTRouteInventory()` unchanged otherwise — only the declared types swap.

- [ ] **Step 3: Build to verify no caller broke**

Run: `cd Levara && go build ./...`
Expected: success.

- [ ] **Step 4: Run existing REST contract tests**

Run: `cd Levara && go test ./internal/http/ -run 'TestRESTRouteInventory'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add Levara/internal/http/routes.go
git commit -m "http: alias RouteSpec/APIStatus to contract package types"
```

---

## Task 3: Migrate Schema inventory to shared types

**Files:**
- Modify: `Levara/internal/http/schema_inventory.go`
- Modify: `Levara/internal/http/architecture_contract_test.go`

- [ ] **Step 1: Inspect existing schema_inventory.go**

Run: `grep -n "type SchemaObject\|type DBProvider\|type SchemaKind\|SchemaInventory" Levara/internal/http/schema_inventory.go`
Note the exact type names and provider/kind constants.

- [ ] **Step 2: Replace local types with aliases**

In `Levara/internal/http/schema_inventory.go`:

```go
import "github.com/stek0v/levara/internal/contract"

type SchemaObject = contract.SchemaObject
type DBProvider = string
type SchemaKind = string

const (
	DBPostgres  DBProvider = "postgres"
	DBSQLite    DBProvider = "sqlite"
	SchemaTable SchemaKind = "table"
	SchemaIndex SchemaKind = "index"
)
```

Adjust any field references in `SchemaInventory()` so it writes to `Provider`/`Kind`/`Name` on `contract.SchemaObject`.

- [ ] **Step 3: Update existing contract test if any field shifted**

Inspect `Levara/internal/http/architecture_contract_test.go` `TestSchemaInventoryCoversCoreTables` — current code uses `obj.Kind == SchemaTable` and `obj.Provider`. Confirm those still resolve through the aliases; no edit needed if names match.

- [ ] **Step 4: Build + test**

Run:
```
cd Levara && go build ./... && go test ./internal/http/ -run 'TestSchema'
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add Levara/internal/http/schema_inventory.go Levara/internal/http/architecture_contract_test.go
git commit -m "http: alias SchemaObject to contract package types"
```

---

## Task 4: Add gRPC inventory walking proto descriptors

**Files:**
- Create: `Levara/internal/grpc/inventory.go`
- Create: `Levara/internal/grpc/inventory_test.go`

- [ ] **Step 1: Confirm v2 descriptor symbol**

Run: `grep -rn "File_levara_v2_proto\|File_levara_proto" Levara/proto/pb`
Expected: both symbols exist. Note the package paths so the import path is correct (likely `github.com/stek0v/levara/proto/pb`; v2 may live in `proto/pbv2`).

- [ ] **Step 2: Write the failing test**

`Levara/internal/grpc/inventory_test.go`:

```go
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
}
```

- [ ] **Step 3: Run test (FAIL)**

Run: `cd Levara && go test ./internal/grpc/ -run 'TestGRPCInventoryCoversV1Critical'`
Expected: FAIL — `GRPCInventory` undefined.

- [ ] **Step 4: Implement `GRPCInventory`**

`Levara/internal/grpc/inventory.go`:

```go
package grpc

import (
	"sort"

	"github.com/stek0v/levara/internal/contract"
	pb "github.com/stek0v/levara/proto/pb"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// GRPCInventory walks every service exposed on :50051 and emits one
// contract.GRPCMethod per RPC. v2 is opt-in via the import below — if
// the v2 symbol is renamed or removed, this file is the single place
// to update.
func GRPCInventory() []contract.GRPCMethod {
	files := []protoreflect.FileDescriptor{
		pb.File_levara_proto,
	}
	if v2 := loadV2File(); v2 != nil {
		files = append(files, v2)
	}

	var out []contract.GRPCMethod
	for _, file := range files {
		services := file.Services()
		for i := 0; i < services.Len(); i++ {
			svc := services.Get(i)
			fullName := string(svc.FullName())
			methods := svc.Methods()
			for j := 0; j < methods.Len(); j++ {
				m := methods.Get(j)
				out = append(out, contract.GRPCMethod{
					Service: fullName,
					Method:  string(m.Name()),
					Status:  classifyGRPC(fullName, string(m.Name())),
				})
			}
		}
	}
	sort.Sort(contract.ByGRPCMethod(out))
	return out
}

func classifyGRPC(service, method string) contract.Status {
	if service == "levara.v2.LevaraServiceV2" {
		return contract.StatusCanonical
	}
	switch method {
	case "Add", "Save", "Create": // v1 aliases for Insert
		return contract.StatusAlias
	}
	return contract.StatusCanonical
}
```

- [ ] **Step 5: Wire v2 descriptor (if it lives in a separate package)**

Add `Levara/internal/grpc/inventory_v2.go`:

```go
package grpc

import (
	"google.golang.org/protobuf/reflect/protoreflect"
	pbv2 "github.com/stek0v/levara/proto/pbv2"
)

func loadV2File() protoreflect.FileDescriptor {
	return pbv2.File_levara_v2_proto
}
```

If v2 lives in the same `pb` package, fold this into `inventory.go` and return `pb.File_levara_v2_proto` directly. If v2 does not exist yet, `loadV2File` returns `nil`.

- [ ] **Step 6: Run test (PASS)**

Run: `cd Levara && go test ./internal/grpc/ -run 'TestGRPCInventory'`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add Levara/internal/grpc/inventory.go Levara/internal/grpc/inventory_v2.go Levara/internal/grpc/inventory_test.go
git commit -m "grpc: add GRPCInventory walking v1+v2 proto descriptors"
```

---

## Task 5: Extend MCP descriptors with Group/Status + add MCPInventory()

**Files:**
- Modify: `Levara/pkg/mcp/tools.go` (file defining `ToolDescriptor`)
- Create: `Levara/pkg/mcp/inventory.go`
- Modify: `Levara/pkg/mcp/architecture_contract_test.go`

- [ ] **Step 1: Locate ToolDescriptor**

Run: `grep -n "type ToolDescriptor\|func ToolDescriptors" Levara/pkg/mcp/*.go`
Confirm the struct file.

- [ ] **Step 2: Add Group/Status fields**

In the file with `type ToolDescriptor struct`, add two optional fields. Default-zero must compile and behave like canonical:

```go
type ToolDescriptor struct {
	Name         string
	InputSchema  any
	OutputSchema any
	Group        string // optional — e.g. "memory", "graph", "search"
	Status       string // empty => "canonical"
	// existing fields...
}
```

- [ ] **Step 3: Write the failing test**

In `Levara/pkg/mcp/architecture_contract_test.go`, append:

```go
func TestMCPInventoryCoversCritical(t *testing.T) {
	got := map[string]bool{}
	for _, m := range MCPInventory() {
		got[m.Name] = true
	}
	for _, name := range []string{"search", "save_memory", "wake_up", "set_context"} {
		if !got[name] {
			t.Fatalf("MCPInventory missing %s", name)
		}
	}
}
```

- [ ] **Step 4: Run test (FAIL)**

Run: `cd Levara && go test ./pkg/mcp/ -run 'TestMCPInventoryCoversCritical'`
Expected: FAIL — `MCPInventory` undefined.

- [ ] **Step 5: Implement MCPInventory**

`Levara/pkg/mcp/inventory.go`:

```go
package mcp

import (
	"sort"

	"github.com/stek0v/levara/internal/contract"
)

func MCPInventory() []contract.MCPTool {
	descs := ToolDescriptors()
	out := make([]contract.MCPTool, 0, len(descs))
	for _, d := range descs {
		status := contract.Status(d.Status)
		if status == "" {
			status = contract.StatusCanonical
		}
		out = append(out, contract.MCPTool{
			Name:   d.Name,
			Group:  d.Group,
			Status: status,
		})
	}
	sort.Sort(contract.ByMCPTool(out))
	return out
}
```

- [ ] **Step 6: Run test (PASS)**

Run: `cd Levara && go test ./pkg/mcp/ -run 'TestMCPInventoryCovers'`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add Levara/pkg/mcp/tools.go Levara/pkg/mcp/inventory.go Levara/pkg/mcp/architecture_contract_test.go
git commit -m "mcp: add MCPInventory() built on ToolDescriptors"
```

---

## Task 6: `cmd/contract` skeleton + deterministic collect

**Files:**
- Create: `Levara/cmd/contract/main.go`
- Create: `Levara/cmd/contract/collect.go`
- Create: `Levara/cmd/contract/contract_test.go`

- [ ] **Step 1: Write the failing test**

`Levara/cmd/contract/contract_test.go`:

```go
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
}
```

- [ ] **Step 2: Run test (FAIL)**

Run: `cd Levara && go test ./cmd/contract/...`
Expected: FAIL — package doesn't exist.

- [ ] **Step 3: Implement collect**

`Levara/cmd/contract/collect.go`:

```go
package main

import (
	"sort"

	"github.com/stek0v/levara/internal/contract"
	grpcsvc "github.com/stek0v/levara/internal/grpc"
	httpsvc "github.com/stek0v/levara/internal/http"
	"github.com/stek0v/levara/pkg/mcp"
)

func collect(gitRev, generatedAt string) contract.Contract {
	rest := append([]contract.RESTRoute(nil), httpsvc.RESTRouteInventory()...)
	sort.Sort(contract.ByRESTRoute(rest))

	schema := append([]contract.SchemaObject(nil), httpsvc.SchemaInventory()...)
	sort.Sort(contract.BySchemaObject(schema))

	grpc := grpcsvc.GRPCInventory()
	tools := mcp.MCPInventory()

	return contract.Contract{
		GeneratedAt: generatedAt,
		GitRev:      gitRev,
		REST:        rest,
		GRPC:        grpc,
		MCP:         tools,
		Schema:      schema,
	}
}
```

- [ ] **Step 4: Implement main shell**

`Levara/cmd/contract/main.go`:

```go
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: contract <generate|validate> [flags]")
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	outDir := fs.String("out", "docs", "output directory")
	repoRoot := fs.String("repo", ".", "repo root (for AGENTS.md, deployment-matrix)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fail(err.Error())
	}

	gitRev := gitCommit()
	generatedAt := gitCommitTime()
	c := collect(gitRev, generatedAt)

	switch cmd {
	case "generate":
		if err := writeAll(c, *outDir, *repoRoot); err != nil {
			fail(err.Error())
		}
	case "validate":
		if err := validate(c, *outDir, *repoRoot); err != nil {
			fail(err.Error())
		}
	default:
		fail("unknown command: " + cmd)
	}
}

func fail(msg string) { fmt.Fprintln(os.Stderr, msg); os.Exit(1) }

func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func gitCommitTime() string {
	out, err := exec.Command("git", "log", "-1", "--format=%cI").Output()
	if err != nil {
		return "1970-01-01T00:00:00Z"
	}
	return strings.TrimSpace(string(out))
}

// writeAll and validate are defined in render_md.go / render_json.go / agents_md.go / validate.go.
func writeAll(c contract.Contract, outDir, repoRoot string) error { return nil }
func validate(c contract.Contract, outDir, repoRoot string) error { return nil }
```

Note: `writeAll` and `validate` are stubs here so the package builds; later tasks fill them in and remove these no-op declarations.

- [ ] **Step 5: Run test (PASS)**

Run: `cd Levara && go test ./cmd/contract/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add Levara/cmd/contract/
git commit -m "cmd/contract: skeleton + deterministic collect"
```

---

## Task 7: Render `docs/contract.json` with atomic write

**Files:**
- Create: `Levara/cmd/contract/render_json.go`
- Modify: `Levara/cmd/contract/contract_test.go`

- [ ] **Step 1: Append failing test**

In `Levara/cmd/contract/contract_test.go`:

```go
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
```

Add imports `encoding/json`, `os` at the top.

- [ ] **Step 2: Run test (FAIL)**

Run: `cd Levara && go test ./cmd/contract/ -run TestRenderJSONByteIdentical`
Expected: FAIL — `writeJSON` undefined.

- [ ] **Step 3: Implement writeJSON**

`Levara/cmd/contract/render_json.go`:

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/stek0v/levara/internal/contract"
)

func writeJSON(c contract.Contract, outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return atomicWrite(filepath.Join(outDir, "contract.json"), b)
}

func atomicWrite(dst string, data []byte) error {
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
```

- [ ] **Step 4: Run test (PASS)**

Run: `cd Levara && go test ./cmd/contract/ -run TestRenderJSONByteIdentical`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add Levara/cmd/contract/render_json.go Levara/cmd/contract/contract_test.go
git commit -m "cmd/contract: render contract.json with atomic write"
```

---

## Task 8: Render `docs/api-contract.md`

**Files:**
- Create: `Levara/cmd/contract/render_md.go`
- Modify: `Levara/cmd/contract/contract_test.go`

- [ ] **Step 1: Append failing test**

```go
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
```

Add `strings` import.

- [ ] **Step 2: Run test (FAIL)**

Run: `cd Levara && go test ./cmd/contract/ -run TestRenderMarkdown`
Expected: FAIL.

- [ ] **Step 3: Implement writeMarkdown**

`Levara/cmd/contract/render_md.go`:

```go
package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/stek0v/levara/internal/contract"
)

func writeMarkdown(c contract.Contract, outDir string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Levara API Contract\n\n")
	fmt.Fprintf(&b, "_Generated by `make contract`. Do not edit._\n\n")
	fmt.Fprintf(&b, "- git_rev: `%s`\n", c.GitRev)
	fmt.Fprintf(&b, "- generated_at: `%s`\n\n", c.GeneratedAt)

	fmt.Fprintf(&b, "## REST\n\n| Method | Path | Status | Group |\n|---|---|---|---|\n")
	for _, r := range c.REST {
		fmt.Fprintf(&b, "| %s | `%s` | %s | %s |\n", r.Method, r.Path, r.Status, r.Group)
	}

	fmt.Fprintf(&b, "\n## gRPC\n\n| Service | Method | Status |\n|---|---|---|\n")
	for _, m := range c.GRPC {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", m.Service, m.Method, m.Status)
	}

	fmt.Fprintf(&b, "\n## MCP\n\n| Tool | Group | Status |\n|---|---|---|\n")
	for _, t := range c.MCP {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", t.Name, t.Group, t.Status)
	}

	fmt.Fprintf(&b, "\n## DB Schema\n\n| Provider | Kind | Name |\n|---|---|---|\n")
	for _, s := range c.Schema {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", s.Provider, s.Kind, s.Name)
	}

	return atomicWrite(filepath.Join(outDir, "api-contract.md"), []byte(b.String()))
}
```

- [ ] **Step 4: Run test (PASS)**

Run: `cd Levara && go test ./cmd/contract/ -run TestRenderMarkdown`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add Levara/cmd/contract/render_md.go Levara/cmd/contract/contract_test.go
git commit -m "cmd/contract: render docs/api-contract.md"
```

---

## Task 9: Regenerate MCP section of AGENTS.md between markers

**Files:**
- Create: `Levara/cmd/contract/agents_md.go`
- Modify: `Levara/cmd/contract/contract_test.go`
- Bootstrap edit: repo-root `AGENTS.md`

- [ ] **Step 1: Bootstrap markers in AGENTS.md (one-time manual edit)**

Open `AGENTS.md`. Find the MCP tools section (or the most appropriate place — likely under a `## MCP Tools` heading) and insert markers:

```markdown
<!-- BEGIN: contract-mcp -->
<!-- END: contract-mcp -->
```

If the section does not exist yet, add at the bottom:

```markdown
## MCP Tools

<!-- BEGIN: contract-mcp -->
<!-- END: contract-mcp -->
```

Commit this bootstrap separately so the generator's first run produces a clean diff:

```bash
git add AGENTS.md
git commit -m "agents: add MCP contract markers for cmd/contract regeneration"
```

- [ ] **Step 2: Append failing test**

```go
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
```

- [ ] **Step 3: Run test (FAIL)**

Run: `cd Levara && go test ./cmd/contract/ -run TestRewriteAgentsMD`
Expected: FAIL.

- [ ] **Step 4: Implement**

`Levara/cmd/contract/agents_md.go`:

```go
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stek0v/levara/internal/contract"
)

const (
	mcpBegin = "<!-- BEGIN: contract-mcp -->"
	mcpEnd   = "<!-- END: contract-mcp -->"
)

func rewriteAgentsMD(c contract.Contract, repoRoot string) error {
	path := filepath.Join(repoRoot, "AGENTS.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	s := string(raw)
	i := strings.Index(s, mcpBegin)
	j := strings.Index(s, mcpEnd)
	if i < 0 || j < 0 || j < i {
		return errors.New("AGENTS.md missing contract-mcp markers")
	}

	var body strings.Builder
	body.WriteString(mcpBegin + "\n\n")
	body.WriteString("| Tool | Group | Status |\n|---|---|---|\n")
	for _, t := range c.MCP {
		fmt.Fprintf(&body, "| %s | %s | %s |\n", t.Name, t.Group, t.Status)
	}
	body.WriteString("\n")

	out := s[:i] + body.String() + s[j:]
	return atomicWrite(path, []byte(out))
}
```

- [ ] **Step 5: Run test (PASS)**

Run: `cd Levara && go test ./cmd/contract/ -run TestRewriteAgentsMD`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add Levara/cmd/contract/agents_md.go Levara/cmd/contract/contract_test.go
git commit -m "cmd/contract: regenerate AGENTS.md MCP section between markers"
```

---

## Task 10: Implement `writeAll` and wire `generate`

**Files:**
- Modify: `Levara/cmd/contract/main.go`

- [ ] **Step 1: Replace stubs in main.go**

Remove the two stub function declarations and let `main.go` only hold orchestration. Add at bottom of `main.go` (after deleting the stubs):

```go
func writeAll(c contract.Contract, outDir, repoRoot string) error {
	if err := writeJSON(c, outDir); err != nil {
		return err
	}
	if err := writeMarkdown(c, outDir); err != nil {
		return err
	}
	return rewriteAgentsMD(c, repoRoot)
}
```

And import `"github.com/stek0v/levara/internal/contract"`.

- [ ] **Step 2: First end-to-end generate**

From repo root:

```bash
cd Levara && go run ./cmd/contract generate -out ../docs -repo ..
```

Expected: creates `docs/api-contract.md`, `docs/contract.json`, and rewrites the marker section in `AGENTS.md`.

- [ ] **Step 3: Re-run to confirm idempotence**

```bash
cd Levara && go run ./cmd/contract generate -out ../docs -repo ..
git status -- docs AGENTS.md
```

Expected: second run produces zero git diff (no changes since last run).

- [ ] **Step 4: Commit generated artefacts + main change**

```bash
git add Levara/cmd/contract/main.go docs/api-contract.md docs/contract.json AGENTS.md
git commit -m "cmd/contract: first generated artefacts (REST/gRPC/MCP/schema)"
```

---

## Task 11: Implement `validate` (drift + deployment-matrix links)

**Files:**
- Create: `Levara/cmd/contract/validate.go`
- Modify: `Levara/cmd/contract/main.go` (remove `validate` stub)

- [ ] **Step 1: Append failing test**

In `contract_test.go`:

```go
func TestValidateDetectsDrift(t *testing.T) {
	dir := t.TempDir()
	c := collect("rev-1", "2026-05-24T00:00:00Z")
	if err := writeAll(c, dir, dir); err != nil {
		// AGENTS.md not present in tmp; skip that arm by ensuring file exists.
		if err := os.WriteFile(dir+"/AGENTS.md",
			[]byte("<!-- BEGIN: contract-mcp -->\n<!-- END: contract-mcp -->\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeAll(c, dir, dir); err != nil {
			t.Fatal(err)
		}
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
```

- [ ] **Step 2: Run test (FAIL)**

Run: `cd Levara && go test ./cmd/contract/ -run TestValidateDetectsDrift`
Expected: FAIL.

- [ ] **Step 3: Implement validate**

`Levara/cmd/contract/validate.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/stek0v/levara/internal/contract"
)

func validate(c contract.Contract, outDir, repoRoot string) error {
	if err := compareFile(c, outDir, "contract.json", renderJSONBytes); err != nil {
		return err
	}
	if err := compareFile(c, outDir, "api-contract.md", renderMarkdownBytes); err != nil {
		return err
	}
	if err := validateDeploymentMatrix(c, repoRoot); err != nil {
		return err
	}
	return nil
}

type rendererFn func(contract.Contract) ([]byte, error)

func compareFile(c contract.Contract, outDir, name string, render rendererFn) error {
	want, err := render(c)
	if err != nil {
		return err
	}
	got, err := os.ReadFile(filepath.Join(outDir, name))
	if err != nil {
		return fmt.Errorf("%s missing — run `make contract`: %w", name, err)
	}
	if !bytes.Equal(want, got) {
		return fmt.Errorf("%s drifted — run `make contract` and commit the result", name)
	}
	return nil
}

func renderJSONBytes(c contract.Contract) ([]byte, error) {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func renderMarkdownBytes(c contract.Contract) ([]byte, error) {
	// duplicated from writeMarkdown — keep formats in lockstep
	var b strings.Builder
	fmt.Fprintf(&b, "# Levara API Contract\n\n")
	fmt.Fprintf(&b, "_Generated by `make contract`. Do not edit._\n\n")
	fmt.Fprintf(&b, "- git_rev: `%s`\n", c.GitRev)
	fmt.Fprintf(&b, "- generated_at: `%s`\n\n", c.GeneratedAt)
	fmt.Fprintf(&b, "## REST\n\n| Method | Path | Status | Group |\n|---|---|---|---|\n")
	for _, r := range c.REST {
		fmt.Fprintf(&b, "| %s | `%s` | %s | %s |\n", r.Method, r.Path, r.Status, r.Group)
	}
	fmt.Fprintf(&b, "\n## gRPC\n\n| Service | Method | Status |\n|---|---|---|\n")
	for _, m := range c.GRPC {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", m.Service, m.Method, m.Status)
	}
	fmt.Fprintf(&b, "\n## MCP\n\n| Tool | Group | Status |\n|---|---|---|\n")
	for _, t := range c.MCP {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", t.Name, t.Group, t.Status)
	}
	fmt.Fprintf(&b, "\n## DB Schema\n\n| Provider | Kind | Name |\n|---|---|---|\n")
	for _, s := range c.Schema {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", s.Provider, s.Kind, s.Name)
	}
	return []byte(b.String()), nil
}

var endpointRe = regexp.MustCompile("`(GET|POST|PUT|DELETE|PATCH|HEAD) (/[^`]+)`")

func validateDeploymentMatrix(c contract.Contract, repoRoot string) error {
	path := filepath.Join(repoRoot, "docs/deployment-matrix.md")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, r := range c.REST {
		known[r.Method+" "+r.Path] = true
	}
	for _, m := range endpointRe.FindAllStringSubmatch(string(raw), -1) {
		key := m[1] + " " + m[2]
		if !known[key] {
			return fmt.Errorf("deployment-matrix references unknown REST endpoint: %s", key)
		}
	}
	return nil
}
```

Refactor `writeMarkdown` to call `renderMarkdownBytes` to keep formats identical:

```go
func writeMarkdown(c contract.Contract, outDir string) error {
	b, err := renderMarkdownBytes(c)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(outDir, "api-contract.md"), b)
}
```

Same for `writeJSON`:

```go
func writeJSON(c contract.Contract, outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	b, err := renderJSONBytes(c)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(outDir, "contract.json"), b)
}
```

Remove the `validate` stub from `main.go`.

- [ ] **Step 4: Run all cmd/contract tests (PASS)**

Run: `cd Levara && go test ./cmd/contract/...`
Expected: PASS.

- [ ] **Step 5: End-to-end validate**

From repo root after Task 10 commit:

```bash
cd Levara && go run ./cmd/contract validate -out ../docs -repo ..
echo $?
```

Expected: exit 0, no stderr.

- [ ] **Step 6: Negative check**

Manually corrupt:

```bash
echo "{}" > docs/contract.json
cd Levara && go run ./cmd/contract validate -out ../docs -repo ..
```

Expected: non-zero exit, stderr says "contract.json drifted — run `make contract` and commit the result".

Restore:

```bash
cd Levara && go run ./cmd/contract generate -out ../docs -repo ..
```

- [ ] **Step 7: Commit**

```bash
git add Levara/cmd/contract/validate.go Levara/cmd/contract/render_md.go Levara/cmd/contract/render_json.go Levara/cmd/contract/main.go Levara/cmd/contract/contract_test.go
git commit -m "cmd/contract: validate drift + deployment-matrix endpoint refs"
```

---

## Task 12: Makefile targets `contract` + `contract-check`

**Files:**
- Modify (or create): `Levara/Makefile`

- [ ] **Step 1: Inspect existing Makefile**

Run: `ls Levara/Makefile && grep -n '^\.PHONY\|^[a-z]\+:' Levara/Makefile 2>/dev/null || echo no-makefile`

- [ ] **Step 2: Add targets**

If the Makefile exists, append:

```make
.PHONY: contract contract-check
contract:
	go run ./cmd/contract generate -out ../docs -repo ..

contract-check:
	go run ./cmd/contract validate -out ../docs -repo ..
```

If it does not exist, create `Levara/Makefile`:

```make
.PHONY: contract contract-check
contract:
	go run ./cmd/contract generate -out ../docs -repo ..

contract-check:
	go run ./cmd/contract validate -out ../docs -repo ..
```

- [ ] **Step 3: Smoke-test**

Run:
```
cd Levara && make contract && make contract-check
```
Expected: both succeed; `git status` shows no changes since artefacts are already current.

- [ ] **Step 4: Commit**

```bash
git add Levara/Makefile
git commit -m "make: add contract + contract-check targets"
```

---

## Task 13: CI gate (drift check on every PR)

**Files:**
- Modify or create: CI config (likely `.github/workflows/ci.yml`)

- [ ] **Step 1: Locate existing CI**

Run: `ls .github/workflows/ 2>/dev/null && grep -rn "go test\|make " .github/workflows/ 2>/dev/null | head -20`
Find the workflow that runs Go tests so the new step lands beside them.

- [ ] **Step 2: Add contract-check step**

In the workflow that already sets up Go (e.g. `ci.yml` → `jobs.test`), add a step **after** `go test`:

```yaml
      - name: Contract drift check
        working-directory: Levara
        run: make contract-check
```

If no workflow exists, create `.github/workflows/contract.yml`:

```yaml
name: contract
on: [pull_request, push]
jobs:
  contract:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.26'
      - name: Contract drift check
        working-directory: Levara
        run: make contract-check
```

- [ ] **Step 3: Validate locally (simulate clean state)**

Run:
```
cd Levara && make contract-check
```
Expected: exit 0.

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/
git commit -m "ci: gate PRs on architecture contract drift"
```

---

## Task 14: Final integration smoke test

- [ ] **Step 1: Full build + test**

Run:
```
cd Levara && go build ./... && go test ./...
```
Expected: PASS.

- [ ] **Step 2: Round-trip generate**

Run:
```
cd Levara && make contract && git status
```
Expected: zero diff.

- [ ] **Step 3: Spot-check artefacts**

Read `docs/api-contract.md` and confirm all four sections render with non-empty tables. Read `docs/contract.json` and confirm `git_rev` matches `git rev-parse HEAD`. Read `AGENTS.md` and confirm marker block contains the MCP tool table.

- [ ] **Step 4: Final commit (only if anything moved)**

```bash
git status
# if clean: nothing to do.
# if there are doc updates because the rendering improved mid-plan, commit them:
git add docs/ AGENTS.md
git commit -m "contract: regenerate artefacts after final integration check"
```

---

## Self-Review Notes

- **Spec coverage:** All four artefacts (md, JSON, AGENTS.md section, deployment-matrix validation) are covered. SSOT, drift gate, deterministic output, atomic write — all present. Hybrid SSOT (Go inventories) implemented in Tasks 2–5. Migration plan order mirrors spec.
- **Type consistency:** `Status`, `Contract`, `RESTRoute`, `GRPCMethod`, `MCPTool`, `SchemaObject` — defined in Task 1, used unchanged in 2/3/4/5/6+. Sort wrappers (`ByRESTRoute`, etc.) introduced once and reused.
- **Open question deferred:** The spec leaves how to embed swaggo summaries open. This plan does not consume swaggo — it only uses the Go inventories. Adding swaggo enrichment is intentionally out of scope and can be a follow-up plan.
- **Risk flagged:** `Levara/proto/pbv2` package may not exist in the current tree. Task 4 step 5 covers both cases — fold into pb or stub `loadV2File` to return nil.
