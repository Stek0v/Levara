import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { extractWebClientBindings } from './extract_web_client_bindings.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, '../..');

async function loadRows() {
  const source = await fs.readFile(path.join(repoRoot, 'webui/src/lib/api.ts'), 'utf8');
  return extractWebClientBindings(source, 'webui/src/lib/api.ts');
}

function byClient(rows, client) {
  return rows.filter((row) => row.client === client);
}

function only(rows, client) {
  const matches = byClient(rows, client);
  assert.equal(matches.length, 1, `${client} should have exactly one API call`);
  return matches[0];
}

test('extracts representative Web API client bindings from levara object', async () => {
  const rows = await loadRows();

  assert.ok(rows.length >= 52, `expected at least 52 client bindings, got ${rows.length}`);
  assert.deepEqual(
    { method: only(rows, 'health').method, path: only(rows, 'health').path },
    { method: 'GET', path: '/health' },
  );
  assert.deepEqual(
    { method: only(rows, 'login').method, path: only(rows, 'login').path },
    { method: 'POST', path: '/api/v1/auth/login' },
  );
  assert.deepEqual(
    { method: only(rows, 'workspaceSearch').method, path: only(rows, 'workspaceSearch').path },
    { method: 'POST', path: '/api/v1/workspace/search' },
  );
  assert.deepEqual(
    { method: only(rows, 'runSync').method, path: only(rows, 'runSync').path },
    { method: 'POST', path: '/api/v1/sync/run' },
  );
  assert.deepEqual(
    { method: only(rows, 'mcpSessions').method, path: only(rows, 'mcpSessions').path },
    { method: 'GET', path: '`/api/v1/admin/mcp/sessions?limit=${limit}`' },
  );
});

test('preserves template paths and methods without cross-client drift', async () => {
  const rows = await loadRows();

  assert.deepEqual(
    { method: only(rows, 'datasets').method, path: only(rows, 'datasets').path, path_kind: only(rows, 'datasets').path_kind },
    { method: 'GET', path: '`/api/v1/datasets?page=${page}&limit=${limit}`', path_kind: 'template' },
  );
  assert.deepEqual(
    { method: only(rows, 'deleteDataset').method, path: only(rows, 'deleteDataset').path },
    { method: 'DELETE', path: '`/api/v1/datasets/${id}`' },
  );
  assert.deepEqual(
    { method: only(rows, 'graphPath').method, path: only(rows, 'graphPath').path },
    { method: 'GET', path: '`/api/v1/graph/path?${q.toString()}`' },
  );
  assert.deepEqual(
    { method: only(rows, 'syncStatus').method, path: only(rows, 'syncStatus').path },
    { method: 'GET', path: '`/api/v1/sync/status?limit=${limit}`' },
  );
  assert.equal(
    byClient(rows, 'workspaceSearch').some((row) => row.path.includes('/sync/')),
    false,
    'workspaceSearch must not drift to sync endpoints',
  );
});

test('does not emit empty or duplicate binding rows', async () => {
  const rows = await loadRows();
  const seen = new Set();

  for (const row of rows) {
    assert.ok(row.client, 'client should be populated');
    assert.ok(row.path, `path should be populated for ${row.client}`);
    const key = `${row.client}\0${row.call_index}\0${row.path}`;
    assert.equal(seen.has(key), false, `duplicate binding row: ${key}`);
    seen.add(key);
  }
});
