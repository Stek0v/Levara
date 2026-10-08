#!/usr/bin/env python3
"""Score frozen imported-chat retrieval results without network or database access.

Result adapters stay outside this harness.  They write the small JSON envelope
described by ``--print-example``; a separate manifest pins every input by
SHA-256 before this program reads and scores it.
"""

import argparse
import hashlib
import json
import math
from pathlib import Path
import re
import statistics
import tempfile


HERE = Path(__file__).resolve().parent
DEFAULT_CASES = HERE / "chat_retrieval_quality_cases.json"
VARIANTS = tuple(f"Q{i}" for i in range(13))
TOP_KS = (1, 3, 5, 10)
SEALED_MINIMUM_CASES = 120
VARIANT_KINDS = {
    "Q0": "raw_sql_like", "Q1": "postgres_fts", "Q2": "sqlite_fts5",
    "Q3": "dense_v1", "Q4": "dense_v2", "Q5": "dense_v2_rerank",
    "Q6": "postgres_fts_dense_rrf", "Q7": "conversation_summary",
    "Q8": "segment_summary", "Q9": "distilled_memory",
    "Q10": "fts_conversation_summary", "Q11": "fts_segment_summary",
    "Q12": "fts_distilled_memory",
}
DENSE_VARIANTS = frozenset(("Q3", "Q4", "Q5", "Q6", "Q7", "Q8", "Q9", "Q10", "Q11", "Q12"))
LEXICAL_VARIANTS = frozenset(("Q0", "Q1", "Q2", "Q6", "Q10", "Q11", "Q12"))
HYBRID_VARIANTS = frozenset(("Q6", "Q10", "Q11", "Q12"))
DERIVED_VARIANTS = frozenset(("Q7", "Q8", "Q9", "Q10", "Q11", "Q12"))
DERIVED_KINDS = {
    "Q7": "conversation_summary", "Q8": "segment_summary", "Q9": "distilled_memory",
    "Q10": "conversation_summary", "Q11": "segment_summary", "Q12": "distilled_memory",
}


def sha256(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def canonical_sha256(value):
    payload = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(payload).hexdigest()


def valid_sha256(value):
    return isinstance(value, str) and re.fullmatch(r"[0-9a-f]{64}", value) is not None


def percentile(values, fraction):
    if not values:
        return None
    ordered = sorted(values)
    return ordered[max(0, math.ceil(len(ordered) * fraction) - 1)]


def load_json(path):
    def unique_object(pairs):
        value = {}
        for key, item in pairs:
            if key in value:
                raise ValueError(f"duplicate JSON key {key!r}")
            value[key] = item
        return value

    try:
        return json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=unique_object)
    except (OSError, json.JSONDecodeError) as error:
        raise ValueError(f"cannot read {path}: {error}") from error


def require(condition, message):
    if not condition:
        raise ValueError(message)


def require_named_object(value, label):
    require(isinstance(value, dict), f"{label} must be an object")
    require(isinstance(value.get("name"), str) and value["name"], f"{label}.name is required")
    require(isinstance(value.get("parameters"), dict), f"{label}.parameters must be an object")


def require_model(value, label):
    require(isinstance(value, dict), f"{label} must be an object")
    require(isinstance(value.get("model_id"), str) and value["model_id"], f"{label}.model_id is required")
    require(valid_sha256(value.get("model_sha256")), f"{label}.model_sha256 must be SHA-256")


def validate_variant_config(variant_id, config):
    require(isinstance(config, dict), f"{variant_id}: config must be an object")
    require(config.get("variant_id") == variant_id, f"{variant_id}: config variant mismatch")
    require(config.get("retrieval_kind") == VARIANT_KINDS[variant_id],
            f"{variant_id}: retrieval kind mismatch")
    require(config.get("evaluation_mode") in {"developer_calibration", "sealed_candidate"},
            f"{variant_id}: evaluation_mode is invalid")
    expected = {"variant_id", "retrieval_kind", "evaluation_mode"}
    if variant_id in LEXICAL_VARIANTS:
        require_named_object(config.get("lexical"), f"{variant_id}.lexical")
        expected.add("lexical")
    if variant_id in DENSE_VARIANTS:
        embedding = config.get("embedding")
        require_model(embedding, f"{variant_id}.embedding")
        require(type(embedding.get("dimension")) is int and embedding["dimension"] > 0,
                f"{variant_id}.embedding.dimension must be a positive integer")
        require(embedding.get("normalization") in {"none", "l2"},
                f"{variant_id}.embedding.normalization is invalid")
        require(embedding.get("distance_metric") in {"cosine", "dot", "l2"},
                f"{variant_id}.embedding.distance_metric is invalid")
        require(set(embedding) == {"model_id", "model_sha256", "dimension", "normalization", "distance_metric"},
                f"{variant_id}.embedding fields are not canonical")
        require_named_object(config.get("index"), f"{variant_id}.index")
        expected.update(("embedding", "index"))
    if variant_id == "Q5":
        require_model(config.get("reranker"), "Q5.reranker")
        require(isinstance(config["reranker"].get("parameters"), dict),
                "Q5.reranker.parameters must be an object")
        require(set(config["reranker"]) == {"model_id", "model_sha256", "parameters"},
                "Q5.reranker fields are not canonical")
        expected.add("reranker")
    if variant_id in HYBRID_VARIANTS:
        require_named_object(config.get("fusion"), f"{variant_id}.fusion")
        expected.add("fusion")
    if variant_id in DERIVED_VARIANTS:
        derivation = config.get("derivation")
        require_model(derivation, f"{variant_id}.derivation")
        require(derivation.get("artifact_kind") == DERIVED_KINDS[variant_id],
                f"{variant_id}: derived artifact kind mismatch")
        require(valid_sha256(derivation.get("prompt_sha256")),
                f"{variant_id}.derivation.prompt_sha256 must be SHA-256")
        require(valid_sha256(derivation.get("renderer_sha256")),
                f"{variant_id}.derivation.renderer_sha256 must be SHA-256")
        require(set(derivation) == {"artifact_kind", "model_id", "model_sha256",
                                    "prompt_sha256", "renderer_sha256"},
                f"{variant_id}.derivation fields are not canonical")
        expected.add("derivation")
    require(set(config) == expected, f"{variant_id}: config fields are not canonical")


def validate_artifacts(variant_id, item, base):
    artifacts = item.get("artifacts", [])
    require(isinstance(artifacts, list), f"{variant_id}: artifacts must be a list")
    if variant_id in DERIVED_VARIANTS:
        require(artifacts, f"{variant_id}: derived artifacts are required")
    else:
        require(not artifacts, f"{variant_id}: unexpected derived artifacts")
    identities = []
    for artifact in artifacts:
        require(isinstance(artifact, dict) and set(artifact) == {"role", "path", "sha256"},
                f"{variant_id}: artifact fields are not canonical")
        require(isinstance(artifact["role"], str) and artifact["role"],
                f"{variant_id}: artifact role is required")
        require(isinstance(artifact["path"], str) and artifact["path"],
                f"{variant_id}: artifact path is required")
        require(valid_sha256(artifact["sha256"]), f"{variant_id}: artifact SHA-256 is invalid")
        require(sha256(base / artifact["path"]) == artifact["sha256"],
                f"{variant_id}: artifact {artifact['role']} SHA-256 mismatch")
        identities.append({"role": artifact["role"], "sha256": artifact["sha256"]})
    require(len({entry["role"] for entry in identities}) == len(identities),
            f"{variant_id}: duplicate artifact role")
    return identities, canonical_sha256(identities)


def validate_cases(fixture):
    require(fixture.get("schema_version") == 1, "unsupported case schema")
    require(fixture.get("synthetic") is True, "only explicitly synthetic cases are accepted")
    cases = fixture.get("cases")
    require(isinstance(cases, list) and cases, "cases must be a non-empty list")
    ids = [case.get("id") for case in cases]
    require(all(isinstance(value, str) and value for value in ids), "every case needs an id")
    require(len(ids) == len(set(ids)), "case ids must be unique")
    for case in cases:
        prefix = f"case {case['id']}"
        require(isinstance(case.get("query"), str) and case["query"].strip(), f"{prefix}: missing query")
        require(case.get("kind") in {"known", "unknown"}, f"{prefix}: invalid kind")
        for field in ("required_fact_ids", "relevant_message_ids", "relevant_session_ids"):
            require(isinstance(case.get(field), list), f"{prefix}: {field} must be a list")
            require(all(isinstance(value, str) and value for value in case[field]),
                    f"{prefix}: {field} values must be non-empty strings")
            require(len(case[field]) == len(set(case[field])), f"{prefix}: duplicate {field} values")
        allowed = case.get("allowed_provenance")
        require(isinstance(allowed, list), f"{prefix}: allowed_provenance must be a list")
        for entry in allowed:
            require(isinstance(entry, dict), f"{prefix}: allowed provenance must be an object")
            require(entry.get("source_kind") in {"message", "part"},
                    f"{prefix}: invalid allowed source_kind")
            for field in ("source_id", "session_id", "owner_id", "tenant_id", "project_id"):
                require(isinstance(entry.get(field), str) and entry[field],
                        f"{prefix}: allowed provenance needs {field}")
        allowed_tuples = [tuple(entry[field] for field in
                          ("source_kind", "source_id", "session_id", "owner_id", "tenant_id", "project_id"))
                          for entry in allowed]
        require(len(allowed_tuples) == len(set(allowed_tuples)), f"{prefix}: duplicate allowed provenance")
        require(case["kind"] == "unknown" or case["relevant_session_ids"],
                f"{prefix}: known query needs a relevant session")
        allowed_messages = {entry["source_id"] for entry in allowed if entry["source_kind"] == "message"}
        allowed_sessions = {entry["session_id"] for entry in allowed}
        require(set(case["relevant_message_ids"]) <= allowed_messages,
                f"{prefix}: relevant messages must be allowed")
        require(set(case["relevant_session_ids"]) <= allowed_sessions,
                f"{prefix}: relevant sessions must be allowed")
    return cases


def resolve_pinned_inputs(manifest_path, cases_override=None, sealed=False):
    manifest = load_json(manifest_path)
    require(manifest.get("schema_version") == 1, "unsupported manifest schema")
    base = manifest_path.resolve().parent
    require(cases_override is not None or isinstance(manifest.get("cases_path"), str),
            "manifest cases_path is required")
    require(isinstance(manifest.get("corpus_path"), str), "manifest corpus_path is required")
    require(valid_sha256(manifest.get("cases_sha256")), "manifest cases_sha256 is invalid")
    require(valid_sha256(manifest.get("corpus_sha256")), "manifest corpus_sha256 is invalid")
    cases_path = cases_override.resolve() if cases_override else base / manifest["cases_path"]
    require(sha256(cases_path) == manifest.get("cases_sha256"), "case fixture SHA-256 mismatch")
    corpus_path = base / manifest["corpus_path"]
    require(sha256(corpus_path) == manifest.get("corpus_sha256"), "corpus SHA-256 mismatch")
    fixture = load_json(cases_path)
    cases = validate_cases(fixture)
    pinned = []
    variants = manifest.get("variants")
    require(isinstance(variants, dict) and variants, "manifest variants must be a non-empty object")
    if sealed:
        require(len(cases) >= SEALED_MINIMUM_CASES,
                f"sealed report requires at least {SEALED_MINIMUM_CASES} cases")
        require(set(variants) == set(VARIANTS), "sealed report requires exactly Q0-Q12")
        require(all(item.get("config", {}).get("evaluation_mode") == "sealed_candidate"
                    for item in variants.values()),
                "sealed report rejects developer calibration configurations")
    for variant_id in VARIANTS:
        if variant_id not in variants:
            continue
        item = variants[variant_id]
        require(isinstance(item, dict), f"{variant_id}: manifest entry must be an object")
        require(isinstance(item.get("path"), str) and item["path"],
                f"{variant_id}: result path is required")
        require(valid_sha256(item.get("sha256")), f"{variant_id}: result SHA-256 is invalid")
        config = item.get("config")
        validate_variant_config(variant_id, config)
        config_sha256 = canonical_sha256(config)
        require(valid_sha256(item.get("config_sha256")), f"{variant_id}: config SHA-256 is invalid")
        require(item.get("config_sha256") == config_sha256,
                f"{variant_id}: config SHA-256 mismatch")
        artifacts, artifacts_sha256 = validate_artifacts(variant_id, item, base)
        path = base / item["path"]
        require(sha256(path) == item.get("sha256"), f"{variant_id} result SHA-256 mismatch")
        result = load_json(path)
        require(result.get("schema_version") == 1, f"{variant_id}: unsupported result schema")
        require(result.get("variant_id") == variant_id, f"{variant_id}: variant id mismatch")
        require(result.get("corpus_sha256") == manifest.get("corpus_sha256"),
                f"{variant_id}: corpus SHA-256 mismatch")
        require(result.get("config_sha256") == config_sha256,
                f"{variant_id}: result config SHA-256 mismatch")
        require(result.get("artifacts_sha256") == artifacts_sha256,
                f"{variant_id}: result artifact SHA-256 mismatch")
        pinned.append((variant_id, path, item["sha256"], config, config_sha256,
                       artifacts, artifacts_sha256, result))
    require(len(pinned) == len(variants), "manifest contains an unknown Q variant")
    return manifest, cases_path, corpus_path, cases, pinned


def hit_key(hit):
    if hit["source_kind"] == "message":
        return ("message", hit["message_id"])
    return ("part", hit["session_id"], hit["part_id"])


def ranked_relevances(hits, case):
    seen_messages = set()
    seen_sessions = set()
    values = []
    for hit in hits:
        message = hit.get("message_id")
        session = hit["session_id"]
        if message in case["relevant_message_ids"] and message not in seen_messages:
            seen_messages.add(message)
            values.append(2)
        elif (message not in case["relevant_message_ids"] and
              session in case["relevant_session_ids"] and session not in seen_sessions):
            seen_sessions.add(session)
            values.append(1)
        else:
            values.append(0)
    return values


def source_allowed(hit, case):
    if not complete_provenance(hit):
        return False
    source_id = hit["message_id"] if hit["source_kind"] == "message" else hit["part_id"]
    actual = (hit["source_kind"], source_id, hit["session_id"], hit["owner_id"],
              hit["tenant_id"], hit["project_id"])
    return actual in {
        tuple(entry[field] for field in
              ("source_kind", "source_id", "session_id", "owner_id", "tenant_id", "project_id"))
        for entry in case["allowed_provenance"]
    }


def complete_provenance(hit):
    return (all(hit.get(field) for field in ("session_id", "owner_id", "tenant_id", "project_id"))
            and ((hit.get("source_kind") == "message" and bool(hit.get("message_id")) and not hit.get("part_id"))
                 or (hit.get("source_kind") == "part" and bool(hit.get("part_id")) and not hit.get("message_id"))))


def authorized(hit, case):
    return source_allowed(hit, case)


def dcg(relevances):
    return sum((2 ** value - 1) / math.log2(rank + 1)
               for rank, value in enumerate(relevances, 1))


def validate_hit(hit, case_id):
    prefix = f"{case_id}: malformed hit"
    for field in ("session_id", "owner_id", "tenant_id", "project_id"):
        require(isinstance(hit.get(field), str) and hit[field], f"{prefix}: missing {field}")
    require(hit.get("source_kind") in {"message", "part"}, f"{prefix}: invalid source_kind")
    message, part = hit.get("message_id"), hit.get("part_id")
    if hit["source_kind"] == "message":
        require(isinstance(message, str) and message and part in (None, ""),
                f"{prefix}: message source requires only message_id")
    else:
        require(isinstance(part, str) and part and message in (None, ""),
                f"{prefix}: part source requires only part_id")
    facts = hit.get("fact_ids")
    require(isinstance(facts, list) and all(isinstance(value, str) and value for value in facts),
            f"{prefix}: fact_ids must contain strings")
    require(len(facts) == len(set(facts)), f"{prefix}: duplicate fact_ids")
    require(type(hit.get("is_stale")) is bool and type(hit.get("is_active")) is bool,
            f"{prefix}: stale flags must be booleans")


def score_case(case, row):
    require(row.get("case_id") == case["id"], f"result order differs at {case['id']}")
    latency = row.get("latency_ms")
    require(type(latency) in (int, float) and math.isfinite(latency) and latency >= 0,
            f"{case['id']}: invalid latency_ms")
    hits = row.get("hits")
    require(isinstance(hits, list), f"{case['id']}: hits must be a list")
    require(all(isinstance(hit, dict) for hit in hits), f"{case['id']}: each hit must be an object")
    for hit in hits:
        validate_hit(hit, case["id"])
    top = hits[:10]
    rel = ranked_relevances(top, case)
    first = next((rank for rank, value in enumerate(rel, 1) if value), None)
    ideal = sorted([2] * len(case["relevant_message_ids"]) +
                   [1] * len(case["relevant_session_ids"]),
                   reverse=True)[:10]
    required = set(case["required_fact_ids"])
    found = {fact for hit in top for fact in hit.get("fact_ids", [])}
    duplicate_count = len(top) - len({hit_key(hit) for hit in top})
    unauthorized = sum(not authorized(hit, case) for hit in top)
    incomplete = sum(not complete_provenance(hit) for hit in top)
    active_stale = sum(bool(hit.get("is_stale") and hit.get("is_active")) for hit in top)
    return {
        "case_id": case["id"],
        "kind": case["kind"],
        "latency_ms": latency,
        "returned": len(hits),
        "recall": {f"@{k}": int(any(rel[:k])) for k in TOP_KS},
        "reciprocal_rank_at_10": 0 if first is None else 1 / first,
        "ndcg_at_10": 0 if not ideal else dcg(rel) / dcg(ideal),
        "all_required_facts": required.issubset(found),
        "session_recalled": any(hit.get("session_id") in case["relevant_session_ids"] for hit in top),
        "exact_message_recalled": any(hit.get("message_id") in case["relevant_message_ids"] for hit in top),
        "stale_hits": sum(bool(hit.get("is_stale")) for hit in top),
        "duplicate_hits": duplicate_count,
        "unauthorized_hits": unauthorized,
        "incomplete_provenance_hits": incomplete,
        "active_stale_hits": active_stale,
        "unknown_nonempty": case["kind"] == "unknown" and bool(hits),
    }


def ratio(numerator, denominator):
    return 0 if not denominator else numerator / denominator


def score_variant(variant_id, result, cases):
    rows = result.get("queries")
    require(isinstance(rows, list), f"{variant_id}: queries must be a list")
    require(len(rows) == len(cases), f"{variant_id}: expected {len(cases)} query rows")
    scored = [score_case(case, row) for case, row in zip(cases, rows)]
    known = [row for row in scored if row["kind"] == "known"]
    unknown = [row for row in scored if row["kind"] == "unknown"]
    returned = sum(min(row["returned"], 10) for row in scored)
    latencies = [row["latency_ms"] for row in scored]
    unauthorized = sum(row["unauthorized_hits"] for row in scored)
    incomplete = sum(row["incomplete_provenance_hits"] for row in scored)
    active_stale = sum(row["active_stale_hits"] for row in scored)
    return {
        "variant_id": variant_id,
        "quality": {
            **{f"recall@{k}": ratio(sum(row["recall"][f"@{k}"] for row in known), len(known)) for k in TOP_KS},
            "mrr@10": ratio(sum(row["reciprocal_rank_at_10"] for row in known), len(known)),
            "ndcg@10": ratio(sum(row["ndcg_at_10"] for row in known), len(known)),
            "all_required_facts_success": ratio(sum(row["all_required_facts"] for row in known), len(known)),
            "session_recall@10": ratio(sum(row["session_recalled"] for row in known), len(known)),
            "exact_message_recall@10": ratio(sum(row["exact_message_recalled"] for row in known), len(known)),
            "stale_hit_rate": ratio(sum(row["stale_hits"] for row in scored), returned),
            "duplicate_hit_rate": ratio(sum(row["duplicate_hits"] for row in scored), returned),
            "unknown_query_nonempty_rate": ratio(sum(row["unknown_nonempty"] for row in unknown), len(unknown)),
        },
        "latency_ms": {
            "count": len(latencies), "mean": statistics.fmean(latencies),
            "p50": percentile(latencies, .50), "p95": percentile(latencies, .95),
            "p99": percentile(latencies, .99), "max": max(latencies),
        },
        "hard_gates": {
            "zero_unauthorized_hits": unauthorized == 0,
            "complete_allowed_source_provenance": incomplete == 0 and unauthorized == 0,
            "zero_active_stale_version_hits": active_stale == 0,
        },
        "gate_counts": {
            "unauthorized_hits": unauthorized,
            "incomplete_provenance_hits": incomplete,
            "active_stale_hits": active_stale,
        },
        "cases": scored,
    }


def semantic_digest(report):
    semantic = {
        "schema_version": report["schema_version"],
        "cases_sha256": report["inputs"]["cases_sha256"],
        "corpus_sha256": report["inputs"]["corpus_sha256"],
        "results": [{"variant_id": item["variant_id"], "sha256": item["sha256"],
                     "config": item["config"],
                     "config_sha256": item["config_sha256"],
                     "artifacts": item["artifacts"],
                     "artifacts_sha256": item["artifacts_sha256"]}
                    for item in report["inputs"]["results"]],
        "variant_order": report["variant_order"],
        "variants": report["variants"],
    }
    payload = json.dumps(semantic, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(payload).hexdigest()


def build_report(manifest_path, cases_override=None, sealed=False):
    manifest, cases_path, corpus_path, cases, pinned = resolve_pinned_inputs(
        manifest_path, cases_override, sealed)
    variants = [score_variant(variant, result, cases)
                for variant, _, _, _, _, _, _, result in pinned]
    report = {
        "schema_version": 1,
        "inputs": {
            "manifest": str(manifest_path),
            "cases": str(cases_path),
            "cases_sha256": manifest["cases_sha256"],
            "corpus": str(corpus_path),
            "corpus_sha256": manifest["corpus_sha256"],
            "results": [{"variant_id": variant, "path": str(path), "sha256": digest,
                         "config": config, "config_sha256": config_digest, "artifacts": artifacts,
                         "artifacts_sha256": artifacts_digest}
                        for variant, path, digest, config, config_digest,
                        artifacts, artifacts_digest, _ in pinned],
        },
        "variant_order": [variant["variant_id"] for variant in variants],
        "variants": variants,
        "note": "Safety, quality, and latency are intentionally reported separately; no composite score is produced.",
    }
    report["semantic_sha256"] = semantic_digest(report)
    return report


def self_check():
    fixture = load_json(DEFAULT_CASES)
    cases = validate_cases(fixture)

    def make_hit(entry, facts):
        message = entry["source_id"] if entry["source_kind"] == "message" else None
        part = entry["source_id"] if entry["source_kind"] == "part" else None
        return {"source_kind": entry["source_kind"], "message_id": message, "part_id": part,
                "session_id": entry["session_id"], "owner_id": entry["owner_id"],
                "tenant_id": entry["tenant_id"], "project_id": entry["project_id"],
                "fact_ids": facts, "is_stale": False, "is_active": True}

    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        case_path = root / "cases.json"
        case_path.write_text(json.dumps(fixture, ensure_ascii=False), encoding="utf-8")
        corpus_path = root / "corpus.jsonl"
        corpus_path.write_bytes(b'{"synthetic":true}\n')
        rows = []
        for case in cases:
            if case["kind"] == "unknown":
                hits = []
            else:
                wanted = set(case["relevant_message_ids"])
                entry = next((item for item in case["allowed_provenance"]
                              if item["source_kind"] == "message" and item["source_id"] in wanted),
                             case["allowed_provenance"][0])
                hits = [make_hit(entry, case["required_fact_ids"])]
            rows.append({"case_id": case["id"], "latency_ms": 1.0, "hits": hits})
        q0_config = {"variant_id": "Q0", "retrieval_kind": "raw_sql_like",
                     "evaluation_mode": "developer_calibration",
                     "lexical": {"name": "sql_like", "parameters": {"case_sensitive": False}}}
        q0_config_digest = canonical_sha256(q0_config)
        no_artifacts_digest = canonical_sha256([])
        result_path = root / "q0.json"
        result_path.write_text(json.dumps({"schema_version": 1, "variant_id": "Q0",
                                           "corpus_sha256": sha256(corpus_path),
                                           "config_sha256": q0_config_digest,
                                           "artifacts_sha256": no_artifacts_digest, "queries": rows}))
        artifact_path = root / "conversation-summaries.jsonl"
        artifact_path.write_bytes(b'{"synthetic_summary":true}\n')
        artifact = {"role": "conversation_summaries", "path": artifact_path.name,
                    "sha256": sha256(artifact_path)}
        artifact_identities = [{"role": artifact["role"], "sha256": artifact["sha256"]}]
        q7_config = {
            "variant_id": "Q7", "retrieval_kind": "conversation_summary",
            "evaluation_mode": "developer_calibration",
            "embedding": {"model_id": "synthetic-embed", "model_sha256": "1" * 64,
                          "dimension": 768, "normalization": "l2", "distance_metric": "cosine"},
            "index": {"name": "hnsw", "parameters": {"m": 16}},
            "derivation": {"artifact_kind": "conversation_summary", "model_id": "synthetic-generator",
                           "model_sha256": "2" * 64, "prompt_sha256": "3" * 64,
                           "renderer_sha256": "4" * 64},
        }
        q7_config_digest = canonical_sha256(q7_config)
        q7_result_path = root / "q7.json"
        q7_result_path.write_text(json.dumps({"schema_version": 1, "variant_id": "Q7",
                                              "corpus_sha256": sha256(corpus_path),
                                              "config_sha256": q7_config_digest,
                                              "artifacts_sha256": canonical_sha256(artifact_identities),
                                              "queries": rows}))
        manifest_path = root / "manifest.json"
        manifest = {"schema_version": 1, "cases_path": "cases.json", "cases_sha256": sha256(case_path),
                    "corpus_path": "corpus.jsonl", "corpus_sha256": sha256(corpus_path),
                    "variants": {
                        "Q0": {"path": "q0.json", "sha256": sha256(result_path),
                               "config": q0_config, "config_sha256": q0_config_digest},
                        "Q7": {"path": "q7.json", "sha256": sha256(q7_result_path),
                               "config": q7_config, "config_sha256": q7_config_digest,
                               "artifacts": [artifact]},
                    }}
        manifest_path.write_text(json.dumps(manifest))
        report = build_report(manifest_path)
        q0 = report["variants"][0]
        require(all(q0["hard_gates"].values()), "happy path failed a hard gate")
        require(q0["quality"]["recall@10"] == 1, "happy path recall failed")
        require(q0["quality"]["unknown_query_nonempty_rate"] == 0, "unknown query did not abstain")
        require(len(report["semantic_sha256"]) == 64, "semantic report digest missing")
        original_digest = report["semantic_sha256"]
        report["inputs"]["manifest"] = "/different/host/manifest.json"
        report["inputs"]["cases"] = "/different/host/cases.json"
        report["inputs"]["corpus"] = "/different/host/corpus.jsonl"
        report["inputs"]["results"][0]["path"] = "/different/host/q0.json"
        require(semantic_digest(report) == original_digest, "semantic digest includes host paths")
        wrong_label = dict(q7_config, retrieval_kind="segment_summary")
        try:
            validate_variant_config("Q7", wrong_label)
        except ValueError as error:
            require("retrieval kind mismatch" in str(error), "variant mismatch failed incorrectly")
        else:
            raise RuntimeError("mislabeled variant config was accepted")
        try:
            validate_variant_config("Q7", None)
        except ValueError as error:
            require("config must be an object" in str(error), "missing config failed incorrectly")
        else:
            raise RuntimeError("missing variant config was accepted")
        manifest["variants"]["Q0"]["config"]["lexical"]["parameters"]["case_sensitive"] = True
        manifest_path.write_text(json.dumps(manifest))
        try:
            build_report(manifest_path)
        except ValueError as error:
            require("config SHA-256 mismatch" in str(error), "mutated config failed incorrectly")
        else:
            raise RuntimeError("mutated config was accepted with its old digest")
        manifest["variants"]["Q0"]["config"]["lexical"]["parameters"]["case_sensitive"] = False
        manifest_path.write_text(json.dumps(manifest))
        missing_derivation = dict(q7_config)
        del missing_derivation["derivation"]
        try:
            validate_variant_config("Q7", missing_derivation)
        except ValueError as error:
            require("derivation" in str(error), "missing derivation failed incorrectly")
        else:
            raise RuntimeError("derived variant without generator config was accepted")
        wrong_dimension = json.loads(json.dumps(q7_config))
        wrong_dimension["embedding"]["dimension"] = "768"
        try:
            validate_variant_config("Q7", wrong_dimension)
        except ValueError as error:
            require("positive integer" in str(error), "wrong config type failed incorrectly")
        else:
            raise RuntimeError("string embedding dimension was accepted")
        try:
            validate_artifacts("Q7", {"artifacts": []}, root)
        except ValueError as error:
            require("required" in str(error), "missing artifact failed incorrectly")
        else:
            raise RuntimeError("derived variant without artifacts was accepted")
        artifact_bytes = artifact_path.read_bytes()
        artifact_path.write_bytes(artifact_bytes + b"changed")
        try:
            build_report(manifest_path)
        except ValueError as error:
            require("artifact conversation_summaries SHA-256 mismatch" in str(error),
                    "artifact mutation failed incorrectly")
        else:
            raise RuntimeError("modified derived artifact was accepted")
        artifact_path.write_bytes(artifact_bytes)
        duplicate_path = root / "duplicate.json"
        duplicate_path.write_text('{"variants":{"Q0":{},"Q0":{}}}')
        try:
            load_json(duplicate_path)
        except ValueError as error:
            require("duplicate JSON key 'Q0'" in str(error), "duplicate variant failed incorrectly")
        else:
            raise RuntimeError("duplicate variant key was accepted")
        try:
            build_report(manifest_path, sealed=True)
        except ValueError as error:
            require("at least 120 cases" in str(error), "sealed mode failed for the wrong reason")
        else:
            raise RuntimeError("sealed mode accepted the eight-case developer fixture")
        # Session/part provenance is allowed even without an exact message id.
        part_case = cases[0]
        part_entry = next(item for item in part_case["allowed_provenance"] if item["source_kind"] == "part")
        part_hit = make_hit(part_entry, part_case["required_fact_ids"])
        require(complete_provenance(part_hit) and authorized(part_hit, part_case),
                "allowed session/part provenance was rejected")
        part_row = {"case_id": part_case["id"], "latency_ms": 1, "hits": [part_hit]}
        require(not score_case(part_case, part_row)["exact_message_recalled"],
                "part provenance was treated as exact-message provenance")
        foreign_message = dict(part_hit, source_kind="message", message_id="foreign-message")
        require(not source_allowed(foreign_message, part_case),
                "foreign message passed through an allowed part")
        malformed = dict(foreign_message, part_id=part_entry["source_id"])
        try:
            score_case(part_case, {"case_id": part_case["id"], "latency_ms": 1, "hits": [malformed]})
        except ValueError as error:
            require("requires only message_id" in str(error), "ambiguous identity failed incorrectly")
        else:
            raise RuntimeError("ambiguous message/part identity was accepted")
        ndcg_hits = [make_hit(item, part_case["required_fact_ids"])
                     for item in part_case["allowed_provenance"]
                     if item["source_kind"] == "message" and item["source_id"] in part_case["relevant_message_ids"]]
        ndcg_hits.append(part_hit)
        ndcg_score = score_case(part_case, {"case_id": part_case["id"], "latency_ms": 1, "hits": ndcg_hits})
        require(0 <= ndcg_score["ndcg_at_10"] <= 1, f"nDCG exceeded 1: {ndcg_score['ndcg_at_10']}")
        require(math.isclose(ndcg_score["ndcg_at_10"], 1),
                "ideal ranking with an extra same-session part did not score 1")
        acl_case = next(case for case in cases if case["id"] == "authorization-collision")
        first_acl, second_acl = acl_case["allowed_provenance"][0], acl_case["allowed_provenance"][-1]
        mixed_acl = make_hit(first_acl, acl_case["required_fact_ids"])
        mixed_acl["tenant_id"] = second_acl["tenant_id"]
        require(not authorized(mixed_acl, acl_case), "Cartesian mix of allowed ACL fields was accepted")
        hazardous = dict(part_hit, is_stale=True, is_active=True, owner_id="foreign-owner")
        hazard_row = {"case_id": part_case["id"], "latency_ms": 2.0,
                      "hits": [hazardous, hazardous.copy()]}
        hazard_score = score_case(part_case, hazard_row)
        require(hazard_score["unauthorized_hits"] == 2, "unauthorized hits were missed")
        require(hazard_score["active_stale_hits"] == 2, "active stale hits were missed")
        require(hazard_score["duplicate_hits"] == 1, "duplicate hit was missed")
        malformed_bool = dict(part_hit, is_stale=1)
        try:
            score_case(part_case, {"case_id": part_case["id"], "latency_ms": 1, "hits": [malformed_bool]})
        except ValueError as error:
            require("booleans" in str(error), "malformed boolean failed incorrectly")
        else:
            raise RuntimeError("integer stale flag was accepted as a boolean")
        unknown = cases[-1]
        unknown_row = {"case_id": unknown["id"], "latency_ms": 1.0, "hits": [hazardous]}
        require(score_case(unknown, unknown_row)["unknown_nonempty"], "unknown nonempty result was missed")
        corpus_bytes = corpus_path.read_bytes()
        corpus_path.write_bytes(corpus_bytes + b"changed")
        try:
            build_report(manifest_path)
        except ValueError as error:
            require("corpus SHA-256 mismatch" in str(error), "corpus mutation failed incorrectly")
        else:
            raise RuntimeError("modified corpus input was accepted")
        corpus_path.write_bytes(corpus_bytes)
        # A changed frozen input must be rejected before scoring.
        result_path.write_text(result_path.read_text() + " ")
        try:
            build_report(manifest_path)
        except ValueError as error:
            require("SHA-256 mismatch" in str(error), "result mutation failed incorrectly")
        else:
            raise RuntimeError("modified result input was accepted")
    print(f"PASS: {len(cases)} synthetic cases; frozen hashes, safety gates, quality and latency metrics verified.")


def print_example():
    config = {"variant_id": "Q0", "retrieval_kind": "raw_sql_like",
              "evaluation_mode": "developer_calibration",
              "lexical": {"name": "sql_like", "parameters": {"case_sensitive": False}}}
    print(json.dumps({
        "manifest": {"schema_version": 1, "cases_path": "cases.json", "cases_sha256": "<sha256>",
                     "corpus_path": "corpus.jsonl", "corpus_sha256": "<sha256>",
                     "variants": {"Q0": {"path": "q0-results.json", "sha256": "<sha256>",
                                             "config": config, "config_sha256": canonical_sha256(config)}}},
        "result": {"schema_version": 1, "variant_id": "Q0", "corpus_sha256": "<sha256>",
                   "config_sha256": canonical_sha256(config),
                   "artifacts_sha256": canonical_sha256([]),
                   "queries": [{"case_id": "exact-error", "latency_ms": 1.2, "hits": []}]},
    }, indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", type=Path)
    parser.add_argument("--cases", type=Path, help="optional path override; hash must still match the manifest")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--self-check", action="store_true")
    parser.add_argument("--print-example", action="store_true")
    parser.add_argument("--sealed", action="store_true",
                        help="require at least 120 cases and exactly Q0-Q12")
    args = parser.parse_args()
    if args.self_check:
        self_check()
        return 0
    if args.print_example:
        print_example()
        return 0
    if not args.manifest or not args.output:
        parser.error("--manifest and --output are required")
    report = build_report(args.manifest, args.cases, args.sealed)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except ValueError as error:
        raise SystemExit(f"ERROR: {error}") from error
