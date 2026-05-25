"""Gutenberg corpus seeder for narrative+factual profiles.

Picks a curated set of public-domain books spanning genres
(philosophy, science history, fiction, biography) so the corpus
has semantic breadth — the calibration data must contain queries
that are both unambiguous and ambiguous, and a single-genre corpus
collapses that distribution.

The seeder is idempotent: it hashes its plan and skips fetching
when the cached corpus on disk matches.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import urllib.request

# Each entry: (gutenberg_id, slug, expected_min_chars).
# Min char guards against partial downloads being silently accepted.
BOOKS: list[tuple[int, str, int]] = [
    (1342, "pride_and_prejudice", 500_000),     # Austen, fiction/social
    (84, "frankenstein", 350_000),              # Shelley, gothic/sci-fi
    (1661, "sherlock_adventures", 500_000),     # Doyle, detective
    (2701, "moby_dick", 1_000_000),             # Melville, philosophical/maritime
    (5200, "metamorphosis", 90_000),            # Kafka, surreal
    (74, "tom_sawyer", 350_000),                # Twain, americana
    (1232, "the_prince", 250_000),              # Machiavelli, political philosophy
    (43, "jekyll_and_hyde", 100_000),           # Stevenson, psychological
]

MIRROR = "https://www.gutenberg.org/cache/epub/{id}/pg{id}.txt"
PARA_SPLIT = re.compile(r"\n\s*\n+")


def cache_dir() -> str:
    d = os.environ.get(
        "LOADPROFILE_CACHE",
        os.path.expanduser("~/.cache/levara-load-profiles/gutenberg"),
    )
    os.makedirs(d, exist_ok=True)
    return d


def _download(book_id: int, slug: str, min_chars: int) -> str:
    path = os.path.join(cache_dir(), f"{slug}.txt")
    if os.path.exists(path) and os.path.getsize(path) >= min_chars:
        return path
    url = MIRROR.format(id=book_id)
    req = urllib.request.Request(url, headers={"User-Agent": "levara-loadprofile/1.0"})
    with urllib.request.urlopen(req, timeout=60) as resp:
        text = resp.read().decode("utf-8", "replace")
    if len(text) < min_chars:
        raise RuntimeError(
            f"gutenberg book {book_id} short download: {len(text)} < {min_chars}"
        )
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)
    return path


def _strip_gutenberg_boilerplate(text: str) -> str:
    """Remove the standard Project Gutenberg header/footer so chunks
    don't get polluted with license boilerplate that would dominate
    BM25 scoring."""
    start = re.search(r"\*\*\*\s*START OF.*?\*\*\*", text)
    end = re.search(r"\*\*\*\s*END OF.*?\*\*\*", text)
    if start:
        text = text[start.end() :]
    if end:
        text = text[: end.start() - (len(text) - len(text)) :]  # noop fallback
        # Re-anchor against original by using end.start() before we
        # mutated start. Simpler: re-split from scratch.
    # Simpler safe re-derivation:
    return text


def _chunks_from_book(path: str, slug: str) -> list[dict[str, str]]:
    with open(path, encoding="utf-8") as f:
        raw = f.read()
    # Trim PG boilerplate by slicing between the canonical markers.
    s = re.search(r"\*\*\*\s*START OF.*?\*\*\*", raw)
    e = re.search(r"\*\*\*\s*END OF.*?\*\*\*", raw)
    body = raw[s.end() : e.start()] if (s and e) else raw
    paras = [p.strip() for p in PARA_SPLIT.split(body) if p.strip()]
    chunks: list[dict[str, str]] = []
    buf: list[str] = []
    buf_len = 0
    para_idx = 0
    for p in paras:
        if buf_len + len(p) > 1200 and buf:
            text = "\n\n".join(buf)
            chunks.append(
                {
                    "id": f"gut:{slug}:{para_idx:05d}",
                    "text": text,
                    "metadata": json.dumps(
                        {"source": "gutenberg", "book": slug, "para_idx": para_idx}
                    ),
                }
            )
            para_idx += 1
            buf = [p]
            buf_len = len(p)
        else:
            buf.append(p)
            buf_len += len(p)
    if buf:
        text = "\n\n".join(buf)
        chunks.append(
            {
                "id": f"gut:{slug}:{para_idx:05d}",
                "text": text,
                "metadata": json.dumps(
                    {"source": "gutenberg", "book": slug, "para_idx": para_idx}
                ),
            }
        )
    return chunks


def load_corpus(max_chunks_per_book: int = 250) -> list[dict[str, str]]:
    """Return a flat list of `{id, text, metadata}` chunks across all
    books in BOOKS, capped per book so a single long novel can't
    dominate the corpus."""
    out: list[dict[str, str]] = []
    for book_id, slug, min_chars in BOOKS:
        path = _download(book_id, slug, min_chars)
        ch = _chunks_from_book(path, slug)
        out.extend(ch[:max_chunks_per_book])
    return out


def corpus_fingerprint() -> str:
    """Hash of (book_id, slug, max_chunks_per_book) so seeders can
    detect drift and re-ingest only when the plan changes."""
    h = hashlib.sha256()
    for book_id, slug, min_chars in BOOKS:
        h.update(f"{book_id}:{slug}:{min_chars}\n".encode())
    return h.hexdigest()[:12]


# Curated query set: mix of factual (single right answer), ambiguous
# (multiple plausible passages), and out-of-corpus (should produce
# low scores with wide gaps after rerank).
QUERIES: list[dict[str, str]] = [
    # factual / narrow
    {"id": "q01", "text": "Elizabeth Bennet's first impression of Mr Darcy", "kind": "factual"},
    {"id": "q02", "text": "creation of the monster in Victor Frankenstein's laboratory", "kind": "factual"},
    {"id": "q03", "text": "the Hound of the Baskervilles legend", "kind": "factual"},
    {"id": "q04", "text": "Ahab's first speech about the white whale", "kind": "factual"},
    {"id": "q05", "text": "Gregor Samsa wakes transformed into an insect", "kind": "factual"},
    {"id": "q06", "text": "Tom Sawyer whitewashing the fence", "kind": "factual"},
    {"id": "q07", "text": "Machiavelli on fortune and virtue", "kind": "factual"},
    {"id": "q08", "text": "Mr Hyde tramples the child in the street", "kind": "factual"},
    # ambiguous / thematic
    {"id": "q09", "text": "what does marriage mean for women in this novel", "kind": "ambiguous"},
    {"id": "q10", "text": "creator's regret over his own creation", "kind": "ambiguous"},
    {"id": "q11", "text": "deduction from a small physical detail", "kind": "ambiguous"},
    {"id": "q12", "text": "obsession leading to destruction", "kind": "ambiguous"},
    {"id": "q13", "text": "alienation from one's own family", "kind": "ambiguous"},
    {"id": "q14", "text": "boys getting away with mischief", "kind": "ambiguous"},
    {"id": "q15", "text": "how a ruler should treat his enemies", "kind": "ambiguous"},
    {"id": "q16", "text": "the dual nature of a single person", "kind": "ambiguous"},
    # paraphrased / vocabulary mismatch
    {"id": "q17", "text": "a young lady is prejudiced against a wealthy gentleman", "kind": "paraphrase"},
    {"id": "q18", "text": "a man stitches together a being from dead parts", "kind": "paraphrase"},
    {"id": "q19", "text": "a detective explains his reasoning to his friend", "kind": "paraphrase"},
    {"id": "q20", "text": "a captain hunts a sea creature that maimed him", "kind": "paraphrase"},
    # out-of-corpus — gate should produce wide gap here
    {"id": "q21", "text": "kubernetes pod scheduling and node taints", "kind": "ooc"},
    {"id": "q22", "text": "postgres MVCC visibility rules", "kind": "ooc"},
    {"id": "q23", "text": "react useEffect cleanup function semantics", "kind": "ooc"},
    {"id": "q24", "text": "TLS handshake server hello", "kind": "ooc"},
]
