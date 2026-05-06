"""
Conftest for Levara adapter unit tests.

Sets up lightweight stubs in sys.modules for heavy cognee dependencies
BEFORE any test file is collected, then loads LevaraAdapter via importlib
so that subsequent `from cognee... import LevaraAdapter` works.
"""

import importlib.util
import sys
import types
import uuid
from pathlib import Path
from unittest.mock import MagicMock

_REPO_ROOT = Path(__file__).parent.parent / "cognee"  # .../new_db/cognee/


def _stub(name: str) -> types.ModuleType:
    """Ensure a module stub exists in sys.modules, return it."""
    if name not in sys.modules:
        mod = types.ModuleType(name)
        mod.__path__ = []
        mod.__package__ = name
        sys.modules[name] = mod
    return sys.modules[name]


# ── top-level cognee stub (prevents __init__.py execution) ───────────────────
_cog = _stub("cognee")

# ── DataPoint ─────────────────────────────────────────────────────────────────
class _DataPoint:
    metadata: dict = {}
    belongs_to_set: list = []

    def __init__(self, **kwargs):
        self.id = kwargs.get("id", uuid.uuid4())
        for k, v in kwargs.items():
            setattr(self, k, v)

    @staticmethod
    def get_embeddable_data(dp) -> str:
        idx = getattr(dp, "metadata", {}).get("index_fields", [])
        return getattr(dp, idx[0], "") if idx else ""


_stub("cognee.infrastructure")
_eng = _stub("cognee.infrastructure.engine")
_eng.DataPoint = _DataPoint
_stub("cognee.infrastructure.engine.models")

_eng_utils = _stub("cognee.infrastructure.engine.utils")
def _parse_id(val):
    if isinstance(val, uuid.UUID):
        return val
    try:
        return uuid.UUID(str(val))
    except ValueError:
        return uuid.uuid4()
_eng_utils.parse_id = _parse_id

# ── modules.storage.utils ─────────────────────────────────────────────────────
_stub("cognee.modules")
_stub("cognee.modules.storage")
_stor = _stub("cognee.modules.storage.utils")
def _get_own_properties(dp):
    if hasattr(dp, "model_dump"):
        return dp.model_dump()
    d = vars(dp).copy()
    d.pop("__dict__", None)
    return d
_stor.get_own_properties = _get_own_properties
_stor.copy_model = MagicMock()

# ── ScoredResult ─────────────────────────────────────────────────────────────
class _ScoredResult:
    def __init__(self, id, score, payload=None):
        self.id = id
        self.score = score
        self.payload = payload
    def __eq__(self, other):
        return isinstance(other, _ScoredResult) and self.id == other.id

_stub("cognee.infrastructure.databases")
_vec_mod = _stub("cognee.infrastructure.databases.vector")
_vec_mod.get_vector_engine = MagicMock()
_stub("cognee.infrastructure.databases.vector.models")
_sr_mod = _stub("cognee.infrastructure.databases.vector.models.ScoredResult")
_sr_mod.ScoredResult = _ScoredResult

# ── exceptions ────────────────────────────────────────────────────────────────
class _MissingQueryParameterError(Exception):
    pass

class _CollectionNotFoundError(Exception):
    pass

_exc = _stub("cognee.infrastructure.databases.exceptions")
_exc.MissingQueryParameterError = _MissingQueryParameterError

_vec_exc = _stub("cognee.infrastructure.databases.vector.exceptions")
_vec_exc.CollectionNotFoundError = _CollectionNotFoundError
_vec_exc_inner = _stub("cognee.infrastructure.databases.vector.exceptions.exceptions")
_vec_exc_inner.CollectionNotFoundError = _CollectionNotFoundError

# ── EmbeddingEngine ───────────────────────────────────────────────────────────
class _EmbeddingEngine:
    async def embed_text(self, texts): ...
    def get_vector_size(self): ...

_stub("cognee.infrastructure.databases.vector.embeddings")
_emb = _stub("cognee.infrastructure.databases.vector.embeddings.EmbeddingEngine")
_emb.EmbeddingEngine = _EmbeddingEngine

# ── VectorDBInterface ─────────────────────────────────────────────────────────
_iface = _stub("cognee.infrastructure.databases.vector.vector_db_interface")
class _VectorDBInterface:
    pass
_iface.VectorDBInterface = _VectorDBInterface

# ── normalize_distances (used by LanceDB adapter) ────────────────────────────
_vec_utils = _stub("cognee.infrastructure.databases.vector.utils")
def _normalize_distances(results):
    """Convert raw _distance scores to 0–1 similarity scores."""
    if not results:
        return []
    distances = [float(r.get("_distance", 0)) for r in results]
    max_d = max(distances) or 1.0
    return [1.0 - (d / max_d) for d in distances]
_vec_utils.normalize_distances = _normalize_distances

# ── files.storage (used by LanceDB adapter prune) ────────────────────────────
_stub("cognee.infrastructure.files")
_files_storage = _stub("cognee.infrastructure.files.storage")
_files_storage.get_file_storage = MagicMock(return_value=MagicMock(remove_all=MagicMock()))

# ── copy_model (used by LanceDB adapter schema generation) ───────────────────
from pydantic import BaseModel as _BaseModel
def _copy_model(model_type, include_fields=None, exclude_fields=None):
    """Minimal copy_model: returns a new Pydantic model with only included fields."""
    import typing
    include = include_fields or {}
    exclude = set(exclude_fields or [])
    fields = {}
    for name, field in model_type.model_fields.items():
        if name in exclude:
            continue
        if name in include:
            fields[name] = include[name]
        else:
            ann = field.annotation
            default = field.default
            import dataclasses
            fields[name] = (ann, default if default is not dataclasses.MISSING else ...)
    return type(f"Copy_{model_type.__name__}", (_BaseModel,), {"__annotations__": {k: v[0] for k, v in fields.items()}, **{k: v[1] for k, v in fields.items() if v[1] is not ...}})
_stor.copy_model = _copy_model

# ── shared.logging_utils stub ─────────────────────────────────────────────────
_stub("cognee.shared")
_logging_utils = _stub("cognee.shared.logging_utils")

import logging as _logging

def _get_logger():
    return _logging.getLogger("cognee")

def _setup_logging():
    return _get_logger()

_logging_utils.get_logger = _get_logger
_logging_utils.setup_logging = _setup_logging

# ── cognee.version stub ───────────────────────────────────────────────────────
_version_mod = _stub("cognee.version")
_version_mod.get_cognee_version = MagicMock(return_value="0.0.0-test")

# ── cognee.exceptions stub ────────────────────────────────────────────────────
_exceptions_mod = _stub("cognee.exceptions")
class _CogneeApiError(Exception):
    name = None
    message = None
    status_code = None
_exceptions_mod.CogneeApiError = _CogneeApiError

# ── litellm stub (needed to load cognee.infrastructure.llm.utils) ─────────────
_litellm = _stub("litellm")
_litellm.model_cost = {}
_litellm_exc = _stub("litellm.exceptions")
class _AuthenticationError(Exception):
    pass
_litellm_exc.AuthenticationError = _AuthenticationError
_litellm.exceptions = _litellm_exc

# ── LLM utils stubs and load ──────────────────────────────────────────────────
_stub("cognee.infrastructure.llm")
_stub("cognee.infrastructure.llm.config")

_llm_gateway = _stub("cognee.infrastructure.llm.LLMGateway")
_llm_gateway.LLMGateway = MagicMock()

_stub("cognee.infrastructure.llm.structured_output_framework")
_stub("cognee.infrastructure.llm.structured_output_framework.litellm_instructor")
_stub("cognee.infrastructure.llm.structured_output_framework.litellm_instructor.llm")
_get_llm_client_mod = _stub("cognee.infrastructure.llm.structured_output_framework.litellm_instructor.llm.get_llm_client")
_get_llm_client_mod.get_llm_client = MagicMock()

_llm_utils_path = (
    _REPO_ROOT / "cognee" / "infrastructure" / "llm" / "utils.py"
)
if _llm_utils_path.exists():
    try:
        _llm_utils_spec = importlib.util.spec_from_file_location(
            "cognee.infrastructure.llm.utils",
            _llm_utils_path,
        )
        _llm_utils_mod = importlib.util.module_from_spec(_llm_utils_spec)
        sys.modules["cognee.infrastructure.llm.utils"] = _llm_utils_mod
        _llm_utils_spec.loader.exec_module(_llm_utils_mod)
    except Exception as _e:
        import warnings
        warnings.warn(f"Could not load llm/utils.py: {_e}")

# ── API client stubs (used by Layer 2 tests) ──────────────────────────────────
_stub("cognee.api")
_stub("cognee.api.v1")
_stub("cognee.api.v1.health")

# Stub health.py module and load it
_health_path = _REPO_ROOT / "cognee" / "api" / "v1" / "health" / "health.py"
if _health_path.exists():
    try:
        _health_spec = importlib.util.spec_from_file_location(
            "cognee.api.v1.health.health",
            _health_path,
        )
        _health_mod = importlib.util.module_from_spec(_health_spec)
        sys.modules["cognee.api.v1.health.health"] = _health_mod
        _health_spec.loader.exec_module(_health_mod)
    except Exception as _e:
        import warnings
        warnings.warn(f"Could not load health.py: {_e}")

# ── Load LanceDBAdapter via importlib ─────────────────────────────────────────
_lancedb_pkg = _stub("cognee.infrastructure.databases.vector.lancedb")

_lance_adapter_path = (
    _REPO_ROOT / "cognee" / "infrastructure" / "databases" / "vector"
    / "lancedb" / "LanceDBAdapter.py"
)
if _lance_adapter_path.exists():
    try:
        _lance_spec = importlib.util.spec_from_file_location(
            "cognee.infrastructure.databases.vector.lancedb.LanceDBAdapter",
            _lance_adapter_path,
        )
        _lance_mod = importlib.util.module_from_spec(_lance_spec)
        sys.modules["cognee.infrastructure.databases.vector.lancedb.LanceDBAdapter"] = _lance_mod
        _lance_spec.loader.exec_module(_lance_mod)
        _lancedb_pkg.LanceDBAdapter = _lance_mod.LanceDBAdapter
    except Exception as _e:
        import warnings
        warnings.warn(f"Could not load LanceDBAdapter: {_e}")

# ── Load LevaraAdapter via importlib ────────────────────────────────────────
_levara_pkg = _stub("cognee.infrastructure.databases.vector.levara")

# Register the generated protobuf modules BEFORE loading the adapter,
# so that `from .generated import levara_pb2` resolves correctly.
_generated_dir = (
    _REPO_ROOT / "cognee" / "infrastructure" / "databases" / "vector"
    / "levara" / "generated"
)
_generated_pkg_name = "cognee.infrastructure.databases.vector.levara.generated"

# Register the generated package
_generated_pkg = _stub(_generated_pkg_name)

# Load levara_pb2
_pb2_path = _generated_dir / "levara_pb2.py"
if _pb2_path.exists():
    _pb2_spec = importlib.util.spec_from_file_location(
        f"{_generated_pkg_name}.levara_pb2", _pb2_path
    )
    _pb2_mod = importlib.util.module_from_spec(_pb2_spec)
    sys.modules[f"{_generated_pkg_name}.levara_pb2"] = _pb2_mod
    _pb2_spec.loader.exec_module(_pb2_mod)
    _generated_pkg.levara_pb2 = _pb2_mod
else:
    _pb2_mod = _stub(f"{_generated_pkg_name}.levara_pb2")
    _generated_pkg.levara_pb2 = _pb2_mod

# Load levara_pb2_grpc
_pb2_grpc_path = _generated_dir / "levara_pb2_grpc.py"
if _pb2_grpc_path.exists():
    _pb2_grpc_spec = importlib.util.spec_from_file_location(
        f"{_generated_pkg_name}.levara_pb2_grpc", _pb2_grpc_path
    )
    _pb2_grpc_mod = importlib.util.module_from_spec(_pb2_grpc_spec)
    sys.modules[f"{_generated_pkg_name}.levara_pb2_grpc"] = _pb2_grpc_mod
    # levara_pb2_grpc.py uses `import levara_pb2` (bare name); alias it
    sys.modules["levara_pb2"] = _pb2_mod
    _pb2_grpc_spec.loader.exec_module(_pb2_grpc_mod)
    # Clean up the bare alias so it doesn't pollute the global namespace
    sys.modules.pop("levara_pb2", None)
    _generated_pkg.levara_pb2_grpc = _pb2_grpc_mod
else:
    _pb2_grpc_mod = _stub(f"{_generated_pkg_name}.levara_pb2_grpc")
    _generated_pkg.levara_pb2_grpc = _pb2_grpc_mod

# Now load the adapter (its `from .generated import levara_pb2` will resolve)
_adapter_path = (
    _REPO_ROOT / "cognee" / "infrastructure" / "databases" / "vector"
    / "levara" / "LevaraAdapter.py"
)
_spec = importlib.util.spec_from_file_location(
    "cognee.infrastructure.databases.vector.levara.LevaraAdapter",
    _adapter_path,
)
_adapter_mod = importlib.util.module_from_spec(_spec)
sys.modules["cognee.infrastructure.databases.vector.levara.LevaraAdapter"] = _adapter_mod
_spec.loader.exec_module(_adapter_mod)

# Attach to parent package so `from cognee...levara.LevaraAdapter import X` works
_levara_pkg.LevaraAdapter = _adapter_mod.LevaraAdapter
