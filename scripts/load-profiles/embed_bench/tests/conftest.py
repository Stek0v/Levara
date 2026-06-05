"""Make the load-profiles harness importable in tests.

Mirrors what p1/p2/etc. do at runtime: prepend scripts/load-profiles/ to
sys.path so `from embed_bench.X import ...` resolves the way it does for
`import runner` etc.
"""
import sys
from pathlib import Path

LOAD_PROFILES_ROOT = Path(__file__).resolve().parents[2]  # scripts/load-profiles/
if str(LOAD_PROFILES_ROOT) not in sys.path:
    sys.path.insert(0, str(LOAD_PROFILES_ROOT))
