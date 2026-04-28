const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

const { LevaraClient } = require("../dist/index.js");

function tempOutbox() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "memory-levara-"));
  return path.join(dir, "outbox.ndjson");
}

function readOutbox(file) {
  if (!fs.existsSync(file)) return [];
  return fs.readFileSync(file, "utf8").trim().split("\n").filter(Boolean).map(JSON.parse);
}

test("store failure writes exactly one outbox row", async () => {
  const outbox = tempOutbox();
  global.fetch = async () => ({
    ok: false,
    status: 500,
    text: async () => "down",
  });

  const client = new LevaraClient({
    levaraUrl: "http://levara.local:8080",
    jwtToken: "token",
    fallbackFile: outbox,
  });

  await assert.rejects(() => client.store("k", "v", "fact"), /HTTP 500/);
  const rows = readOutbox(outbox);
  assert.equal(rows.length, 1);
  assert.equal(rows[0].op, "store");
  assert.equal(rows[0].key, "k");
});

test("failed flush preserves one row and does not replay appended duplicates", async () => {
  const outbox = tempOutbox();
  fs.writeFileSync(outbox, JSON.stringify({ ts: 1, op: "store", key: "k", value: "v", type: "fact" }) + "\n");

  let attempts = 0;
  global.fetch = async () => {
    attempts += 1;
    return attempts === 1
      ? { ok: false, status: 503, text: async () => "down" }
      : { ok: true, status: 200, text: async () => "", json: async () => ({}) };
  };

  const client = new LevaraClient({
    levaraUrl: "http://levara.local:8080",
    jwtToken: "token",
    fallbackFile: outbox,
  });

  await client.flush();
  assert.equal(attempts, 1);
  const rows = readOutbox(outbox);
  assert.equal(rows.length, 1);
  assert.equal(rows[0].key, "k");
});

test("successful flush clears the outbox", async () => {
  const outbox = tempOutbox();
  fs.writeFileSync(outbox, JSON.stringify({ ts: 1, op: "forget", key: "k" }) + "\n");

  global.fetch = async () => ({
    ok: true,
    status: 200,
    text: async () => "",
    json: async () => ({}),
  });

  const client = new LevaraClient({
    levaraUrl: "http://levara.local:8080",
    jwtToken: "token",
    fallbackFile: outbox,
  });

  await client.flush();
  assert.deepEqual(readOutbox(outbox), []);
});
