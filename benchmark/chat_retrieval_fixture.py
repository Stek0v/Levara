#!/usr/bin/env python3
"""Deterministic synthetic imported-chat corpus for developer calibration."""

import json


OWNER = "owner-a"
TENANT = "tenant-a"
SHARED = "project-shared"
PRIVATE = "project-private-a"


def message(case_id, text, facts, *, project=SHARED, owner=OWNER, tenant=TENANT,
            stale=False, active=True, revoked=False, suffix="current"):
    return {
        "message_id": f"msg-{case_id}-{suffix}", "session_id": f"session-{case_id}",
        "owner_id": owner, "tenant_id": tenant, "project_id": project,
        "text": text, "fact_annotations": [{"fact_id": fact, "offset": max(0, len(text) - 1)}
                                             for fact in facts],
        "fact_ids": facts,
        "is_stale": stale, "is_active": active,
        "revoked": revoked,
    }


def build():
    long_text = "synthetic filler " * 14000 + " tail marker BIGMSG_204K answer omega-77"
    specs = [
        ("error", "Where was ERR_CONN_421 recorded?", "Error ERR_CONN_421 occurred in worker alpha.",
         ["fact-error-421"], "known"),
        ("code", "Which session mentions ParseLedgerFrame?", "The code symbol ParseLedgerFrame validates WAL frames.",
         ["fact-code-symbol"], "known"),
        ("date", "When was synthetic freeze day?", "Synthetic freeze day was 2026-10-08.",
         ["fact-date"], "known"),
        ("version", "Which exact version fixed the parser?", "Parser correction shipped in version 3.7.11.",
         ["fact-version"], "known"),
        ("russian", "Какой таймаут выбрали для воркера?", "Для воркера выбрали таймаут 45 секунд.",
         ["fact-timeout"], "known"),
        ("english", "What retry count was selected?", "The selected retry count is five attempts.",
         ["fact-retry-count"], "known"),
        ("cross-language", "Какой retry budget у worker pool?", "The worker pool retry budget is seven attempts.",
         ["fact-cross-language"], "known"),
        ("multiple", "What port and protocol does synthetic relay use?", "Synthetic relay uses port 7443.",
         ["fact-relay-port", "fact-relay-protocol"], "known"),
        ("long", "Where is BIGMSG_204K and what answer follows it?", long_text,
         ["fact-large-message"], "known"),
        ("corrected", "What is the corrected cache limit?", "The corrected cache limit is 64 entries.",
         ["fact-cache-current"], "known"),
        ("acl", "Where is phrase violet turbine 913 authorized?", "Identical phrase violet turbine 913 is authorized here.",
         ["fact-authorized-copy"], "known"),
        ("negation", "Which component must not enable unsafe fallback?", "Indexer must not enable unsafe fallback.",
         ["fact-negation"], "known"),
        ("fusion-overlap", "Which fusion overlap token is canonical?",
         "Fusion overlap token FUSE_77 is canonical.", ["fact-fusion-overlap"], "known"),
        ("unknown", "What is the launch code for nonexistent project Zeta?", "", [], "unknown"),
    ]
    records = []
    cases = []
    for case_id, query, text, facts, kind in specs:
        if kind == "unknown":
            cases.append(case(case_id, query, kind, facts, None))
            continue
        message_facts = ["fact-relay-port"] if case_id == "multiple" else facts
        record = message(case_id, text, message_facts, project=PRIVATE if case_id == "acl" else SHARED)
        records.append(record)
        cases.append(case(case_id, query, kind, facts, record))
    records.append(message("multiple", "Synthetic relay protocol is mutual TLS.",
                           ["fact-relay-protocol"], suffix="second"))
    cases[7]["relevant_message_ids"].append("msg-multiple-second")
    add_allowed(cases[7], records[-1])
    records.extend([
        message("corrected", "The cache limit is 16 entries.", ["fact-cache-stale"],
                stale=True, active=True, suffix="stale"),
        message("acl-foreign", "Identical phrase violet turbine 913 is authorized here.",
                ["fact-foreign"], owner="owner-b", tenant="tenant-b", project="project-foreign"),
        message("acl-revoked", "Identical phrase violet turbine 913 is authorized here.",
                ["fact-revoked"], revoked=True, suffix="revoked"),
    ])
    fixture = {"schema_version": 1, "synthetic": True,
               "description": "Generated developer calibration oracle; not sealed production evidence.",
               "cases": cases}
    return records, fixture


def case(case_id, query, kind, facts, record):
    value = {"id": case_id, "query": query, "kind": kind,
             "required_fact_ids": facts, "relevant_message_ids": [],
             "relevant_session_ids": [], "allowed_provenance": []}
    if record:
        value["relevant_message_ids"].append(record["message_id"])
        value["relevant_session_ids"].append(record["session_id"])
        add_allowed(value, record)
    return value


def add_allowed(case_value, record, source_kind="message", source_id=None):
    case_value["allowed_provenance"].append({
        "source_kind": source_kind, "source_id": source_id or record["message_id"],
        "session_id": record["session_id"], "owner_id": record["owner_id"],
        "tenant_id": record["tenant_id"], "project_id": record["project_id"],
    })


def jsonl_bytes(records):
    return b"".join((json.dumps(record, ensure_ascii=False, sort_keys=True) + "\n").encode()
                    for record in records)
