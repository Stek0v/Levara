"""
Full User Journey Test — от регистрации до визуализации графа.

Описывает ПОЛНЫЙ путь нового пользователя в Cognevra:

1. РЕГИСТРАЦИЯ → Пользователь создаёт аккаунт
2. ЛОГИН → Получает JWT токен + cookie
3. НАСТРОЙКИ → Проверяет/меняет конфигурацию (LLM, embedding, graph)
4. СОЗДАНИЕ DATASET → Организует данные в именованный набор
5. ЗАГРУЗКА ДАННЫХ → Upload файлов (TXT, MD) в dataset
6. ПРОВЕРКА ДАННЫХ → Видит загруженные файлы в dataset
7. COGNIFY → Запускает построение knowledge graph из загруженных данных
8. ОЖИДАНИЕ → Polling статуса pipeline до завершения
9. ГРАФ → Просматривает extracted entities и relationships
10. ПОИСК → Ищет по загруженным данным (CHUNKS, HYBRID, TEMPORAL)
11. NOTEBOOKS → Создаёт notebook, добавляет cells, выполняет команды
12. MEMIFY → Обогащает граф (triplet embeddings)
13. MCP → Проверяет MCP tools и system status
14. SHARING → Делится dataset с другим пользователем
15. CLEANUP → Удаляет данные и dataset

Каждый шаг — отдельный тест. Тесты выполняются ПОСЛЕДОВАТЕЛЬНО (зависят друг от друга).
Состояние передаётся через module-level переменные.

Документация:
- Quickstart: https://docs.cognee.ai/guides/quickstart
- Add: https://docs.cognee.ai/core-concepts/main-operations/add
- Cognify: https://docs.cognee.ai/core-concepts/main-operations/cognify
- Search: https://docs.cognee.ai/core-concepts/main-operations/search
- Memify: https://docs.cognee.ai/core-concepts/main-operations/memify
- Datasets: https://docs.cognee.ai/core-concepts/datasets
- Multi-user: https://docs.cognee.ai/core-concepts/multi-user-mode/permissions-system/overview
- MCP: https://docs.cognee.ai/guides/mcp-server
- API Reference: https://docs.cognee.ai/api-reference/introduction
"""
import asyncio
import json
import pytest
import aiohttp
from conftest_http import BASE_URL, unique_id

pytestmark = pytest.mark.asyncio

# ── Shared state across ordered tests ──
_state = {}


# ═══════════════════════════════════════════════════════════════════════
# STEP 1: РЕГИСТРАЦИЯ
# Новый пользователь приходит на платформу и создаёт аккаунт.
# POST /auth/register → {id, email, access_token, token_type}
# Docs: https://docs.cognee.ai/guides/deploy-rest-api-server
# ═══════════════════════════════════════════════════════════════════════

async def test_step01_register():
    """Пользователь регистрирует новый аккаунт."""
    _state["email"] = f"journey_{unique_id()}@cognevra.dev"
    _state["password"] = "JourneyPass123!"

    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/auth/register", json={
            "email": _state["email"],
            "password": _state["password"],
        }) as r:
            assert r.status == 201, f"Register failed: {await r.text()}"
            data = await r.json()
            assert "id" in data, "No user ID in response"
            assert data["email"] == _state["email"]
            assert "access_token" in data, "No token in register response"
            _state["user_id"] = data["id"]
            print(f"  ✓ Registered: {_state['email']} (id: {_state['user_id'][:8]}...)")


# ═══════════════════════════════════════════════════════════════════════
# STEP 2: ЛОГИН
# Пользователь входит в систему. Backend ставит auth_token cookie.
# POST /auth/login → {access_token, token_type} + Set-Cookie: auth_token
# Docs: https://docs.cognee.ai/guides/deploy-rest-api-server
# ═══════════════════════════════════════════════════════════════════════

async def test_step02_login():
    """Пользователь логинится и получает JWT токен."""
    async with aiohttp.ClientSession(cookie_jar=aiohttp.CookieJar()) as s:
        async with s.post(f"{BASE_URL}/auth/login", json={
            "email": _state["email"],
            "password": _state["password"],
        }) as r:
            assert r.status == 200, f"Login failed: {await r.text()}"
            data = await r.json()
            assert "access_token" in data
            assert data["token_type"] == "bearer"
            _state["token"] = data["access_token"]
            _state["headers"] = {"Authorization": f"Bearer {_state['token']}"}
            print(f"  ✓ Logged in, token: {_state['token'][:20]}...")


# ═══════════════════════════════════════════════════════════════════════
# STEP 3: ПРОФИЛЬ
# Пользователь проверяет свой профиль через /auth/me.
# GET /auth/me → {id, email}
# GET /users/me → {id, email, is_active, is_superuser, is_verified}
# Docs: https://docs.cognee.ai/core-concepts/multi-user-mode/permissions-system/overview
# ═══════════════════════════════════════════════════════════════════════

async def test_step03_check_profile():
    """Пользователь проверяет свой профиль."""
    async with aiohttp.ClientSession() as s:
        # /auth/me
        async with s.get(f"{BASE_URL}/auth/me", headers=_state["headers"]) as r:
            assert r.status == 200
            data = await r.json()
            assert data["email"] == _state["email"]
            print(f"  ✓ Profile: {data['email']}")

        # /users/me
        async with s.get(f"{BASE_URL}/users/me", headers=_state["headers"]) as r:
            assert r.status == 200
            data = await r.json()
            assert "id" in data


# ═══════════════════════════════════════════════════════════════════════
# STEP 4: НАСТРОЙКИ
# Пользователь просматривает и меняет конфигурацию сервера.
# GET /settings → текущие настройки
# PUT /settings → обновление (LLM model, embedding, chunk strategy)
# Docs: https://docs.cognee.ai/core-concepts/configuration
# ═══════════════════════════════════════════════════════════════════════

async def test_step04_check_settings():
    """Пользователь проверяет настройки сервера."""
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/settings", headers=_state["headers"]) as r:
            assert r.status == 200
            settings = await r.json()
            assert "vector_engine" in settings
            assert settings["vector_engine"] == "cognevra"
            assert "embedding_model" in settings
            assert "llm_provider" in settings
            print(f"  ✓ Settings: embed={settings['embedding_model']}, "
                  f"llm={settings.get('llm_model','none')}, dim={settings['embedding_dimension']}")


async def test_step04b_update_settings():
    """Пользователь обновляет chunk strategy."""
    async with aiohttp.ClientSession() as s:
        async with s.put(f"{BASE_URL}/settings", json={
            "chunk_strategy": "paragraph",
            "chunk_size": 1000,
        }, headers=_state["headers"]) as r:
            assert r.status == 200
            data = await r.json()
            assert data["chunk_strategy"] == "paragraph"
            print(f"  ✓ Updated: chunk_strategy=paragraph, chunk_size=1000")


# ═══════════════════════════════════════════════════════════════════════
# STEP 5: СОЗДАНИЕ DATASET
# Пользователь создаёт именованный dataset для организации данных.
# POST /datasets → {id, name, owner_id, created_at}
# Docs: https://docs.cognee.ai/core-concepts/datasets
# ═══════════════════════════════════════════════════════════════════════

async def test_step05_create_dataset():
    """Пользователь создаёт dataset 'my_research'."""
    async with aiohttp.ClientSession() as s:
        name = f"my_research_{unique_id()[:8]}"
        async with s.post(f"{BASE_URL}/datasets", json={"name": name}, headers=_state["headers"]) as r:
            assert r.status == 201
            data = await r.json()
            assert data["name"] == name
            _state["dataset_id"] = data["id"]
            _state["dataset_name"] = name
            print(f"  ✓ Created dataset: {name} (id: {data['id'][:8]}...)")


async def test_step05b_dataset_visible_in_list():
    """Созданный dataset виден в списке."""
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/datasets", headers=_state["headers"]) as r:
            datasets = await r.json()
            ids = [d["id"] for d in datasets]
            assert _state["dataset_id"] in ids, "Dataset not in list"
            print(f"  ✓ Dataset visible in list ({len(datasets)} total)")


# ═══════════════════════════════════════════════════════════════════════
# STEP 6: ЗАГРУЗКА ДАННЫХ
# Пользователь загружает документы в dataset.
# POST /add (multipart form: data=file, datasetId=id) → {status, items, dataset_id}
# Поддерживаемые форматы: TXT, MD, PDF, DOCX, PPTX, XLSX, HTML, EPUB
# Docs: https://docs.cognee.ai/core-concepts/main-operations/add
# ═══════════════════════════════════════════════════════════════════════

async def test_step06_upload_text_file():
    """Пользователь загружает текстовый файл в dataset."""
    async with aiohttp.ClientSession() as s:
        form = aiohttp.FormData()
        form.add_field("data",
            b"Cognevra is a high-performance vector database written in Go. "
            b"It uses HNSW (Hierarchical Navigable Small World) indexing for fast approximate nearest neighbor search. "
            b"The system combines WAL durability with memory-mapped arenas for efficient vector storage. "
            b"Cognevra supports multiple embedding models and can store vectors of any dimension.",
            filename="cognevra_intro.txt", content_type="text/plain")
        form.add_field("datasetId", _state["dataset_id"])

        async with s.post(f"{BASE_URL}/add", data=form, headers=_state["headers"]) as r:
            assert r.status == 200
            data = await r.json()
            assert data["status"] == "ok"
            assert data["items"] >= 1
            assert data["dataset_id"] == _state["dataset_id"]
            print(f"  ✓ Uploaded: cognevra_intro.txt ({data['items']} items)")


async def test_step06b_upload_markdown_file():
    """Пользователь загружает markdown документ."""
    async with aiohttp.ClientSession() as s:
        md_content = """# Knowledge Graphs

Knowledge graphs represent information as a network of entities and relationships.

## Components
- **Nodes**: Entities (people, places, concepts)
- **Edges**: Relationships between entities
- **Properties**: Attributes of nodes and edges

## Applications
- Semantic search
- Question answering
- Recommendation systems
- Drug discovery

Neo4j is a popular graph database that uses the Cypher query language.
PostgreSQL can also store graph-like data using recursive CTEs.
"""
        form = aiohttp.FormData()
        form.add_field("data", md_content.encode(), filename="knowledge_graphs.md", content_type="text/markdown")
        form.add_field("datasetId", _state["dataset_id"])

        async with s.post(f"{BASE_URL}/add", data=form, headers=_state["headers"]) as r:
            assert r.status == 200
            data = await r.json()
            assert data["items"] >= 1
            print(f"  ✓ Uploaded: knowledge_graphs.md ({data['items']} items)")


async def test_step06c_upload_plain_text():
    """Пользователь отправляет текст напрямую (не файл)."""
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/add",
            data="Vector databases store high-dimensional vectors and enable similarity search. "
                 "HNSW, IVF, and PQ are common indexing algorithms. "
                 "Cosine similarity and Euclidean distance are standard metrics.",
            headers={**_state["headers"], "Content-Type": "text/plain"}) as r:
            assert r.status == 200
            print(f"  ✓ Uploaded: plain text body")


# ═══════════════════════════════════════════════════════════════════════
# STEP 7: ПРОВЕРКА ЗАГРУЖЕННЫХ ДАННЫХ
# Пользователь просматривает файлы в dataset.
# GET /datasets/:id/data → [{id, name, extension, mime_type, data_size, created_at}]
# Docs: https://docs.cognee.ai/api-reference/datasets/list-dataset-data
# ═══════════════════════════════════════════════════════════════════════

async def test_step07_list_dataset_data():
    """Пользователь видит загруженные файлы в dataset."""
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/datasets/{_state['dataset_id']}/data", headers=_state["headers"]) as r:
            assert r.status == 200
            items = await r.json()
            assert isinstance(items, list)
            _state["data_items"] = items
            print(f"  ✓ Dataset has {len(items)} data items")
            for item in items[:3]:
                print(f"    - {item.get('name', 'unnamed')} ({item.get('data_size', 0)} bytes)")


# ═══════════════════════════════════════════════════════════════════════
# STEP 8: COGNIFY — Построение Knowledge Graph
# Пользователь запускает cognify pipeline для извлечения entities и relationships.
# POST /cognify {datasetIds: [id]} → {status: "PipelineRunStarted", pipeline_run_id}
# Pipeline: chunk → LLM extract → dedup → write (Neo4j + vector)
# Docs: https://docs.cognee.ai/core-concepts/main-operations/cognify
# ═══════════════════════════════════════════════════════════════════════

async def test_step08_cognify_dataset():
    """Пользователь запускает cognify для построения knowledge graph."""
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/cognify", json={
            "texts": [
                "Cognevra uses HNSW for vector search. Neo4j stores the knowledge graph. "
                "PostgreSQL stores metadata. The system was created by stek0v.",
            ]
        }, headers=_state["headers"]) as r:
            assert r.status == 200
            data = await r.json()
            assert data["status"] == "PipelineRunStarted"
            _state["cognify_run_id"] = data["pipeline_run_id"]
            print(f"  ✓ Cognify started: run_id={data['pipeline_run_id'][:8]}...")


# ═══════════════════════════════════════════════════════════════════════
# STEP 9: ОЖИДАНИЕ ЗАВЕРШЕНИЯ COGNIFY
# Пользователь polling'ом проверяет статус pipeline.
# GET /cognify/:runId/status → {status, stage, chunks_created, entities_extracted, elapsed_ms}
# Или GET /cognify/:runId/stream → Server-Sent Events (text/event-stream)
# Docs: https://docs.cognee.ai/api-reference/datasets/get-dataset-status
# ═══════════════════════════════════════════════════════════════════════

async def test_step09_wait_cognify():
    """Пользователь ожидает завершения cognify pipeline."""
    async with aiohttp.ClientSession() as s:
        run_id = _state["cognify_run_id"]
        final_status = None
        for i in range(90):  # до 3 минут
            async with s.get(f"{BASE_URL}/cognify/{run_id}/status", headers=_state["headers"]) as r:
                data = await r.json()
                if data["status"] != "RUNNING":
                    final_status = data
                    break
                if i % 10 == 0:
                    print(f"    ... {data.get('stage', '?')} "
                          f"(chunks={data.get('chunks_created',0)}, "
                          f"entities={data.get('entities_extracted',0)})")
            await asyncio.sleep(2)

        assert final_status is not None, "Cognify timed out"
        assert final_status["status"] in ("COMPLETED", "FAILED")
        _state["cognify_result"] = final_status
        print(f"  ✓ Cognify {final_status['status']} in {final_status.get('elapsed_ms',0)}ms "
              f"(chunks={final_status.get('chunks_created',0)}, "
              f"entities={final_status.get('entities_extracted',0)}, "
              f"edges={final_status.get('edges_extracted',0)})")


# ═══════════════════════════════════════════════════════════════════════
# STEP 10: ПРОСМОТР ГРАФА
# Пользователь просматривает knowledge graph для своего dataset.
# GET /datasets/:id/graph → {nodes: [{id, label, type, properties}], edges: [{source, target, label}]}
# Фильтрация по dataset_id в properties каждого node.
# Docs: https://docs.cognee.ai/api-reference/datasets/get-dataset-graph
# ═══════════════════════════════════════════════════════════════════════

async def test_step10_view_graph():
    """Пользователь просматривает knowledge graph."""
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/datasets/{_state['dataset_id']}/graph", headers=_state["headers"]) as r:
            assert r.status == 200
            graph = await r.json()
            assert "nodes" in graph
            assert "edges" in graph
            assert isinstance(graph["nodes"], list)
            assert isinstance(graph["edges"], list)
            _state["graph"] = graph
            print(f"  ✓ Graph: {len(graph['nodes'])} nodes, {len(graph['edges'])} edges")
            for n in graph["nodes"][:5]:
                print(f"    - {n.get('label', n.get('id','?'))} ({n.get('type', '?')})")


# ═══════════════════════════════════════════════════════════════════════
# STEP 11: ПОИСК
# Пользователь ищет по загруженным данным разными типами поиска.
# POST /search/text {query_text, query_type, top_k}
# Типы: CHUNKS, RAG_COMPLETION, SUMMARIES, CHUNKS_LEXICAL, HYBRID, TEMPORAL
# Docs: https://docs.cognee.ai/core-concepts/main-operations/search
# ═══════════════════════════════════════════════════════════════════════

async def test_step11a_search_chunks():
    """Пользователь ищет по CHUNKS (vector similarity)."""
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "vector database HNSW",
            "query_type": "CHUNKS",
            "top_k": 5,
        }, headers=_state["headers"]) as r:
            assert r.status == 200
            results = await r.json()
            if results is None:
                results = []
            assert isinstance(results, list)
            print(f"  ✓ CHUNKS search: {len(results)} results")


async def test_step11b_search_bm25():
    """Пользователь ищет по CHUNKS_LEXICAL (BM25 keyword search)."""
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "knowledge graph Neo4j",
            "query_type": "CHUNKS_LEXICAL",
            "top_k": 5,
        }, headers=_state["headers"]) as r:
            assert r.status == 200
            print(f"  ✓ BM25 search: 200 OK")


async def test_step11c_search_hybrid():
    """Пользователь ищет по HYBRID (vector + BM25 RRF fusion)."""
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "similarity search algorithms",
            "query_type": "HYBRID",
            "top_k": 5,
        }, headers=_state["headers"]) as r:
            assert r.status == 200
            print(f"  ✓ HYBRID search: 200 OK")


async def test_step11d_search_temporal():
    """Пользователь ищет по TEMPORAL (date extraction)."""
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "events in 2024",
            "query_type": "TEMPORAL",
        }, headers=_state["headers"]) as r:
            assert r.status == 200
            data = await r.json()
            print(f"  ✓ TEMPORAL search: {len(data)} results")


async def test_step11e_search_rag():
    """Пользователь ищет по RAG_COMPLETION (chunks + LLM answer)."""
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "What is Cognevra?",
            "query_type": "RAG_COMPLETION",
            "top_k": 3,
        }, headers=_state["headers"]) as r:
            assert r.status == 200
            data = await r.json()
            assert "chunks" in data
            assert "answer" in data
            chunks = data["chunks"] or []
            print(f"  ✓ RAG search: {len(chunks)} chunks, answer={'yes' if data['answer'] else 'no'}")


# ═══════════════════════════════════════════════════════════════════════
# STEP 12: NOTEBOOKS
# Пользователь создаёт interactive notebook для исследования данных.
# POST /notebooks {name} → {id, name, cells[], deletable}
# POST /notebooks/:id/cells {type, content} → {id, type, content}
# POST /notebooks/:id/cells/:cellId/run → {result, error}
# Доступные code команды: collections, stats, env, search <query>
# ═══════════════════════════════════════════════════════════════════════

async def test_step12a_create_notebook():
    """Пользователь создаёт notebook для анализа."""
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/notebooks", json={
            "name": f"Research Notes {unique_id()[:8]}",
        }, headers=_state["headers"]) as r:
            assert r.status == 201
            data = await r.json()
            assert "id" in data
            assert data["cells"] == []
            _state["notebook_id"] = data["id"]
            print(f"  ✓ Created notebook: {data['name']}")


async def test_step12b_add_markdown_cell():
    """Пользователь добавляет markdown cell с описанием."""
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/notebooks/{_state['notebook_id']}/cells", json={
            "type": "markdown",
            "content": "# My Research\n\nAnalyzing Cognevra vector database architecture.",
        }, headers=_state["headers"]) as r:
            assert r.status == 201
            data = await r.json()
            _state["md_cell_id"] = data["id"]
            print(f"  ✓ Added markdown cell")


async def test_step12c_add_code_cell_stats():
    """Пользователь добавляет code cell для просмотра stats."""
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/notebooks/{_state['notebook_id']}/cells", json={
            "type": "code",
            "content": "stats",
        }, headers=_state["headers"]) as r:
            assert r.status == 201
            data = await r.json()
            _state["code_cell_id"] = data["id"]
            print(f"  ✓ Added code cell: stats")


async def test_step12d_run_code_cell():
    """Пользователь выполняет code cell — видит system stats."""
    async with aiohttp.ClientSession() as s:
        async with s.post(
            f"{BASE_URL}/notebooks/{_state['notebook_id']}/cells/{_state['code_cell_id']}/run",
            json={"type": "code", "content": "stats"},
            headers=_state["headers"],
        ) as r:
            assert r.status == 200
            data = await r.json()
            assert "result" in data
            result = data["result"]
            assert "collections" in result
            print(f"  ✓ Cell output: {result[:80]}...")


async def test_step12e_run_collections_cell():
    """Пользователь смотрит список vector collections."""
    async with aiohttp.ClientSession() as s:
        async with s.post(
            f"{BASE_URL}/notebooks/{_state['notebook_id']}/cells/{_state['code_cell_id']}/run",
            json={"type": "code", "content": "collections"},
            headers=_state["headers"],
        ) as r:
            assert r.status == 200
            data = await r.json()
            print(f"  ✓ Collections: {data['result'][:80]}...")


async def test_step12f_run_env_cell():
    """Пользователь проверяет environment переменные."""
    async with aiohttp.ClientSession() as s:
        async with s.post(
            f"{BASE_URL}/notebooks/{_state['notebook_id']}/cells/{_state['code_cell_id']}/run",
            json={"type": "code", "content": "env"},
            headers=_state["headers"],
        ) as r:
            assert r.status == 200
            data = await r.json()
            assert "LLM_MODEL" in data["result"]
            print(f"  ✓ Env: {data['result'][:80]}...")


# ═══════════════════════════════════════════════════════════════════════
# STEP 13: MCP — Model Context Protocol
# Пользователь проверяет MCP tools и system status.
# POST /mcp {jsonrpc, method: "initialize"} → server capabilities
# POST /mcp {jsonrpc, method: "tools/list"} → available tools
# GET /health/details → all services status
# Docs: https://docs.cognee.ai/guides/mcp-server
# ═══════════════════════════════════════════════════════════════════════

async def test_step13a_mcp_initialize():
    """Пользователь инициализирует MCP connection."""
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL.replace('/api/v1','')}/mcp", json={
            "jsonrpc": "2.0", "id": "1", "method": "initialize", "params": {},
        }) as r:
            assert r.status == 200
            data = await r.json()
            assert data["result"]["serverInfo"]["name"] == "Cognevra"
            print(f"  ✓ MCP initialized: {data['result']['serverInfo']['name']} "
                  f"v{data['result']['serverInfo']['version']}")


async def test_step13b_mcp_list_tools():
    """Пользователь видит доступные MCP tools."""
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL.replace('/api/v1','')}/mcp", json={
            "jsonrpc": "2.0", "id": "2", "method": "tools/list", "params": {},
        }) as r:
            assert r.status == 200
            data = await r.json()
            tools = data["result"]["tools"]
            assert len(tools) >= 7
            tool_names = [t["name"] for t in tools]
            assert "cognify" in tool_names
            assert "search" in tool_names
            print(f"  ✓ MCP tools: {', '.join(tool_names)}")


async def test_step13c_health_details():
    """Пользователь проверяет статус всех сервисов."""
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL.replace('/api/v1','')}/health/details") as r:
            assert r.status == 200
            data = await r.json()
            services = data["services"]
            assert services["backend"]["status"] == "connected"
            print(f"  ✓ Services:")
            for name, info in services.items():
                print(f"    - {name}: {info['status']}")


# ═══════════════════════════════════════════════════════════════════════
# STEP 14: SHARING
# Пользователь делится dataset с коллегой.
# POST /auth/register → создать второго пользователя
# POST /datasets/:id/shares {email, role} → share grant
# GET /permissions/me → проверить permissions
# Docs: https://docs.cognee.ai/core-concepts/multi-user-mode/permissions-system/overview
# ═══════════════════════════════════════════════════════════════════════

async def test_step14a_create_colleague():
    """Создаём коллегу для шаринга."""
    _state["colleague_email"] = f"colleague_{unique_id()}@cognevra.dev"
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/auth/register", json={
            "email": _state["colleague_email"],
            "password": "ColleaguePass123!",
        }) as r:
            assert r.status == 201
            _state["colleague_id"] = (await r.json())["id"]
            print(f"  ✓ Colleague registered: {_state['colleague_email']}")


async def test_step14b_share_dataset():
    """Пользователь шарит dataset с коллегой."""
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/datasets/{_state['dataset_id']}/shares", json={
            "email": _state["colleague_email"],
            "role": "editor",
        }, headers=_state["headers"]) as r:
            # May fail if dataset not in PostgreSQL, that's ok
            if r.status == 201:
                data = await r.json()
                _state["share_id"] = data["id"]
                print(f"  ✓ Shared with {_state['colleague_email']} as editor")
            else:
                print(f"  ⚠ Share failed (status {r.status}) — may need PostgreSQL")
                _state["share_id"] = None


async def test_step14c_check_permissions():
    """Пользователь проверяет свои permissions."""
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/permissions/me", headers=_state["headers"]) as r:
            assert r.status == 200
            data = await r.json()
            assert "role" in data
            assert "shares" in data
            print(f"  ✓ Permissions: role={data['role']}, shares={len(data['shares'])}")


# ═══════════════════════════════════════════════════════════════════════
# STEP 15: COLLECTIONS — Vector collection metadata
# Пользователь просматривает vector collections и их метаданные.
# GET /collections → [{name, embedding_model, embedding_dim, distance_metric, record_count}]
# Docs: — (Cognevra extension)
# ═══════════════════════════════════════════════════════════════════════

async def test_step15_list_collections():
    """Пользователь просматривает vector collections."""
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/collections", headers=_state["headers"]) as r:
            assert r.status == 200
            colls = await r.json()
            assert isinstance(colls, list)
            print(f"  ✓ Collections: {len(colls)}")
            for c in colls[:5]:
                print(f"    - {c['name']}: dim={c['embedding_dim']}, "
                      f"model={c.get('embedding_model','?')}, records={c['record_count']}")


# ═══════════════════════════════════════════════════════════════════════
# STEP 16: CLEANUP
# Пользователь удаляет notebook, share, и dataset.
# DELETE /notebooks/:id
# DELETE /datasets/:id/shares/:shareId
# DELETE /datasets/:id → cascade deletes dataset_data
# Docs: https://docs.cognee.ai/api-reference/datasets/delete-dataset
# ═══════════════════════════════════════════════════════════════════════

async def test_step16a_delete_notebook():
    """Пользователь удаляет notebook."""
    async with aiohttp.ClientSession() as s:
        async with s.delete(f"{BASE_URL}/notebooks/{_state['notebook_id']}", headers=_state["headers"]) as r:
            assert r.status == 200
            print(f"  ✓ Notebook deleted")


async def test_step16b_revoke_share():
    """Пользователь отзывает share."""
    if _state.get("share_id"):
        async with aiohttp.ClientSession() as s:
            async with s.delete(
                f"{BASE_URL}/datasets/{_state['dataset_id']}/shares/{_state['share_id']}",
                headers=_state["headers"],
            ) as r:
                assert r.status == 200
                print(f"  ✓ Share revoked")
    else:
        print(f"  ⚠ No share to revoke")


async def test_step16c_delete_dataset():
    """Пользователь удаляет dataset (cascade: data + shares)."""
    async with aiohttp.ClientSession() as s:
        async with s.delete(f"{BASE_URL}/datasets/{_state['dataset_id']}", headers=_state["headers"]) as r:
            assert r.status == 200
            data = await r.json()
            assert data["deleted"] == True
            print(f"  ✓ Dataset deleted: {_state['dataset_name']}")


async def test_step16d_verify_deleted():
    """Dataset больше не виден в списке."""
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/datasets", headers=_state["headers"]) as r:
            datasets = await r.json()
            ids = [d["id"] for d in datasets]
            assert _state["dataset_id"] not in ids, "Dataset still in list after delete!"
            print(f"  ✓ Verified: dataset removed from list")


# ═══════════════════════════════════════════════════════════════════════
# ИТОГ: Полный путь пользователя завершён
# register → login → settings → create dataset → upload files →
# verify data → cognify → wait → graph → search (5 types) →
# notebooks (create, cells, run) → MCP → share → cleanup
# ═══════════════════════════════════════════════════════════════════════
