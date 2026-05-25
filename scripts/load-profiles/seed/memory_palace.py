"""Synthetic memory-palace corpus for P4.

Models the workload of an LLM-agent memory system: lots of short
records tagged by `hall` (fact / decision / discovery / preference
/ advice / event) and `room` (project subsystem). The dataset is
generated deterministically from a seed so it's reproducible across
hosts and over time — no external network dependency, which matters
because P4 runs on the Pi where downloads are slow.

The distribution shape is what matters for calibration: many
records share vocabulary across rooms (the word "auth" appears in
both auth and deploy rooms, with different meanings), and a small
number of records are near-duplicates with subtle distinguishing
detail. That's the regime where rerank gates earn or waste budget.
"""

from __future__ import annotations

import hashlib
import json
import random
from typing import Iterable

# Hall vocabulary mirrors the mfs MCP contract — keep these aligned
# with the project memory schema described in CLAUDE.md so the
# distribution shape resembles real agent traffic.
HALLS = ["fact", "decision", "discovery", "preference", "advice", "event"]
ROOMS = [
    "auth",
    "deploy",
    "mcp",
    "rerank",
    "ingest",
    "graph",
    "ui",
    "observability",
]

# Templates: each (hall, list of templates with {tokens}). The {x_*}
# placeholders are filled from disjoint vocab pools per room so the
# resulting records have realistic room-specific content while still
# overlapping enough to produce ambiguous queries.
TEMPLATES: dict[str, list[str]] = {
    "fact": [
        "{room} service runs on port {port} with {protocol}",
        "{room} endpoint /{path} returns {shape}",
        "{room} requires environment variable {env}={val}",
        "{room} component {comp} version {ver} is the current shipped build",
    ],
    "decision": [
        "decided to use {choice} for {room} instead of {alt}. why: {reason}",
        "rolled back {comp} change in {room} because {reason}",
        "moved {workload} from {old_host} to {new_host}",
        "adopted {pattern} pattern across {room} after {reason}",
    ],
    "discovery": [
        "{room} bug: {symptom}. root cause: {cause}. fix: {fix}",
        "found that {comp} silently fails when {condition}",
        "{room} latency spike traced to {cause}",
        "regression in {comp}: {symptom} after {trigger}",
    ],
    "preference": [
        "user prefers {style} for {room} responses",
        "user wants {behavior} when working on {room}",
        "for {room} changes always {action} first",
        "do not {antipattern} in {room} code",
    ],
    "advice": [
        "before changing {room}, run {check}",
        "when {room} alerts fire, look at {dashboard} first",
        "to debug {comp} issues, enable {flag} and check {log}",
        "rotate {secret} in {room} every {period}",
    ],
    "event": [
        "{date}: shipped {feature} to {room}",
        "{date}: incident in {room} — {symptom} for {duration}",
        "{date}: cut release {version} of {comp}",
        "{date}: {room} migration step {step} completed",
    ],
}

POOLS: dict[str, dict[str, list[str]]] = {
    "default": {
        "port": ["8080", "8443", "9090", "50051", "7777", "8888", "11434"],
        "protocol": ["HTTP", "HTTPS", "gRPC", "stdio MCP", "TCP"],
        "path": ["search", "auth/login", "rerank/info", "datasets", "memory/save"],
        "shape": ["JSON", "JSONL", "protobuf", "SSE stream", "msgpack"],
        "env": ["JWT_SECRET", "RERANK_ENDPOINT", "EMBED_ENDPOINT", "LLM_API_KEY", "DB_DSN"],
        "val": ["set", "required", "auto-generated", "loaded from vault"],
        "comp": ["levara-server", "memoryfs", "mem0", "ollama", "qwen-rerank"],
        "ver": ["b4fface", "0.4.2", "v2.1.0", "dev"],
        "choice": ["sqlite", "postgres", "neo4j", "redis", "in-memory cache"],
        "alt": ["lancedb", "qdrant", "weaviate", "pinecone", "filesystem"],
        "reason": [
            "performance was 5x better",
            "ops complexity dropped",
            "license required it",
            "user explicitly asked",
            "test data was inconclusive",
            "production traffic showed the limit",
        ],
        "workload": ["embedding model", "rerank model", "LLM inference", "metric scraping"],
        "old_host": ["10.23.0.53", "10.23.0.64", "Mac local", "docker host"],
        "new_host": ["10.23.0.53", "10.23.0.64", "GPU node", "managed cloud"],
        "pattern": ["circuit breaker", "outbox", "event sourcing", "saga", "two-phase"],
        "symptom": [
            "p99 latency over 5s",
            "OOM on insert",
            "embedding mismatch",
            "404 on existing keys",
            "duplicate writes",
            "missing audit lines",
        ],
        "cause": [
            "WAL fsync blocking",
            "GPU memory fragmentation",
            "HNSW rebuild thrashing",
            "rate limiter misconfig",
            "JWT clock skew",
            "embedding dim mismatch",
        ],
        "fix": ["batched fsync", "pinned worker count", "explicit cache eviction", "added retry budget"],
        "condition": ["payload exceeds 1MB", "embedding is null", "rerank times out"],
        "trigger": ["dependency bump", "model swap", "config reload"],
        "style": ["terse", "step-by-step", "with code samples", "diagram-first"],
        "behavior": ["no summaries at the end", "show diff inline", "explain trade-offs"],
        "action": ["run tests", "check audit log", "snapshot DB", "warn user"],
        "antipattern": ["mock the embedder", "skip JWT checks", "log secrets", "swallow errors"],
        "check": ["go test ./...", "make swag", "pytest tests/ -v", "k6 smoke"],
        "dashboard": ["grafana p99 board", "rerank-outcome dashboard", "ingest queue depth"],
        "flag": ["DEBUG=1", "LEVARA_TRACE=true", "RERANK_VERBOSE=1"],
        "log": ["/var/log/levara.log", "audit log", "ollama stderr"],
        "secret": ["JWT_SECRET", "DB password", "LLM API key", "service JWTs"],
        "period": ["90 days", "30 days", "before each release"],
        "date": ["2026-03-15", "2026-04-02", "2026-04-28", "2026-05-10", "2026-05-25"],
        "feature": ["rerank gate threshold", "audit log rotation", "MCP tool groups", "P0 plan"],
        "duration": ["15 minutes", "2 hours", "until reboot"],
        "version": ["v0.4.0", "v0.4.1", "v0.5.0", "b4fface"],
        "step": ["1 of 4", "2 of 4", "3 of 4", "final"],
    },
}


def _rng(seed: str) -> random.Random:
    return random.Random(int(hashlib.sha256(seed.encode()).hexdigest(), 16) % (2**32))


def _fill(template: str, rng: random.Random, room: str) -> str:
    pool = POOLS["default"]
    out = template
    for token in [t for t in pool if "{" + t + "}" in template]:
        out = out.replace("{" + token + "}", rng.choice(pool[token]))
    out = out.replace("{room}", room)
    return out


def load_corpus(per_room_per_hall: int = 12) -> list[dict[str, str]]:
    """Generate per_room_per_hall × |ROOMS| × |HALLS| records.

    Defaults produce 12 × 8 × 6 = 576 short records. Each gets a
    stable id derived from (room, hall, index, content hash) so
    re-running the seeder is a no-op when the plan hasn't changed.
    """
    out: list[dict[str, str]] = []
    for room in ROOMS:
        for hall in HALLS:
            for i in range(per_room_per_hall):
                rng = _rng(f"{room}:{hall}:{i}")
                tpl = rng.choice(TEMPLATES[hall])
                text = _fill(tpl, rng, room)
                rec_id = f"mem:{room}:{hall}:{i:03d}"
                # Prefix each record with a room/hall preamble so the
                # text clears Levara's default MinChunkChars=80 floor
                # in the paragraph-merged chunker. Without this, all
                # 576 records (33-100 chars) get dropped at chunk time
                # and the collection ends up empty. The preamble also
                # gives the embedder useful per-record context.
                text = (
                    f"[room={room} hall={hall}] {hall} memory in the "
                    f"{room} subsystem (record {rec_id}). {text}"
                )
                out.append(
                    {
                        "id": rec_id,
                        "text": text,
                        "metadata": json.dumps(
                            {
                                "source": "memory_palace",
                                "room": room,
                                "hall": hall,
                                "kind": "memory",
                            }
                        ),
                    }
                )
    return out


def corpus_fingerprint() -> str:
    h = hashlib.sha256()
    h.update(f"halls={'|'.join(HALLS)}\n".encode())
    h.update(f"rooms={'|'.join(ROOMS)}\n".encode())
    for hall, tpls in TEMPLATES.items():
        for t in tpls:
            h.update(f"{hall}:{t}\n".encode())
    return h.hexdigest()[:12]


# Recall-shaped query set: agent prompts probing the corpus from
# multiple angles. Tag with expected_room / expected_hall when known
# so the analyzer can verify filter-aware rerank actually improves
# precision on filtered queries.
QUERIES: list[dict[str, str | None]] = [
    # broad — many plausible answers across rooms
    {"id": "q01", "text": "what decisions have we made about deployment", "kind": "broad", "expected_hall": "decision"},
    {"id": "q02", "text": "any preferences the user has shared", "kind": "broad", "expected_hall": "preference"},
    {"id": "q03", "text": "recent events on the rerank stack", "kind": "broad", "expected_hall": "event", "expected_room": "rerank"},
    {"id": "q04", "text": "discoveries about ingest latency", "kind": "broad", "expected_hall": "discovery", "expected_room": "ingest"},
    {"id": "q05", "text": "facts about authentication setup", "kind": "broad", "expected_hall": "fact", "expected_room": "auth"},
    {"id": "q06", "text": "advice for working on observability", "kind": "broad", "expected_hall": "advice", "expected_room": "observability"},
    # narrow / sharp
    {"id": "q07", "text": "what port does the gRPC server listen on", "kind": "sharp", "expected_hall": "fact"},
    {"id": "q08", "text": "why did we pick sqlite over lancedb", "kind": "sharp", "expected_hall": "decision"},
    {"id": "q09", "text": "what fixes the WAL fsync latency", "kind": "sharp", "expected_hall": "discovery"},
    {"id": "q10", "text": "how often should we rotate the JWT secret", "kind": "sharp", "expected_hall": "advice"},
    # ambiguous — room overlap
    {"id": "q11", "text": "auth changes in deploy", "kind": "ambiguous"},
    {"id": "q12", "text": "rerank ingest path", "kind": "ambiguous"},
    {"id": "q13", "text": "graph mcp tool", "kind": "ambiguous"},
    {"id": "q14", "text": "ui observability dashboard", "kind": "ambiguous"},
    # paraphrase
    {"id": "q15", "text": "the user does not want long summaries", "kind": "paraphrase", "expected_hall": "preference"},
    {"id": "q16", "text": "things broke right after a model swap", "kind": "paraphrase", "expected_hall": "discovery"},
    {"id": "q17", "text": "what happened on 2026-05-10 in the project", "kind": "paraphrase", "expected_hall": "event"},
    {"id": "q18", "text": "before pushing changes to auth, what do I run", "kind": "paraphrase", "expected_hall": "advice", "expected_room": "auth"},
    # incident-style
    {"id": "q19", "text": "p99 latency spike", "kind": "incident", "expected_hall": "discovery"},
    {"id": "q20", "text": "rate limit misconfiguration", "kind": "incident", "expected_hall": "discovery"},
    # out-of-corpus
    {"id": "q21", "text": "best fountain pen ink for cotton paper", "kind": "ooc"},
    {"id": "q22", "text": "rugby world cup quarterfinal results", "kind": "ooc"},
    {"id": "q23", "text": "watercolor brush sizes", "kind": "ooc"},
    {"id": "q24", "text": "vintage synthesizer envelope generators", "kind": "ooc"},
]
