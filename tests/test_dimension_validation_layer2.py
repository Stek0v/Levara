"""
Layer 2 tests: lifespan() from client.py — startup dimension validation.

Tests that lifespan() calls health_check() on the vector engine at startup
and handles errors gracefully (logs but doesn't crash the server).

Since client.py imports many router modules that aren't available in tests,
we stub all missing modules before loading it via importlib.
"""

import importlib.util
import logging
import sys
import types
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest


_REPO_ROOT = Path(__file__).parent.parent / "cognee"


def _ensure_stub(name):
    """Ensure a module stub exists in sys.modules."""
    if name not in sys.modules:
        mod = types.ModuleType(name)
        mod.__path__ = []
        mod.__package__ = name
        sys.modules[name] = mod
    return sys.modules[name]


# All modules imported by client.py that need stubs
_CLIENT_DEPS = [
    "cognee.api.v1.cloud",
    "cognee.api.v1.cloud.routers",
    "cognee.api.v1.notebooks",
    "cognee.api.v1.notebooks.routers",
    "cognee.api.v1.permissions",
    "cognee.api.v1.permissions.routers",
    "cognee.api.v1.settings",
    "cognee.api.v1.settings.routers",
    "cognee.api.v1.datasets",
    "cognee.api.v1.datasets.routers",
    "cognee.api.v1.cognify",
    "cognee.api.v1.cognify.routers",
    "cognee.api.v1.search",
    "cognee.api.v1.search.routers",
    "cognee.api.v1.ontologies",
    "cognee.api.v1.ontologies.routers",
    "cognee.api.v1.ontologies.routers.get_ontology_router",
    "cognee.api.v1.memify",
    "cognee.api.v1.memify.routers",
    "cognee.api.v1.add",
    "cognee.api.v1.add.routers",
    "cognee.api.v1.delete",
    "cognee.api.v1.delete.routers",
    "cognee.api.v1.responses",
    "cognee.api.v1.responses.routers",
    "cognee.api.v1.sync",
    "cognee.api.v1.sync.routers",
    "cognee.api.v1.health.routers",
    "cognee.api.v1.update",
    "cognee.api.v1.update.routers",
    "cognee.api.v1.prune",
    "cognee.api.v1.prune.routers",
    "cognee.api.v1.interactions",
    "cognee.api.v1.interactions.routers",
    "cognee.api.v1.users",
    "cognee.api.v1.users.routers",
    "cognee.modules.users.methods.get_authenticated_user",
    "cognee.infrastructure.databases.relational",
    "cognee.infrastructure.databases.relational.get_relational_engine",
    "cognee.modules.users.methods",
    "cognee.infrastructure.databases.vector.embeddings.config",
]

# Stub all deps
for _dep in _CLIENT_DEPS:
    mod = _ensure_stub(_dep)

# Attach mock functions to stubs that client.py calls at module level
for _router_mod in [n for n in _CLIENT_DEPS if n.endswith(".routers") or n.endswith("_router")]:
    m = sys.modules[_router_mod]
    # Set all get_*_router functions as MagicMocks returning MagicMocks
    for attr in dir(m):
        pass  # Already empty
    # Use __getattr__ fallback
    m.__getattr__ = lambda name, m=m: MagicMock()

# REQUIRE_AUTHENTICATION needs to be a bool
_auth_mod = sys.modules["cognee.modules.users.methods.get_authenticated_user"]
_auth_mod.REQUIRE_AUTHENTICATION = False

# Embedding config for startup banner
_emb_config = sys.modules["cognee.infrastructure.databases.vector.embeddings.config"]
_mock_config = MagicMock()
_mock_config.embedding_model = "test-model"
_mock_config.embedding_provider = "test"
_mock_config.embedding_endpoint = "http://test"
_mock_config.embedding_dimensions = 768
_emb_config.get_embedding_config = MagicMock(return_value=_mock_config)


def _load_lifespan():
    """Load client.py via importlib and return the lifespan function."""
    mod_name = "cognee.api.client"
    # Remove cached version
    sys.modules.pop(mod_name, None)

    client_path = _REPO_ROOT / "cognee" / "api" / "client.py"
    spec = importlib.util.spec_from_file_location(mod_name, client_path)
    mod = importlib.util.module_from_spec(spec)
    sys.modules[mod_name] = mod
    spec.loader.exec_module(mod)
    return mod.lifespan


class TestLifespanLayer2:
    """Tests for the dimension validation inside lifespan()."""

    @pytest.mark.asyncio
    async def test_lifespan_calls_health_check(self):
        """Engine with health_check → called exactly once."""
        mock_engine = MagicMock()
        mock_engine.health_check = AsyncMock(return_value=None)

        mock_db_engine = MagicMock()
        mock_db_engine.create_database = AsyncMock()

        sys.modules["cognee.infrastructure.databases.relational"].get_relational_engine = (
            MagicMock(return_value=mock_db_engine)
        )
        sys.modules["cognee.modules.users.methods"].get_default_user = AsyncMock()
        sys.modules["cognee.infrastructure.databases.vector"].get_vector_engine = (
            MagicMock(return_value=mock_engine)
        )

        lifespan = _load_lifespan()
        app = MagicMock()
        async with lifespan(app):
            pass

        mock_engine.health_check.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_lifespan_skips_without_health_check(self):
        """Engine without health_check attribute → no error."""
        mock_engine = MagicMock(spec=[])

        mock_db_engine = MagicMock()
        mock_db_engine.create_database = AsyncMock()

        sys.modules["cognee.infrastructure.databases.relational"].get_relational_engine = (
            MagicMock(return_value=mock_db_engine)
        )
        sys.modules["cognee.modules.users.methods"].get_default_user = AsyncMock()
        sys.modules["cognee.infrastructure.databases.vector"].get_vector_engine = (
            MagicMock(return_value=mock_engine)
        )

        lifespan = _load_lifespan()
        app = MagicMock()
        async with lifespan(app):
            pass

    @pytest.mark.asyncio
    async def test_lifespan_logs_not_crashes_on_mismatch(self, caplog):
        """health_check raises RuntimeError → logged as STARTUP VALIDATION FAILED, server continues."""
        mock_engine = MagicMock()
        mock_engine.health_check = AsyncMock(
            side_effect=RuntimeError("DIMENSION MISMATCH: server dim=512, engine dim=768")
        )

        mock_db_engine = MagicMock()
        mock_db_engine.create_database = AsyncMock()

        sys.modules["cognee.infrastructure.databases.relational"].get_relational_engine = (
            MagicMock(return_value=mock_db_engine)
        )
        sys.modules["cognee.modules.users.methods"].get_default_user = AsyncMock()
        sys.modules["cognee.infrastructure.databases.vector"].get_vector_engine = (
            MagicMock(return_value=mock_engine)
        )

        lifespan = _load_lifespan()
        app = MagicMock()
        with caplog.at_level(logging.ERROR):
            async with lifespan(app):
                pass

        assert any("STARTUP VALIDATION FAILED" in r.message for r in caplog.records), (
            f"Expected 'STARTUP VALIDATION FAILED' in logs, got: "
            f"{[r.message for r in caplog.records]}"
        )

    @pytest.mark.asyncio
    async def test_lifespan_not_skippable_by_env_var(self):
        """COGNEE_SKIP_CONNECTION_TEST=true → health_check is still called."""
        mock_engine = MagicMock()
        mock_engine.health_check = AsyncMock(return_value=None)

        mock_db_engine = MagicMock()
        mock_db_engine.create_database = AsyncMock()

        sys.modules["cognee.infrastructure.databases.relational"].get_relational_engine = (
            MagicMock(return_value=mock_db_engine)
        )
        sys.modules["cognee.modules.users.methods"].get_default_user = AsyncMock()
        sys.modules["cognee.infrastructure.databases.vector"].get_vector_engine = (
            MagicMock(return_value=mock_engine)
        )

        with patch.dict("os.environ", {"COGNEE_SKIP_CONNECTION_TEST": "true"}):
            lifespan = _load_lifespan()
            app = MagicMock()
            async with lifespan(app):
                pass

        mock_engine.health_check.assert_awaited_once()
