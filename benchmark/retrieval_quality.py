#!/usr/bin/env python3
"""Fixed-corpus retrieval smoke, stdlib only; never a production/enterprise certification.

python3 benchmark/retrieval_quality.py --self-check
python3 benchmark/retrieval_quality.py --output-dir /tmp/levara-quality-run --sqlite-db /tmp/levara-quality-data/levara.db

Requires a fresh, isolated SQLite server on loopback :18124 and an existing
local embedding service. It creates one dataset, snapshots the corpus and all
responses, and never changes queries, scoring or ranking after seeing results.
"""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import math
from pathlib import Path
import re
import sqlite3
import subprocess
import tempfile
import time
import unicodedata
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
FIXTURE = Path(__file__).with_name('retrieval_quality_cases.json')


def normalized(value):
    return re.sub(r'\s+', ' ', unicodedata.normalize('NFKC', value).replace('`', '').replace('*', '').casefold()).strip()


def supports(text, needles):
    return all(normalized(needle) in normalized(text) for needle in needles)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def load_corpus(paths, snapshot):
    if snapshot is None:
        return {path: (ROOT / path).read_bytes() for path in paths}
    manifest = json.loads((snapshot / 'manifest.json').read_text())
    assert set(paths) == set(manifest['documents']), 'Snapshot document set differs'
    corpus = {path: (snapshot / 'corpus' / Path(path).name).read_bytes() for path in paths}
    assert {p: digest(data) for p, data in corpus.items()} == manifest['corpus_sha256'], 'Snapshot hash mismatch'
    return corpus


def percentile(values, p):
    return sorted(values)[max(0, math.ceil(len(values) * p) - 1)] if values else None


def request(base, path, payload=None, content_type='application/json'):
    body = payload if isinstance(payload, bytes) else json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(base + path, data=body, headers={'Content-Type': content_type})
    start = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=120) as response:
            status, raw = response.status, response.read().decode()
    except urllib.error.HTTPError as error:
        status, raw = error.code, error.read().decode()
    elapsed = round((time.perf_counter() - start) * 1000, 3)
    try:
        data = json.loads(raw)
    except json.JSONDecodeError:
        data = {'unparsed_body': raw}
    return {'status': status, 'latency_ms': elapsed, 'body': data}


def require_ok(response):
    if response['status'] != 200:
        raise RuntimeError(f"HTTP {response['status']}: {response['body']}")
    return response['body']


def multipart(path, content, dataset_id, name):
    boundary = 'levara-quality-' + uuid.uuid4().hex
    fields = {'datasetName': name}
    if dataset_id:
        fields['dataset_id'] = dataset_id
    payload = ''.join(f'--{boundary}\r\nContent-Disposition: form-data; name="{k}"\r\n\r\n{v}\r\n' for k, v in fields.items()).encode()
    payload += (f'--{boundary}\r\nContent-Disposition: form-data; name="data"; filename="{path.name}"\r\n'
               'Content-Type: text/markdown; charset=utf-8\r\n\r\n').encode() + content
    payload += f'\r\n--{boundary}--\r\n'.encode()
    return payload, 'multipart/form-data; boundary=' + boundary


def grade(response, case, documents, dataset_id, collection, top_k):
    body = response['body']
    items = body.get('items') or [] if isinstance(body, dict) else body or []
    if not isinstance(items, list):
        items = []
    provenance = []
    rank = None
    for index, hit in enumerate(items[:top_k], 1):
        meta = hit.get('metadata') or {}
        doc = documents.get(meta.get('document_id'))
        valid = bool(doc and meta.get('dataset_id') == dataset_id and meta.get('collection') == collection
                     and meta.get('generation') == doc['generation']
                     and meta.get('content_revision') == doc['content_revision'])
        provenance.append(valid)
        text = meta.get('text') or hit.get('text') or ''
        if (rank is None and valid and doc['path'] in case['accepted_sources']
                and supports(text, case['answer_all'])):
            rank = index
    if response['status'] != 200:
        rank = None
    return {'pass': rank is not None,
            'rank': rank, 'returned': len(items), 'valid_provenance': sum(provenance),
            'checked_provenance': len(provenance),
            'provenance_ok': bool(items) and all(provenance) and len(items) <= top_k}


def self_check():
    assert supports('A `first`\nsecond fact', ['first second'])
    assert not supports('unrelated answer', ['first second'])
    docs = {'d': {'source_revision': 12, 'content_revision': 0, 'generation': 'g', 'path': 'a.md'}}
    case = {'accepted_sources': ['a.md'], 'answer_all': ['correct answer']}
    hit = {'metadata': {'document_id': 'd', 'dataset_id': 's', 'collection': 'c',
                       'generation': 'g', 'content_revision': 0, 'text': 'correct answer'}}
    response = {'status': 200, 'body': {'items': [hit]}}
    assert grade(response, case, docs, 's', 'c', 3)['pass']
    hit['metadata']['dataset_id'] = 'foreign'
    assert not grade(response, case, docs, 's', 'c', 3)['pass']
    hit['metadata']['dataset_id'] = 's'
    hit['metadata']['generation'] = 'old-generation'
    assert not grade(response, case, docs, 's', 'c', 3)['pass']
    hit['metadata']['generation'] = 'g'
    hit['metadata']['content_revision'] = 1
    assert not grade(response, case, docs, 's', 'c', 3)['pass']
    hit['metadata']['content_revision'] = 0
    assert not grade(response, {**case, 'accepted_sources': ['other.md']}, docs, 's', 'c', 3)['pass']
    assert not grade({'status': 200, 'body': {'items': []}}, case, docs, 's', 'c', 3)['pass']
    assert not grade({'status': 503, 'body': {'detail': 'unavailable'}}, case, docs, 's', 'c', 3)['pass']
    failed = grade({**response, 'status': 503}, case, docs, 's', 'c', 3)
    assert not failed['pass'] and failed['rank'] is None
    assert percentile([1, 2, 3, 4, 5], .95) == 5
    with tempfile.TemporaryDirectory() as temporary:
        snapshot = Path(temporary)
        (snapshot / 'corpus').mkdir()
        (snapshot / 'corpus' / 'a.md').write_bytes(b'frozen')
        (snapshot / 'manifest.json').write_text(json.dumps({
            'documents': ['docs/a.md'], 'corpus_sha256': {'docs/a.md': digest(b'frozen')}}))
        assert load_corpus(['docs/a.md'], snapshot) == {'docs/a.md': b'frozen'}
        (snapshot / 'corpus' / 'a.md').write_bytes(b'changed')
        try:
            load_corpus(['docs/a.md'], snapshot)
        except AssertionError:
            pass
        else:
            raise AssertionError('Modified snapshot was accepted')
    print('PASS: scorer rejects empty, failed, foreign and stale evidence; checks answer text.')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base', default='http://127.0.0.1:18124')
    parser.add_argument('--embed', default='http://127.0.0.1:9101')
    parser.add_argument('--output-dir', type=Path)
    parser.add_argument('--cases', type=Path, default=FIXTURE, help='Fixed question fixture; the original30 remain the default')
    parser.add_argument('--snapshot-run', type=Path, help='Replay a previous run\'s hash-verified frozen corpus')
    parser.add_argument('--rerank', action='store_true', help='Request the configured real reranker')
    parser.add_argument('--sqlite-db', type=Path, help='Dedicated test server DB, read-only publication verification')
    parser.add_argument('--self-check', action='store_true')
    args = parser.parse_args()
    self_check()
    fixture = json.loads(args.cases.read_text())
    corpus = load_corpus(fixture['documents'], args.snapshot_run)
    assert len({q['id'] for q in fixture['cases']}) == len(fixture['cases'])
    assert len({normalized(q['query']) for q in fixture['cases']}) == len(fixture['cases'])
    assert len({Path(path).name for path in corpus}) == len(corpus)
    assert fixture['cases'] and fixture['top_k'] == 3
    assert len(fixture['cases']) == fixture.get('expected_cases', len(fixture['cases']))
    assert fixture['primary_strategy'] in fixture['strategies']
    for case in fixture['cases']:
        assert case['kind'] in ('exact', 'semantic', 'operational')
        assert normalized(case['query']) and case['answer_all'] and all(normalized(n) for n in case['answer_all'])
        source = fixture['documents'][case['doc']]
        assert supports(corpus[source].decode(), case['answer_all']), f"Ground truth missing: {case['id']}"
        case['accepted_sources'] = [source]
    if args.self_check:
        print(f"PASS: fixed ground truth for {len(fixture['cases'])} queries in {len(corpus)} real documents.")
        return 0
    if not args.output_dir or not args.sqlite_db:
        parser.error('--output-dir and --sqlite-db are required for a live run')
    server = urllib.parse.urlsplit(args.base)
    embedding = urllib.parse.urlsplit(args.embed)
    if server.hostname != '127.0.0.1' or server.port != 18124 or embedding.hostname != '127.0.0.1':
        parser.error('Use the dedicated isolated server at 127.0.0.1:18124 and a loopback embedder')
    out = args.output_dir
    out.mkdir(parents=True, exist_ok=False)
    (out / 'corpus').mkdir()
    (out / 'runner.py').write_bytes(Path(__file__).read_bytes())
    for path, data in corpus.items():
        (out / 'corpus' / Path(path).name).write_bytes(data)
    fixture['corpus_sha256'] = {p: digest(data) for p, data in corpus.items()}
    fixture['fixture_sha256'] = digest(args.cases.read_bytes())
    fixture['corpus_bytes'] = sum(map(len, corpus.values()))
    fixture['locked_at'] = datetime.now(timezone.utc).isoformat()
    (out / 'manifest.json').write_text(json.dumps(fixture, ensure_ascii=False, indent=2))
    print('LOCKED: fixture and corpus snapshots saved before ingestion or scored queries.', flush=True)
    started = time.perf_counter()
    evidence = {'started_at': fixture['locked_at'], 'fixture_sha256': fixture['fixture_sha256'],
                'harness_sha256': digest(Path(__file__).read_bytes()),
                'git_head': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(),
                'config': {'base': args.base, 'db': 'SQLite', 'auth': False, 'top_k': fixture['top_k'],
                           'rerank': args.rerank, 'graph': False, 'concurrency': 1, 'rate_limit_per_minute': 1000,
                           'snapshot_run': str(args.snapshot_run) if args.snapshot_run else None},
                'limitations': ['Small developer-authored smoke, not held-out statistical evaluation',
                                'Retrieval of cited passages, not generated answer correctness',
                                'No ACL, PostgreSQL, load, SLA or enterprise readiness claim',
                                'REST CHUNKS can internally decompose a query; strategies use their real API behavior',
                                'Three separate unscored readiness queries'],
                'uploads': [], 'queries': []}

    def save():
        (out / 'results.json').write_text(json.dumps(evidence, ensure_ascii=False, indent=2))

    try:
        evidence['health'] = require_ok(request(args.base, '/health'))
        evidence['embedding_health'] = require_ok(request(args.embed, '/health'))
        model = evidence['embedding_health']['model']
        probe = require_ok(request(args.embed, '/v1/embeddings', {'model': model, 'input': ['quality preflight']}))
        evidence['embedding_dimension'] = len(probe['data'][0]['embedding'])
        assert evidence['embedding_dimension'] == 768
        print(f"PREFLIGHT: server ready; {model}, dim=768.", flush=True)
        collection = 'quality-' + uuid.uuid4().hex[:12]
        evidence['collection'] = collection
        dataset_id = ''
        for path, content in corpus.items():
            body, content_type = multipart(Path(path), content, dataset_id, collection)
            response = request(args.base, '/api/v1/add', body, content_type)
            evidence['uploads'].append({'path': path, 'response': response})
            dataset_id = require_ok(response)['dataset_id']
        evidence['dataset_id'] = dataset_id
        listing = require_ok(request(args.base, f'/api/v1/datasets/{dataset_id}/data'))
        documents = {}
        for entry in listing:
            matches = [p for p in corpus if Path(p).name == entry['name']]
            assert len(matches) == 1, entry
            documents[entry['id']] = {'path': matches[0], 'source_revision': entry['source_revision'],
                                     'raw_content_hash': entry['raw_content_hash']}
        assert len(documents) == len(corpus)
        evidence['documents'] = documents
        job = require_ok(request(args.base, '/api/v1/cognify', {'datasets': [dataset_id], 'mode': 'rag', 'collection': collection}))
        evidence['ingestion_job'] = job
        deadline = time.monotonic() + 300
        while True:
            status = require_ok(request(args.base, f"/api/v1/cognify/{job['pipeline_run_id']}/status"))
            evidence['ingestion_status'] = status
            save()
            if status.get('status') in ('COMPLETED', 'FAILED', 'ERROR', 'PARTIAL'):
                assert status['status'] == 'COMPLETED', status
                break
            if time.monotonic() > deadline:
                raise TimeoutError('Ingestion did not complete within 300 seconds')
            time.sleep(1)
        # Source revision and resource content revision are separate counters.
        # Verify the published generation against the actual source before scoring.
        with sqlite3.connect(args.sqlite_db.resolve().as_uri() + '?mode=ro', uri=True) as db:
            db.row_factory = sqlite3.Row
            publications = [dict(row) for row in db.execute(
                'SELECT p.data_id, p.content_revision, p.generation, p.source_revision, p.raw_content_hash, '
                'p.lineage_verified, d.source_revision AS current_source_revision, '
                'd.raw_content_hash AS current_raw_content_hash '
                'FROM document_index_publications p JOIN data d ON d.id=p.data_id '
                'WHERE p.dataset_id=? AND p.collection_name=?',
                (dataset_id, collection))]
        assert len(publications) == len(documents)
        for publication in publications:
            doc = documents[publication['data_id']]
            assert publication['lineage_verified'] == 1
            assert publication['source_revision'] == publication['current_source_revision'] == doc['source_revision']
            assert publication['raw_content_hash'] == publication['current_raw_content_hash'] == doc['raw_content_hash']
            assert publication['generation']
            doc.update(content_revision=publication['content_revision'], generation=publication['generation'])
        evidence['publications'] = publications
        evidence['ingestion_elapsed_s'] = round(time.perf_counter() - started, 3)
        print(f'INGESTED: {len(corpus)} full documents; verifying both search branches.', flush=True)
        evidence['readiness'] = []
        for strategy in fixture['strategies']:
            response = request(args.base, '/api/v1/search/text', {'query_text': 'LEVARA_WORKSPACE_WATCH_ASYNC_INDEX',
                'query_type': strategy, 'collection': collection, 'top_k': fixture['top_k'], 'rerank': args.rerank, 'include_debug': True})
            items = require_ok(response).get('items') or []
            assert items and any('LEVARA_WORKSPACE_WATCH_ASYNC_INDEX' in (i.get('metadata', {}).get('text') or '') for i in items), (strategy, response)
            evidence['readiness'].append({'strategy': strategy, 'response': response})
        # Round-robin strategy order avoids giving one strategy all first/cached requests.
        for index, case in enumerate(fixture['cases']):
            strategies = fixture['strategies'][index % 3:] + fixture['strategies'][:index % 3]
            for strategy in strategies:
                response = request(args.base, '/api/v1/search/text', {'query_text': case['query'], 'query_type': strategy,
                    'collection': collection, 'top_k': fixture['top_k'], 'rerank': args.rerank, 'include_debug': True})
                result = grade(response, case, documents, dataset_id, collection, fixture['top_k'])
                evidence['queries'].append({'id': case['id'], 'kind': case['kind'], 'query': case['query'],
                    'strategy': strategy, **result, 'response': response})
            save()
            print(f"[{index+1:03}/{len(fixture['cases'])}] {case['id']}: " + ', '.join(f"{r['strategy']}={'PASS' if r['pass'] else 'MISS'}" for r in evidence['queries'][-3:]), flush=True)
        evidence['summary'] = {}
        for strategy in fixture['strategies']:
            rows = [r for r in evidence['queries'] if r['strategy'] == strategy]
            latency = [r['response']['latency_ms'] for r in rows]
            evidence['summary'][strategy] = {'queries': len(rows), 'passed': sum(r['pass'] for r in rows),
                'hit_at_3': sum(r['pass'] for r in rows) / len(rows),
                'mrr_at_3': sum(1/r['rank'] for r in rows if r['rank']) / len(rows),
                'source_checks': sum(r['checked_provenance'] for r in rows),
                'valid_sources': sum(r['valid_provenance'] for r in rows),
                'all_provenance_valid': all(r['provenance_ok'] for r in rows),
                'http_errors': sum(r['response']['status'] != 200 for r in rows),
                'p50_ms': percentile(latency, .5), 'p95_ms': percentile(latency, .95),
                'misses': [r['id'] for r in rows if not r['pass']]}
        evidence['total_elapsed_s'] = round(time.perf_counter() - started, 3)
        save()
        print(json.dumps(evidence['summary'], ensure_ascii=False, indent=2))
        primary = evidence['summary'][fixture['primary_strategy']]
        return 0 if primary['passed'] == primary['queries'] and primary['all_provenance_valid'] else 2
    except Exception as error:
        evidence['error'] = f'{type(error).__name__}: {error}'
        evidence['total_elapsed_s'] = round(time.perf_counter() - started, 3)
        save()
        raise


if __name__ == '__main__':
    raise SystemExit(main())
