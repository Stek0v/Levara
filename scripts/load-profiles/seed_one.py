#!/usr/bin/env python3
"""Seed ONE collection via the full cognify pipeline against bench Levara.

Called by run_all_models.sh. Reads target URL + token from args/env,
loads the code corpus, calls seed_via_cognify.

Usage:
    python seed_one.py --target-url http://10.23.0.53:8091 \\
        --collection loadprofile_p4_main_potion
"""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

LOAD_PROFILES_ROOT = Path(__file__).resolve().parent
if str(LOAD_PROFILES_ROOT) not in sys.path:
    sys.path.insert(0, str(LOAD_PROFILES_ROOT))

import runner
from seed import code_corpus


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--target-url", required=True)
    p.add_argument("--target-name", default="bench")
    p.add_argument("--collection", required=True)
    args = p.parse_args()

    token = runner.load_token_from_env_or_file(args.target_name)
    target = runner.Target(
        name=args.target_name,
        base_url=args.target_url,
        token=token,
    )

    chunks = code_corpus.load_corpus()
    print(f"[seed_one] loaded {len(chunks)} chunks", file=sys.stderr)
    code_corpus.seed_via_cognify(target, args.collection, chunks=chunks)
    print(f"[seed_one] seeded collection={args.collection}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
