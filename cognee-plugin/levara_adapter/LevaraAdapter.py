"""
Levara adapter for Cognee's VectorDBInterface.

Uses gRPC transport to communicate with Levara Go server (port 50051).
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

from .generated import levara_pb2 as pb
from .generated import levara_pb2_grpc as pb_grpc

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


class LevaraAdapter(VectorDBInterface):
    """
    Cognee VectorDBInterface adapter for Levara (gRPC, Go backend).

    Configuration:
        VECTOR_DB_PROVIDER=levara
        VECTOR_DB_URL=localhost:50051   (gRPC address, no http://)
    """

    name = "Levara"

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
        self._stub = pb_grpc.LevaraServiceStub(self._channel)
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
                    f"Levara unavailable at {self.url}: {e.details()}"
                ) from e
            raise RuntimeError(
                f"Levara gRPC error ({e.code().name}): {e.details()}"
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
            raise RuntimeError(f"Levara CreateCollection failed: {resp.error}")

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
                f"Levara batch insert partial failure: "
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

    # ------------------------------------------------------------------ triplets

    async def process_triplets(
        self,
        nodes: list[dict],
        edges: list[dict],
    ) -> list[dict]:
        """Process graph edges into deduplicated triplets via Go gRPC.

        Args:
            nodes: List of dicts with keys: id, text
            edges: List of dicts with keys: source_id, target_id, relationship_name, edge_text (optional)

        Returns:
            List of dicts with keys: id, from_node_id, to_node_id, text
        """
        pb_nodes = [
            pb.GraphNode(id=str(n.get("id", "")), text=str(n.get("text", "")))
            for n in nodes
        ]
        pb_edges = [
            pb.GraphEdge(
                source_id=str(e.get("source_id", "")),
                target_id=str(e.get("target_id", "")),
                relationship_name=str(e.get("relationship_name", "")),
                edge_text=str(e.get("edge_text", "")),
            )
            for e in edges
        ]

        resp = await self._safe_call(
            self._stub.ProcessTriplets(pb.ProcessTripletsReq(
                nodes=pb_nodes,
                edges=pb_edges,
            ))
        )

        return [
            {
                "id": t.id,
                "from_node_id": t.from_node_id,
                "to_node_id": t.to_node_id,
                "text": t.text,
            }
            for t in resp.triplets
        ]

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

    # ------------------------------------------------------------------ file I/O

    async def hash_files(self, file_paths: list[str], max_concurrent: int = 8) -> list[dict]:
        """Hash files in parallel via Go goroutine pool.

        Returns list of dicts with: file_path, sha256, file_size, mime_type, error
        """
        resp = await self._safe_call(
            self._stub.HashFiles(pb.HashFilesReq(
                file_paths=file_paths,
                max_concurrent=max_concurrent,
            ))
        )
        return [
            {
                "file_path": r.file_path,
                "sha256": r.sha256,
                "file_size": r.file_size,
                "mime_type": r.mime_type,
                "error": r.error,
            }
            for r in resp.results
        ]

    async def list_directory(self, root_path: str, recursive: bool = True, extensions: list[str] | None = None) -> list[str]:
        """List files in directory via Go concurrent traversal.

        Returns list of absolute file paths.
        """
        resp = await self._safe_call(
            self._stub.ListDirectory(pb.ListDirectoryReq(
                root_path=root_path,
                recursive=recursive,
                extensions=extensions or [],
            ))
        )
        return list(resp.file_paths)

    # ------------------------------------------------------------------ search aggregation

    async def aggregate_search(
        self,
        edges: list[dict],
        top_k: int = 10,
    ) -> dict:
        """Rank and format search results via Go aggregator.

        Args:
            edges: List of dicts with keys: source_id, source_name, source_text,
                   source_distance, target_id, target_name, target_text,
                   target_distance, relationship_name, edge_distance
            top_k: Number of top results to return

        Returns:
            Dict with: ranked_edges (list), formatted_context (str), unique_nodes (int)
        """
        pb_edges = [
            pb.ScoredEdge(
                source_id=str(e.get("source_id", "")),
                source_name=str(e.get("source_name", "")),
                source_text=str(e.get("source_text", "")),
                source_distance=float(e.get("source_distance", 0)),
                target_id=str(e.get("target_id", "")),
                target_name=str(e.get("target_name", "")),
                target_text=str(e.get("target_text", "")),
                target_distance=float(e.get("target_distance", 0)),
                relationship_name=str(e.get("relationship_name", "")),
                edge_distance=float(e.get("edge_distance", 0)),
            )
            for e in edges
        ]

        resp = await self._safe_call(
            self._stub.AggregateSearch(pb.AggregateSearchReq(
                edges=pb_edges,
                top_k=top_k,
            ))
        )

        return {
            "ranked_edges": [
                {
                    "source_id": r.source_id,
                    "source_name": r.source_name,
                    "target_id": r.target_id,
                    "target_name": r.target_name,
                    "relationship_name": r.relationship_name,
                    "score": r.score,
                }
                for r in resp.ranked_edges
            ],
            "formatted_context": resp.formatted_context,
            "unique_nodes": resp.unique_nodes,
        }
