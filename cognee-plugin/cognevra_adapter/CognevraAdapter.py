"""
Cognevra adapter for Cognee's VectorDBInterface.

Uses gRPC transport to communicate with Cognevra Go server (port 50051).
Native collections, real delete (HNSW tombstone + WAL), binary protobuf encoding.
"""

import asyncio
import json
import logging
from typing import Any, Dict, List, Optional
from uuid import UUID

import grpc
import grpc.aio

from cognee.infrastructure.databases.exceptions import MissingQueryParameterError
from cognee.infrastructure.engine import DataPoint
from cognee.infrastructure.engine.utils import parse_id
from cognee.modules.storage.utils import get_own_properties

from ..embeddings.EmbeddingEngine import EmbeddingEngine
from ..models.ScoredResult import ScoredResult
from ..vector_db_interface import VectorDBInterface

from .generated import cognevra_pb2 as pb
from .generated import cognevra_pb2_grpc as pb_grpc

logger = logging.getLogger(__name__)


def _serialize_for_json(obj: Any) -> Any:
    """Convert obj to a JSON-serializable structure (handles UUID, Pydantic models)."""
    if isinstance(obj, (str, int, float, bool, type(None))):
        return obj
    if isinstance(obj, dict):
        return {k: _serialize_for_json(v) for k, v in obj.items()}
    if isinstance(obj, list):
        return [_serialize_for_json(item) for item in obj]
    if isinstance(obj, UUID):
        return str(obj)
    if hasattr(obj, "model_dump"):
        return _serialize_for_json(obj.model_dump())
    if hasattr(obj, "__dict__"):
        return _serialize_for_json(vars(obj))
    return str(obj)


class _IndexPoint(DataPoint):
    """Minimal DataPoint used for vector index entries."""

    id: str
    text: str
    metadata: dict = {"index_fields": ["text"]}
    belongs_to_set: List[str] = []


class CognevraAdapter(VectorDBInterface):
    """
    Cognee VectorDBInterface adapter for Cognevra (gRPC, Go backend).

    Configuration:
        VECTOR_DB_PROVIDER=cognevra
        VECTOR_DB_URL=localhost:50051   (gRPC address, no http://)
    """

    name = "Cognevra"

    def __init__(
        self,
        url: Optional[str],
        api_key: Optional[str],
        embedding_engine: EmbeddingEngine,
        database_name: Optional[str] = None,
    ):
        raw_url = url or "localhost:50051"
        self.url = raw_url.replace("http://", "").replace("https://", "")
        self.embedding_engine = embedding_engine
        self._channel = grpc.aio.insecure_channel(self.url)
        self._stub = pb_grpc.CognevraServiceStub(self._channel)
        self._embedding_cache: Dict[str, List[float]] = {}
        self._embedding_cache_maxsize = 4096

    # ------------------------------------------------------------------ helpers

    async def _safe_call(self, coro):
        """Wrap gRPC calls with human-readable errors."""
        try:
            return await coro
        except grpc.aio.AioRpcError as e:
            if e.code() in (grpc.StatusCode.UNAVAILABLE, grpc.StatusCode.DEADLINE_EXCEEDED):
                raise ConnectionError(
                    f"Cognevra unavailable at {self.url}: {e.details()}"
                ) from e
            raise RuntimeError(
                f"Cognevra gRPC error ({e.code().name}): {e.details()}"
            ) from e

    async def close(self) -> None:
        """Close the gRPC channel. Must be called on shutdown."""
        await self._channel.close()

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
        return results

    # ------------------------------------------------------------------ collections

    async def has_collection(self, collection_name: str) -> bool:
        resp = await self._safe_call(
            self._stub.HasCollection(pb.HasCollectionReq(name=collection_name))
        )
        return resp.exists

    async def create_collection(self, collection_name: str, payload_schema: Any = None):
        resp = await self._safe_call(
            self._stub.CreateCollection(pb.CreateCollectionReq(name=collection_name))
        )
        if not resp.ok:
            raise RuntimeError(f"Cognevra CreateCollection failed: {resp.error}")

    # ------------------------------------------------------------------ data points

    async def create_data_points(self, collection_name: str, data_points: List[DataPoint]):
        texts = [DataPoint.get_embeddable_data(dp) for dp in data_points]
        vectors = await self.embed_data(texts)

        records = []
        for dp, vector in zip(data_points, vectors):
            properties = get_own_properties(dp)
            properties["id"] = str(properties["id"])
            serialized = _serialize_for_json(properties)
            records.append(pb.InsertRecord(
                id=str(dp.id),
                vector=vector,
                metadata_json=json.dumps(serialized, ensure_ascii=False),
            ))

        resp = await self._safe_call(
            self._stub.BatchInsert(pb.BatchInsertReq(
                collection=collection_name,
                records=records,
            ))
        )
        if resp.failed:
            raise RuntimeError(
                f"Cognevra batch insert partial failure: "
                f"{resp.failed} records failed. Errors: {list(resp.errors)}"
            )

    async def retrieve(self, collection_name: str, data_point_ids: List[str]) -> List[ScoredResult]:
        resp = await self._safe_call(
            self._stub.GetByID(pb.GetByIDReq(
                collection=collection_name,
                ids=[str(dp_id) for dp_id in data_point_ids],
            ))
        )
        results = []
        for record in resp.records:
            if record.found:
                payload = json.loads(record.metadata_json) if record.metadata_json else {}
                results.append(ScoredResult(
                    id=parse_id(record.id),
                    payload=payload,
                    score=0.0,
                ))
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
        fetch_k = effective_limit * 4 if node_name else effective_limit

        resp = await self._safe_call(
            self._stub.Search(pb.SearchReq(
                collection=collection_name,
                vector=query_vector,
                top_k=fetch_k,
            ))
        )

        results = list(resp.results)

        if node_name:
            node_name_set = set(node_name)
            filtered = []
            for r in results:
                meta = json.loads(r.metadata_json) if r.metadata_json else {}
                if any(x in node_name_set for x in meta.get("belongs_to_set", ())):
                    filtered.append(r)
            results = filtered

        results = results[:effective_limit]

        return [
            ScoredResult(
                id=parse_id(r.id),
                payload=json.loads(r.metadata_json) if include_payload and r.metadata_json else None,
                score=1.0 - float(r.score),
            )
            for r in results
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
        ids = [str(dp_id) for dp_id in data_point_ids]
        await self._safe_call(
            self._stub.Delete(pb.DeleteReq(collection=collection_name, ids=ids))
        )

    async def prune(self):
        resp = await self._safe_call(self._stub.ListCollections(pb.Empty()))
        for col_name in resp.collections:
            await self._safe_call(
                self._stub.DropCollection(pb.DropCollectionReq(name=col_name))
            )
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

    # ------------------------------------------------------------------ chunking

    async def chunk_text(
        self,
        text: str,
        max_chunk_size: int = 600,
        document_id: str = "",
        strategy: str = "merged",
        char_per_token: float = 4.0,
    ) -> list:
        """Chunk text using Go chunker via gRPC.

        Args:
            text: Input text to chunk
            max_chunk_size: Maximum chunk size in TOKENS
            document_id: Parent document UUID string (for deterministic chunk IDs)
            strategy: "merged" (default), "paragraph", or "sentence"
            char_per_token: Estimated characters per token (model-dependent)

        Returns:
            List of dicts with keys: id, text, chunk_size, chunk_index, cut_type
            chunk_size is estimated from char count (actual token counting done by caller)
        """
        estimated_max_chars = int(max_chunk_size * char_per_token)

        resp = await self._safe_call(
            self._stub.ChunkText(pb.ChunkTextReq(
                text=text,
                strategy=strategy,
                min_chunk_chars=80,
                max_chunk_chars=estimated_max_chars,
                document_id=document_id,
            ))
        )

        cut_type_map = {"paragraph": "paragraph_end", "sentence": "sentence_cut"}

        return [
            {
                "id": chunk.id,
                "text": chunk.text,
                "chunk_size": len(chunk.text.split()),  # word count estimate; caller should use tokenizer
                "chunk_index": chunk.chunk_index,
                "cut_type": cut_type_map.get(chunk.cut_type, "default"),
                "chapter": chunk.chapter,
            }
            for chunk in resp.chunks
        ]
