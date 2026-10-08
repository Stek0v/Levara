#!/usr/bin/env python3
"""Deterministic selected-path imported-chat retrieval benchmark.

This is synthetic calibration evidence for Q0/Q1/Q2/Q4/Q6 only.  It uses the
configured real embedding endpoint and real PostgreSQL/SQLite lexical engines;
it deliberately does not claim a sealed Q0-Q12 result.
"""

from __future__ import annotations

import argparse
import copy
import hashlib
import importlib.metadata
import io
import json
import math
import os
from pathlib import Path
import platform
import re
import socket
import sqlite3
import statistics
import subprocess
import sys
import tempfile
import time
import urllib.request
import urllib.error

import numpy as np
import psycopg


SEED = 20261008
ACTOR = {"user_id": "alice", "tenant_id": "tenant-main"}
SHARES = {"project-shared": {"alice": "viewer"}}
TOP_K = 10
RRF_K = 60.0
DOCUMENT_MODEL = "embeddinggemma-300m"
QUERY_MODEL = "embeddinggemma-300m:query"
DIMENSION = 768
VARIANTS = ("Q0", "Q1", "Q2", "Q4", "Q6", "S1", "S2")
VARIANT_POLICY = {
    "Q0": {"role": "diagnostic_like", "quality_gate": False},
    "Q1": {"role": "diagnostic_postgres_fts", "quality_gate": False},
    "Q2": {"role": "portable_lexical_candidate", "quality_gate": True},
    "Q4": {"role": "dense_baseline", "quality_gate": True},
    "Q6": {"role": "selected_full_hybrid", "quality_gate": True},
    "S1": {"role": "compact_experiment_dense", "quality_gate": True},
    "S2": {"role": "compact_experiment_hybrid", "quality_gate": True},
}
QUALITY_GATES = {
    "recall_at_5_min": .95,
    "all_required_facts_at_10_min": .95,
    "session_recall_at_10_min": .95,
    "unknown_nonempty_rate_max": 0.0,
    "q6_noninferiority_margin": .03,
}
PART_MAX_BYTES = 2_000
DEFAULT_MODEL_SNAPSHOT = ("/Volumes/my_mac/levara-v3-cache-stash/huggingface/hub/"
                          "models--unsloth--embeddinggemma-300m/snapshots/"
                          "bfa3c846ac738e62aa61806ef9112d34acb1dc5a")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def canonical_bytes(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True,
                      separators=(",", ":")).encode("utf-8")


def digest(value):
    return hashlib.sha256(canonical_bytes(value)).hexdigest()


def file_digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def tree_content_digest(path):
    root = Path(path)
    require(root.is_dir(), f"model snapshot is unavailable: {root}")
    files = sorted(item for item in root.rglob("*") if item.is_file())
    require(files, "model snapshot has no files")
    hasher = hashlib.sha256()
    total = 0
    for item in files:
        relative = item.relative_to(root).as_posix().encode("utf-8")
        payload = item.read_bytes()
        total += len(payload)
        hasher.update(len(relative).to_bytes(8, "big"))
        hasher.update(relative)
        hasher.update(len(payload).to_bytes(8, "big"))
        hasher.update(payload)
    return {"tree_content_sha256": hasher.hexdigest(),
            "hash_algorithm": "sha256(length-framed-relative-path-and-content)",
            "file_count": len(files),
            "byte_count": total, "revision": root.name}


def endpoint_source_digest(repo_root):
    root = Path(repo_root)
    relative_paths = (
        "scripts/load-profiles/embed_bench/server.py",
        "scripts/load-profiles/embed_bench/backends.py",
        "scripts/load-profiles/embed_bench/recipes.py",
    )
    files = {relative: file_digest(root / relative) for relative in relative_paths}
    return {"files": files, "combined_sha256": digest(files)}


def bind_managed_listener(port):
    require(0 <= port <= 65535, "managed endpoint port is invalid")
    listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    try:
        listener.bind(("127.0.0.1", port))
        listener.listen(128)
        listener.set_inheritable(True)
        return listener
    except Exception:
        listener.close()
        raise


def managed_uvicorn_argv(python, inherited_fd):
    return [str(python), "-m", "uvicorn", "embed_bench.server:app",
            "--fd", str(inherited_fd)]


def cleanup_failed_child(process, log_handle):
    if process is not None:
        try:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=10)
        except Exception:
            try:
                process.kill()
                process.wait(timeout=10)
            except Exception:
                pass
    if log_handle is not None:
        try:
            log_handle.close()
        except Exception:
            pass


class ManagedEndpoint:
    def __init__(self, repo_root, model_snapshot, requested_port=0):
        self.repo_root = Path(repo_root)
        self.model_snapshot_path = Path(model_snapshot)
        self.requested_port = requested_port
        self.process = None
        self.log_handle = None
        self.metadata = {}
        self.endpoint = ""
        self._stopped = False

    def start(self):
        require(self.process is None, "managed endpoint already started")
        python = self.repo_root / "scripts/load-profiles/.venv-embed/bin/python3"
        require(python.is_file(), f"managed endpoint executable missing: {python}")
        resolved_python = python.resolve()
        executable_before = file_digest(resolved_python)
        source_before = endpoint_source_digest(self.repo_root)
        model_before = tree_content_digest(self.model_snapshot_path)
        listener = bind_managed_listener(self.requested_port)
        port = listener.getsockname()[1]
        require(1 <= port <= 65535, "managed endpoint port is invalid")
        argv = managed_uvicorn_argv(python, listener.fileno())
        env_allowlist = {
            "EMBED_BENCH_MODEL": "gemma-full",
            "HF_HOME": str(self.model_snapshot_path.parents[3]),
            "PYTHONUNBUFFERED": "1",
            "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
            "LANG": os.environ.get("LANG", "C.UTF-8"),
        }
        log_path = Path(tempfile.gettempdir()) / f"levara-managed-embed-{port}.log"
        try:
            self.log_handle = log_path.open("wb")
        except Exception:
            listener.close()
            raise
        started = time.time()
        try:
            inherited_fd = listener.fileno()
            try:
                self.process = subprocess.Popen(
                    argv, cwd=self.repo_root / "scripts/load-profiles",
                    env=env_allowlist, stdin=subprocess.DEVNULL,
                    stdout=self.log_handle, stderr=subprocess.STDOUT,
                    start_new_session=True, pass_fds=(inherited_fd,),
                )
            finally:
                listener.close()
            self.endpoint = f"http://127.0.0.1:{port}/v1/embeddings"
            health_url = f"http://127.0.0.1:{port}/health"
            health = None
            deadline = time.monotonic() + 240
            while time.monotonic() < deadline:
                if self.process.poll() is not None:
                    break
                try:
                    with urllib.request.urlopen(health_url, timeout=2) as response:
                        health = json.load(response)
                    if (health.get("model") == "embeddinggemma-300m-full-v1"
                            and health.get("dim") == DIMENSION
                            and health.get("backend") == "transformers"):
                        break
                except Exception:
                    pass
                time.sleep(.25)
            require(health is not None and health.get("model") == "embeddinggemma-300m-full-v1"
                    and health.get("dim") == DIMENSION and health.get("backend") == "transformers",
                    "managed endpoint failed pinned health validation")
            self.metadata = {
                "managed": True, "pid": self.process.pid, "started_at_unix": started,
                "ready_at_unix": time.time(), "port": port, "health": health,
                "argv": argv,
                "listener_binding": {"mechanism": "parent_bound_inherited_fd",
                                     "loopback": "127.0.0.1", "port": port,
                                     "inherited_fd": inherited_fd,
                                     "passed_to_pid": self.process.pid},
                "environment_allowlist": {key: env_allowlist[key] for key in
                                          ("EMBED_BENCH_MODEL", "HF_HOME", "PYTHONUNBUFFERED")},
                "child_executable": str(python),
                "child_executable_resolved": str(resolved_python),
                "child_executable_sha256_before": executable_before,
                "source_before": source_before, "model_before": model_before,
                "log_path": str(log_path),
            }
            return self
        except BaseException:
            cleanup_failed_child(self.process, self.log_handle)
            raise

    def stop_and_verify(self):
        if self._stopped:
            return self.metadata
        self._stopped = True
        if self.process is not None and self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=10)
        if self.log_handle is not None:
            self.log_handle.close()
        if not self.metadata:
            return self.metadata
        source_after = endpoint_source_digest(self.repo_root)
        model_after = tree_content_digest(self.model_snapshot_path)
        executable_after = file_digest(self.metadata["child_executable_resolved"])
        source_unchanged = self.metadata["source_before"] == source_after
        model_unchanged = self.metadata["model_before"] == model_after
        executable_unchanged = (self.metadata["child_executable_sha256_before"]
                                == executable_after)
        require(source_unchanged, "managed endpoint source hash changed during run")
        require(model_unchanged, "managed endpoint model tree hash changed during run")
        require(executable_unchanged, "managed endpoint executable hash changed during run")
        log_path = Path(self.metadata["log_path"])
        self.metadata.update({
            "stopped_at_unix": time.time(), "exit_code": self.process.returncode,
            "process_cleanup_verified": self.process.poll() is not None,
            "source_after": source_after, "model_after": model_after,
            "child_executable_sha256_after": executable_after,
            "source_unchanged": source_unchanged, "model_unchanged": model_unchanged,
            "child_executable_unchanged": executable_unchanged,
            "log_sha256": file_digest(log_path), "log_bytes": log_path.stat().st_size,
            "process_identity_verified": (source_unchanged and model_unchanged
                                          and executable_unchanged
                                          and self.process.poll() is not None),
        })
        return self.metadata


def percentile(values, fraction):
    require(values, "percentile requires at least one value")
    ordered = sorted(values)
    return ordered[max(0, math.ceil(len(ordered) * fraction) - 1)]


def authorized(record):
    if record["tenant_id"] != ACTOR["tenant_id"] or not record["owner_id"]:
        return False
    if record["owner_id"] == ACTOR["user_id"]:
        return True
    role = SHARES.get(record["project_id"], {}).get(ACTOR["user_id"], "")
    return role in {"viewer", "editor", "admin"}


def build_fixture():
    records, cases = [], []

    def add_case(category, query, texts, facts=None, owner=None, project=None,
                 stale_text=None):
        case_no = len(cases)
        case_id = f"c{case_no:03d}"
        session_id = f"session-{case_id}"
        owner = owner or ("bob" if case_no % 5 == 0 else "alice")
        project = project or ("project-shared" if owner == "bob" else "project-private")
        fact_specs = facts or [[(f"fact-{case_id}", query)] for _ in texts]
        relevant = []
        required = []
        for index, text in enumerate(texts):
            message_id = f"message-{case_id}-{index}"
            relevant.append(message_id)
            doc_facts = []
            for fact_id, marker in fact_specs[index]:
                require(marker in text, f"{case_id}: fact marker absent from source text")
                doc_facts.append({"id": fact_id, "marker": marker})
                required.append(fact_id)
            records.append({
                "message_id": message_id, "session_id": session_id,
                "owner_id": owner, "tenant_id": "tenant-main",
                "project_id": project, "active": True, "stale": False,
                "content": text, "facts": doc_facts,
            })
        if stale_text is not None:
            records.append({
                "message_id": f"message-{case_id}-stale", "session_id": session_id,
                "owner_id": owner, "tenant_id": "tenant-main",
                "project_id": project, "active": False, "stale": True,
                "content": stale_text, "facts": [],
            })
        cases.append({
            "id": case_id, "category": category, "query": query,
            "kind": "known", "relevant_message_ids": relevant,
            "relevant_session_ids": [session_id],
            "required_fact_ids": sorted(set(required)),
        })
        return case_id

    for i in range(12):
        token = f"CASEID-{i:04d}-OMEGA"
        add_case("identifier", token, [f"Deployment identifier is {token}."])
    for i in range(12):
        token = f"ERR-{41000+i}-WIDGET"
        add_case("error", token, [f"The observed service error is {token}."])
    for i in range(12):
        token = f"v{7+i}.3.{100+i}"
        add_case("version", token, [f"The pinned component version is {token}."])
    for i in range(12):
        token = f"203{1+i//9}-{1+i%9:02d}-{10+i:02d}"
        add_case("date", token, [f"The scheduled archival date is {token}."])
    for i in range(12):
        token = str(810000 + i * 37)
        add_case("number", token, [f"The verified capacity number is {token} records."])
    for i in range(12):
        token = f"ResolveWidgetCase{i:02d}"
        add_case("code", token, [f"Call the code symbol {token} after validation."])
    for i in range(10):
        marker = f"ru-node-{i:02d}"
        query = f"Где хранится резервная копия {marker}?"
        text = f"Для {marker} резервная копия хранится в северном архиве."
        add_case("ru", query, [text], [[(f"fact-ru-{i}", marker)]])
    for i in range(10):
        marker = f"en-node-{i:02d}"
        query = f"Where is the backup for {marker} stored?"
        text = f"The backup for {marker} is stored in the western archive."
        add_case("en", query, [text], [[(f"fact-en-{i}", marker)]])
    for i in range(10):
        marker = f"bridge-{i:02d}"
        query = f"Where is the backup for {marker}?"
        text = f"Резервная копия для {marker} находится в восточном архиве."
        add_case("cross_language", query, [text], [[(f"fact-cross-{i}", marker)]])
    for i in range(8):
        left, right = f"pair-{i:02d}-alpha", f"pair-{i:02d}-beta"
        query = f"Find both {left} and {right} settings"
        texts = [f"First setting marker is {left}.", f"Second setting marker is {right}."]
        facts = [[(f"fact-pair-{i}-a", left)], [(f"fact-pair-{i}-b", right)]]
        add_case("cross_message", query, texts, facts)
    for i in range(6):
        marker, current, old = f"corrected-{i:02d}", str(8800+i), str(7700+i)
        query = f"current port for {marker}"
        text = f"The current port for {marker} is {current}."
        stale = f"The old port for {marker} was {old}."
        add_case("corrected", query, [text], [[(f"fact-current-{i}", current)]],
                 stale_text=stale)
    for i in range(6):
        marker = f"negative-flag-{i:02d}"
        query = f"Is {marker} enabled?"
        text = f"{marker} is disabled; it is not enabled."
        add_case("negation", query, [text], [[(f"fact-negation-{i}", "disabled")]])
    for i in range(4):
        marker = f"long-tail-{i:02d}-needle"
        query = marker
        padding = (f"synthetic padding for long source {i} " * 7000)
        text = padding + f" final retained evidence {marker}."
        require(len(text.encode("utf-8")) > 200 * 1024, "long source is too small")
        add_case("long_tail", query, [text], [[(f"fact-long-{i}", marker)]])
    for i in range(6):
        cases.append({
            "id": f"c{len(cases):03d}", "category": "unknown",
            "query": f"unseen-quasar-{i:02d}-no-such-evidence", "kind": "unknown",
            "relevant_message_ids": [], "relevant_session_ids": [],
            "required_fact_ids": [],
        })

    # Same-text controls exercise private, revoked and foreign isolation.  They
    # remain in the frozen source corpus but never leave the process for
    # embedding/indexing because authorization runs first.
    for case in cases[:12]:
        original = next(r for r in records if r["message_id"] in case["relevant_message_ids"])
        for suffix, owner, tenant, project in (
            ("private", "mallory", "tenant-main", "project-private-mallory"),
            ("revoked", "bob", "tenant-main", "project-revoked"),
            ("foreign", "eve", "tenant-foreign", "project-shared"),
        ):
            clone = dict(original)
            clone.update({
                "message_id": f"{original['message_id']}-{suffix}",
                "session_id": f"{original['session_id']}-{suffix}",
                "owner_id": owner, "tenant_id": tenant, "project_id": project,
                "facts": [],
            })
            records.append(clone)

    require(len(cases) >= 120, "fixture must contain at least 120 cases")
    require(len({c["id"] for c in cases}) == len(cases), "duplicate case id")
    require(len({r["message_id"] for r in records}) == len(records), "duplicate message id")
    return records, cases


def build_calibration_fixture():
    records, cases = [], []
    for i in range(12):
        marker = f"calibration-beacon-{i:02d}"
        if i % 4 == 0:
            query = marker
            content = f"Pinned calibration identifier {marker}."
        elif i % 4 == 1:
            query = f"Where is the backup for {marker}?"
            content = f"The backup for {marker} is stored in the calibration vault."
        elif i % 4 == 2:
            query = f"Где резервная копия {marker}?"
            content = f"Резервная копия {marker} находится в калибровочном архиве."
        else:
            query = f"Where is {marker} stored?"
            content = f"Объект {marker} хранится в калибровочном архиве."
        message = f"calibration-message-{i:02d}"
        session = f"calibration-session-{i:02d}"
        records.append({"message_id": message, "session_id": session, "owner_id": "alice",
                        "tenant_id": "tenant-main", "project_id": "project-private",
                        "active": True, "stale": False, "content": content,
                        "facts": [{"id": f"calibration-fact-{i:02d}", "marker": marker}]})
        cases.append({"id": f"cal-known-{i:02d}", "kind": "known", "query": query,
                      "relevant_message_ids": [message]})
    for i in range(12):
        cases.append({"id": f"cal-unknown-{i:02d}", "kind": "unknown",
                      "query": f"unseen-quasar-{100+i:03d}-no-such-evidence",
                      "relevant_message_ids": []})
    return records, cases


def build_validation_fixture():
    records, cases = [], []

    def add(index, category, query, content, marker, stale_text=None):
        case_id = f"validation-{index:02d}"
        message_id = f"validation-message-{index:02d}"
        session_id = f"validation-session-{index:02d}"
        records.append({"message_id": message_id, "session_id": session_id,
                        "owner_id": "alice" if index % 3 else "bob",
                        "tenant_id": "tenant-main",
                        "project_id": "project-private" if index % 3 else "project-shared",
                        "active": True, "stale": False, "content": content,
                        "facts": [{"id": f"validation-fact-{index:02d}", "marker": marker}]})
        if stale_text:
            stale = dict(records[-1])
            stale.update({"message_id": message_id + "-stale", "active": False,
                          "stale": True, "content": stale_text, "facts": []})
            records.append(stale)
        cases.append({"id": case_id, "category": category, "query": query, "kind": "known",
                      "relevant_message_ids": [message_id],
                      "relevant_session_ids": [session_id],
                      "required_fact_ids": [f"validation-fact-{index:02d}"]})

    for i in range(4):
        marker = f"FINALCHECK-{i:02d}-SIGMA"
        add(i, "validation_identifier", f"Locate {marker}",
            f"The final validation identifier is {marker}.", marker)
    for i in range(4, 8):
        marker = f"final-ru-node-{i:02d}"
        add(i, "validation_ru", f"Найди архив для узла {marker}",
            f"Узел {marker} сохранён в финальном янтарном архиве.", marker)
    for i in range(8, 12):
        marker = f"final-bridge-{i:02d}"
        add(i, "validation_cross_language", f"Which archive contains {marker}?",
            f"Мост {marker} находится в финальном кобальтовом архиве.", marker)
    for i in range(12, 15):
        marker = f"FinalizeWidgetPath{i:02d}"
        add(i, "validation_code", f"Locate implementation symbol {marker}",
            f"Implementation dispatch uses the symbol {marker}.", marker)
    for i in range(15, 17):
        marker, port = f"final-corrected-{i:02d}", str(9900 + i)
        add(i, "validation_corrected", f"Which port is current for {marker}?",
            f"The final current port for {marker} is {port}.", port,
            f"An obsolete note assigned {marker} to port {8800+i}.")
    marker = "final-negative-switch-17"
    add(17, "validation_negation", f"Is {marker} active?",
        f"The switch {marker} is inactive and must not be activated.", marker)
    for i in range(6):
        cases.append({"id": f"validation-unknown-{i:02d}",
                      "category": "validation_unknown",
                      "query": f"missing-comet-final-{i:02d}-without-record",
                      "kind": "unknown", "relevant_message_ids": [],
                      "relevant_session_ids": [], "required_fact_ids": []})
    require(len(cases) == 24, "validation subset must contain exactly 24 cases")
    return records, cases


def split_utf8(text, max_bytes=PART_MAX_BYTES):
    raw = text.encode("utf-8")
    parts = []
    while raw:
        end = min(max_bytes, len(raw))
        while end > 0:
            try:
                part = raw[:end].decode("utf-8")
                break
            except UnicodeDecodeError:
                end -= 1
        require(end > 0, "unable to split UTF-8 text")
        parts.append(part)
        raw = raw[end:]
    return parts or [""]


def prepare_documents(records):
    documents = []
    for record in records:
        if not record["active"] or record["stale"] or not authorized(record):
            continue
        parts = split_utf8(record["content"])
        for index, text in enumerate(parts):
            part_id = "" if len(parts) == 1 else f"part-{record['message_id']}-{index+1:04d}"
            fact_ids = [fact["id"] for fact in record["facts"] if fact["marker"] in text]
            documents.append({
                "doc_id": part_id or record["message_id"],
                "source_kind": "part" if part_id else "message",
                "source_id": part_id or record["message_id"],
                "message_id": record["message_id"], "session_id": record["session_id"],
                "owner_id": record["owner_id"], "tenant_id": record["tenant_id"],
                "project_id": record["project_id"], "content": text,
                "fact_ids": fact_ids, "active": True, "stale": False,
            })
    documents.sort(key=lambda item: item["doc_id"])
    require(len({d["doc_id"] for d in documents}) == len(documents), "duplicate document id")
    return documents


def prepare_compact_segments(records):
    compact_records = []
    for record in records:
        if not record["active"] or record["stale"] or not authorized(record):
            continue
        content = record["content"]
        if len(content) > 600:
            content = content[:300] + "\n[deterministic middle elision]\n" + content[-300:]
        compact = dict(record)
        compact["content"] = content
        compact_records.append(compact)
    documents = prepare_documents(compact_records)
    for document in documents:
        document["doc_id"] = "compact-" + document["doc_id"]
        document["source_kind"] = "part"
        document["source_id"] = document["doc_id"]
    require(len({d["doc_id"] for d in documents}) == len(documents),
            "duplicate compact segment id")
    return documents


def provenance(document):
    return {key: document[key] for key in
            ("source_kind", "source_id", "message_id", "session_id",
             "owner_id", "tenant_id", "project_id")}


def validate_fixture(records, cases, documents):
    categories = {case["category"] for case in cases}
    required = {"identifier", "error", "version", "date", "number", "code", "ru", "en",
                "cross_language", "cross_message", "corrected", "negation", "unknown", "long_tail"}
    require(required <= categories, f"missing categories: {sorted(required-categories)}")
    require(any(len(r["content"].encode("utf-8")) > 200 * 1024 for r in records),
            "missing >200 KiB synthetic source")
    require(any(r["stale"] for r in records), "missing stale control")
    require(any(not authorized(r) for r in records), "missing unauthorized control")
    allowed_messages = {d["message_id"] for d in documents}
    for case in cases:
        require(case["kind"] in {"known", "unknown"}, "invalid case kind")
        if case["kind"] == "known":
            require(set(case["relevant_message_ids"]) <= allowed_messages,
                    f"{case['id']}: relevant message is not authorized")
            require(case["required_fact_ids"], f"{case['id']}: known case lacks facts")
        else:
            require(not case["relevant_message_ids"] and not case["required_fact_ids"],
                    f"{case['id']}: unknown case has oracle labels")


class Embedder:
    def __init__(self, endpoint, timeout, request_delay_ms=0.0):
        self.endpoint = endpoint
        self.timeout = timeout
        self.returned_models = set()
        self.http_5xx_count = 0
        self.retry_count = 0
        self.adaptive_split_count = 0
        self.request_delay_seconds = request_delay_ms / 1000.0
        self.last_request_started = 0.0

    def embed(self, texts, model):
        require(texts and all(isinstance(t, str) for t in texts), "invalid embedding input")
        wait = self.request_delay_seconds - (time.perf_counter() - self.last_request_started)
        if wait > 0:
            time.sleep(wait)
        self.last_request_started = time.perf_counter()
        request = urllib.request.Request(
            self.endpoint,
            data=json.dumps({"model": model, "input": texts}).encode("utf-8"),
            headers={"content-type": "application/json"},
        )
        for attempt in range(3):
            try:
                with urllib.request.urlopen(request, timeout=self.timeout) as response:
                    payload = json.load(response)
                break
            except urllib.error.HTTPError as error:
                detail = error.read(512).decode("utf-8", errors="replace")
                if error.code >= 500:
                    self.http_5xx_count += 1
                if error.code < 500 or attempt == 2:
                    raise RuntimeError(f"embedding HTTP {error.code}: {detail}") from error
                self.retry_count += 1
                time.sleep(.1 * (attempt + 1))
        require(isinstance(payload, dict) and isinstance(payload.get("data"), list),
                "embedding response lacks data")
        require(len(payload["data"]) == len(texts), "embedding batch count mismatch")
        ordered = sorted(payload["data"], key=lambda item: item.get("index", -1))
        require([item.get("index") for item in ordered] == list(range(len(texts))),
                "embedding batch indices are missing or duplicated")
        vectors = np.asarray([item.get("embedding") for item in ordered], dtype=np.float32)
        require(vectors.shape == (len(texts), DIMENSION),
                f"embedding shape mismatch: {vectors.shape}")
        require(np.isfinite(vectors).all(), "embedding contains non-finite values")
        norms = np.linalg.norm(vectors, axis=1)
        require(np.all(norms > 0), "embedding contains zero-norm vector")
        returned = payload.get("model")
        if isinstance(returned, str) and returned:
            self.returned_models.add(returned)
        return vectors / norms[:, None]


def dedupe_hits(rows, documents_by_id, limit=TOP_K):
    hits, seen = [], set()
    for doc_id, score in rows:
        doc = documents_by_id[doc_id]
        if doc["message_id"] in seen:
            continue
        seen.add(doc["message_id"])
        hits.append({
            "doc_id": doc_id, "message_id": doc["message_id"],
            "session_id": doc["session_id"], "score": float(score),
            "fact_ids": doc["fact_ids"], "provenance": provenance(doc),
            "stale": doc["stale"], "active": doc["active"],
        })
        if len(hits) == limit:
            break
    return hits


def q0_search(query, documents, documents_by_id):
    needle = query.casefold()
    rows = [(d["doc_id"], 1.0) for d in documents if needle in d["content"].casefold()]
    return dedupe_hits(rows, documents_by_id)


def sqlite_setup(documents):
    connection = sqlite3.connect(":memory:")
    require(connection.execute("select sqlite_compileoption_used('ENABLE_FTS5')").fetchone()[0] == 1,
            "SQLite lacks FTS5")
    connection.execute("CREATE VIRTUAL TABLE selected_path_fts USING fts5(doc_id UNINDEXED, content, tokenize='unicode61')")
    connection.executemany("INSERT INTO selected_path_fts(doc_id,content) VALUES(?,?)",
                           [(d["doc_id"], d["content"]) for d in documents])
    return connection


def sqlite_query(connection, query, documents_by_id):
    tokens = re.findall(r"\w+", query.casefold(), flags=re.UNICODE)
    if not tokens:
        return []
    expression = " OR ".join('"' + token.replace('"', '""') + '"' for token in tokens)
    rows = connection.execute(
        "SELECT doc_id,-bm25(selected_path_fts) AS score FROM selected_path_fts "
        "WHERE selected_path_fts MATCH ? ORDER BY score DESC,doc_id LIMIT 100",
        (expression,),
    ).fetchall()
    return dedupe_hits(rows, documents_by_id)


def postgres_setup(dsn, documents):
    connection = psycopg.connect(dsn)
    connection.execute("BEGIN ISOLATION LEVEL REPEATABLE READ")
    connection.execute("CREATE TEMP TABLE selected_path_fts(doc_id text PRIMARY KEY, content text NOT NULL) ON COMMIT DROP")
    with connection.cursor() as cursor:
        cursor.executemany("INSERT INTO selected_path_fts(doc_id,content) VALUES(%s,%s)",
                           [(d["doc_id"], d["content"]) for d in documents])
    connection.execute("CREATE INDEX selected_path_fts_gin ON selected_path_fts USING gin(to_tsvector('simple',content))")
    return connection


def postgres_query(connection, query, documents_by_id):
    rows = connection.execute(
        """SELECT doc_id,ts_rank_cd(to_tsvector('simple',content),plainto_tsquery('simple',%s)) AS score
           FROM selected_path_fts
           WHERE to_tsvector('simple',content) @@ plainto_tsquery('simple',%s)
           ORDER BY score DESC,doc_id LIMIT 100""", (query, query)).fetchall()
    return dedupe_hits(rows, documents_by_id)


def dense_query(vector, matrix, documents, documents_by_id):
    scores = matrix @ vector
    order = np.lexsort((np.asarray([d["doc_id"] for d in documents]), -scores))
    rows = [(documents[int(i)]["doc_id"], float(scores[int(i)])) for i in order]
    return dedupe_hits(rows, documents_by_id)


def embed_document_batches(embedder, documents, batch_size, char_limit, model):
    batches, batch, batch_chars = [], [], 0
    for document in documents:
        size = len(document["content"])
        if batch and (len(batch) >= batch_size or batch_chars + size > char_limit):
            batches.append(batch)
            batch, batch_chars = [], 0
        batch.append(document)
        batch_chars += size
    if batch:
        batches.append(batch)

    def embed_adaptive(items):
        try:
            return embedder.embed([item["content"] for item in items], model)
        except RuntimeError:
            if len(items) == 1:
                raise RuntimeError(f"embedding failed for synthetic document {items[0]['doc_id']} "
                                   f"({len(items[0]['content'])} chars)")
            embedder.adaptive_split_count += 1
            middle = len(items) // 2
            return np.vstack((embed_adaptive(items[:middle]), embed_adaptive(items[middle:])))

    return np.vstack([embed_adaptive(items) for items in batches])


def rrf_hits(left, right, documents_by_id):
    fused = {}
    best_doc = {}
    for hits in (left, right):
        for rank, hit in enumerate(hits, 1):
            message = hit["message_id"]
            fused[message] = fused.get(message, 0.0) + 1.0 / (RRF_K + rank)
            if message not in best_doc or hit["score"] > best_doc[message][1]:
                best_doc[message] = (hit["doc_id"], hit["score"])
    rows = [(best_doc[mid][0], score) for mid, score in fused.items()]
    rows.sort(key=lambda item: (-item[1], item[0]))
    return dedupe_hits(rows, documents_by_id)


def gate_tokens(text):
    return set(re.findall(r"[\w]+(?:-[\w]+)*", text.casefold(), flags=re.UNICODE))


def document_frequencies(documents):
    frequencies = {}
    for document in documents:
        for token in gate_tokens(document["content"]):
            frequencies[token] = frequencies.get(token, 0) + 1
    return frequencies


def rare_overlap_confidence(query, hits, documents_by_id, frequencies):
    rare = {token for token in gate_tokens(query) if frequencies.get(token, 0) <= 1}
    if not rare:
        return 0.0
    best = 0
    for hit in hits:
        tokens = gate_tokens(documents_by_id[hit["doc_id"]]["content"])
        best = max(best, len(rare & tokens))
    return float(best)


def calibrate_gates(calibration_cases, embedder, matrix, documents, lookup,
                    compact_matrix, compact_documents, compact_lookup,
                    sqlite_db, frequencies, query_model):
    dense = {"known": [], "unknown": []}
    compact = {"known": [], "unknown": []}
    lexical = {"known": [], "unknown": []}
    for case in calibration_cases:
        vector = embedder.embed([case["query"]], query_model)[0]
        full_hits = dense_query(vector, matrix, documents, lookup)
        compact_hits = dense_query(vector, compact_matrix, compact_documents, compact_lookup)
        lexical_hits = sqlite_query(sqlite_db, case["query"], lookup)
        require(full_hits and compact_hits, "calibration dense search returned no candidates")
        dense[case["kind"]].append(full_hits[0]["score"])
        compact[case["kind"]].append(compact_hits[0]["score"])
        lexical[case["kind"]].append(
            rare_overlap_confidence(case["query"], lexical_hits, lookup, frequencies))
    dense_threshold = max(dense["unknown"]) + .04
    compact_threshold = max(compact["unknown"]) + .04
    lexical_threshold = (max(lexical["unknown"]) + min(lexical["known"])) / 2.0
    require(min(lexical["known"]) > max(lexical["unknown"]),
            "lexical calibration classes do not separate")
    require(statistics.fmean(score >= dense_threshold for score in dense["known"]) >= .90,
            "dense calibration threshold rejects more than 10% known cases")
    require(statistics.fmean(score >= compact_threshold for score in compact["known"]) >= .90,
            "compact calibration threshold rejects more than 10% known cases")
    return {
        "method": "separate synthetic calibration subset; max unknown + frozen 0.04 dense margin; rare-token overlap midpoint",
        "margin_frozen_after_development_observation": True,
        "case_count": len(calibration_cases), "dense_cosine": dense_threshold,
        "compact_cosine": compact_threshold, "lexical_rare_overlap": lexical_threshold,
        "calibration_cases_sha256": digest(calibration_cases),
    }


def dcg(relevances):
    return sum(rel / math.log2(index + 2) for index, rel in enumerate(relevances))


def score_variant(cases, results):
    recalls = {k: [] for k in (1, 3, 5, 10)}
    reciprocal, ndcgs, all_facts, session_recall = [], [], [], []
    unauthorized = stale = duplicates = unknown_nonempty = 0
    for case in cases:
        hits = results[case["id"]]
        messages = [hit["message_id"] for hit in hits]
        if len(messages) != len(set(messages)):
            duplicates += 1
        for hit in hits:
            p = hit["provenance"]
            shadow = {**p, "active": hit["active"], "stale": hit["stale"]}
            if not authorized(shadow):
                unauthorized += 1
            if hit["stale"] or not hit["active"]:
                stale += 1
        if case["kind"] == "unknown":
            unknown_nonempty += int(bool(hits))
            continue
        relevant = set(case["relevant_message_ids"])
        for k in recalls:
            recalls[k].append(int(bool(relevant & set(messages[:k]))))
        ranks = [index + 1 for index, message in enumerate(messages[:10]) if message in relevant]
        reciprocal.append(1.0 / min(ranks) if ranks else 0.0)
        rels = [1 if message in relevant else 0 for message in messages[:10]]
        ideal = [1] * min(len(relevant), 10)
        ndcgs.append(dcg(rels) / dcg(ideal) if ideal else 0.0)
        found_facts = {fact for hit in hits[:10] for fact in hit["fact_ids"]}
        all_facts.append(int(set(case["required_fact_ids"]) <= found_facts))
        session_recall.append(int(bool(set(case["relevant_session_ids"]) &
                                       {hit["session_id"] for hit in hits[:10]})))
    mean = lambda values: statistics.fmean(values) if values else None
    return {
        **{f"recall_at_{k}": mean(values) for k, values in recalls.items()},
        "mrr_at_10": mean(reciprocal), "ndcg_at_10": mean(ndcgs),
        "all_required_facts_at_10": mean(all_facts),
        "session_recall_at_10": mean(session_recall),
        "unauthorized_hit_count": unauthorized, "stale_hit_count": stale,
        "duplicate_result_case_count": duplicates,
        "unknown_nonempty_rate": unknown_nonempty / sum(c["kind"] == "unknown" for c in cases),
    }


def latency_stats(values):
    return {"count": len(values), "p50_ms": percentile(values, .50),
            "p95_ms": percentile(values, .95), "p99_ms": percentile(values, .99),
            "mean_ms": statistics.fmean(values)}


def checked_metric(metrics, field, minimum=0.0, maximum=1.0, integer=False):
    if not isinstance(metrics, dict) or field not in metrics:
        return None, f"{field}:missing"
    value = metrics[field]
    if type(value) not in (int, float) or not math.isfinite(float(value)):
        return None, f"{field}:invalid"
    if integer and (type(value) is not int or value < minimum):
        return None, f"{field}:invalid"
    if value < minimum or maximum is not None and value > maximum:
        return None, f"{field}:out_of_range"
    return value, None


def evaluate_variant_gates(validation_metrics):
    rows = {}
    for variant in VARIANTS:
        metrics = validation_metrics.get(variant) if isinstance(validation_metrics, dict) else None
        safety_failures = []
        for field in ("unauthorized_hit_count", "stale_hit_count",
                      "duplicate_result_case_count"):
            value, error = checked_metric(metrics, field, minimum=0, maximum=None, integer=True)
            if error:
                safety_failures.append(error)
            elif value != 0:
                safety_failures.append(field)
        quality_failures = []
        if VARIANT_POLICY[variant]["quality_gate"]:
            for field, minimum in (("recall_at_5", QUALITY_GATES["recall_at_5_min"]),
                                   ("all_required_facts_at_10",
                                    QUALITY_GATES["all_required_facts_at_10_min"]),
                                   ("session_recall_at_10",
                                    QUALITY_GATES["session_recall_at_10_min"])):
                value, error = checked_metric(metrics, field)
                if error:
                    quality_failures.append(error)
                elif value < minimum:
                    quality_failures.append(field)
            unknown, error = checked_metric(metrics, "unknown_nonempty_rate")
            if error:
                quality_failures.append(error)
            elif unknown > QUALITY_GATES["unknown_nonempty_rate_max"]:
                quality_failures.append("unknown_nonempty_rate")
        rows[variant] = {
            **VARIANT_POLICY[variant],
            "safety_pass": not safety_failures,
            "safety_failures": safety_failures,
            "quality_pass": (None if not VARIANT_POLICY[variant]["quality_gate"]
                             else not quality_failures),
            "quality_failures": quality_failures,
        }
    q4 = validation_metrics.get("Q4") if isinstance(validation_metrics, dict) else None
    q6 = validation_metrics.get("Q6") if isinstance(validation_metrics, dict) else None
    margin = QUALITY_GATES["q6_noninferiority_margin"]
    noninferiority_failures = []
    for field in ("recall_at_5", "all_required_facts_at_10"):
        q4_value, q4_error = checked_metric(q4, field)
        q6_value, q6_error = checked_metric(q6, field)
        if q4_error or q6_error:
            noninferiority_failures.append(f"{field}:invalid")
        elif q6_value < q4_value - margin:
            noninferiority_failures.append(field)
    rows["Q6"]["noninferiority_to_Q4_pass"] = not noninferiority_failures
    rows["Q6"]["noninferiority_failures"] = noninferiority_failures
    hard_safety_pass = all(row["safety_pass"] for row in rows.values())
    selected_path_pass = (hard_safety_pass and rows["Q6"]["quality_pass"]
                          and rows["Q6"]["noninferiority_to_Q4_pass"])
    compact_candidate_pass = (hard_safety_pass and rows["S1"]["quality_pass"]
                              and rows["S2"]["quality_pass"])
    return {"frozen_policy": VARIANT_POLICY, "frozen_thresholds": QUALITY_GATES,
            "rows": rows, "hard_safety_pass": hard_safety_pass,
            "selected_path_pass": selected_path_pass,
            "compact_candidate_pass": compact_candidate_pass}


def bind_selected_path_to_identity(gate_evaluation, identity_required, identity_verified):
    retrieval_pass = bool(gate_evaluation.get("selected_path_pass"))
    identity_pass = bool(identity_verified) if identity_required else True
    result = copy.deepcopy(gate_evaluation)
    result["retrieval_selected_path_pass"] = retrieval_pass
    result["endpoint_identity_required"] = bool(identity_required)
    result["endpoint_identity_pass"] = identity_pass
    result["selected_path_pass"] = retrieval_pass and identity_pass
    return result


def self_check():
    records, cases = build_fixture()
    validation_records, validation_cases = build_validation_fixture()
    documents = prepare_documents(records)
    compact = prepare_compact_segments(records)
    validate_fixture(records, cases, documents)
    require(digest(build_fixture()[0]) == digest(records), "fixture is nondeterministic")
    require(not ({case["id"] for case in cases} & {case["id"] for case in validation_cases}),
            "development and validation case ids overlap")
    require(all(record["message_id"].startswith("validation-") for record in validation_records),
            "validation source identity is not isolated")
    lookup = {d["doc_id"]: d for d in documents}
    sql = sqlite_setup(documents)
    try:
        hit = sqlite_query(sql, cases[0]["query"], lookup)
        require(hit and hit[0]["message_id"] in cases[0]["relevant_message_ids"],
                "SQLite exact identifier self-check failed")
    finally:
        sql.close()
    require(all(authorized(d) and d["active"] and not d["stale"] for d in documents),
            "pre-egress authorization/stale filter failed")
    require(sum(len(d["content"]) for d in compact) < sum(len(d["content"]) for d in documents),
            "compact derivative did not reduce retained text")
    try:
        dedupe_hits([(documents[0]["doc_id"], 2), (documents[0]["doc_id"], 1)], lookup)
    except Exception as error:
        raise ValueError(f"dedupe self-check failed: {error}") from error
    require(len(dedupe_hits([(documents[0]["doc_id"], 2),
                             (documents[0]["doc_id"], 1)], lookup)) == 1,
            "duplicate result was not removed")

    class FakeResponse:
        def __init__(self, payload): self.payload = payload
        def __enter__(self): return self
        def __exit__(self, *_): return False
        def read(self): return canonical_bytes(self.payload)

    real_urlopen = urllib.request.urlopen
    try:
        urllib.request.urlopen = lambda *_args, **_kwargs: FakeResponse({"data": []})
        try:
            Embedder("http://offline.invalid", 1).embed(["x"], DOCUMENT_MODEL)
        except ValueError as error:
            require("count mismatch" in str(error), "wrong embedding-count error")
        else:
            raise ValueError("embedding count mismatch was accepted")
        urllib.request.urlopen = lambda *_args, **_kwargs: FakeResponse({
            "data": [{"index": 0, "embedding": [0.0] * DIMENSION}]})
        try:
            Embedder("http://offline.invalid", 1).embed(["x"], DOCUMENT_MODEL)
        except ValueError as error:
            require("zero-norm" in str(error), "wrong zero-norm error")
        else:
            raise ValueError("zero-norm embedding was accepted")
        failures = {"count": 0}
        def fail_urlopen(*_args, **_kwargs):
            failures["count"] += 1
            raise urllib.error.HTTPError("http://offline.invalid", 503, "down", {}, io.BytesIO(b"down"))
        urllib.request.urlopen = fail_urlopen
        try:
            Embedder("http://offline.invalid", 1).embed(["x"], DOCUMENT_MODEL)
        except RuntimeError as error:
            require("HTTP 503" in str(error) and failures["count"] == 3,
                    "endpoint failure retry/error contract failed")
        else:
            raise ValueError("endpoint failure was accepted")
    finally:
        urllib.request.urlopen = real_urlopen
    passing = {variant: {"recall_at_5": 1.0, "all_required_facts_at_10": 1.0,
                         "session_recall_at_10": 1.0, "unknown_nonempty_rate": 0.0,
                         "unauthorized_hit_count": 0, "stale_hit_count": 0,
                         "duplicate_result_case_count": 0} for variant in VARIANTS}
    gates = evaluate_variant_gates(passing)
    require(gates["selected_path_pass"] and gates["compact_candidate_pass"]
            and gates["hard_safety_pass"], "passing gate fixture was rejected")
    failing = json.loads(json.dumps(passing))
    failing["S1"]["unknown_nonempty_rate"] = 1.0
    gates = evaluate_variant_gates(failing)
    require(gates["selected_path_pass"] and not gates["compact_candidate_pass"],
            "compact failure incorrectly failed selected path")
    failing["Q6"]["recall_at_5"] = .90
    gates = evaluate_variant_gates(failing)
    require(not gates["selected_path_pass"], "known-query regression passed selected path")
    failing = json.loads(json.dumps(passing))
    failing["Q0"]["unauthorized_hit_count"] = 1
    gates = evaluate_variant_gates(failing)
    require(not gates["hard_safety_pass"] and not gates["selected_path_pass"],
            "safety violation passed overall gates")
    bound = bind_selected_path_to_identity(evaluate_variant_gates(passing), True, False)
    require(bound["retrieval_selected_path_pass"] and not bound["selected_path_pass"],
            "managed selected path passed without verified endpoint identity")
    bound = bind_selected_path_to_identity(evaluate_variant_gates(passing), True, True)
    require(bound["selected_path_pass"], "verified managed selected path was rejected")
    invalid_values = (float("nan"), float("inf"), float("-inf"), "1", None)
    safety_fields = ("unauthorized_hit_count", "stale_hit_count",
                     "duplicate_result_case_count")
    quality_fields = ("recall_at_5", "all_required_facts_at_10",
                      "session_recall_at_10", "unknown_nonempty_rate")
    for variant in VARIANTS:
        for field in safety_fields:
            for invalid in (*invalid_values, -1, 1.5, True):
                adversarial = copy.deepcopy(passing)
                adversarial[variant][field] = invalid
                gates = evaluate_variant_gates(adversarial)
                require(not gates["rows"][variant]["safety_pass"]
                        and not gates["hard_safety_pass"],
                        f"invalid safety metric passed: {variant}/{field}/{invalid!r}")
            adversarial = copy.deepcopy(passing)
            del adversarial[variant][field]
            gates = evaluate_variant_gates(adversarial)
            require(not gates["rows"][variant]["safety_pass"],
                    f"missing safety metric passed: {variant}/{field}")
    for variant in ("Q2", "Q4", "Q6", "S1", "S2"):
        for field in quality_fields:
            for invalid in (*invalid_values, -0.1, 1.1, True):
                adversarial = copy.deepcopy(passing)
                adversarial[variant][field] = invalid
                gates = evaluate_variant_gates(adversarial)
                require(gates["rows"][variant]["quality_pass"] is False,
                        f"invalid quality metric passed: {variant}/{field}/{invalid!r}")
            adversarial = copy.deepcopy(passing)
            del adversarial[variant][field]
            gates = evaluate_variant_gates(adversarial)
            require(gates["rows"][variant]["quality_pass"] is False,
                    f"missing quality metric passed: {variant}/{field}")
    listener = bind_managed_listener(0)
    occupied_port = listener.getsockname()[1]
    try:
        try:
            unexpected = bind_managed_listener(occupied_port)
        except OSError:
            pass
        else:
            unexpected.close()
            raise ValueError("occupied managed port was accepted")
        argv = managed_uvicorn_argv("/test/python", listener.fileno())
        require("--fd" in argv and "--port" not in argv,
                "managed endpoint is not bound through inherited listener")
    finally:
        listener.close()

    class FakeStartupChild:
        def __init__(self):
            self.returncode = None
            self.terminated = False
            self.killed = False
            self.wait_timeouts = []

        def poll(self):
            return self.returncode

        def terminate(self):
            self.terminated = True

        def wait(self, timeout):
            self.wait_timeouts.append(timeout)
            if not self.killed:
                raise subprocess.TimeoutExpired("fake-managed-endpoint", timeout)
            self.returncode = -9
            return self.returncode

        def kill(self):
            self.killed = True

    class FakeStartupLog:
        def __init__(self):
            self.closed = False

        def close(self):
            self.closed = True

    fake_child = FakeStartupChild()
    fake_log = FakeStartupLog()
    readiness_error = RuntimeError("fake readiness validation failed")
    try:
        try:
            raise readiness_error
        except BaseException:
            cleanup_failed_child(fake_child, fake_log)
            raise
    except RuntimeError as caught:
        require(caught is readiness_error,
                "managed startup cleanup replaced the readiness failure")
    require(fake_child.terminated and fake_child.killed and fake_child.poll() == -9,
            "managed readiness failure did not terminate and kill the child")
    require(fake_child.wait_timeouts == [15, 10],
            "managed readiness failure did not use bounded waits")
    require(fake_log.closed, "managed readiness failure did not close its log")
    print(json.dumps({"self_check": "ok", "cases": len(cases),
                      "source_records": len(records), "indexed_parts": len(documents),
                      "fixture_sha256": digest(records), "cases_sha256": digest(cases)},
                     sort_keys=True))


def run(args, managed_endpoint=None):
    harness_source_before = file_digest(__file__)
    model_before = (managed_endpoint.metadata["model_before"] if managed_endpoint is not None
                    else tree_content_digest(args.model_snapshot))
    records, cases = build_fixture()
    calibration_records, calibration_cases = build_calibration_fixture()
    validation_records, validation_cases = build_validation_fixture()
    records.extend(calibration_records)
    records.extend(validation_records)
    documents = prepare_documents(records)  # authorization happens before endpoint egress
    compact_documents = prepare_compact_segments(records)
    validate_fixture(records, cases, documents)
    lookup = {d["doc_id"]: d for d in documents}
    compact_lookup = {d["doc_id"]: d for d in compact_documents}
    unauthorized_ids = {r["message_id"] for r in records if not authorized(r)}
    require(not unauthorized_ids & {d["message_id"] for d in documents},
            "unauthorized source survived pre-egress filter")

    embedder = Embedder(args.embed_endpoint, args.timeout, args.request_delay_ms)
    embed_started = time.perf_counter()
    matrix = embed_document_batches(embedder, documents, args.batch_size,
                                    args.batch_char_limit, args.document_model)
    compact_matrix = embed_document_batches(embedder, compact_documents,
                                             args.batch_size, args.batch_char_limit,
                                             args.document_model)
    corpus_embed_ms = (time.perf_counter() - embed_started) * 1000.0
    require(matrix.shape == (len(documents), DIMENSION), "corpus matrix shape mismatch")
    require(compact_matrix.shape == (len(compact_documents), DIMENSION),
            "compact matrix shape mismatch")
    sqlite_db = sqlite_setup(documents)
    pg = postgres_setup(args.pg_dsn, documents)
    cleanup_verified = False
    results = {variant: {} for variant in VARIANTS}
    latencies = {variant: [] for variant in VARIANTS}
    overlap_count = 0
    try:
        pg_version = pg.execute("SHOW server_version").fetchone()[0]
        frequencies = document_frequencies(documents)
        thresholds = calibrate_gates(
            calibration_cases, embedder, matrix, documents, lookup,
            compact_matrix, compact_documents, compact_lookup,
            sqlite_db, frequencies, args.query_model)
        for case in cases + validation_cases:
            query = case["query"]
            start = time.perf_counter(); q0 = q0_search(query, documents, lookup)
            latencies["Q0"].append((time.perf_counter() - start) * 1000.0)
            start = time.perf_counter(); q1 = postgres_query(pg, query, lookup)
            latencies["Q1"].append((time.perf_counter() - start) * 1000.0)
            start = time.perf_counter(); q2 = sqlite_query(sqlite_db, query, lookup)
            if rare_overlap_confidence(query, q2, lookup, frequencies) < thresholds["lexical_rare_overlap"]:
                q2 = []
            latencies["Q2"].append((time.perf_counter() - start) * 1000.0)
            start = time.perf_counter()
            query_vector = embedder.embed([query], args.query_model)[0]
            query_embed_ms = (time.perf_counter() - start) * 1000.0
            start = time.perf_counter()
            q4 = dense_query(query_vector, matrix, documents, lookup)
            if not q4 or q4[0]["score"] < thresholds["dense_cosine"]:
                q4 = []
            q4_search_ms = (time.perf_counter() - start) * 1000.0
            start = time.perf_counter()
            s1 = dense_query(query_vector, compact_matrix, compact_documents, compact_lookup)
            if not s1 or s1[0]["score"] < thresholds["compact_cosine"]:
                s1 = []
            s1_search_ms = (time.perf_counter() - start) * 1000.0
            q4_ms = query_embed_ms + q4_search_ms
            s1_ms = query_embed_ms + s1_search_ms
            latencies["Q4"].append(q4_ms)
            start = time.perf_counter(); q6 = rrf_hits(q1, q4, lookup)
            fuse_ms = (time.perf_counter() - start) * 1000.0
            latencies["Q6"].append(latencies["Q1"][-1] + q4_ms + fuse_ms)
            start = time.perf_counter(); s2 = rrf_hits(q1, s1, {**lookup, **compact_lookup})
            compact_fuse_ms = (time.perf_counter() - start) * 1000.0
            latencies["S1"].append(s1_ms)
            latencies["S2"].append(latencies["Q1"][-1] + s1_ms + compact_fuse_ms)
            overlap_count += int(bool({h["message_id"] for h in q1} &
                                      {h["message_id"] for h in q4}))
            for variant, hits in zip(VARIANTS, (q0, q1, q2, q4, q6, s1, s2)):
                results[variant][case["id"]] = hits
    finally:
        sqlite_db.close()
        pg.rollback()
        cleanup_verified = pg.execute("SELECT to_regclass('pg_temp.selected_path_fts') IS NULL").fetchone()[0]
        pg.rollback()
        pg.close()
    require(cleanup_verified, "PostgreSQL temporary table survived rollback")
    require(overlap_count > 0, "fusion inputs never overlapped")

    development_metrics = {variant: score_variant(cases, results[variant]) for variant in VARIANTS}
    validation_metrics = {variant: score_variant(validation_cases, results[variant]) for variant in VARIANTS}
    gate_evaluation = evaluate_variant_gates(validation_metrics)
    if managed_endpoint is not None:
        managed_metadata = managed_endpoint.stop_and_verify()
        model_after = managed_metadata["model_after"]
    else:
        managed_metadata = None
        model_after = tree_content_digest(args.model_snapshot)
    require(model_before == model_after, "model tree hash changed during benchmark")
    harness_source_after = file_digest(__file__)
    require(harness_source_before == harness_source_after,
            "benchmark source hash changed during run")
    response_alias_verified = (embedder.returned_models == {"embeddinggemma-300m-full-v1"})
    endpoint_identity_verified = bool(
        managed_metadata and managed_metadata.get("process_identity_verified")
        and response_alias_verified)
    gate_evaluation = bind_selected_path_to_identity(
        gate_evaluation, managed_endpoint is not None, endpoint_identity_verified)

    config = {
        "evidence_kind": "template_generated_synthetic_selected_path",
        "seed": SEED, "variants": list(VARIANTS), "top_k": TOP_K,
        "variant_policy": VARIANT_POLICY, "quality_gates": QUALITY_GATES,
        "document_model_requested": args.document_model,
        "query_model_requested": args.query_model, "dimension": DIMENSION,
        "model_tree_content_sha256": (model_before["tree_content_sha256"]
                                      if endpoint_identity_verified else None),
        "normalization": "l2", "distance_metric": "cosine_exact",
        "postgres_fts": {"configuration": "simple", "query": "plainto_tsquery"},
        "sqlite_fts5": {"tokenizer": "unicode61", "query_join": "OR"},
        "fusion": {"name": "rrf", "k": RRF_K, "dedupe": "message_id"},
        "compact_derivative": {"name": "deterministic_head_tail_segment",
                               "head_chars": 300, "tail_chars": 300,
                               "semantic_summary": False},
        "abstention": thresholds,
        "authorization": "exact owner/tenant/project share before indexing and embedding",
        "part_max_bytes": PART_MAX_BYTES,
        "embedding_batch_size": args.batch_size,
        "embedding_batch_char_limit": args.batch_char_limit,
        "embedding_request_delay_ms": args.request_delay_ms,
    }
    case_manifest = [{k: c[k] for k in ("id", "category", "kind", "relevant_message_ids",
                                         "relevant_session_ids", "required_fact_ids")} for c in cases]
    validation_manifest = [{k: c[k] for k in ("id", "category", "kind",
                                              "relevant_message_ids", "relevant_session_ids",
                                              "required_fact_ids")} for c in validation_cases]
    compact_results = {
        variant: {case_id: [{"message_id": h["message_id"], "session_id": h["session_id"],
                             "doc_id": h["doc_id"], "score": h["score"],
                             "fact_ids": h["fact_ids"], "provenance": h["provenance"]}
                            for h in hits]
                  for case_id, hits in by_case.items()}
        for variant, by_case in results.items()
    }
    report = {
        "schema_version": 1,
        "label": "selected-path synthetic calibration; not sealed Q0-Q12 evidence",
        "unavailable_actual_variants": {
            "Q3": "256d encoder unavailable", "Q5": "reranker unavailable",
            "Q7-Q12": "frozen reviewed derived artifacts unavailable",
        },
        "source_sha256": harness_source_after,
        "source_sha256_before": harness_source_before,
        "source_sha256_after": harness_source_after,
        "source_unchanged": harness_source_before == harness_source_after,
        "config": config,
        "config_sha256": digest(config), "corpus_sha256": digest(records),
        "cases_sha256": digest(case_manifest),
        "validation_cases_sha256": digest(validation_manifest),
        "results_sha256": digest(compact_results),
        "counts": {"cases": len(cases), "known_cases": sum(c["kind"] == "known" for c in cases),
                   "unknown_cases": sum(c["kind"] == "unknown" for c in cases),
                   "source_records": len(records), "authorized_active_parts": len(documents),
                   "calibration_cases": len(calibration_cases),
                   "validation_cases": len(validation_cases),
                   "compact_segments": len(compact_documents),
                   "unauthorized_source_controls": len(unauthorized_ids),
                   "source_over_200k": sum(len(r["content"].encode("utf-8")) > 200*1024 for r in records),
                   "fusion_overlap_cases": overlap_count},
        "versions": {"python": platform.python_version(), "numpy": np.__version__,
                     "psycopg": importlib.metadata.version("psycopg"),
                     "sqlite": sqlite3.sqlite_version, "postgresql": pg_version},
        "embedding": {"endpoint": args.embed_endpoint,
                      "endpoint_kind": args.endpoint_kind,
                      "forwarder_target": ("10.23.0.64:9101"
                                           if args.endpoint_kind == "forwarder_unverified" else None),
                      "returned_model_labels": sorted(embedder.returned_models),
                      "dimension": DIMENSION, "corpus_embedding_ms": corpus_embed_ms,
                      "upstream_model_byte_hash": (model_before["tree_content_sha256"]
                                                   if endpoint_identity_verified else None),
                      "candidate_local_snapshot": model_before,
                      "candidate_snapshot_is_endpoint_identity": endpoint_identity_verified,
                      "model_before": model_before, "model_after": model_after,
                      "model_unchanged": model_before == model_after,
                      "endpoint_identity_verified": endpoint_identity_verified,
                      "response_model_alias_verified": response_alias_verified,
                      "managed_endpoint": managed_metadata,
                      "current_run_http_5xx_count": embedder.http_5xx_count,
                      "current_run_retry_count": embedder.retry_count,
                      "current_run_adaptive_split_count": embedder.adaptive_split_count,
                      "pre_run_observation": "earlier attempts observed transient HTTP 500 for oversized batches and then short requests; endpoint recovered without restart"},
        "postgres_temp_cleanup_verified": cleanup_verified,
        "gates": gate_evaluation,
        "metrics": {"development": development_metrics, "validation": validation_metrics},
        "latency": {variant: latency_stats(values) for variant, values in latencies.items()},
        "cases": case_manifest, "validation_cases": validation_manifest,
        "results": compact_results,
    }
    output = Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_bytes(canonical_bytes(report) + b"\n")
    print(json.dumps({"output": str(output.resolve()), "sha256": file_digest(output),
                      "development_cases": len(cases),
                      "validation_cases": len(validation_cases),
                      "metrics": {"development": development_metrics,
                                  "validation": validation_metrics},
                      "hard_safety_pass": gate_evaluation["hard_safety_pass"],
                      "selected_path_pass": gate_evaluation["selected_path_pass"],
                      "compact_candidate_pass": gate_evaluation["compact_candidate_pass"],
                      "postgres_temp_cleanup_verified": cleanup_verified},
                     ensure_ascii=False, sort_keys=True))
    return gate_evaluation["selected_path_pass"]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--self-check", action="store_true")
    parser.add_argument("--embed-endpoint", default="http://127.0.0.1:9102/v1/embeddings")
    parser.add_argument("--document-model", default=DOCUMENT_MODEL)
    parser.add_argument("--query-model", default=QUERY_MODEL)
    parser.add_argument("--endpoint-kind", choices=("forwarder_unverified", "local_pinned"),
                        default="forwarder_unverified")
    parser.add_argument("--managed-endpoint", action="store_true",
                        help="launch and bind evidence to an isolated local gemma-full sidecar")
    parser.add_argument("--managed-port", type=int, default=0,
                        help="managed loopback port; zero selects an unused port")
    parser.add_argument("--pg-dsn", default=os.environ.get("LEVARA_BENCH_PG_DSN",
                                                           "dbname=levara host=127.0.0.1"))
    parser.add_argument("--model-snapshot", default=DEFAULT_MODEL_SNAPSHOT)
    parser.add_argument("--output", default=str(Path(tempfile.gettempdir()) /
                                                 "levara-selected-path-quality.json"))
    parser.add_argument("--batch-size", type=int, default=1)
    parser.add_argument("--batch-char-limit", type=int, default=2_200)
    parser.add_argument("--timeout", type=float, default=60.0)
    parser.add_argument("--request-delay-ms", type=float, default=0.0)
    args = parser.parse_args()
    require(args.batch_size > 0, "batch-size must be positive")
    require(args.batch_char_limit >= PART_MAX_BYTES, "batch-char-limit must fit one source part")
    require(args.timeout > 0, "timeout must be positive")
    require(args.request_delay_ms >= 0, "request-delay-ms must be nonnegative")
    require(0 <= args.managed_port <= 65535, "managed-port is invalid")
    if args.self_check:
        self_check()
    else:
        managed = None
        selected_path_pass = False
        try:
            if args.managed_endpoint:
                repo_root = Path(__file__).resolve().parent.parent
                managed = ManagedEndpoint(repo_root, args.model_snapshot,
                                          args.managed_port).start()
                args.embed_endpoint = managed.endpoint
                args.document_model = "embeddinggemma-300m-full-v1"
                args.query_model = "embeddinggemma-300m-full-v1:query"
                args.endpoint_kind = "managed_local_pinned"
            selected_path_pass = run(args, managed)
        finally:
            if managed is not None:
                managed.stop_and_verify()
        if not selected_path_pass:
            raise SystemExit(2)


if __name__ == "__main__":
    main()
