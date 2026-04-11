# Phase 1 Test Results — 2026-04-10

## Summary

| Suite | Tests | Passed | Failed | Time |
|-------|-------|--------|--------|------|
| Go: chunker/sliding | 19 | 19 | 0 | 0.7s |
| Go: rerank | 11 | 11 | 0 | 0.6s |
| Go: pipeline | 3 | 3 | 0 | 1.3s |
| Python: RAG mode (MCP) | 12 | 12 | 0 | 13.4s |
| Python: regression (smoke+palace) | 34 | 34 | 0 | 3.6s |
| **Total** | **79** | **79** | **0** | **19.6s** |

---

## Go: pkg/chunker — Sliding Window (19 tests)

```
=== RUN   TestChunkBySliding_Empty
--- PASS: TestChunkBySliding_Empty (0.00s)
=== RUN   TestChunkBySliding_WhitespaceOnly
--- PASS: TestChunkBySliding_WhitespaceOnly (0.00s)
=== RUN   TestChunkBySliding_ShorterThanWindow
--- PASS: TestChunkBySliding_ShorterThanWindow (0.00s)
=== RUN   TestChunkBySliding_ExactWindow
--- PASS: TestChunkBySliding_ExactWindow (0.00s)
=== RUN   TestChunkBySliding_WindowPlusOne
--- PASS: TestChunkBySliding_WindowPlusOne (0.00s)
=== RUN   TestChunkBySliding_TwoFullChunks
--- PASS: TestChunkBySliding_TwoFullChunks (0.00s)
=== RUN   TestChunkBySliding_OverlapContent
--- PASS: TestChunkBySliding_OverlapContent (0.00s)
=== RUN   TestChunkBySliding_OverlapClamping
--- PASS: TestChunkBySliding_OverlapClamping (0.00s)
=== RUN   TestChunkBySliding_OverlapLargerThanWindow
--- PASS: TestChunkBySliding_OverlapLargerThanWindow (0.00s)
=== RUN   TestChunkBySliding_ZeroOverlap
--- PASS: TestChunkBySliding_ZeroOverlap (0.00s)
=== RUN   TestChunkBySliding_UnicodeRunes
--- PASS: TestChunkBySliding_UnicodeRunes (0.00s)
=== RUN   TestChunkBySliding_CRLFNormalization
--- PASS: TestChunkBySliding_CRLFNormalization (0.00s)
=== RUN   TestChunkBySliding_SmallWindowFiltered
--- PASS: TestChunkBySliding_SmallWindowFiltered (0.00s)
=== RUN   TestChunkBySliding_LargeText
    sliding_test.go:199: 1MB text: 2334 chunks in 14.7ms
--- PASS: TestChunkBySliding_LargeText (0.02s)
=== RUN   TestChunkBySliding_DeterministicIDs
--- PASS: TestChunkBySliding_DeterministicIDs (0.00s)
=== RUN   TestChunkBySliding_ChunkIndex
--- PASS: TestChunkBySliding_ChunkIndex (0.00s)
=== RUN   TestChunkBySliding_CutType
--- PASS: TestChunkBySliding_CutType (0.00s)
=== RUN   TestChunkBySliding_NegativeOverlap
--- PASS: TestChunkBySliding_NegativeOverlap (0.00s)
=== RUN   TestChunkBySliding_ZeroWindow
--- PASS: TestChunkBySliding_ZeroWindow (0.00s)
PASS
ok  github.com/stek0v/cognevra/pkg/chunker  0.702s
```

## Go: pkg/rerank — Reranker Client (11 tests)

```
=== RUN   TestRerank_Success
--- PASS: TestRerank_Success (0.00s)
=== RUN   TestRerank_EmptyEndpoint
--- PASS: TestRerank_EmptyEndpoint (0.00s)
=== RUN   TestRerank_NilClient
--- PASS: TestRerank_NilClient (0.00s)
=== RUN   TestRerank_Timeout
--- PASS: TestRerank_Timeout (0.20s)
=== RUN   TestRerank_ServerError
--- PASS: TestRerank_ServerError (0.00s)
=== RUN   TestRerank_EmptyDocuments
--- PASS: TestRerank_EmptyDocuments (0.00s)
=== RUN   TestRerank_SingleDocument
--- PASS: TestRerank_SingleDocument (0.00s)
=== RUN   TestRerank_CohereFormat
--- PASS: TestRerank_CohereFormat (0.00s)
=== RUN   TestRerank_AlternativeScoreField
--- PASS: TestRerank_AlternativeScoreField (0.00s)
=== RUN   TestRerank_MalformedJSON
--- PASS: TestRerank_MalformedJSON (0.00s)
=== RUN   TestRerank_TopNRespected
--- PASS: TestRerank_TopNRespected (0.00s)
PASS
ok  github.com/stek0v/cognevra/pkg/rerank  0.607s
```

## Go: pipeline — SearchPipeline (3 tests)

```
=== RUN   TestSearchByVector
    search_test.go:84: SearchByVector: top=target-1 score=1.0000, 10 results
--- PASS: TestSearchByVector (0.69s)
=== RUN   TestSearchCollectionIsolation
--- PASS: TestSearchCollectionIsolation (0.13s)
=== RUN   TestSearchNonExistentCollection
--- PASS: TestSearchNonExistentCollection (0.00s)
PASS
ok  github.com/stek0v/cognevra/pipeline  1.263s
```

## Python: test_mcp_rag_mode.py — RAG Mode (12 tests)

```
test_mcp_rag_mode.py::TestRAGModeCognify::test_rag_mode_basic PASSED            [  8%]
test_mcp_rag_mode.py::TestRAGModeCognify::test_rag_mode_no_llm_required PASSED  [ 16%]
test_mcp_rag_mode.py::TestRAGModeCognify::test_rag_mode_with_room_tags PASSED   [ 25%]
test_mcp_rag_mode.py::TestRAGModeCognify::test_full_mode_default PASSED         [ 33%]
test_mcp_rag_mode.py::TestRAGModeCognify::test_rag_mode_speed PASSED            [ 41%]
test_mcp_rag_mode.py::TestSlidingWindowChunking::test_sliding_basic PASSED      [ 50%]
test_mcp_rag_mode.py::TestSlidingWindowChunking::test_sliding_vs_merged PASSED  [ 58%]
test_mcp_rag_mode.py::TestSlidingWindowChunking::test_sliding_custom_overlap PASSED [ 66%]
test_mcp_rag_mode.py::TestReranker::test_rerank_flag_no_error PASSED            [ 75%]
test_mcp_rag_mode.py::TestReranker::test_rerank_false_default PASSED            [ 83%]
test_mcp_rag_mode.py::TestReranker::test_rerank_empty_results PASSED            [ 91%]
test_mcp_rag_mode.py::TestRAGAndFullIsolation::test_rag_then_full_same_collection PASSED [100%]

12 passed in 13.39s
```

## Python: Regression — Smoke + Palace (34 tests)

```
test_mcp_smoke.py::TestProtocol::test_health PASSED                             [  2%]
test_mcp_smoke.py::TestProtocol::test_initialize_returns_capabilities PASSED    [  5%]
test_mcp_smoke.py::TestProtocol::test_tools_list_returns_19 PASSED             [  8%]
test_mcp_smoke.py::TestProtocol::test_notification_returns_202 PASSED          [ 11%]
test_mcp_smoke.py::TestProtocol::test_ping PASSED                              [ 14%]
test_mcp_smoke.py::TestProtocol::test_invalid_session_returns_404 PASSED       [ 17%]
test_mcp_smoke.py::TestProtocol::test_unknown_method_returns_error PASSED      [ 20%]
test_mcp_smoke.py::TestBasicTools::test_save_memory PASSED                     [ 23%]
test_mcp_smoke.py::TestBasicTools::test_list_memories PASSED                   [ 26%]
test_mcp_smoke.py::TestBasicTools::test_recall_memory_like PASSED              [ 29%]
test_mcp_smoke.py::TestBasicTools::test_save_chat PASSED                       [ 32%]
test_mcp_smoke.py::TestBasicTools::test_recall_chat PASSED                     [ 35%]
test_mcp_smoke.py::TestBasicTools::test_list_data PASSED                       [ 38%]
test_mcp_smoke.py::TestBasicTools::test_get_project_context PASSED             [ 41%]
test_mcp_smoke.py::TestBasicTools::test_search_empty_collection PASSED         [ 44%]
test_mcp_smoke.py::TestResources::test_resources_list PASSED                   [ 47%]
test_mcp_smoke.py::TestResources::test_resources_read_collections PASSED       [ 50%]
test_mcp_palace.py::TestPalaceToolsRegistered::test_palace_tools_present PASSED [ 52%]
test_mcp_palace.py::TestPalaceToolsRegistered::test_save_memory_schema_has_room_hall PASSED [ 55%]
test_mcp_palace.py::TestRoomHallMemories::test_save_with_room_hall PASSED      [ 58%]
test_mcp_palace.py::TestRoomHallMemories::test_invalid_hall_rejected PASSED    [ 61%]
test_mcp_palace.py::TestRoomHallMemories::test_recall_filters_by_hall PASSED   [ 64%]
test_mcp_palace.py::TestRoomHallMemories::test_list_filters_by_room PASSED     [ 67%]
test_mcp_palace.py::TestPinAndWakeUp::test_pin_then_wake_up_returns_pinned PASSED [ 70%]
test_mcp_palace.py::TestPinAndWakeUp::test_unpin_removes_from_wake_up PASSED   [ 73%]
test_mcp_palace.py::TestPinAndWakeUp::test_wake_up_respects_token_budget PASSED [ 76%]
test_mcp_palace.py::TestPinAndWakeUp::test_pin_unknown_key_errors PASSED       [ 79%]
test_mcp_palace.py::TestQueryEntity::test_query_unknown_entity PASSED          [ 82%]
test_mcp_palace.py::TestQueryEntity::test_query_with_as_of_accepted PASSED     [ 85%]
test_mcp_palace.py::TestDiary::test_diary_round_trip PASSED                    [ 88%]
test_mcp_palace.py::TestDiary::test_diary_namespace_isolated PASSED            [ 91%]
test_mcp_palace.py::TestTagsAndRoomData::test_add_with_tags_and_room PASSED    [ 94%]
test_mcp_palace.py::TestTagsAndRoomData::test_list_data_filters_by_tag PASSED  [ 97%]
test_mcp_palace.py::TestSearchFilterShape::test_search_accepts_room_tags PASSED [100%]

34 passed in 3.63s
```

## Bugfix: db.Insert double-marshal

**Problem:** `db.Insert()` called `json.Marshal(data)` on a string containing valid JSON, wrapping it in extra quotes. Metadata `{"room":"auth","tags":["security"]}` became `"\"{ ... }\""`. `chunkMetaMatches()` could not parse the double-encoded JSON, so room/tags search filters always returned 0 results.

**Fix:** `internal/store/db.go:185` — detect when `data` is already `string`/`[]byte`/`json.RawMessage` with valid JSON prefix (`{` or `[`) and use raw bytes without re-marshaling.

**Verified:**
- room=auth filter: 1 result (was 0 before fix)
- tags=[security] filter: 1 result (was 0 before fix)
- room=deploy (wrong room): 0 results (correct negative)

## Pre-existing issues (not introduced by Phase 1)

- `TestDistSIMDCorrectness` — SIMD floating-point precision (~0.00001 diff). Existed before changes.
- `pkg/vectorstore` — build error (`VectroRecord.Metadata` undefined). Existed before changes.
- `internal/grpc/service_test.go` — vet error (`pb.NewLevaraServiceClient` undefined). Existed before changes.
