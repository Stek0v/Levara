# Upgrade guide — 20.04 release

This release closes the 20-task plan in `docs/reviews/20.04-tasks.md`. Most of
it is internal hardening, but five changes have **deployment impact** —
existing operators must read this file before rolling out.

## Required environment changes

### 1. `JWT_SECRET` must be set in production

Pre-20.04 the server generated a random 32-byte HS256 secret on every
startup when `JWT_SECRET` was empty. That worked for single-process dev
but invalidated all live tokens whenever the process restarted. With
**T19 (gRPC JWT auth)** the same secret now signs both HTTP and gRPC
tokens, and gRPC clients keep their JWT in metadata across calls.

```bash
# Generate once, persist in your secrets manager:
openssl rand -hex 32
# Then export in every replica's environment:
export JWT_SECRET=...
```

If you still leave it empty: HTTP keeps working but every restart logs
out every user **and** kills every active gRPC stream until the client
re-authenticates.

### 2. gRPC clients must send `authorization: Bearer <jwt>` metadata

T19 enforces auth on all gRPC RPCs except the public whitelist
(`levara.v1.LevaraService/Info`). Run with `-require-auth=false`
during the rollout window — that puts the interceptor in **permissive
mode**: missing tokens are still allowed through (logged, not rejected),
which lets you upgrade clients RPC by RPC. Once every client sends a
token, flip to `-require-auth=true`.

```bash
# Migration window
./server -require-auth=false

# Hardened production
./server -require-auth=true
```

### 3. `ENV=dev` controls Swagger UI exposure

T13 mounts `/swagger/*` only when `ENV=dev` (or unset, for backward
compat with current installs). For prod, set `ENV=production` so the
endpoint 404s. Spec generation (`make swag`) is independent of this
flag — it's a build-time step.

### 4. Rate-limit defaults are tighter than zero

T2 added in-memory limiters:

| Surface | Limit | Key |
|---|---|---|
| `/auth/login` + `/auth/register` (combined) | 10 req / minute | source IP |
| All other `/api/v1/*` | 100 req / minute | JWT user_id, fallback IP |
| gRPC | 100 req / minute, burst 20 | peer IP |
| MCP | per-session, depends on tool | `Mcp-Session-Id` header |

Watch `levara_rate_limit_rejected_total{channel, bucket}` after rollout.
If batch ingestion hits the cap, raise it via the equivalent constructor
in `cmd/server/main.go` — there's no env var override (yet).

### 5. New Prometheus metrics

Add scrape labels for the new series; existing Grafana dashboards stay
backward-compatible (we only added, never removed).

| New metric | Cardinality |
|---|---|
| `levara_http_requests_total{operation, status, user_id}` | bounded — top-50 user_ids + `other` + `anon` per refresh window (1m) |
| `levara_http_duration_seconds{operation, user_id}` | bounded same way |
| `levara_user_bucket_size` | gauge: number of promoted user_ids |
| `levara_rate_limit_rejected_total{channel, bucket}` | low (handful of buckets) |
| `levara_cognify_panics_total{stage}` | low (stages enumerated) |

`user_id` series count is hard-capped at ~52 per `operation` thanks to
`internal/metrics.UserBucket`. If you want raw per-user series instead,
patch `cmd/server/main.go` (`metrics.NewUserBucket(50, time.Minute)` →
larger N) and accept the cardinality cost.

## Database / schema migrations

**None.** The handler-level changes (T11 datasets tests, T16 WAL
recovery) didn't touch any table. The only WAL-format-adjacent change
(T16) is read-only — old WAL files replay correctly under the new
single-pass logic, and the new code writes the same entries the old code
did.

## Compatibility matrix

| Component | Before | After |
|---|---|---|
| HTTP API | `/api/v1/...` | unchanged surface, new fields in errors |
| gRPC v1 | `levara.v1.LevaraService` | unchanged, requires JWT |
| gRPC v2 | — | new `levara.v2.LevaraServiceV2` (3-month deprecation window for v1) |
| MCP transport | JSON-RPC 2.0 | unchanged, tools now carry `outputSchema` (additive) |
| WebUI auth | — | new auth-guard + `/login?next=` redirect |
| Settings persistence | localStorage only | backend-resolved, localStorage keeps FOUC role |

## Rollback

Each change is in its own commit between `95a95df` (start of 20.04) and
`c444a22` (end). To roll back a single layer (e.g. revert gRPC auth
because a client wasn't ready):

```bash
git revert <commit-sha>
git push
```

The commits are ordered to be safely reversible in any order — they
don't share state.

## Operator checklist

- [ ] Set `JWT_SECRET` in every replica's environment.
- [ ] Set `ENV=production` (or omit) in prod; Swagger UI must not be public.
- [ ] Update gRPC clients to send `authorization` metadata. Use
      `-require-auth=false` during the migration window.
- [ ] Update Prometheus / Grafana to scrape and chart the new
      `levara_http_*` and `levara_rate_limit_*` series.
- [ ] Run `make swag` in CI so `docs/swagger.{json,yaml}` stays current.
- [ ] Smoke-test cognify with a tiny dataset post-upgrade — the new
      panic-recovery + run-status TTL janitor should leave the registry
      empty after 1h of idle.
