import sys
from pathlib import Path

LOAD_PROFILES_ROOT = Path(__file__).resolve().parent
if str(LOAD_PROFILES_ROOT) not in sys.path:
    sys.path.insert(0, str(LOAD_PROFILES_ROOT))
