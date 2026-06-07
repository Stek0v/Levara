# Architecture Contract — SSOT + codegen

**Status:** design
**Date:** 2026-05-24
**Owner:** stek0v
**Brainstorm transcript:** session with Claude on 2026-05-23

## Problem

Levara exposes four public surfaces — REST (~120 endpoints), gRPC v1+v2
(~50 RPCs), MCP tools (~25), and DB schema (~40 tables/indexes). Each one
is currently inventoried by a different mechanism:

- `internal/http/routes.go` — hand-maintained `RESTRouteInventory()` with
  a drift test against the Fiber stack.
- `internal/http/schema_inventory.go` — derived from migration statements.
- `internal/grpc/architecture_contract_test.go` — runtime check against
  the proto descriptor with a critical-method whitelist.
- `pkg/mcp/architecture_contract_test.go` — iterates `ToolDescriptors()`
  with a critical-tool whitelist.

Three different styles of "architecture contract" live in one repository.
None of them produces a doc artifact. `CLAUDE.md` lists "25 MCP tools",
`AGENTS.md` describes the room×hall model with a hand-written tool list,
`docs/codebase-component-analysis.md` enumerates packages, and
`docs/deployment-matrix.md` references endpoints/flags by name — all four
drift independently of code.

## Goal

Promote the existing inventories into a single contract pipeline whose
output is committed and CI-gated. The same Go inventory functions that
power the runtime drift tests also feed a generator that produces:

- `docs/api-contract.md` — unified human-readable contract for all four
  surfaces.
- `docs/contract.json` — machine-readable export for external SDKs,
  deployment adapters, and future review gates.
- `AGENTS.md` MCP-tools section, regenerated in-place between markers.
- Validation of `docs/deployment-matrix.md` references against the
  generated contract.

CI runs `make contract` and fails on any `git diff` in the outputs.

## Non-goals

- Replacing `.proto` files as the wire-format source of truth. Proto
  stays canonical for gRPC; the inventory layer reads its descriptor.
- Generating REST handler code. `RegisterAPI` continues to be written
  by hand; inventory is read-only.
- Replacing swaggo. Swagger generation (`make swag`) is independent and
  unchanged; both consume the same handlers.
- Versioning or persisting historical contracts. Each commit has exactly
  one current contract.

## Constraints

- `cmd/contract` must not run Fiber, open a DB, or call external
  services. Inventory functions are pure data.
- Generator output must be bit-identical across runs at the same commit
  (no wall-clock timestamps).
- Existing architecture-contract tests must keep working as independent
  drift guards — they cannot depend on `cmd/contract`.
- No new runtime dependencies. Standard library only for rendering.

## Approach

A single Go binary `cmd/contract` imports each inventory function,
assembles a `Contract` struct, and writes markdown + JSON + the
AGENTS.md MCP section. Architecture-contract tests in `internal/http`,
`internal/grpc`, and `pkg/mcp` remain in place; they validate that the
inventory functions accurately reflect the runtime registration. The
generator and the tests are independent — either can break without
disabling the other.

Considered alternatives (rejected during brainstorm):

- **Per-surface `go:generate` directives + aggregator.** More moving
  parts, two layers of generation, harder to keep in sync.
- **Test-as-generator (`go test -tags contract`).** Reuses walk logic
  but couples the generator to the test harness; `make contract`
  becomes `go test`, which is unintuitive.

## Architecture

```
                  ┌────────────────────────────────────────────────┐
                  │                cmd/contract/main.go             │
                  │   walks 4 surfaces → renders markdown + JSON   │
                  └────────────────────────────────────────────────┘
                          │                  │                  │
        ┌─────────────────┴──────────┐       │                  │
        ▼                            ▼       ▼                  ▼
  ┌───────────┐   ┌────────────────┐  ┌────────────┐   ┌────────────────┐
  │  REST     │   │  gRPC v1+v2    │  │  MCP       │   │  DB schema     │
  │ inventory │   │  proto         │  │ descriptors│   │  migrations    │
  │ (Go list) │   │  descriptors   │  │  (Go)      │   │  (Go strings)  │
  └───────────┘   └────────────────┘  └────────────┘   └────────────────┘
        │                │                  │                  │
        └────────────────┴──────────────────┴──────────────────┘
                                 │
                                 ▼
        Outputs (committed to git, CI fails on drift):
          • docs/api-contract.md
          • docs/contract.json
          • AGENTS.md (in-place section between markers)
          • [validation] deployment-matrix.md references
```

## Components

### Shared types (`internal/contract/types.go`)

A new package owns the shared shape so `cmd/contract` does not have to
import `internal/http` only for type definitions.

```go
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
    Method, Path, Group string
    Status              Status
}

type GRPCMethod struct {
    Service, Method, Group string
    Status                 Status
    InputType, OutputType  string
    Streaming              bool
}

type MCPTool struct {
    Name, Group  string
    Status       Status
    HasInputSch  bool
    HasOutputSch bool
}

type SchemaObject struct {
    Provider string // "postgres" | "sqlite"
    Kind     string // "table" | "index" | "alter"
    Name     string
}
```

`internal/http.APIStatus`, `internal/http.RouteSpec`, and
`internal/http.SchemaObject` become aliases or are replaced inline (same
module, no external consumers).

### Per-surface inventories

| Surface | Location | Function | Change |
|---|---|---|---|
| REST | `internal/http/routes.go` | `RESTRouteInventory() []contract.RESTRoute` | Rename type; list unchanged. |
| Schema | `internal/http/schema_inventory.go` | `SchemaInventory() []contract.SchemaObject` | `DBProvider` becomes string; parser unchanged. |
| gRPC | `internal/grpc/inventory.go` (new) | `GRPCInventory() []contract.GRPCMethod` | Walks `pb.File_levara_proto` + `pb.File_levara_v2_proto`. v2 deprecated aliases (`Add`/`Save`/`Create`) get `StatusAlias`. Critical-set from existing test maps to `StatusCanonical`. |
| MCP | `pkg/mcp/descriptors.go` | extend `ToolDescriptor`; new `MCPInventory() []contract.MCPTool` | Add `Group`/`Status` fields. Group inferred by name prefix (memory_/search_/workspace_/...). |

### Generator (`cmd/contract`)

```
cmd/contract/
  main.go        — flag parsing, orchestration, atomic writes
  render_md.go   — markdown rendering via text/template
  render_json.go — json.MarshalIndent with stable ordering
  agents_md.go   — splices MCP table between markers in AGENTS.md
  validate.go    — scans deployment-matrix.md for endpoint/flag refs
```

`main.go` sketch:

```go
func main() {
    outDir := flag.String("out", "docs", "output directory")
    flag.Parse()

    c := contract.Contract{
        GeneratedAt: readGitTime(),
        GitRev:      readGitRev(),
        REST:        http.RESTRouteInventory(),
        GRPC:        grpc.GRPCInventory(),
        MCP:         mcp.MCPInventory(),
        Schema:      http.SchemaInventory(),
    }
    sortAll(&c)

    writeAtomic(filepath.Join(*outDir, "api-contract.md"), renderMarkdown(c))
    writeAtomic(filepath.Join(*outDir, "contract.json"), renderJSON(c))
    updateAgentsMD("AGENTS.md", c.MCP)

    if errs := validateDeploymentMatrix("docs/deployment-matrix.md", c); len(errs) > 0 {
        for _, e := range errs { fmt.Fprintln(os.Stderr, "validate:", e) }
        os.Exit(2)
    }
}
```

**Determinism rules:**

- `sortAll` orders by (Group, Method/Path/Name) byte-wise.
- `GeneratedAt` is the HEAD commit timestamp from `git log -1 --format=%cI`,
  not wall clock.
- Atomic writes use write-to-tmp + rename.

### Output: `docs/api-contract.md`

```
# Levara API Contract
(auto-generated by cmd/contract — do not edit; run `make contract`)

## REST  (~120 endpoints)
| Group | Method | Path | Status |
...

## gRPC v1
| Group | Method | Streaming | Status |
...

## gRPC v2
| Group | Method | Streaming | Status |
...

## MCP Tools (~25 tools)
| Group | Name | Status |
...

## DB Schema
### PostgreSQL tables
- principals, users, api_keys, ...
### SQLite tables (parity check)
- principals, users, api_keys, ...
### Parity gaps
(empty when full parity)
```

### Output: `AGENTS.md` MCP section

Generator looks for the markers:

```
<!-- contract:mcp-start -->
... (regenerated table)
<!-- contract:mcp-end -->
```

If markers are absent → exit 1 with instruction to add them once
manually. This prevents the generator from silently overwriting the
entire `AGENTS.md` body.

### Makefile target

```makefile
.PHONY: contract
contract:
	@go run ./cmd/contract -out ../docs

.PHONY: contract-check
contract-check: contract
	@git diff --exit-code docs/api-contract.md docs/contract.json AGENTS.md \
	  || (echo "contract drift — run 'make contract' and commit" && exit 1)
```

CI runs `make contract-check` as a dedicated step (alongside `go test`).

## Data flow

```
1. cmd/contract starts (defaults: -out=docs)
2. readGitRev()    → "c90f98b"
3. readGitTime()   → commit timestamp
4. each inventory function returns its []Spec
5. sortAll(&contract)
6. render markdown → docs/.api-contract.md.tmp
7. render JSON     → docs/.contract.json.tmp
8. updateAgentsMD  → splices between markers → AGENTS.md.tmp
9. validateDeploymentMatrix → check refs
10. atomic rename all tmp files
11. exit 0
```

Architecture tests run independently:

```
go test ./internal/http -run TestRESTRouteInventoryMatchesRegisterAPI
go test ./internal/grpc -run TestGRPCInventoryMatchesProto
go test ./pkg/mcp -run TestMCPInventoryMatchesDescriptors
```

These tests do not depend on `cmd/contract`.

## Error handling

Fail loud and immediately; the generator is a dev/CI tool, not
production code.

| Scenario | Behavior |
|---|---|
| `git` missing from PATH | warning to stderr; GitRev="unknown"; continue |
| Inventory function panics | no recovery; surface the Go stack |
| AGENTS.md markers absent | exit 1 with bootstrap instructions |
| deployment-matrix.md absent | warning; skip validation |
| Orphan reference in deployment-matrix | exit 2 with offender list |
| `git diff --exit-code` shows drift in CI | CI fails with "run make contract" |

Not explicitly handled: races (in-process single-threaded), concurrent
file writes (atomic rename), locale-dependent sorting (byte-wise).

## Testing

**Existing drift guards** stay in place and are extended to compare the
full inventory rather than only a critical-set whitelist:

- `internal/http/architecture_contract_test.go` — RESTRouteInventory ↔ Fiber stack
- `internal/grpc/architecture_contract_test.go` — proto descriptor ↔ GRPCInventory
- `pkg/mcp/architecture_contract_test.go` — ToolDescriptors ↔ MCPInventory

**New tests:**

| File | What it verifies |
|---|---|
| `internal/grpc/inventory_test.go` | `GRPCInventory()` covers both proto files; streaming flag correct; status mapping correct |
| `pkg/mcp/inventory_test.go` | `MCPInventory()` matches `ToolDescriptors()`; every tool has Group + Status |
| `cmd/contract/render_md_test.go` | golden-file test: fixture Contract → expected markdown (bit-identical) |
| `cmd/contract/render_json_test.go` | golden JSON; stable ordering |
| `cmd/contract/agents_md_test.go` | in-place update preserves everything outside markers; idempotent |
| `cmd/contract/validate_test.go` | fixture deployment-matrix with orphan reference → expected error |
| `cmd/contract/main_test.go` | integration: run main against tmp out-dir; 3 files appear and parse |

No coverage percentage target. Every new function gets at least one
happy-path test plus an error-path test where applicable.

## Migration plan

This spec describes the end state. The actual ordering of work belongs
in the implementation plan; key dependencies are:

1. `internal/contract` package with shared types must land before any
   inventory function is migrated.
2. Each surface migration (REST → Schema → gRPC → MCP) can ship as its
   own PR; intermediate states are valid (the others stay on legacy
   types until migrated).
3. `cmd/contract` lands once all four inventories use shared types.
4. `make contract-check` enters CI only after a clean `make contract`
   produces stable output across two consecutive runs at the same
   commit.
5. AGENTS.md marker bootstrap is a one-time manual edit before the
   first generation.

## Open questions

None at design time. Implementation-level details (exact markdown
column widths, JSON field naming) are intentionally left to the
implementation plan.
