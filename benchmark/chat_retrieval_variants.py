#!/usr/bin/env python3
"""Run deterministic offline Q0-Q12 imported-chat developer calibration."""

import argparse
from collections import defaultdict
import hashlib
import json
import math
from pathlib import Path
import re
import sqlite3
import tempfile
import time
import unicodedata

from chat_retrieval_fixture import OWNER, PRIVATE, SHARED, TENANT, build, jsonl_bytes, message
from chat_retrieval_quality import (DERIVED_VARIANTS, VARIANT_KINDS, build_report,
                                    canonical_sha256, score_case, sha256)


TOKEN_MAP = {
    "какой": "what", "выбрали": "selected", "воркера": "worker",
    "таймаут": "timeout", "секунд": "seconds", "где": "where", "решили": "decided",
    "повтор": "retry", "попыток": "attempts", "у": "", "для": "", "и": "and",
}
STOPWORDS = {"a", "an", "and", "for", "in", "is", "it", "of", "the", "to", "was", "what",
             "when", "where", "which", "with", "recorded", "mentions", "session", "selected",
             "exact", "synthetic", "does", "must", "here"}
HEX = lambda label: hashlib.sha256(label.encode()).hexdigest()


def tokens(text):
    raw = re.findall(r"[\w.-]+", unicodedata.normalize("NFKC", text).casefold())
    mapped = [TOKEN_MAP.get(token, token) for token in raw]
    return [token for token in mapped if token and token not in STOPWORDS]


def normalized(text):
    return " ".join(tokens(text))


def visible(record):
    return (record["owner_id"] == OWNER and record["tenant_id"] == TENANT
            and record["project_id"] in {SHARED, PRIVATE} and not record.get("revoked")
            and record["is_active"] and not record["is_stale"])


def raw_document(record):
    document = {**record, "source_kind": "message", "source_id": record["message_id"], "part_id": None}
    document.pop("fact_annotations", None)
    return document


def derived_documents(records):
    sessions = defaultdict(list)
    for record in records:
        scope = (record["owner_id"], record["tenant_id"], record["project_id"], record["session_id"])
        sessions[scope].append(record)
    conversation, segments, distilled = [], [], []
    for scope in sorted(sessions):
        owner_id, tenant_id, project_id, session_id = scope
        group = sessions[scope]
        excerpts = " ".join(item["text"][:260] + " " + item["text"][-260:] for item in group)
        facts = sorted({annotation["fact_id"] for item in group
                        for annotation in item.get("fact_annotations", [])
                        if annotation["fact_id"] in item["fact_ids"] and
                        (annotation["offset"] < 260 or
                         annotation["offset"] >= max(0, len(item["text"]) - 260))})
        scope_id = hashlib.sha256("\0".join(scope).encode()).hexdigest()[:16]
        common = {"session_id": session_id, "owner_id": owner_id,
                  "tenant_id": tenant_id, "project_id": project_id,
                  "message_id": None, "fact_ids": facts, "is_stale": False, "is_active": True}
        conversation_id = f"conv:{scope_id}:{session_id}"
        segment_id = f"seg:{scope_id}:{session_id}:0"
        conversation.append({**common, "source_kind": "part", "source_id": conversation_id,
                             "part_id": conversation_id, "text": "Conversation summary: " + excerpts})
        segments.append({**common, "source_kind": "part", "source_id": segment_id,
                         "part_id": segment_id, "text": "Segment summary: " + excerpts})
        case_id = session_id.removeprefix("session-")
        distill_id = f"distill:{scope_id}:{case_id}"
        distilled.append({**common, "source_kind": "part", "source_id": distill_id,
                          "part_id": distill_id, "text": "Distilled facts: " + excerpts})
    return conversation, segments, distilled


def allow_all_visible_sources(fixture, documents):
    allowed = [{"source_kind": doc["source_kind"], "source_id": doc["source_id"],
                "session_id": doc["session_id"], "owner_id": doc["owner_id"],
                "tenant_id": doc["tenant_id"], "project_id": doc["project_id"]}
               for doc in documents]
    unique = list({tuple(item.values()): item for item in allowed}.values())
    unique.sort(key=lambda item: tuple(item.values()))
    for case in fixture["cases"]:
        case["allowed_provenance"] = unique


def hit(doc):
    return {"source_kind": doc["source_kind"], "message_id": doc.get("message_id"),
            "part_id": doc.get("part_id"), "session_id": doc["session_id"],
            "owner_id": doc["owner_id"], "tenant_id": doc["tenant_id"],
            "project_id": doc["project_id"], "fact_ids": doc["fact_ids"],
            "is_stale": doc["is_stale"], "is_active": doc["is_active"]}


def dedupe_ranked(scored, limit=10):
    seen, output = set(), []
    for _, doc in sorted(scored, key=lambda item: (-item[0], item[1]["source_id"])):
        identity = (doc["source_kind"], doc["source_id"], doc["session_id"])
        if identity in seen:
            continue
        seen.add(identity)
        output.append(doc)
        if len(output) == limit:
            break
    return output


def like_search(documents, query):
    needle = normalized(query)
    return [doc for doc in documents if needle in normalized(doc["text"])][:10]


class FTSIndex:
    def __init__(self, documents):
        self.connection = sqlite3.connect(":memory:")
        self.connection.execute("CREATE VIRTUAL TABLE docs USING fts5(source_id UNINDEXED, text, tokenize='unicode61')")
        self.connection.executemany("INSERT INTO docs(source_id,text) VALUES (?,?)",
                                    [(doc["source_id"], normalized(doc["text"])) for doc in documents])
        self.lookup = {doc["source_id"]: doc for doc in documents}

    def search(self, query):
        terms = sorted(set(tokens(query)))
        if not terms:
            return []
        expression = " OR ".join('"' + term.replace('"', '""') + '"' for term in terms)
        rows = self.connection.execute(
            "SELECT source_id, bm25(docs) FROM docs WHERE docs MATCH ? ORDER BY bm25(docs), source_id LIMIT 10",
            (expression,)).fetchall()
        minimum = min(2, len(terms))
        return [self.lookup[source_id] for source_id, _ in rows
                if len(set(terms) & set(tokens(self.lookup[source_id]["text"]))) >= minimum]

    def close(self):
        self.connection.close()


def hash_vector(text, dimension, salt):
    vector = [0.0] * dimension
    for token in tokens(text):
        digest = hashlib.sha256((salt + "\0" + token).encode()).digest()
        index = int.from_bytes(digest[:4], "big") % dimension
        vector[index] += 1.0 if digest[4] & 1 else -1.0
    norm = math.sqrt(sum(value * value for value in vector))
    return vector if norm == 0 else [value / norm for value in vector]


def dense_scores(documents, query, dimension, salt):
    query_vector = hash_vector(query, dimension, salt)
    scored = []
    for doc in documents:
        vector = hash_vector(doc["text"], dimension, salt)
        score = sum(left * right for left, right in zip(query_vector, vector))
        minimum = min(2, len(set(tokens(query))))
        overlap = len(set(tokens(query)) & set(tokens(doc["text"])))
        if score >= 0.18 and overlap >= minimum:
            scored.append((score, doc))
    return scored


def dense_search(documents, query, dimension, salt):
    return dedupe_ranked(dense_scores(documents, query, dimension, salt))


def rerank_search(documents, query):
    candidates = dedupe_ranked(dense_scores(documents, query, 128, "q4"), 20)
    query_terms = set(tokens(query))
    scored = []
    for doc in candidates:
        overlap = len(query_terms & set(tokens(doc["text"])))
        scored.append((overlap, doc))
    return dedupe_ranked(scored)


def rrf(*rankings):
    scores, docs = defaultdict(float), {}
    for ranking in rankings:
        for rank, doc in enumerate(ranking, 1):
            scores[doc["source_id"]] += 1 / (60 + rank)
            docs[doc["source_id"]] = doc
    return dedupe_ranked([(score, docs[source_id]) for source_id, score in scores.items()])


def config(variant_id):
    value = {"variant_id": variant_id, "retrieval_kind": VARIANT_KINDS[variant_id],
             "evaluation_mode": "developer_calibration"}
    if variant_id in {"Q0", "Q1", "Q2", "Q6", "Q10", "Q11", "Q12"}:
        names = {"Q0": "python_sql_like_calibration", "Q1": "sqlite_fts5_postgres_fts_emulation",
                 "Q2": "sqlite_fts5_native"}
        value["lexical"] = {"name": names.get(variant_id, "sqlite_fts5_native"),
                            "parameters": {"tokenizer": "unicode61", "scope": "acl_prefiltered"}}
    if variant_id not in {"Q0", "Q1", "Q2"}:
        dimension = 64 if variant_id == "Q3" else 128
        model = f"offline_signed_hash_encoder_{dimension}d_calibration"
        value["embedding"] = {"model_id": model, "model_sha256": HEX(model), "dimension": dimension,
                              "normalization": "l2", "distance_metric": "cosine"}
        value["index"] = {"name": "stdlib_bruteforce_calibration",
                          "parameters": {"candidate_limit": 10, "minimum_score": 0.18}}
    if variant_id == "Q5":
        model = "offline_token_overlap_reranker_calibration"
        value["reranker"] = {"model_id": model, "model_sha256": HEX(model),
                             "parameters": {"candidate_limit": 20}}
    if variant_id in {"Q6", "Q10", "Q11", "Q12"}:
        value["fusion"] = {"name": "reciprocal_rank_fusion", "parameters": {"k": 60}}
    if variant_id in DERIVED_VARIANTS:
        kinds = {"Q7": "conversation_summary", "Q8": "segment_summary", "Q9": "distilled_memory",
                 "Q10": "conversation_summary", "Q11": "segment_summary", "Q12": "distilled_memory"}
        generator = "offline_extract_first_last_calibration"
        value["derivation"] = {"artifact_kind": kinds[variant_id], "model_id": generator,
                               "model_sha256": HEX(generator), "prompt_sha256": HEX("no-llm-template-v1"),
                               "renderer_sha256": HEX("first-last-renderer-v1")}
    return value


def run(output_dir):
    output_dir.mkdir(parents=True, exist_ok=False)
    records, fixture = build()
    visible_records = [record for record in records if visible(record)]
    raw = [raw_document(record) for record in visible_records]
    conversation, segments, distilled = derived_documents(visible_records)
    all_visible = raw + conversation + segments + distilled
    allow_all_visible_sources(fixture, all_visible)
    corpus_path = output_dir / "corpus.jsonl"
    corpus_path.write_bytes(jsonl_bytes(records))
    cases_path = output_dir / "cases.json"
    cases_path.write_text(json.dumps(fixture, ensure_ascii=False, sort_keys=True, indent=2) + "\n")
    derived = {"conversation_summary": conversation, "segment_summary": segments,
               "distilled_memory": distilled}
    artifact_paths = {}
    for kind, docs in derived.items():
        path = output_dir / f"{kind}.jsonl"
        path.write_bytes(jsonl_bytes(docs))
        artifact_paths[kind] = path

    variants, performance = {}, {}
    for number in range(13):
        variant_id = f"Q{number}"
        cfg = config(variant_id)
        cfg_digest = canonical_sha256(cfg)
        artifact_entries = []
        if variant_id in DERIVED_VARIANTS:
            kind = cfg["derivation"]["artifact_kind"]
            path = artifact_paths[kind]
            artifact_entries = [{"role": kind, "path": path.name, "sha256": sha256(path)}]
        artifact_identity = [{"role": item["role"], "sha256": item["sha256"]}
                             for item in artifact_entries]
        build_started = time.perf_counter_ns()
        lexical_index = FTSIndex(raw) if variant_id in {"Q1", "Q2", "Q6", "Q10", "Q11", "Q12"} else None
        build_ns = time.perf_counter_ns() - build_started
        started = time.perf_counter_ns()
        query_rows = []
        try:
            for case_value in fixture["cases"]:
                query = case_value["query"]
                if variant_id == "Q0":
                    found = like_search(raw, query)
                elif variant_id in {"Q1", "Q2"}:
                    found = lexical_index.search(query)
                elif variant_id in {"Q3", "Q4"}:
                    found = dense_search(raw, query, cfg["embedding"]["dimension"], variant_id.casefold())
                elif variant_id == "Q5":
                    found = rerank_search(raw, query)
                elif variant_id == "Q6":
                    found = rrf(lexical_index.search(query), dense_search(raw, query, 128, "q4"))
                else:
                    kind = cfg["derivation"]["artifact_kind"]
                    docs = derived[kind]
                    semantic = dense_search(docs, query, 128, "q4")
                    found = rrf(lexical_index.search(query), semantic) if lexical_index else semantic
                query_rows.append({"case_id": case_value["id"], "latency_ms": 0,
                                   "hits": [hit(doc) for doc in found]})
        finally:
            if lexical_index:
                lexical_index.close()
        elapsed = time.perf_counter_ns() - started
        performance[variant_id] = {"queries": len(query_rows), "index_build_ns": build_ns,
                                   "total_ns": elapsed,
                                   "mean_us": elapsed / len(query_rows) / 1000}
        result = {"schema_version": 1, "variant_id": variant_id, "corpus_sha256": sha256(corpus_path),
                  "config_sha256": cfg_digest, "artifacts_sha256": canonical_sha256(artifact_identity),
                  "latency_note": "Scored latency is zero for deterministic quality files; measured timing is in performance.json.",
                  "queries": query_rows}
        result_path = output_dir / f"{variant_id.lower()}-results.json"
        result_path.write_text(json.dumps(result, ensure_ascii=False, sort_keys=True, indent=2) + "\n")
        entry = {"path": result_path.name, "sha256": sha256(result_path),
                 "config": cfg, "config_sha256": cfg_digest}
        if artifact_entries:
            entry["artifacts"] = artifact_entries
        variants[variant_id] = entry
    manifest = {"schema_version": 1, "cases_path": cases_path.name, "cases_sha256": sha256(cases_path),
                "corpus_path": corpus_path.name, "corpus_sha256": sha256(corpus_path), "variants": variants}
    manifest_path = output_dir / "manifest.json"
    manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, sort_keys=True, indent=2) + "\n")
    report = build_report(manifest_path)
    (output_dir / "report.json").write_text(json.dumps(report, ensure_ascii=False, sort_keys=True, indent=2) + "\n")
    performance["limitations"] = ["Developer calibration, not sealed production evidence",
                                  "Q1 is explicitly a SQLite FTS5 PostgreSQL-FTS emulation",
                                  "Q3-Q12 use named offline surrogate models; no external model calls"]
    (output_dir / "performance.json").write_text(json.dumps(performance, indent=2, sort_keys=True) + "\n")
    total = sum(path.stat().st_size for path in output_dir.iterdir() if path.is_file())
    if total > 8 * 1024 * 1024:
        raise RuntimeError(f"developer calibration exceeded 8 MiB: {total}")
    return report, total


def self_check():
    records, fixture = build()
    multi_case = next(case for case in fixture["cases"] if case["id"] == "multiple")
    multi_docs = [raw_document(record) for record in records if record["session_id"] == "session-multiple"]

    def grade_multi(documents):
        row = {"case_id": "multiple", "latency_ms": 0, "hits": [hit(doc) for doc in documents]}
        return score_case(multi_case, row)["all_required_facts"]

    if grade_multi(multi_docs[:1]) or grade_multi(multi_docs[1:]):
        raise RuntimeError("one multi-fact message incorrectly satisfies both required facts")
    if not grade_multi(multi_docs):
        raise RuntimeError("both multi-fact messages did not satisfy both required facts")
    shared = message("scope-shared", "shared-only marker", ["fact-shared"], project=SHARED)
    private = message("scope-private", "private-only marker", ["fact-private"], project=PRIVATE)
    shared["session_id"] = private["session_id"] = "session-same-across-projects"
    scoped_summaries, _, _ = derived_documents([shared, private])
    if len(scoped_summaries) != 2 or len({doc["source_id"] for doc in scoped_summaries}) != 2:
        raise RuntimeError("same session id across projects was merged or reused a part id")
    by_project = {doc["project_id"]: doc for doc in scoped_summaries}
    if ("private-only" in by_project[SHARED]["text"] or
            "shared-only" in by_project[PRIVATE]["text"]):
        raise RuntimeError("derived summary leaked text across project scope")
    middle = message("middle", "placeholder", ["fact-middle-only"])
    middle["text"] = "a" * 300 + "b" * 300
    middle["fact_annotations"] = [{"fact_id": "fact-middle-only", "offset": 300}]
    middle_summary = derived_documents([middle])[0][0]
    if "fact-middle-only" in middle_summary["fact_ids"]:
        raise RuntimeError("derived artifact claimed a fact omitted from retained content")
    identical = "adversarial identical reranker text token"
    left_record = message("rerank-a", identical, [])
    right_record = message("rerank-z", identical, [])
    left_record["fact_ids"], right_record["fact_ids"] = ["oracle-many", "oracle-more"], []
    left_record["fact_annotations"] = [{"fact_id": "oracle-many", "offset": 0},
                                       {"fact_id": "oracle-more", "offset": 1}]
    right_record["fact_annotations"] = []
    first_order = [doc["source_id"] for doc in
                   rerank_search([raw_document(left_record), raw_document(right_record)], identical)]
    left_record["fact_ids"], right_record["fact_ids"] = [], ["oracle-many", "oracle-more"]
    left_record["fact_annotations"] = []
    right_record["fact_annotations"] = [{"fact_id": "oracle-many", "offset": 0},
                                         {"fact_id": "oracle-more", "offset": 1}]
    second_order = [doc["source_id"] for doc in
                    rerank_search([raw_document(left_record), raw_document(right_record)], identical)]
    if first_order != second_order:
        raise RuntimeError("Q5 ranking read fact_ids oracle labels")
    visible_raw = [raw_document(record) for record in records if visible(record)]
    fusion_case = next(case for case in fixture["cases"] if case["id"] == "fusion-overlap")
    fusion_target = fusion_case["relevant_message_ids"][0]
    fusion_index = FTSIndex(visible_raw)
    try:
        lexical = fusion_index.search(fusion_case["query"])
    finally:
        fusion_index.close()
    semantic = dense_search(visible_raw, fusion_case["query"], 128, "q4")
    if fusion_target not in {doc["message_id"] for doc in lexical} or fusion_target not in {
            doc["message_id"] for doc in semantic}:
        raise RuntimeError("fusion overlap source did not appear in both rankings")
    fused = rrf(lexical, semantic)
    if sum(doc["message_id"] == fusion_target for doc in fused) != 1:
        raise RuntimeError("fusion did not deduplicate the overlapping source")
    conversations, segments, distills = derived_documents([record for record in records if visible(record)])
    if any("[fact:" in doc["text"] for doc in visible_raw + conversations + segments + distills):
        raise RuntimeError("fact annotation leaked into ranker-visible text")
    with tempfile.TemporaryDirectory() as temporary:
        root = Path(temporary)
        first, first_bytes = run(root / "a")
        second, second_bytes = run(root / "b")
        if first["semantic_sha256"] != second["semantic_sha256"]:
            raise RuntimeError("semantic digest changed across deterministic rerun")
        first_manifest = json.loads((root / "a" / "manifest.json").read_text())
        second_manifest = json.loads((root / "b" / "manifest.json").read_text())
        if first_manifest != second_manifest:
            raise RuntimeError("manifest changed across deterministic rerun")
        if not all(all(row["hard_gates"].values()) for row in first["variants"]):
            raise RuntimeError("offline adapter failed a hard gate")
        if first_bytes > 8 * 1024 * 1024 or second_bytes > 8 * 1024 * 1024:
            raise RuntimeError("disk bound exceeded")
        print(f"PASS: Q0-Q12 offline calibration deterministic; semantic={first['semantic_sha256']}; bytes={first_bytes}.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", type=Path)
    parser.add_argument("--self-check", action="store_true")
    args = parser.parse_args()
    if args.self_check:
        self_check()
        return 0
    if not args.output_dir:
        parser.error("--output-dir is required")
    report, total = run(args.output_dir)
    print(json.dumps({"semantic_sha256": report["semantic_sha256"], "bytes": total,
                      "report": str(args.output_dir / "report.json")}, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
