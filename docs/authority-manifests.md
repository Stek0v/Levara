# Task Authority Manifests

A task can bind a workspace YAML manifest by content digest. The HTTP claim
path verifies that binding before a bound step is claimed. The bounded server
workspace executor also checks the digest, tool allowlist, and directory grants
at the actual file action. These checks apply to its supported workspace actions;
the manifest is not a universal sandbox or a replacement for host authorization.

Use this alongside [Task Runtime](long-horizon-runtime.md),
[workspace operations](markdown-native-workspace.md) and the
[generated tool schemas](api-contract.md).

## Binding a manifest to a task

Create the manifest under the configured workspace root and hash its exact
bytes. The public `task_open` argument is `authority`, for example:

```json
{
  "authority": {
    "auto_run": true,
    "allowed_tools": ["workspace_read", "workspace_write"],
    "manifest": "tasks/example/authority.yaml",
    "manifest_sha256": "REPLACE_WITH_SHA256_OF_EXACT_FILE_BYTES"
  }
}
```

This is the authority fragment, not a complete `task_open` request. The other
required task fields are in the [runtime workflow](long-horizon-runtime.md).
`authority_json` is an internal stored field, not the public argument name.

The manifest path must be workspace-relative. The loader rejects absolute
paths, traversal and symlink escape from its workspace root. The size limit is
64 KiB. A content change after binding causes a digest mismatch at step claim;
update the binding intentionally when changing the authorized plan.

## Manifest format

```yaml
allowed_tools:
  - workspace_read
  - workspace_write
allowed_paths:
  - projects/example/main/tasks/artifacts
allowed_networks: []
```

Use narrow task-owned paths and only necessary tools/hosts. For the bounded
workspace executor, `allowed_paths` must name existing canonical directories
under the configured workspace root; individual file grants are rejected.
The target must descend from a granted directory. The task's `allowed_tools`
and manifest tool list must both permit the action. These declarations do not
grant workspace access, credentials, deployment, or publication permission;
an authenticated owner, live lease, and workspace grant are also required.
DevMode is denied. `allowed_networks` does not enable network actions in this executor.

## Enforced checks and integration limits

| Surface | Current behavior |
|---|---|
| `ParseAuthorityManifest` | Parses and validates YAML |
| `LoadAuthorityManifest` | Reads the manifest with workspace containment checks |
| `VerifyTaskManifest` / `VerifyDigest` | Checks the stored file binding; wired into HTTP step claim |
| `CheckToolAccess` | Rejects unlisted tools; used by the bounded workspace executor |
| `CheckPathAccess` | General helper for allowed roots and symlink containment; the bounded executor uses descriptor-based directory confinement |
| `CheckNetworkAccess` | General helper for listed hosts; bounded workspace execution rejects network actions |

The allowlist helpers exist in [pkg/mcp/authority.go](../pkg/mcp/authority.go),
but their existence is not evidence that every tool, file or network operation
is intercepted. The claim integration is in
[authority_http.go](../internal/http/authority_http.go). The optional server task
worker uses [NewTaskExecutor](../internal/http/task_executor.go) for `workspace_read`
and `workspace_write` with `index` absent or false. It requires SQL,
`LEVARA_LONG_HORIZON_RUNTIME=1`, `LEVARA_TASK_WORKER=1`, and explicit
`LEVARA_MCP_TOOLSET=full`: the fence needs both `task_step` and the action tool,
so `long-horizon` alone cannot execute workspace actions. Linux/macOS filesystem
confinement rejects symlink/hard-link escape and unsafe or ambiguous project
paths. Shell, network, indexing, and unknown arguments are unsupported. Keep
host permissions and review gates in force for other actions.

## Verify an executor integration

Before relying on it, test a bound manifest change, an undeclared tool, an
outside-root path, a symlink escape and an unsupported network action through the
actual executor, not just the helper functions. Confirm denied actions produce
no side effect and that an allowed action still works. See [testing](testing.md)
for the distinction between package tests and complete deployment evidence.
