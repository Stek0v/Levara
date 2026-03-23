"""
VectraDB adapter for Cognee's VectorDBInterface.

VectraDB is a Go-based high-performance vector database using HNSW indexing.
It exposes a REST API (POST /api/v1/insert, POST /api/v1/search).

Limitations vs other adapters:
- No native collection support → simulated via ID prefix "{collection}:{id}"
- No delete-by-ID endpoint → deletions only clear the in-memory cache
- No prune endpoint → clears in-memory state only
- Vector dimension must match at server startup (-dim flag)
"""

import asyncio
import logging
from typing import Any, Dict, List, Optional
from uuid import UUID

try:
    import orjson as _json_lib

    def _to_json_bytes(obj: Any) -> bytes:
        return _json_lib.dumps(obj)

    def _from_json_bytes(b: bytes) -> Any:
        return _json_lib.loads(b)

except ImportError:
    import json as _stdlib_json

    def _to_json_bytes(obj: Any) -> bytes:
        return _stdlib_json.dumps(obj).encode()

    def _from_json_bytes(b: bytes) -> Any:
        return _stdlib_json.loads(b)

import json  # still needed for json.RawMessage / type hints elsewhere

import aiohttp

# Types that are always JSON-native — skip the expensive json.dumps probe.
_JSON_PRIMITIVES = (str, int, float, bool, type(None))

from cognee.infrastructure.databases.exceptions import MissingQueryParameterError
from cognee.infrastructure.engine import DataPoint
from cognee.infrastructure.engine.utils import parse_id
from cognee.modules.storage.utils import get_own_properties

from ..embeddings.EmbeddingEngine import EmbeddingEngine
from ..models.ScoredResult import ScoredResult
from ..vector_db_interface import VectorDBInterface

logger = logging.getLogger(__name__)


class _IndexPoint(DataPoint):
    """Minimal DataPoint used for vector index entries (mirrors LanceDB's IndexSchema)."""

    id: str
    text: str
    metadata: dict = {"index_fields": ["text"]}
    belongs_to_set: List[str] = []


def _serialize_payload(data: Any) -> Any:
    """Recursively convert payload to a JSON-serializable structure.

    Fast-path for the common primitive types avoids an expensive json.dumps()
    probe on every leaf value — this is the hottest path during bulk inserts.
    """
    if isinstance(data, _JSON_PRIMITIVES):
        return data
    if isinstance(data, dict):
        return {k: _serialize_payload(v) for k, v in data.items()}
    if isinstance(data, list):
        return [_serialize_payload(item) for item in data]
    if isinstance(data, UUID):
        return str(data)
    if hasattr(data, "model_dump"):
        return _serialize_payload(data.model_dump())
    if hasattr(data, "__dict__"):
        return _serialize_payload(vars(data))
    return str(data)


class VectraDBAdapter(VectorDBInterface):
    """
    Cognee VectorDBInterface adapter for VectraDB (REST API, Go backend).

    Configuration via environment variables (set in Cognee config):
        VECTOR_DB_PROVIDER=vectradb
        VECTOR_DB_URL=http://localhost:8080   (VectraDB REST base URL)
        VECTOR_DB_KEY=<optional bearer token>

    The VectraDB server must be started with a matching -dim flag:
        ./vectradb -bootstrap=true -dim=<embedding_vector_size>
    """

    name = "VectraDB"

    def __init__(
        self,
        url: Optional[str],
        api_key: Optional[str],
        embedding_engine: EmbeddingEngine,
        database_name: Optional[str] = None,
    ):
        self.url = (url or "http://localhost:8080").rstrip("/")
        self.api_key = api_key
        self.embedding_engine = embedding_engine
        # In-memory collection registry (VectraDB has no native collection concept)
        self._collections: set = set()
        # In-memory payload cache for retrieve-by-ID (VectraDB has no get-by-ID endpoint)
        self._id_cache: Dict[str, Dict] = {}
        self._lock = asyncio.Lock()
        # Persistent session — avoids TCP handshake + SSL setup on every request.
        # Lazily created so the adapter can be instantiated outside an event loop.
        self._session: Optional[aiohttp.ClientSession] = None
        self._session_lock = asyncio.Lock()
        # Embedding cache: avoids redundant embedding calls on re-indexing
        self._embedding_cache: Dict[str, List[float]] = {}
        self._embedding_cache_maxsize = 4096
        # ID payload cache: bounded to prevent unbounded memory growth
        self._id_cache_maxsize = 65536

    # ------------------------------------------------------------------ helpers

    def _headers(self) -> Dict[str, str]:
        headers = {"Content-Type": "application/json"}
        if self.api_key:
            headers["Authorization"] = f"Bearer {self.api_key}"
        return headers

    def _prefixed_id(self, collection_name: str, data_point_id: Any) -> str:
        return f"{collection_name}:{data_point_id}"

    def _strip_prefix(self, collection_name: str, prefixed_id: str) -> str:
        prefix = f"{collection_name}:"
        return prefixed_id[len(prefix):]

    def _open_session(self) -> Optional[aiohttp.ClientSession]:
        """Return session if already open, else None (sync, no coroutine overhead)."""
        s = self._session
        return s if s is not None and not s.closed else None

    async def _get_session(self) -> aiohttp.ClientSession:
        """Return the persistent ClientSession, creating it on first use."""
        if self._session is None or self._session.closed:
            async with self._session_lock:
                if self._session is None or self._session.closed:
                    connector = aiohttp.TCPConnector(limit=64, limit_per_host=32)
                    self._session = aiohttp.ClientSession(
                        headers=self._headers(),
                        connector=connector,
                    )
        return self._session

    async def close(self) -> None:
        """Close the underlying HTTP session. Call when the adapter is no longer needed."""
        if self._session and not self._session.closed:
            await self._session.close()
            self._session = None

    async def _post(self, path: str, payload: dict) -> dict:
        session = self._open_session() or await self._get_session()
        async with session.post(f"{self.url}{path}", data=_to_json_bytes(payload)) as resp:
            resp.raise_for_status()
            return await resp.json(loads=_from_json_bytes)

    async def _batch_post(self, records: List[dict]) -> dict:
        """
        POST /api/v1/batch_insert — one HTTP call for the whole batch.
        Falls back to individual _post calls if the server returns 404
        (i.e. running against an older VectraDB without batch support).
        """
        session = self._open_session() or await self._get_session()
        async with session.post(
            f"{self.url}/api/v1/batch_insert",
            data=_to_json_bytes({"records": records}),
        ) as resp:
            if resp.status == 404:
                # Graceful degradation: old server without batch endpoint
                logger.debug("VectraDB: /api/v1/batch_insert not found, falling back to single inserts")
                tasks = [self._post("/api/v1/insert", r) for r in records]
                results = await asyncio.gather(*tasks, return_exceptions=True)
                for r in results:
                    if isinstance(r, Exception):
                        raise r
                return {"inserted": len(records), "failed": 0}
            resp.raise_for_status()
            return await resp.json(loads=_from_json_bytes)

    # ------------------------------------------------------------------ embed

    async def embed_data(self, data: List[str]) -> List[List[float]]:
        results: List[Optional[List[float]]] = [None] * len(data)
        uncached_texts: List[str] = []
        uncached_idx: List[int] = []
        for i, text in enumerate(data):
            if text in self._embedding_cache:
                results[i] = self._embedding_cache[text]
            else:
                uncached_texts.append(text)
                uncached_idx.append(i)
        if uncached_texts:
            vecs = await self.embedding_engine.embed_text(uncached_texts)
            for idx, text, vec in zip(uncached_idx, uncached_texts, vecs):
                if len(self._embedding_cache) >= self._embedding_cache_maxsize:
                    del self._embedding_cache[next(iter(self._embedding_cache))]
                self._embedding_cache[text] = vec
                results[idx] = vec
        return results  # type: ignore[return-value]

    # ------------------------------------------------------------------ collections

    async def has_collection(self, collection_name: str) -> bool:
        return collection_name in self._collections

    async def create_collection(self, collection_name: str, payload_schema: Any = None):
        self._collections.add(collection_name)

    # ------------------------------------------------------------------ data points

    async def create_data_points(self, collection_name: str, data_points: List[DataPoint]):
        self._collections.add(collection_name)

        texts = [DataPoint.get_embeddable_data(dp) for dp in data_points]
        vectors = await self.embed_data(texts)

        # Serialise all payloads and populate the local cache before issuing
        # any network calls.
        batch_records = []
        for dp, vector in zip(data_points, vectors):
            record_id = self._prefixed_id(collection_name, dp.id)
            properties = get_own_properties(dp)
            properties["id"] = str(properties["id"])
            serialized = _serialize_payload(properties)
            if len(self._id_cache) >= self._id_cache_maxsize:
                del self._id_cache[next(iter(self._id_cache))]
            self._id_cache[record_id] = serialized
            batch_records.append({"id": record_id, "vector": vector, "metadata": serialized})

        # Single HTTP call for the entire batch → server groups by shard,
        # fires one Raft.Apply() per shard concurrently (≤ numShards round-trips
        # instead of len(data_points) round-trips).
        result = await self._batch_post(batch_records)
        if result.get("failed", 0):
            raise RuntimeError(
                f"VectraDB batch insert partial failure: "
                f"{result['failed']} records failed. Errors: {result.get('errors', [])}"
            )

    async def retrieve(self, collection_name: str, data_point_ids: List[str]) -> List[ScoredResult]:
        """
        Retrieve data points by ID.
        Uses in-memory cache populated during create_data_points.
        Falls back to an empty list for IDs not in the cache (e.g., after a server restart).
        """
        results = []
        for dp_id in data_point_ids:
            record_id = self._prefixed_id(collection_name, str(dp_id))
            if record_id in self._id_cache:
                results.append(
                    ScoredResult(
                        id=parse_id(str(dp_id)),
                        payload=self._id_cache[record_id],
                        score=0.0,
                    )
                )
            else:
                logger.debug(
                    "VectraDB retrieve: cache miss for %s/%s (restart may have cleared cache)",
                    collection_name,
                    dp_id,
                )
        return results

    # ------------------------------------------------------------------ search

    async def search(
        self,
        collection_name: str,
        query_text: Optional[str] = None,
        query_vector: Optional[List[float]] = None,
        limit: Optional[int] = 15,
        with_vector: bool = False,
        include_payload: bool = False,
        node_name: Optional[List[str]] = None,
    ) -> List[ScoredResult]:
        if query_text is None and query_vector is None:
            raise MissingQueryParameterError()

        if query_text and not query_vector:
            query_vector = (await self.embed_data([query_text]))[0]

        effective_limit = limit or 15

        # Over-fetch to compensate for client-side collection filtering.
        # VectraDB has no server-side filtering; we filter results by ID prefix.
        num_collections = max(len(self._collections), 1)
        fetch_k = min(effective_limit * max(num_collections, 4), 1000)

        response = await self._post("/api/v1/search", {"vector": query_vector, "k": fetch_k})
        raw_results = response.get("results", [])

        # Filter by collection (ID prefix)
        prefix = f"{collection_name}:"
        prefix_len = len(prefix)
        filtered = [r for r in raw_results if r["id"].startswith(prefix)]

        # Filter by belongs_to_set if node_name is provided
        if node_name:
            node_name_set = set(node_name)
            filtered = [
                r
                for r in filtered
                if any(x in node_name_set for x in (r.get("metadata") or {}).get("belongs_to_set", ()))
            ]

        filtered = filtered[:effective_limit]

        return [
            ScoredResult(
                id=parse_id(r["id"][prefix_len:]),
                payload=r.get("metadata") if include_payload else None,
                # VectraDB score is similarity (higher = better).
                # Cognee ScoredResult.score convention: lower = better.
                score=1.0 - float(r["score"]),
            )
            for r in filtered
        ]

    async def batch_search(
        self,
        collection_name: str,
        query_texts: List[str],
        limit: Optional[int] = None,
        with_vectors: bool = False,
        include_payload: bool = False,
        node_name: Optional[List[str]] = None,
    ) -> List[List[ScoredResult]]:
        query_vectors = await self.embed_data(query_texts)
        return await asyncio.gather(
            *[
                self.search(
                    collection_name=collection_name,
                    query_vector=qv,
                    limit=limit,
                    with_vector=with_vectors,
                    include_payload=include_payload,
                    node_name=node_name,
                )
                for qv in query_vectors
            ]
        )

    # ------------------------------------------------------------------ delete / prune

    async def delete_data_points(self, collection_name: str, data_point_ids: List[UUID]):
        """
        VectraDB has no delete-by-ID endpoint.
        Removes from in-memory cache only; the vector remains in the index until server restart.
        """
        for dp_id in data_point_ids:
            self._id_cache.pop(self._prefixed_id(collection_name, str(dp_id)), None)

    async def prune(self):
        """
        VectraDB has no bulk-delete endpoint.
        Clears in-memory collection registry and payload cache.
        The persistent Sled/HNSW data on disk is NOT cleared by this call.
        """
        logger.warning(
            "VectraDB prune(): clearing in-memory state only. "
            "Persistent vector data on the VectraDB server is NOT deleted. "
            "Restart VectraDB with a fresh data directory to fully prune."
        )
        self._collections.clear()
        self._id_cache.clear()
        self._embedding_cache.clear()

    # ------------------------------------------------------------------ indexing

    async def create_vector_index(self, index_name: str, index_property_name: str):
        collection_name = f"{index_name}_{index_property_name}"
        await self.create_collection(collection_name)

    async def index_data_points(
        self, index_name: str, index_property_name: str, data_points: List[DataPoint]
    ):
        collection_name = f"{index_name}_{index_property_name}"
        index_points = [
            _IndexPoint(
                id=str(dp.id),
                text=getattr(dp, dp.metadata["index_fields"][0]),
                belongs_to_set=(dp.belongs_to_set or []),
            )
            for dp in data_points
        ]
        await self.create_data_points(collection_name, index_points)
