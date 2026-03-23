"""Benchmark scenarios for each MCP tool category."""
import asyncio
import time
import uuid
import json


async def scenario_memory(client, session, headers, iterations=50):
    """Save/recall/list memories. Measures hit rate and latency."""
    results = {
        "saves": [],
        "recalls": [],
        "lists": [],
        "hit_rate": 0.0,
        "errors": 0
    }
    prefix = f"bench_mem_{uuid.uuid4().hex[:6]}"
    hits = 0

    for i in range(iterations):
        key = f"{prefix}_{i}"
        value = f"Benchmark value {i}: test data for precision measurement {uuid.uuid4().hex[:8]}"

        # Save memory
        res, lat = await client.call_tool(session, "save_memory", {
            "key": key,
            "value": value,
            "type": "project"
        }, headers)
        results["saves"].append(lat)

        # Recall memory
        res, lat = await client.call_tool(session, "recall_memory", {
            "query": key
        }, headers)
        results["recalls"].append(lat)

        # Check hit
        try:
            content = json.dumps(res) if isinstance(res, dict) else str(res)
            if value[:20] in content or key in content:
                hits += 1
        except Exception:
            results["errors"] += 1

    # List memories (a few times)
    for _ in range(min(5, iterations)):
        res, lat = await client.call_tool(session, "list_memories", {
            "type": "project"
        }, headers)
        results["lists"].append(lat)

    results["hit_rate"] = round(hits / iterations, 4) if iterations > 0 else 0.0

    # Cleanup
    for i in range(iterations):
        key = f"{prefix}_{i}"
        try:
            await client.call_tool(session, "delete_memory", {"key": key}, headers)
        except Exception:
            pass

    return results


async def scenario_chat(client, session, headers, iterations=20):
    """Save/recall/search chats. Measures latency and recall accuracy."""
    results = {
        "saves": [],
        "recalls": [],
        "searches": [],
        "hit_rate": 0.0,
        "errors": 0
    }
    prefix = f"bench_chat_{uuid.uuid4().hex[:6]}"
    hits = 0

    messages_pool = [
        "How do I configure HNSW parameters for optimal recall?",
        "What is the recommended batch size for vector ingestion?",
        "Explain the WAL recovery process after a crash.",
        "How does sharding work with Raft consensus?",
        "What are the memory requirements for 1M vectors at dim=1024?",
        "How to monitor Cognevra metrics with Prometheus?",
        "What is the difference between cosine and L2 distance?",
        "How to set up replication across 3 nodes?",
        "Explain the arena allocator memory layout.",
        "What is the maximum concurrent query throughput?",
        "How to tune efConstruction and M parameters?",
        "Describe the disk persistence format.",
        "How does the collection manager isolate namespaces?",
        "What compression options are available for vectors?",
        "How to benchmark search latency under load?",
        "Explain the HTTP handler request pipeline.",
        "What is the P99 latency target for production?",
        "How to migrate data between Cognevra versions?",
        "Describe the connection pooling strategy.",
        "How to implement hybrid search with metadata filters?",
    ]

    for i in range(iterations):
        msg = messages_pool[i % len(messages_pool)]
        chat_id = f"{prefix}_{i}"

        # Save chat
        res, lat = await client.call_tool(session, "save_chat", {
            "message": msg,
            "role": "user",
            "session_id": chat_id
        }, headers)
        results["saves"].append(lat)

        # Recall chat
        res, lat = await client.call_tool(session, "recall_chat", {
            "session_id": chat_id
        }, headers)
        results["recalls"].append(lat)

        try:
            content = json.dumps(res) if isinstance(res, dict) else str(res)
            if msg[:15] in content:
                hits += 1
        except Exception:
            results["errors"] += 1

    # Search chats
    search_queries = ["HNSW parameters", "batch ingestion", "WAL recovery", "sharding Raft", "memory vectors"]
    for q in search_queries:
        res, lat = await client.call_tool(session, "search_chats", {
            "query": q,
            "top_k": 5
        }, headers)
        results["searches"].append(lat)

    results["hit_rate"] = round(hits / iterations, 4) if iterations > 0 else 0.0
    return results


async def scenario_knowledge(client, session, headers, iterations=10):
    """Add text -> cognify -> search pipeline. Measures end-to-end latency."""
    results = {
        "add_text": [],
        "cognify": [],
        "search": [],
        "errors": 0
    }
    prefix = f"bench_know_{uuid.uuid4().hex[:6]}"

    texts = [
        "Vector databases use approximate nearest neighbor algorithms like HNSW to enable fast similarity search over high-dimensional embeddings. The key parameters are M (max connections per node) and efConstruction (search width during index building).",
        "Write-ahead logging ensures durability by recording mutations before applying them to the main data structure. On crash recovery, the WAL is replayed to reconstruct the latest consistent state.",
        "Sharding distributes data across multiple nodes to handle larger datasets. Consistent hashing or range-based partitioning determines which shard owns each vector. Raft consensus ensures shard replicas stay synchronized.",
        "The arena allocator pre-allocates a large contiguous memory block and hands out chunks sequentially. This eliminates per-allocation overhead and improves cache locality for vector operations.",
        "Cosine similarity measures the angle between two vectors, while L2 (Euclidean) distance measures the straight-line distance. For normalized vectors, cosine similarity and L2 distance are monotonically related.",
        "Prometheus scrapes metrics endpoints at regular intervals and stores time-series data. Grafana dashboards visualize latency percentiles, QPS, memory usage, and error rates.",
        "Batch ingestion pipelines chunk documents, compute embeddings via a model server, and insert vectors in bulk. Optimal batch sizes balance throughput against memory pressure.",
        "Hybrid search combines dense vector similarity with sparse keyword matching (BM25). Reciprocal rank fusion merges the two ranked lists into a single result set.",
        "Connection pooling reuses TCP connections across requests to avoid handshake overhead. HTTP/2 multiplexing further reduces latency by interleaving streams on a single connection.",
        "Quantization reduces vector storage by compressing 32-bit floats to 8-bit integers or binary codes. Product quantization splits vectors into subspaces for more efficient compression.",
    ]

    for i in range(min(iterations, len(texts))):
        text = texts[i]
        dataset_name = f"{prefix}_{i}"

        # Add text
        res, lat = await client.call_tool(session, "add_text", {
            "text": text,
            "dataset_name": dataset_name
        }, headers)
        results["add_text"].append(lat)

        # Cognify (process/embed)
        res, lat = await client.call_tool(session, "cognify", {
            "dataset_name": dataset_name
        }, headers)
        results["cognify"].append(lat)

    # Wait for processing
    await asyncio.sleep(2)

    # Search
    search_queries = [
        "How does HNSW work?",
        "What is write-ahead logging?",
        "How does sharding distribute data?",
        "Explain arena memory allocation",
        "What is cosine similarity?",
    ]
    for q in search_queries:
        res, lat = await client.call_tool(session, "search", {
            "search_query": q,
            "top_k": 5
        }, headers)
        results["search"].append(lat)

    return results


async def scenario_git(client, session, headers):
    """Analyze commits and search git history. Measures latency."""
    results = {
        "analyze": [],
        "search": [],
        "errors": 0
    }

    # Analyze recent commits
    res, lat = await client.call_tool(session, "analyze_commits", {
        "count": 10
    }, headers)
    results["analyze"].append(lat)

    # Search git history
    queries = [
        "bug fix",
        "feature addition",
        "refactor",
        "performance improvement",
        "documentation update"
    ]
    for q in queries:
        res, lat = await client.call_tool(session, "search_commits", {
            "query": q,
            "top_k": 5
        }, headers)
        results["search"].append(lat)

    return results


async def scenario_concurrent(client, session, headers, tool, arguments, concurrency, iterations):
    """Run N concurrent calls to a tool. Measures throughput under load.

    Returns dict with latencies list and computed stats.
    """
    results = {
        "latencies": [],
        "errors": 0,
        "concurrency": concurrency,
        "iterations": iterations
    }
    sem = asyncio.Semaphore(concurrency)
    lock = asyncio.Lock()

    async def one_call(i):
        async with sem:
            args = dict(arguments)
            # Make args unique if they have a key field
            for field in ("key", "session_id", "dataset_name"):
                if field in args:
                    args[field] = f"{args[field]}_{i}"

            res, lat = await client.call_tool(session, tool, args, headers)
            async with lock:
                results["latencies"].append(lat)
                status = client.call_log[-1][2] if client.call_log else "unknown"
                if status != "ok":
                    results["errors"] += 1

    start = time.monotonic()
    await asyncio.gather(*[one_call(i) for i in range(iterations)])
    wall_time = (time.monotonic() - start) * 1000

    results["wall_time_ms"] = round(wall_time, 2)
    results["effective_qps"] = round(iterations / (wall_time / 1000), 2) if wall_time > 0 else 0

    return results


async def run_concurrency_sweep(client, session, headers, concurrency_levels, iterations_per_level=50):
    """Run concurrent benchmarks at multiple concurrency levels.

    Returns dict mapping concurrency level to results.
    """
    sweep = {}

    tools_to_test = [
        ("recall_memory", {"query": "benchmark test"}),
        ("search", {"search_query": "test query", "top_k": 5}),
    ]

    for c in concurrency_levels:
        sweep[c] = {}
        for tool, args in tools_to_test:
            result = await scenario_concurrent(
                client, session, headers,
                tool, args, c, iterations_per_level
            )
            sweep[c][tool] = result

    return sweep
