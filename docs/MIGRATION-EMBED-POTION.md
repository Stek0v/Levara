# Migration runbook — embed model nomic-v2-moe → potion-code-16M

**Target hosts:** prod Levara :8090 (71 collections), Pi 5 :8091 (bench).
**Coexists with:** existing Qwen3 rerank stack on 10.23.0.64 (unchanged).
**Decision basis:** Pi 5 calibration sweep, 2026-05-26.
See [decision_embed_model_potion](../.claude/projects/-Users-stek0v-src-levara/memory/decision_embed_model_potion.md)
and [load-profile-analysis-pi-multimodel.md](load-profile-analysis-pi-multimodel.md).

Migrate the prod Levara embed model from `nomic-embed-text-v2-moe` (768d,
torch+MoE) to `potion-code-16M` (256d, model2vec). Re-embed all 71 prod
collections from chunk text stored in Postgres. **Blue-green per
collection**: old `X` stays live while shadow `X__potion` is built and
verified; cutover is atomic at the routing layer; old data retained 7
days for rollback.

Rollback at every step is explicit. **Nothing below modifies prod data
irreversibly until Phase 4 step 5.**

---

## Why this migration

| | nomic-v2-moe (current) | potion-code-16M (target) |
|---|---|---|
| dim | 768 | 256 |
| params | 475M (MoE) | 16M |
| backend | torch + transformers + einops | model2vec (pure numpy) |
| works on Pi 5 8GB | no (systemd timeout on load) | yes (verified, sidecar :9101) |
| p3 code corpus | n/a (not tested on Pi) | gap p50=0.079, zero_hits=0 |
| p4/p5 NL corpus | n/a (not tested on Pi) | gap p50=0.050, top1 p50=0.30 |
| HNSW memory | baseline | ~3× lower (dim ratio) |

**Granite-97m was tested in the same sweep and returns zero hits on
code corpora — disqualified for any prod role.** Don't substitute it.

---

## Phase 0 — Inventory + sanity (no writes anywhere)

Before touching anything, snapshot prod state. All commands here are
read-only against :8090.

### 0.1 List collections + record counts

```bash
TOKEN=$(cat ~/.levara/prod-token)  # NEVER cat over SSH
curl -s -H "Authorization: Bearer $TOKEN" \
  http://localhost:8090/api/v1/collections | jq '.[] | {name, record_count, embedding_model, embedding_dim}' \
  > inventory-pre-migration.json
```

Verify: 71 entries, all with `embedding_dim: 768` and `embedding_model: nomic-*`.
If any collection already has a non-768 dim, it pre-dates this migration
and must be flagged out of scope.

### 0.2 Smallest-first ordering

```bash
jq -r 'sort_by(.record_count) | .[] | "\(.record_count)\t\(.name)"' inventory-pre-migration.json
```

Migrate in ascending order of `record_count`. The first collection is
the smoke-test; the largest is the last, when the runbook is proven.

### 0.3 Pin a baseline query set per collection

For each collection, save 10 representative queries + their current
top-10 results from `POST /api/v1/search`. This is the **parity
baseline** — Phase 3 will re-run them against the shadow collection.

```bash
mkdir -p baselines/
for coll in $(jq -r '.[].name' inventory-pre-migration.json); do
  ./scripts/reembed/snapshot_baseline.sh "$coll" > "baselines/${coll}.json"
done
```

(Script needs to exist — see [Phase 1 deliverables](#phase-1-deliverables).
It's read-only against prod.)

---

## Phase 1 — Build + verify on bench (no prod touch)

Done locally and on Pi 5 :8091. Nothing here touches :8090.

### 1.1 Deliverables (status)

| | what | status |
|---|---|---|
| 1.1.a | `POST /api/v1/reembed` endpoint (read source → embed → write target) | ✅ exists, `internal/http/reembed.go` |
| 1.1.b | Happy-path integration test (dim change, fake embed server) | ✅ done 2026-05-26, `internal/http/reembed_test.go::TestReembed_HappyPath_DimChange` |
| 1.1.c | Parity-check script — `scripts/reembed/parity_check.py` | ✅ done 2026-05-26 |
| 1.1.d | Baseline snapshot script — `scripts/reembed/snapshot_baseline.py` | ✅ done 2026-05-26 |
| 1.1.e | Atomic rename support in Levara (see §1.3) | ✅ done 2026-05-26 — `POST /collections/:name/rename` (option A), store + handler tests green |
| 1.1.f | Potion embed-server on prod-class host | ⏳ pending host decision |
| 1.1.g | Real-data smoke via pg_dump of one prod collection | ⏳ pending user GO |

### 1.2 Potion embed-server host

Default per user decision 2026-05-26: amd64 prod host, model2vec
backend, OpenAI-compatible `/v1/embeddings` interface (so existing
`pkg/embed.Client` works unchanged). Sidecar service, separate port
from prod Levara. Concrete deployment recipe TBD when host is
provisioned.

Pi 5 sidecar at :9101 stays as it is — useful for bench validation,
not in the prod path.

### 1.3 **BLOCKER**: atomic collection rename

Phase 4 step 5 ("cutover") requires `rename(X__potion → X)` to be
atomic. **Levara today has no rename endpoint.** Available collection
ops:

```
GET    /api/v1/collections
POST   /api/v1/collections
DELETE /api/v1/collections/:name
GET    /api/v1/collections/:name/meta
PUT    /api/v1/collections/:name/meta
POST   /api/v1/reembed
```

Three options to unblock:

| option | what | cost | risk |
|---|---|---|---|
| **A. Add rename endpoint** | `POST /collections/:name/rename` → close Levara struct, `os.Rename` data dir, update metas map, reopen | 4-8h impl + tests; cleanest | rename-during-write races (need write lock) |
| **B. Client-side alias** | Every consumer (mem0, cognee-plugin, MCP) gets new collection name `X__potion`. Old `X` retained read-only. No rename ever. | 0h Levara work, but N clients each need a config flip | fragile — easy to miss a consumer, drift across clients |
| **C. Drop + recreate** | `DELETE X` then `reembed(source=X__potion → X)` — but source = old `X` is already gone. So really: copy `X__potion` records back into a new `X`. Not atomic — brief 404 window. | 1h | reads fail during gap; double the disk during overlap |

**Recommendation: A.** B leaves the prod surface in a permanently
inconsistent state (some collections suffixed, some not) and forces
every future client to learn the convention. C is unsafe for live
reads.

A is a hard dependency for Phase 4 — must land + test before any
prod cutover. Spec it as a separate PR.

### 1.4 Parity threshold

Each shadow collection must clear these thresholds vs the same
queries against the live collection, or the cutover is blocked:

| metric | threshold | rationale |
|---|---|---|
| Jaccard@10 | ≥ 0.6 | At least 6/10 top hits must overlap. Below this the search experience demonstrably changes — needs human review, not auto-migrate. |
| Top-1 stability | ≥ 0.5 | At least half the queries return the same #1 hit. |
| Empty-result rate | ≤ 5% | No more than 5% of queries return zero hits on the shadow (granite's failure mode — guard against unknown future regressions). |
| p50 vector latency | ≤ 1.2× baseline | Potion should actually be **faster**; >1.2× means deploy regression. |

These are starting points. After the first 3-4 collections clear them
cleanly, revisit and tighten if everything is sailing through.

---

## Phase 2 — Pre-flight on prod (one read-only operation)

### 2.1 pg_dump of one collection (requires user GO)

Pick the smallest collection from §0.2. On prod host:

```bash
ssh prod 'pg_dump -Fc -t "<chunks_table>" \
  --where="collection=\"<smallest_coll>\"" \
  -U cognee cognee_db > /tmp/<coll>.dump'
scp prod:/tmp/<coll>.dump bench:/tmp/
ssh bench 'pg_restore --data-only --table=<chunks_table> /tmp/<coll>.dump'
```

This is the **only** prod operation in Phase 2 and is strictly
read-only on prod.

### 2.2 Smoke on real-shaped data

On bench :8091, run the full Phase 4 sequence (sub-phases below) against
the restored collection. Pass = parity thresholds clear. Fail = stop,
debug, do not proceed to Phase 3.

---

## Phase 3 — Stage potion embed-server on prod host

Deploy potion sidecar on the chosen amd64 prod host. Does **not**
touch Levara :8090.

```bash
# On prod-amd64 host:
systemctl start potion-embed.service  # binds :9102 (TBD)
curl localhost:9102/v1/embeddings -d '{"input":["test"], "model":"potion-code-16M"}'
# expect dim=256 vector
```

Gate: sidecar serves /v1/embeddings @ p50 < 50ms for batch=32 on
prod-shaped text.

---

## Phase 4 — Per-collection migration (loop)

For each collection in ascending record_count order, in a separate
window with sign-off:

### 4.1 Pre-flight

```bash
COLL=<name>
SHADOW=${COLL}__potion
ARCHIVE=${COLL}__nomic_archive_$(date +%Y%m%d)

# Baseline snapshot
./scripts/reembed/snapshot_baseline.sh "$COLL" > "baselines/${COLL}.json"

# Confirm record count
PRE_COUNT=$(curl -s -H "Authorization: Bearer $TOKEN" \
  http://localhost:8090/api/v1/collections/${COLL}/meta | jq .record_count)
```

### 4.2 Reembed into shadow

```bash
RUN=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  http://localhost:8090/api/v1/reembed \
  -d "{
    \"source_collection\": \"${COLL}\",
    \"target_collection\": \"${SHADOW}\",
    \"target_model\": \"potion-code-16M\",
    \"target_endpoint\": \"http://prod-amd64:9102/v1/embeddings\",
    \"batch_size\": 64,
    \"delete_source\": false
  }" | jq -r .run_id)

# Poll
while true; do
  STATUS=$(curl -s -H "Authorization: Bearer $TOKEN" \
    http://localhost:8090/api/v1/reembed/${RUN}/status | jq -r .status)
  [ "$STATUS" = "COMPLETED" ] && break
  [ "$STATUS" = "FAILED" ] && { echo "REEMBED FAILED"; exit 1; }
  sleep 5
done
```

### 4.3 Validate shadow

```bash
SHADOW_COUNT=$(curl -s -H "Authorization: Bearer $TOKEN" \
  http://localhost:8090/api/v1/collections/${SHADOW}/meta | jq .record_count)
[ "$SHADOW_COUNT" = "$PRE_COUNT" ] || { echo "COUNT MISMATCH"; exit 1; }

SHADOW_DIM=$(curl -s -H "Authorization: Bearer $TOKEN" \
  http://localhost:8090/api/v1/collections/${SHADOW}/meta | jq .embedding_dim)
[ "$SHADOW_DIM" = "256" ] || { echo "DIM MISMATCH"; exit 1; }
```

### 4.4 Parity check

```bash
./scripts/reembed/parity_check.py \
  --baseline "baselines/${COLL}.json" \
  --shadow "${SHADOW}" \
  --thresholds jaccard10=0.6,top1=0.5,empty=0.05 \
  --target http://localhost:8090
```

Pass = Phase 4.5. Fail = stop, do not cut over, file issue with the
diff report. Do NOT auto-rollback the shadow; keep it for inspection.

### 4.5 Atomic cutover (requires §1.3 option A)

```bash
# Step 1: archive the current live collection
curl -X POST -H "Authorization: Bearer $TOKEN" \
  http://localhost:8090/api/v1/collections/${COLL}/rename \
  -d "{\"new_name\": \"${ARCHIVE}\"}"

# Step 2: promote shadow to live
curl -X POST -H "Authorization: Bearer $TOKEN" \
  http://localhost:8090/api/v1/collections/${SHADOW}/rename \
  -d "{\"new_name\": \"${COLL}\"}"

# Step 3: smoke 3 baseline queries against the new live collection
./scripts/reembed/smoke.sh "${COLL}"
```

The window between step 1 and step 2 is the only moment when reads of
`${COLL}` fail. Rename is local file-system level — sub-second.

### 4.6 Post-cutover

- Mark `${ARCHIVE}` with metadata `retention_until=NOW+7d` via `PUT
  /collections/:name/meta`.
- Update tracking doc with migration timestamp + parity numbers.

### 4.7 Rollback (within 7 days of any single collection)

```bash
# Swap back
curl -X POST -H "Authorization: Bearer $TOKEN" \
  http://localhost:8090/api/v1/collections/${COLL}/rename \
  -d "{\"new_name\": \"${COLL}__potion_failed\"}"

curl -X POST -H "Authorization: Bearer $TOKEN" \
  http://localhost:8090/api/v1/collections/${ARCHIVE}/rename \
  -d "{\"new_name\": \"${COLL}\"}"
```

`${COLL}__potion_failed` retained for forensics.

---

## Phase 5 — System flip + cleanup

After all 71 collections cleared Phase 4:

1. Flip Levara `EMBEDDING_MODEL=potion-code-16M` and
   `EMBEDDING_ENDPOINT=http://prod-amd64:9102/v1/embeddings` env on
   the server's systemd drop-in. Restart prod :8090.
   - New cognify writes will use potion.
   - Old cognify scratch state migration: see §5.1.
2. After 7 days of post-cutover stability, drop all
   `*__nomic_archive_*` collections.
3. Update `CLAUDE.md` Stack table to reflect potion as default.
4. Update `pkg/embed/client.go` default model constant if it's hard-coded
   anywhere (currently is — `text-embedding-3-small`; reembed migration
   doesn't depend on it but new deploy templates should).
5. Decommission nomic-v2-moe from amd64 host. Keep it in
   `scripts/load-profiles/embed_bench/recipes.py` for future testing
   only (e.g. when comparing against next-gen models).

### 5.1 Cognify in-flight runs

Cognify is async — there may be runs in flight when EMBEDDING_MODEL is
flipped. Pre-flip:

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  http://localhost:8090/api/v1/cognify/runs | jq '.[] | select(.status=="RUNNING")'
```

Drain to zero before restart. Failed in-flight runs are recoverable
via re-cognify after the flip.

---

## What's *not* in this runbook

- mem0 + memoryfs client-side changes: they read collection names
  unchanged thanks to §1.3 option A. If we pick B instead, those need
  their own coordinated rollout.
- Postgres schema changes: there are none — the dim change lives
  entirely in Levara's HNSW data dir + metadata, chunks stay 1:1.
- Multi-region: not in scope; prod is single-host today.

---

## Pre-Phase-2 checklist (must all be ✅ before any prod touch)

- [x] §1.3 atomic rename endpoint shipped + tested (2026-05-26)
- [x] §1.1.c parity script written (2026-05-26) — dry-run on bench data pending
- [x] §1.1.d baseline snapshot script written (2026-05-26) — dry-run on bench data pending
- [ ] §1.1.f potion embed-server reachable from prod Levara
- [ ] §1.1.g user GO on pg_dump of one prod collection
- [ ] §1.4 parity thresholds reviewed by user
- [ ] Phase 2 smoke on real-data passes thresholds
