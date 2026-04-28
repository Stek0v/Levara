// memory-levara — PicoClaw memory plugin backed by Levara /api/v1/memories
//
// Replaces memory-lancedb. Same three tools (memory_recall, memory_store,
// memory_forget), same lifecycle hooks (beforeAgentStart, agentEnd).
// REST calls go to Levara; if the host is unreachable, writes queue to an
// NDJSON outbox and flush on next successful recall.

import * as fs from "fs";
import * as os from "os";
import * as path from "path";

// ─── Types ───────────────────────────────────────────────────────────────────

export interface PluginConfig {
  levaraUrl: string;
  jwtToken: string;
  fallbackFile?: string;
  timeoutMs?: number;
}

interface MemoryEntry {
  id?: string;
  key: string;
  value: string;
  type: string;
  owner_id?: string;
  created_at?: string;
  updated_at?: string;
}

interface OutboxOp {
  ts: number;
  op: "store" | "forget";
  key: string;
  value?: string;
  type?: string;
}

// Category (PicoClaw) → type (Levara hall vocab) mapping
const CATEGORY_TO_TYPE: Record<string, string> = {
  fact: "fact",
  decision: "decision",
  preference: "preference",
  entity: "fact",       // graph entities stored as facts
  other: "discovery",   // catch-all → discovery
  advice: "advice",
  discovery: "discovery",
  event: "event",
};

function mapCategory(category?: string): string {
  if (!category) return "fact";
  return CATEGORY_TO_TYPE[category] ?? "fact";
}

// ─── LevaraClient ────────────────────────────────────────────────────────────

export class LevaraClient {
  private baseUrl: string;
  private headers: Record<string, string>;
  private timeoutMs: number;
  private outboxPath: string;
  private outboxQueue: OutboxOp[] = [];
  private flushing = false;

  constructor(cfg: PluginConfig) {
    this.baseUrl = cfg.levaraUrl.replace(/\/$/, "");
    this.headers = {
      "Content-Type": "application/json",
      Authorization: `Bearer ${cfg.jwtToken}`,
    };
    this.timeoutMs = cfg.timeoutMs ?? 5000;
    const fallback = cfg.fallbackFile ?? "~/.clawdbot/outbox.ndjson";
    this.outboxPath = fallback.startsWith("~")
      ? path.join(os.homedir(), fallback.slice(1))
      : fallback;
    this._loadOutbox();
  }

  // POST /api/v1/memories — store a key/value with type
  async store(key: string, value: string, category?: string): Promise<void> {
    const type = mapCategory(category);

    try {
      await this._storeRemote(key, value, type);
    } catch (err) {
      this._enqueueOutbox({ ts: Date.now(), op: "store", key, value, type });
      throw err;
    }
  }

  // GET /api/v1/memories?type=...  — search / list memories
  async recall(query: string, category?: string): Promise<MemoryEntry[]> {
    const type = category ? mapCategory(category) : undefined;
    const qs = type ? `?type=${encodeURIComponent(type)}` : "";

    try {
      const resp = await this._fetch(`/api/v1/memories${qs}`, {
        method: "GET",
      });
      if (!resp.ok) {
        throw new Error(`HTTP ${resp.status}: ${await resp.text()}`);
      }
      const all = (await resp.json()) as MemoryEntry[];
      // Client-side filter by query string (Levara lists by type; no FTS on GET /memories)
      if (!query) return all;
      const q = query.toLowerCase();
      return all.filter(
        (m) =>
          m.key.toLowerCase().includes(q) ||
          m.value.toLowerCase().includes(q)
      );
    } catch (err) {
      // On network failure, try to flush outbox and return empty
      this._tryFlushOutbox();
      return [];
    }
  }

  // DELETE /api/v1/memories/:key — forget a memory by key
  async forget(key: string): Promise<void> {
    try {
      await this._forgetRemote(key);
    } catch (err) {
      this._enqueueOutbox({ ts: Date.now(), op: "forget", key });
      throw err;
    }
  }

  // Flush outbox: replay queued ops to Levara
  async flush(): Promise<void> {
    if (this.outboxQueue.length === 0) return;
    await this._tryFlushOutbox();
  }

  // ─── Private ───────────────────────────────────────────────────

  private async _fetch(endpoint: string, init: RequestInit): Promise<Response> {
    const ac = new AbortController();
    const timer = setTimeout(() => ac.abort(), this.timeoutMs);
    try {
      return await fetch(`${this.baseUrl}${endpoint}`, {
        ...init,
        headers: this.headers,
        signal: ac.signal,
      });
    } finally {
      clearTimeout(timer);
    }
  }

  private async _storeRemote(key: string, value: string, type: string): Promise<void> {
    const resp = await this._fetch("/api/v1/memories", {
      method: "POST",
      body: JSON.stringify({ key, value, type }),
    });
    if (!resp.ok) {
      throw new Error(`HTTP ${resp.status}: ${await resp.text()}`);
    }
  }

  private async _forgetRemote(key: string): Promise<void> {
    const resp = await this._fetch(
      `/api/v1/memories/${encodeURIComponent(key)}`,
      { method: "DELETE" }
    );
    if (!resp.ok) {
      throw new Error(`HTTP ${resp.status}: ${await resp.text()}`);
    }
  }

  private _enqueueOutbox(op: OutboxOp): void {
    this.outboxQueue.push(op);
    this._persistOutbox();
    // Try to flush in background (non-blocking)
    setTimeout(() => this._tryFlushOutbox(), 500);
  }

  private async _tryFlushOutbox(): Promise<void> {
    if (this.flushing || this.outboxQueue.length === 0) return;
    this.flushing = true;
    const pending = this.outboxQueue;
    this.outboxQueue = [];
    const remaining: OutboxOp[] = [];

    for (const op of pending) {
      try {
        if (op.op === "store") {
          await this._storeRemote(op.key, op.value ?? "", op.type ?? "fact");
        } else if (op.op === "forget") {
          await this._forgetRemote(op.key);
        }
      } catch {
        remaining.push(op);
      }
    }

    this.outboxQueue = remaining.concat(this.outboxQueue);
    this._persistOutbox();
    this.flushing = false;
  }

  private _persistOutbox(): void {
    try {
      const ndjson = this.outboxQueue
        .map((op) => JSON.stringify(op))
        .join("\n");
      fs.mkdirSync(path.dirname(this.outboxPath), { recursive: true });
      fs.writeFileSync(this.outboxPath, ndjson + (ndjson ? "\n" : ""));
    } catch {
      // Best-effort; if we can't write outbox nothing we can do
    }
  }

  private _loadOutbox(): void {
    try {
      if (!fs.existsSync(this.outboxPath)) return;
      const lines = fs.readFileSync(this.outboxPath, "utf8").split("\n");
      this.outboxQueue = lines
        .filter((l) => l.trim())
        .map((l) => JSON.parse(l) as OutboxOp);
    } catch {
      this.outboxQueue = [];
    }
  }
}

// ─── Plugin export (PicoClaw plugin API) ─────────────────────────────────────

let _db: LevaraClient | null = null;

export async function beforeAgentStart(config: PluginConfig): Promise<void> {
  _db = new LevaraClient(config);
  // Try to flush any outbox items from previous session
  await _db.flush();
}

export async function agentEnd(): Promise<void> {
  if (_db) {
    await _db.flush();
  }
}

// Tool: memory_store
export async function memory_store(
  key: string,
  value: string,
  category?: string
): Promise<{ saved: boolean }> {
  if (!_db) throw new Error("memory-levara: not initialized");
  await _db.store(key, value, category);
  return { saved: true };
}

// Tool: memory_recall
export async function memory_recall(
  query: string,
  category?: string
): Promise<MemoryEntry[]> {
  if (!_db) throw new Error("memory-levara: not initialized");
  return _db.recall(query, category);
}

// Tool: memory_forget
export async function memory_forget(
  key: string
): Promise<{ deleted: boolean }> {
  if (!_db) throw new Error("memory-levara: not initialized");
  await _db.forget(key);
  return { deleted: true };
}

export default { beforeAgentStart, agentEnd, memory_store, memory_recall, memory_forget };
