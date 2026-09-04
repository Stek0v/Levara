# Task Authority Manifests — author's guide

Authority manifests let a task declare, up front, what it is allowed to
touch: which MCP tools its executor may call, which workspace paths it may
read or write, and (reserved) which network hosts it may contact. The
runtime denies anything the manifest does not declare — loudly, with an
explicit error, never silently.

This is backlog B4. The feature ships as of 2026-09-04.

## Binding a manifest to a task

A manifest is a YAML file inside the workspace. Bind it at task creation
(or plan time) through the task's `authority_json`:

```json
{
  "manifest": "tasks/my-task/authority.yaml",
  "manifest_sha256": "<hex sha256 of the file>"
}
```

- The path must be workspace-relative. Absolute paths and `..` escapes are
  rejected.
- Compute `manifest_sha256` over the exact file bytes at bind time. On every
  step claim the runtime re-reads the file, re-hashes it, and compares. A
  manifest swapped mid-execution fails the claim with a `digest mismatch`
  error.

## Manifest format

```yaml
# MCP tools the executor may call for this task. Everything else is denied.
allowed_tools:
  - search
  - recall_memory

# Workspace-relative directory roots the task may read or write.
# Paths must not be absolute and must not start with "..".
allowed_paths:
  - tasks/my-task/artifacts

# Reserved: network hosts the task may contact. Deny-by-default.
allowed_networks:
  - api.example.com
```

Size limit: 64 KiB.

## Rules the runtime enforces

1. **Explicit denial.** Undeclared tool, path, or host access returns an
   `authority not declared` error naming the missing grant.
2. **Digest pinning.** The manifest content is pinned at bind time; a
   manifest replaced during execution is refused (`manifest digest
   mismatch`).
3. **Symlink containment.** The manifest file itself, and any path a step
   touches, may not cross a symlink out of the workspace root or out of the
   declared `allowed_paths` roots. This is checked per path component, so a
   symlinked *directory* inside an allowed root is caught too.
4. **No cascade.** Every step of a task uses exactly the task's own
   manifest. Authority handed from one step to another (a token produced by
   step 1, consumed by step 2) grants nothing unless the manifest already
   declares it.
5. **Network is reserved.** `allowed_networks` entries are recorded and
   checked, but network execution is deny-by-default everywhere; treat this
   as forward-looking declarative intent, not a grant.

## Writing good manifests

- Start from the smallest set that lets the task finish; widen only on a
  concrete, observed failure naming the missing grant.
- Scope `allowed_paths` to a task-owned directory
  (`tasks/<task-id>/...`) so tasks cannot read each other's artifacts.
- List only read-only tools (`search`, `recall_memory`, `get_project_context`)
  unless the task must produce artifacts.
- Commit the manifest with the task plan so reviewers see the authority
  surface in the diff.
- Re-bind (update `manifest_sha256`) whenever you intentionally change the
  manifest — an accidental on-disk edit is a security signal, not an
  inconvenience.

## Programmatic surface

Everything above lives in `pkg/mcp/authority.go`:

| Call | Purpose |
|---|---|
| `ParseAuthorityManifest` | parse + sanity-check YAML bytes |
| `LoadAuthorityManifest` | read a manifest from the workspace (symlink-safe) |
| `VerifyTaskManifest` | verify the binding recorded in `authority_json` |
| `VerifyDigest` | digest pinning check |
| `CheckToolAccess` / `CheckPathAccess` / `CheckNetworkAccess` | per-grant checks |
| `ErrAuthorityNotDeclared` | sentinel for explicit denial |

The MCP server verifies the digest on every `task_step` claim
(`internal/http/authority_http.go`); executors (including the autonomous
worker) call the `Check*` methods before touching a tool or path.
