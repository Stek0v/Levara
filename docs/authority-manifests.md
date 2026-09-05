# Task Authority Manifests

A task can bind a workspace YAML manifest by content digest. The current HTTP
claim path verifies that binding before a bound task step is claimed. The
package also provides tool/path/network allowlist helpers, but production
executors do not universally call them. A manifest is therefore not an enforced
sandbox or a replacement for the host's authorization.

Use this alongside [Task Runtime](long-horizon-runtime.md),
[workspace operations](markdown-native-workspace.md) and the
[generated tool schemas](api-contract.md).

## Binding a manifest to a task

Create the manifest under the configured workspace root and hash its exact
bytes. The public `task_open` argument is `authority`, for example:

```json
{
  "authority": {
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
  - search
  - recall_memory
allowed_paths:
  - tasks/example/artifacts
allowed_networks:
  - api.example.com
```

Use narrow task-owned paths and only necessary tools/hosts. These fields express
intent that an executor can check. They do not grant filesystem, credentials,
network, deployment or publication permission by themselves.

## Enforced checks and integration limits

| Surface | Current behavior |
|---|---|
| `ParseAuthorityManifest` | Parses and validates YAML |
| `LoadAuthorityManifest` | Reads the manifest with workspace containment checks |
| `VerifyTaskManifest` / `VerifyDigest` | Checks the stored file binding; wired into HTTP step claim |
| `CheckToolAccess` | Helper rejects an unlisted tool when the executor calls it |
| `CheckPathAccess` | Helper checks allowed roots and symlink containment when called |
| `CheckNetworkAccess` | Helper accepts a listed host and rejects an unlisted one when called |

The allowlist helpers exist in [pkg/mcp/authority.go](../pkg/mcp/authority.go),
but their existence is not evidence that every tool, file or network operation
is intercepted. The claim integration is in
[authority_http.go](../internal/http/authority_http.go). The optional server task
worker currently uses a logging executor; it does not implement a full
manifest-enforced execution environment. Keep host permissions and review gates
in force even when a task has a manifest.

## Verify an executor integration

Before relying on it, test a bound manifest change, an undeclared tool, an
outside-root path, a symlink escape and an unlisted network host through the
actual executor, not just the helper functions. Confirm denied actions produce
no side effect and that an allowed action still works. See [testing](testing.md)
for the distinction between package tests and complete deployment evidence.
