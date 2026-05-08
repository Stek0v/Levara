"""agent-memory-app — minimal end-to-end Levara example.

Runs against the local stack started by `make stack-dev`. Embeds three
short sentences with Ollama's nomic-embed-text, inserts them into the
default `mem0` collection, then runs a semantic search and prints the
ranked results.

No SDK required — pure HTTP against Levara's public REST surface.
"""

from __future__ import annotations

import sys
from typing import Any

import requests

LEVARA_URL = "http://localhost:8080"
OLLAMA_URL = "http://localhost:11434"
COLLECTION = "mem0"
EMBED_MODEL = "nomic-embed-text"

CORPUS = [
    ("note-cooking", "Tomato pasta cooks in nine minutes if the water is salted properly."),
    ("note-travel",  "The night train from Vienna to Venice leaves at 21:38 from platform six."),
    ("note-bug",     "Goroutine leak: forgot to close the ticker channel on shutdown."),
]

QUERY = "How do I fix a leaking goroutine?"


def embed(text: str) -> list[float]:
    r = requests.post(f"{OLLAMA_URL}/api/embeddings",
                      json={"model": EMBED_MODEL, "prompt": text}, timeout=120)
    r.raise_for_status()
    return r.json()["embedding"]


def insert(record_id: str, vector: list[float], metadata: dict[str, Any]) -> None:
    r = requests.post(f"{LEVARA_URL}/api/v1/insert",
                      json={"collection": COLLECTION, "id": record_id,
                            "vector": vector, "metadata": metadata}, timeout=10)
    r.raise_for_status()


def search(vector: list[float], k: int = 3) -> list[dict[str, Any]]:
    r = requests.post(f"{LEVARA_URL}/api/v1/search",
                      json={"collection": COLLECTION, "vector": vector, "k": k}, timeout=10)
    r.raise_for_status()
    return r.json().get("results", [])


def main() -> int:
    print(f"→ embedding {len(CORPUS)} notes via Ollama ({EMBED_MODEL})")
    for record_id, text in CORPUS:
        insert(record_id, embed(text), {"text": text})
        print(f"  inserted {record_id}")

    print(f"\n→ query: {QUERY!r}")
    hits = search(embed(QUERY), k=3)
    for rank, hit in enumerate(hits, start=1):
        text = (hit.get("metadata") or {}).get("text", "")
        print(f"  {rank}. id={hit.get('id')}  score={hit.get('score'):.4f}  {text}")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except requests.HTTPError as exc:
        print(f"HTTP error: {exc.response.status_code} {exc.response.text}", file=sys.stderr)
        sys.exit(1)
    except requests.ConnectionError:
        print("Cannot reach Levara/Ollama — run `make stack-dev` first.", file=sys.stderr)
        sys.exit(1)
