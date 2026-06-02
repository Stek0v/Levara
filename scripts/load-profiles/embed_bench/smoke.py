"""Local smoke for the embed-bench backends.

Usage:
    python -m embed_bench.smoke --model jina

Instantiates the real backend (downloads weights to HF cache on first run),
embeds three short phrases, prints shape and L2 norm of each vector.

Run this on Mac before deploying to Pi — if it dies here it will die there.
"""
from __future__ import annotations

import argparse
import math

from embed_bench.backends import make_backend
from embed_bench.recipes import get_recipe


PHRASES = [
    "the cat sat on the mat",
    "import numpy as np",
    "embeddings are vectors that encode semantic meaning",
]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--model", required=True, help="short name from recipes.RECIPES")
    args = parser.parse_args()

    recipe = get_recipe(args.model)
    print(f"[smoke] model={recipe.openai_name} backend={recipe.backend} expected_dim={recipe.dim}")
    backend = make_backend(recipe)
    print(f"[smoke] backend ready, dim={backend.dim}")

    vectors = backend.embed(PHRASES)
    for phrase, vec in zip(PHRASES, vectors):
        norm = math.sqrt(sum(x * x for x in vec))
        print(f"  len={len(vec)} norm={norm:.4f}  {phrase!r}")
    print("[smoke] ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
