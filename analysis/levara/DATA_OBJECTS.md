# DATA OBJECTS — `levara`

Companion to `BUSINESS_RULES.md` (extract-rules, 2026-10-15). Field lists
are verified where a source line is cited; "…" marks fields known to exist
but not enumerated this pass.

| Object | Fields (type) | Consumed/produced by rules | Source |
|---|---|---|---|
| **Memory row** (`memories` table) | id (string, canonical SQL+vector key); key, value (string); type (hall-typed string); room, hall (string, filterable); owner_id (string, isolation scope); collection (string); pin_priority (numeric, optional); superseded/archived flags | RULE-001..004, 013..016, 019..022, 027 | `pkg/mcp/tool_save_recall_memory.go` (`memoryRowColumns`, `appendMemoryFilters`:434) |
| **ToolResult** (MCP tool return) | ok (bool); message (string, truncated value echo); index_status (string); index_job_id (string); error text on failure | RULE-002, 013, 020, 021 | `pkg/mcp/tool_save_recall_memory.go:172-180`; `pkg/mcp` ToolResult type |
| **Breaker** (embed circuit state) | threshold (int, default 3); failures (int); cooldown (duration, default 30s); openUntil (time); generation (int); probing (bool) | RULE-011, 012, 020, 021 | `pkg/embed/breaker.go:29-35,38-46` |
| **Decision** (access verdict) | Allowed (bool); Role (string); Reason (string); DevMode (bool); Authenticated (bool); APIKeyAllowed (bool) | RULE-007, 008 | `pkg/access/policy.go:41-47` |
| **WorkspaceRequest** | UserID, TenantID, ProjectID (string); Action (action vocab); APIKeyPermissions | RULE-007, 008 | `pkg/access/policy.go:33-38` |
| **VisibleDataset** | ID, Name (string); OwnerID (string); CreatedAt (time); RecordCount, TotalSize (numeric); GitHubRepo (string) | RULE-007 (listing surface) | `pkg/access/policy.go:50-57` |
| **VerifiedOptions** (backup input) | DataDir, WorkspacePath, UploadsPath, OutputDir (paths); DBProvider (sqlite\|postgres); StorageBackend; Standalone (bool); NodeID (sanitized); Shards (1..64); Dimension (1..65536); Timeout; MaxFiles | RULE-005 | `pkg/backup/verified.go:40,107` |
| **VerifiedManifest** (backup artifact) | Format; CreatedAt (RFC3339Nano); DBProvider; NodeID; Shards, Dimension (int); Roots (exactly 3); Files (1..MaxFiles) | RULE-006 | `pkg/backup/verified.go:65,217,527` |
| **Graph edge** (temporal KG) | source entity, relationship (rel), target; valid_from / valid_until (time window); superseded_by (edge id); dataset_id (tenant scope; "" = global) | RULE-017, 018 | `pkg/orchestrator/pgupsert.go:35-40`; `pkg/graph` |
| **Hall vocabulary** | fact, event, decision, preference, advice, discovery (closed set; empty = unset) | RULE-001 | `pkg/memoryhall/hall.go` (`hallVocab`) |
| **Index job** (deferred embedding) | ID (string); Status (enum, surfaced as `index_status`) | RULE-020 | `pkg/mcp/tool_save_recall_memory.go:138-170`; `pkg/memoryindex` |
| **Embedder contract** | Dimension() int — output vector dim must match collection index (768, gemma) | RULE-020, 021; dual-run equivalence axis | `pkg/embed/local.go:22-46`; `pkg/embcontract/contract.go` |
| **Overfetch window** (search) | topK*3, floor 10 (gRPC); ACL/room/tags applied inside window | RULE-009, 010, 023, 024 | `internal/grpc/service.go:1035-1037` |
