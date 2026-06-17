package mcp

// ToolDescriptors returns the full list of MCP tools exposed by Levara.
// The slice is constructed fresh on each call to prevent callers from
// mutating the canonical list. Tool InputSchema values follow JSON Schema
// draft 2020-12 (subset supported by MCP clients).
//
// Moved from internal/http/mcp.go during F-4 to make the registry usable
// from future SDK bindings without pulling in the HTTP handler.
func ToolDescriptors() []Tool {
	return []Tool{
		{
			Name:         "cognify",
			Description:  "Transform text data into a structured knowledge graph, or ingest in RAG mode (chunk+embed only, no LLM). Optional room/tags propagate to chunk metadata for filtered search.",
			OutputSchema: runStatusSchema(),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"data":                 map[string]any{"type": "string", "description": "Text data to process into knowledge graph"},
					"collection":           map[string]any{"type": "string", "description": "Target collection name (default: 'default')"},
					"custom_prompt":        map[string]any{"type": "string", "description": "Custom LLM prompt for entity extraction (ignored in rag mode)"},
					"room":                 map[string]any{"type": "string", "description": "Sub-topic label attached to every chunk for filtered retrieval (auth, deploy, ocr-bench)."},
					"tags":                 map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Tags attached to every chunk."},
					"mode":                 map[string]any{"type": "string", "enum": []string{"rag", "full"}, "default": "full", "description": "Pipeline mode. 'rag': chunk+embed only (no LLM, no graph). 'full': complete pipeline with entity extraction."},
					"chunk_strategy":       map[string]any{"type": "string", "enum": []string{"merged", "paragraph", "sentence", "row", "code", "sliding", "auto"}, "default": "merged", "description": "Chunking strategy. 'sliding' enables fixed-window with overlap."},
					"overlap_chars":        map[string]any{"type": "integer", "description": "Overlap in characters for sliding window chunking. Default: 20% of max_chunk_chars."},
					"snap_to_sentence":     map[string]any{"type": "boolean", "default": true, "description": "Snap sliding window boundaries to sentence/word ends (avoids cutting words)."},
					"parent_child":         map[string]any{"type": "boolean", "default": false, "description": "Enable parent-child chunking. Small chunks for search precision, large chunks for context."},
					"document_title":       map[string]any{"type": "string", "description": "Document title for attribution. Prepended as contextual header to chunk embeddings."},
					"document_id":          map[string]any{"type": "string", "description": "Stable document ID for metadata tracking."},
					"community_resolution": map[string]any{"type": "number", "default": 1.0, "description": "Louvain resolution (γ). >1=finer, <1=coarser. Presets: 0.5=coarse, 1.0=medium, 2.0=fine."},
					"dedup_threshold":      map[string]any{"type": "number", "default": 0.95, "description": "Semantic entity dedup threshold (0.5-1.0). Higher=stricter."},
					"min_chunk_chars":      map[string]any{"type": "integer", "default": 80, "description": "Min chunk size in characters."},
					"max_chunk_chars":      map[string]any{"type": "integer", "default": 600, "description": "Max chunk size in characters."},
				},
				"required": []string{"data"},
			},
		},
		{
			Name:        "search",
			Description: "Search the knowledge graph using various strategies. Use AUTO (default) for intelligent routing that analyzes your query and selects the best strategy automatically. Optional room/tags filters narrow chunk results by metadata (overfetched ×3 then post-filtered).",
			OutputSchema: objectSchema(map[string]any{
				"results":     arrayOfObjectsProp(searchResultItemSchema(), "Ranked hits from the selected strategy."),
				"search_type": stringProp("Strategy actually used (may differ from the request under AUTO routing)."),
				"reranked":    booleanProp("Whether cross-encoder reranking was applied."),
				"routing": objectSchema(map[string]any{
					"selected_type": stringProp("Search type selected by AUTO routing."),
					"reason":        stringProp("Router explanation."),
					"confidence":    numberProp("Router confidence."),
					"alternatives":  arrayOfStringsProp("Other candidate search types."),
					"source":        stringProp("Routing source."),
				}),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"search_query":  map[string]any{"type": "string", "description": "Natural language search query"},
					"search_type":   map[string]any{"type": "string", "description": "Search strategy: AUTO (intelligent routing), CHUNKS (vector), HYBRID (vector+BM25), RAG_COMPLETION (vector+LLM answer), GRAPH_COMPLETION (graph traversal+LLM), TEMPORAL (date-aware), SUMMARIES, CHUNKS_LEXICAL (BM25), CODING_RULES (code entities)", "default": "AUTO"},
					"top_k":         map[string]any{"type": "integer", "description": "Number of results to return", "default": 10},
					"collection":    map[string]any{"type": "string", "description": "Project collection name to search in. Leave empty to search all."},
					"room":          map[string]any{"type": "string", "description": "Filter chunks by room (sub-topic) attached during cognify."},
					"tags":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Filter chunks by tags (any-match)."},
					"rerank":        map[string]any{"type": "boolean", "default": false, "description": "Apply cross-encoder reranking to results. Requires RERANK_ENDPOINT configured."},
					"parent_child":  map[string]any{"type": "boolean", "default": false, "description": "Search child chunks for precision, return parent chunks for context."},
					"multi_query":   map[string]any{"type": "boolean", "default": false, "description": "Generate query variants via LLM for broader retrieval. Requires LLM."},
					"dedup":         map[string]any{"type": "boolean", "default": true, "description": "Deduplicate overlapping results (useful for sliding window chunks)."},
					"mode":          map[string]any{"type": "string", "enum": []string{"rag", "graph", "full", "auto"}, "default": "auto", "description": "Search mode. rag: vector only. graph: graph traversal only. full: all backends. auto: router decides."},
					"graph_rerank":  map[string]any{"type": "boolean", "default": false, "description": "Rerank results using graph proximity (entity hop distance)."},
					"vector_weight": map[string]any{"type": "number", "default": 1.0, "description": "RRF weight for vector results in HYBRID / WEIGHTED_HYBRID search."},
					"bm25_weight":   map[string]any{"type": "number", "default": 1.0, "description": "RRF weight for lexical BM25 results in HYBRID / WEIGHTED_HYBRID search."},
				},
				"required": []string{"search_query"},
			},
		},
		{
			Name:        "workspace_access_check",
			Description: "Preflight workspace RBAC for a project before an agent reads, searches, writes, reindexes, reverts, or runs GC.",
			OutputSchema: objectSchema(map[string]any{
				"project_id":      stringProp("Workspace project ID."),
				"user_id":         stringProp("Authenticated user or bot ID, when available."),
				"access":          stringProp("Requested access level: read or write."),
				"allowed":         booleanProp("Whether the caller is allowed."),
				"role":            stringProp("Resolved role, if any."),
				"reason":          stringProp("Machine-readable reason for allow/deny."),
				"authenticated":   booleanProp("Whether a user ID was available."),
				"api_key_allowed": booleanProp("Whether API-key permissions allow this access level."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "string", "description": "Workspace project ID."},
					"access":     map[string]any{"type": "string", "enum": []string{"read", "write"}, "default": "read", "description": "Access level to check."},
				},
				"required": []string{"project_id"},
			},
		},
		{
			Name:        "workspace_context",
			Description: "Bootstrap Codex/Claude workspace context in one call: accessible projects, branches, active generation/collection, watcher/job status, recommended search type, and exact-read policy.",
			OutputSchema: objectSchema(map[string]any{
				"projects":                arrayOfObjectsProp(objectSchema(map[string]any{"project_id": stringProp("Workspace project ID."), "branches": arrayOfObjectsProp(objectSchema(map[string]any{"branch": stringProp("Branch namespace."), "active_generation": stringProp("Active generation, if initialized."), "active_collection": stringProp("Resolved active vector/BM25 collection, if initialized."), "active_chunk_count": integerProp("Active chunk count."), "context_artifact_count": integerProp("Configured context artifacts for this branch.")}), "Branch contexts.")}), "Accessible workspace projects."),
				"default_project_id":      stringProp("First accessible project, if any."),
				"recommended_search_type": stringProp("Recommended search type for workspace_search."),
				"exact_read_required":     booleanProp("True: agents must use workspace_read before answering from a search hit."),
				"watcher":                 objectSchema(map[string]any{"enabled": booleanProp("Whether workspace watcher is running."), "pending_branches": integerProp("Branches with pending watcher reconcile.")}),
				"guidance":                arrayOfStringsProp("Initialization guidance when no accessible workspace is available."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "string", "description": "Optional project to inspect. When omitted, returns all accessible workspace projects."},
					"branch":     map[string]any{"type": "string", "description": "Optional branch filter. Defaults to all local branches or main for uninitialized projects."},
				},
			},
		},
		{
			Name:        "workspace_audit_log",
			Description: "List sanitized workspace audit events for a project. Events record operation/result/user metadata and deliberately omit markdown text, snippets, and exact file paths.",
			OutputSchema: objectSchema(map[string]any{
				"project_id": stringProp("Workspace project ID."),
				"branch":     stringProp("Optional branch filter."),
				"events":     arrayOfObjectsProp(objectSchema(map[string]any{"id": stringProp("Audit event ID."), "at": stringProp("Event timestamp."), "operation": stringProp("Workspace operation."), "result": stringProp("success, failure, or denied."), "user_id": stringProp("User or bot ID, if known.")}), "Audit events newest-first."),
				"total":      integerProp("Total matching events before limit."),
				"limit":      integerProp("Applied result limit."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "string", "description": "Workspace project ID."},
					"branch":     map[string]any{"type": "string", "description": "Optional branch filter."},
					"operation":  map[string]any{"type": "string", "description": "Optional operation filter, for example read, write, search, reindex, revert, gc."},
					"result":     map[string]any{"type": "string", "enum": []string{"success", "failure", "denied"}, "description": "Optional result filter."},
					"limit":      map[string]any{"type": "integer", "default": 100, "description": "Maximum number of events to return, capped at 1000."},
				},
				"required": []string{"project_id"},
			},
		},
		{
			Name:        "workspace_context_artifacts",
			Description: "List configured first-class context artifacts such as OpenAPI specs, DDL, Terraform, ADRs, and runbooks from .kb/context-artifacts.json.",
			OutputSchema: objectSchema(map[string]any{
				"version": integerProp("Context artifact registry version."),
				"path":    stringProp("Registry file path."),
				"total":   integerProp("Number of artifacts returned."),
				"artifacts": arrayOfObjectsProp(objectSchema(map[string]any{
					"id":                 stringProp("Stable artifact ID."),
					"project_id":         stringProp("Workspace project ID."),
					"branch":             stringProp("Workspace branch namespace."),
					"path":               stringProp("Workspace-relative artifact path."),
					"kind":               stringProp("Artifact kind: openapi, ddl, terraform, adr, runbook, markdown, or other."),
					"title":              stringProp("Human-readable title when configured."),
					"room":               stringProp("Search room used when indexed."),
					"tags":               arrayOfStringsProp("Search tags used when indexed."),
					"index":              booleanProp("Whether this artifact is eligible for reindex_artifacts."),
					"include_in_context": booleanProp("Whether agents should consider this artifact part of project context."),
					"exists":             booleanProp("Whether the target file currently exists."),
					"bytes":              integerProp("Current file size."),
					"digest":             stringProp("Current file SHA-256 digest."),
				}), "Resolved context artifacts."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "string", "description": "Optional workspace project filter."},
					"branch":     map[string]any{"type": "string", "description": "Optional branch filter."},
					"kind":       map[string]any{"type": "string", "description": "Optional artifact kind filter."},
					"ids":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional artifact ID allow-list."},
					"index_only": map[string]any{"type": "boolean", "default": false, "description": "Return only artifacts marked index=true."},
				},
			},
		},
		{
			Name:        "workspace_reindex_artifacts",
			Description: "Index configured context artifacts into a workspace generation so OpenAPI/DDL/Terraform/ADR/runbook files participate in workspace_search.",
			OutputSchema: objectSchema(map[string]any{
				"project_id":        stringProp("Workspace project ID."),
				"branch":            stringProp("Workspace branch namespace."),
				"manifest_path":     stringProp("Durable manifest path."),
				"active_generation": stringProp("Active generation after artifact indexing."),
				"artifacts":         arrayOfObjectsProp(objectSchema(map[string]any{"id": stringProp("Artifact ID."), "path": stringProp("Workspace path."), "kind": stringProp("Artifact kind.")}), "Artifacts indexed."),
				"results":           arrayOfObjectsProp(objectSchema(map[string]any{"chunks_created": integerProp("Chunks indexed."), "collection": stringProp("Collection updated.")}), "Per-artifact index results."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id":          map[string]any{"type": "string", "description": "Workspace project ID."},
					"branch":              map[string]any{"type": "string", "default": "main"},
					"generation":          map[string]any{"type": "string", "description": "Generation to create/update."},
					"collection":          map[string]any{"type": "string", "description": "Optional explicit vector/BM25 collection."},
					"artifact_ids":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional artifact IDs to index."},
					"kinds":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional artifact kind filters."},
					"activate_generation": map[string]any{"type": "boolean", "default": false},
					"chunk_strategy":      map[string]any{"type": "string", "enum": []string{"merged", "paragraph", "sentence", "sliding"}, "default": "merged"},
					"min_chunk_chars":     map[string]any{"type": "integer", "default": 80},
					"max_chunk_chars":     map[string]any{"type": "integer", "default": 600},
				},
				"required": []string{"project_id", "generation"},
			},
		},
		{
			Name:        "workspace_ops_status",
			Description: "Return markdown workspace operational health for dashboards and agents: watcher lag, durable indexing job status/dead letters, audit volume, and Prometheus metric refresh.",
			OutputSchema: objectSchema(map[string]any{
				"generated_at": stringProp("RFC3339Nano timestamp for this status snapshot."),
				"project_id":   stringProp("Optional scoped workspace project ID."),
				"branch":       stringProp("Optional scoped branch namespace."),
				"watcher": objectSchema(map[string]any{
					"enabled":          booleanProp("Whether the watcher is running."),
					"pending_branches": integerProp("Branches waiting for watcher reconciliation."),
					"error_count":      integerProp("Watcher error count."),
				}),
				"jobs": objectSchema(map[string]any{
					"total":                 integerProp("Total durable index jobs in scope."),
					"by_status":             objectSchema(map[string]any{}),
					"dead_letter_count":     integerProp("Jobs currently in dead_letter status."),
					"max_lag_seconds":       numberProp("Oldest pending/failed job age in seconds."),
					"oldest_pending_at":     stringProp("Oldest pending/failed job creation timestamp."),
					"oldest_dead_letter_at": stringProp("Oldest dead-letter timestamp."),
				}),
				"audit": objectSchema(map[string]any{
					"total_events":  integerProp("Sanitized audit events found on disk in scope."),
					"files":         integerProp("Audit JSONL files scanned."),
					"by_source":     objectSchema(map[string]any{}),
					"by_result":     objectSchema(map[string]any{}),
					"last_event_at": stringProp("Latest audit event timestamp in scope."),
				}),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "string", "description": "Optional workspace project ID. When omitted, aggregates all local projects."},
					"branch":     map[string]any{"type": "string", "description": "Optional branch filter."},
				},
			},
		},
		{
			Name:        "workspace_conflicts",
			Description: "Compare filesystem Markdown truth with the active generation and report dirty, unindexed, or deleted indexed paths plus watcher/job context for team conflict handling.",
			OutputSchema: objectSchema(map[string]any{
				"project_id":            stringProp("Workspace project ID."),
				"branch":                stringProp("Workspace branch namespace."),
				"active_generation":     stringProp("Manifest active generation."),
				"manifest_path":         stringProp("Durable manifest path."),
				"has_conflicts":         booleanProp("Whether drift or pending watcher state exists."),
				"policy":                stringProp("Human-readable conflict policy."),
				"dirty_paths":           arrayOfObjectsProp(objectSchema(map[string]any{"path": stringProp("Path changed since active generation."), "file_digest": stringProp("Current digest."), "indexed_digest": stringProp("Active generation digest.")}), "Changed Markdown paths."),
				"unindexed_paths":       arrayOfObjectsProp(objectSchema(map[string]any{"path": stringProp("Path present on disk but missing from active generation.")}), "Unindexed Markdown paths."),
				"missing_indexed_paths": arrayOfObjectsProp(objectSchema(map[string]any{"path": stringProp("Path indexed but missing from filesystem truth.")}), "Deleted or moved indexed paths."),
				"jobs_by_status":        objectSchema(map[string]any{}),
				"recommended_actions":   arrayOfStringsProp("Suggested remediation steps."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "string", "description": "Workspace project ID."},
					"branch":     map[string]any{"type": "string", "default": "main"},
				},
				"required": []string{"project_id"},
			},
		},
		{
			Name:        "workspace_search",
			Description: "Search a markdown-native workspace by project/branch without knowing the derived collection. Resolves the active generation from the manifest, applies workspace ACL, returns freshness metadata, and enriches hits with path/heading fields for exact workspace_read follow-up.",
			OutputSchema: objectSchema(map[string]any{
				"project_id":          stringProp("Workspace project ID."),
				"branch":              stringProp("Workspace branch namespace."),
				"manifest_path":       stringProp("Durable manifest path."),
				"active_generation":   stringProp("Manifest active generation."),
				"generation":          stringProp("Generation searched."),
				"collection":          stringProp("Vector/BM25 collection searched."),
				"search_type":         stringProp("Strategy actually used."),
				"reranked":            booleanProp("Whether reranking ran."),
				"freshness":           objectSchema(map[string]any{"stale": booleanProp("True when the searched generation is not the manifest active generation."), "potentially_stale": booleanProp("True when watcher state suggests pending or failed reconciliation."), "last_indexed_at": stringProp("Most recent chunk update timestamp for the searched generation.")}),
				"exact_read_required": booleanProp("True: callers must read the exact markdown file before treating a hit as source of truth."),
				"answer_contract":     objectSchema(map[string]any{"required": booleanProp("Whether citations are mandatory."), "read_tool": stringProp("Tool to call before answering from a hit."), "citation_field": stringProp("Result field containing source metadata.")}),
				"results":             arrayOfObjectsProp(searchResultItemSchema(), "Ranked workspace hits enriched with path, heading_path, and text when metadata provides them."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id":    map[string]any{"type": "string", "description": "Workspace project ID. Also used as dataset_id for ACL filtering."},
					"branch":        map[string]any{"type": "string", "default": "main", "description": "Branch namespace."},
					"generation":    map[string]any{"type": "string", "description": "Optional generation to search. Defaults to manifest active_generation."},
					"collection":    map[string]any{"type": "string", "description": "Optional explicit collection. Defaults to the collection recorded for the resolved generation."},
					"search_query":  map[string]any{"type": "string", "description": "Natural language or keyword query."},
					"query":         map[string]any{"type": "string", "description": "Alias for search_query."},
					"search_type":   map[string]any{"type": "string", "default": "HYBRID", "description": "Search strategy. Defaults to HYBRID for workspace retrieval; use CHUNKS_LEXICAL for keyword-only search without embeddings."},
					"top_k":         map[string]any{"type": "integer", "default": 10},
					"room":          map[string]any{"type": "string", "description": "Filter chunks by room."},
					"tags":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Filter chunks by tags."},
					"mode":          map[string]any{"type": "string", "enum": []string{"rag", "graph", "full", "auto"}, "default": "rag", "description": "Defaults to rag so workspace search does not route into graph-only strategies."},
					"rerank":        map[string]any{"type": "boolean", "default": false},
					"parent_child":  map[string]any{"type": "boolean", "default": false},
					"multi_query":   map[string]any{"type": "boolean", "default": false},
					"dedup":         map[string]any{"type": "boolean", "default": true},
					"graph_rerank":  map[string]any{"type": "boolean", "default": false},
					"vector_weight": map[string]any{"type": "number", "default": 1.0},
					"bm25_weight":   map[string]any{"type": "number", "default": 1.0},
				},
				"required": []string{"project_id"},
			},
		},
		{
			Name:        "workspace_index",
			Description: "Index one markdown file into the markdown-native workspace manifest, dense vector collection, and BM25 sidecar.",
			OutputSchema: objectSchema(map[string]any{
				"project_id":        stringProp("Workspace project ID."),
				"branch":            stringProp("Workspace branch namespace."),
				"manifest_path":     stringProp("Durable manifest path."),
				"active_generation": stringProp("Active generation after this operation, if any."),
				"result": objectSchema(map[string]any{
					"chunks_created":     integerProp("Number of markdown chunks indexed."),
					"vector_ids":         arrayOfStringsProp("Vector IDs written."),
					"deleted_vector_ids": arrayOfStringsProp("Stale vector IDs deleted."),
					"collection":         stringProp("Vector/BM25 collection updated."),
					"generation":         stringProp("Generation updated."),
				}),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id":          map[string]any{"type": "string", "description": "Workspace project ID."},
					"branch":              map[string]any{"type": "string", "default": "main", "description": "Branch namespace."},
					"generation":          map[string]any{"type": "string", "description": "Index generation ID."},
					"collection":          map[string]any{"type": "string", "description": "Optional explicit vector/BM25 collection."},
					"path":                map[string]any{"type": "string", "description": "Markdown path inside the workspace."},
					"text":                map[string]any{"type": "string", "description": "Markdown file content."},
					"file_digest":         map[string]any{"type": "string", "description": "Optional source digest; defaults to sha256(text)."},
					"document_id":         map[string]any{"type": "string", "description": "Stable document ID."},
					"title":               map[string]any{"type": "string", "description": "Human document title."},
					"commit_hash":         map[string]any{"type": "string", "description": "Source commit hash attached to chunk metadata."},
					"room":                map[string]any{"type": "string", "description": "Room/sub-topic metadata."},
					"tags":                map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Tags attached to chunk metadata."},
					"chunk_strategy":      map[string]any{"type": "string", "enum": []string{"merged", "paragraph", "sentence", "sliding"}, "default": "merged"},
					"min_chunk_chars":     map[string]any{"type": "integer", "default": 80},
					"max_chunk_chars":     map[string]any{"type": "integer", "default": 600},
					"overlap_chars":       map[string]any{"type": "integer", "description": "Sliding-window overlap in characters."},
					"snap_to_sentence":    map[string]any{"type": "boolean", "default": true},
					"activate_generation": map[string]any{"type": "boolean", "default": false, "description": "Mark this generation active after successful indexing."},
				},
				"required": []string{"project_id", "generation", "path", "text"},
			},
		},
		{
			Name:        "workspace_read",
			Description: "Read an exact markdown file from the workspace truth layer and return related manifest chunks.",
			OutputSchema: objectSchema(map[string]any{
				"project_id": stringProp("Workspace project ID."),
				"branch":     stringProp("Workspace branch namespace."),
				"path":       stringProp("Markdown path inside the workspace."),
				"text":       stringProp("Markdown file content."),
				"citation":   objectSchema(map[string]any{"source_id": stringProp("Stable source ID."), "source_uri": stringProp("Workspace source URI."), "read_tool": stringProp("Tool that returns this exact source."), "read_args": objectSchema(map[string]any{})}),
				"citations":  arrayOfObjectsProp(objectSchema(map[string]any{"source_id": stringProp("Chunk source ID."), "generation": stringProp("Generation containing this chunk."), "chunk_id": stringProp("Chunk ID."), "vector_id": stringProp("Vector ID.")}), "Chunk-level citations from the manifest."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "string", "description": "Workspace project ID."},
					"branch":     map[string]any{"type": "string", "default": "main", "description": "Branch namespace."},
					"path":       map[string]any{"type": "string", "description": "Markdown path inside the workspace."},
				},
				"required": []string{"project_id", "path"},
			},
		},
		{
			Name:        "workspace_write",
			Description: "Write an exact markdown file into the workspace truth layer and optionally index it.",
			OutputSchema: objectSchema(map[string]any{
				"project_id": stringProp("Workspace project ID."),
				"branch":     stringProp("Workspace branch namespace."),
				"path":       stringProp("Markdown path inside the workspace."),
				"bytes":      integerProp("Bytes written."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id":           map[string]any{"type": "string", "description": "Workspace project ID."},
					"branch":               map[string]any{"type": "string", "default": "main", "description": "Branch namespace."},
					"path":                 map[string]any{"type": "string", "description": "Markdown path inside the workspace."},
					"text":                 map[string]any{"type": "string", "description": "Markdown file content."},
					"expected_file_digest": map[string]any{"type": "string", "description": "Optional optimistic-lock digest from a prior workspace_read; write is rejected if current file digest differs."},
					"index":                map[string]any{"type": "boolean", "description": "Whether to index after writing. Defaults to true when generation is supplied."},
					"generation":           map[string]any{"type": "string", "description": "Index generation ID when indexing."},
					"collection":           map[string]any{"type": "string", "description": "Optional explicit vector/BM25 collection."},
					"activate_generation":  map[string]any{"type": "boolean", "default": false},
					"chunk_strategy":       map[string]any{"type": "string", "enum": []string{"merged", "paragraph", "sentence", "sliding"}, "default": "merged"},
					"min_chunk_chars":      map[string]any{"type": "integer", "default": 80},
					"max_chunk_chars":      map[string]any{"type": "integer", "default": 600},
					"room":                 map[string]any{"type": "string", "description": "Room/sub-topic metadata."},
					"tags":                 map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Tags attached to chunk metadata."},
				},
				"required": []string{"project_id", "path", "text"},
			},
		},
		{
			Name:        "workspace_reindex_paths",
			Description: "Reindex one or more existing markdown files from the workspace truth layer.",
			OutputSchema: objectSchema(map[string]any{
				"project_id":        stringProp("Workspace project ID."),
				"branch":            stringProp("Workspace branch namespace."),
				"manifest_path":     stringProp("Durable manifest path."),
				"active_generation": stringProp("Active generation after reindexing."),
				"results":           arrayOfObjectsProp(objectSchema(map[string]any{"chunks_created": integerProp("Chunks indexed."), "collection": stringProp("Collection updated.")}), "Per-path index results."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id":          map[string]any{"type": "string", "description": "Workspace project ID."},
					"branch":              map[string]any{"type": "string", "default": "main", "description": "Branch namespace."},
					"generation":          map[string]any{"type": "string", "description": "Index generation ID."},
					"collection":          map[string]any{"type": "string", "description": "Optional explicit vector/BM25 collection."},
					"paths":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Markdown paths inside the workspace."},
					"activate_generation": map[string]any{"type": "boolean", "default": false},
					"chunk_strategy":      map[string]any{"type": "string", "enum": []string{"merged", "paragraph", "sentence", "sliding"}, "default": "merged"},
					"min_chunk_chars":     map[string]any{"type": "integer", "default": 80},
					"max_chunk_chars":     map[string]any{"type": "integer", "default": 600},
				},
				"required": []string{"project_id", "generation", "paths"},
			},
		},
		{
			Name:        "workspace_reconcile",
			Description: "Scan the workspace filesystem truth layer and build a fresh markdown index generation from current .md files.",
			OutputSchema: objectSchema(map[string]any{
				"project_id":        stringProp("Workspace project ID."),
				"branch":            stringProp("Workspace branch namespace."),
				"manifest_path":     stringProp("Durable manifest path."),
				"active_generation": stringProp("Active generation after reconciliation."),
				"paths":             arrayOfStringsProp("Markdown paths discovered or reconciled."),
				"results":           arrayOfObjectsProp(objectSchema(map[string]any{"chunks_created": integerProp("Chunks indexed."), "collection": stringProp("Collection updated.")}), "Per-path index results."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id":          map[string]any{"type": "string", "description": "Workspace project ID."},
					"branch":              map[string]any{"type": "string", "default": "main", "description": "Branch namespace."},
					"generation":          map[string]any{"type": "string", "description": "Fresh index generation ID."},
					"collection":          map[string]any{"type": "string", "description": "Optional explicit vector/BM25 collection."},
					"paths":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional markdown paths; defaults to all .md files in the workspace."},
					"activate_generation": map[string]any{"type": "boolean", "default": false, "description": "Mark the reconciled generation active."},
					"chunk_strategy":      map[string]any{"type": "string", "enum": []string{"merged", "paragraph", "sentence", "sliding"}, "default": "merged"},
					"min_chunk_chars":     map[string]any{"type": "integer", "default": 80},
					"max_chunk_chars":     map[string]any{"type": "integer", "default": 600},
				},
				"required": []string{"project_id", "generation"},
			},
		},
		{
			Name:        "workspace_index_jobs",
			Description: "List durable workspace indexing jobs recorded under .kb/jobs for one project/branch, including pending/running/completed/failed/dead-letter attempts.",
			OutputSchema: objectSchema(map[string]any{
				"project_id": stringProp("Workspace project ID."),
				"branch":     stringProp("Workspace branch namespace."),
				"jobs":       arrayOfObjectsProp(objectSchema(map[string]any{"id": stringProp("Job ID."), "status": stringProp("Job status."), "attempts": integerProp("Attempt count."), "last_error": stringProp("Last error, if any.")}), "Durable index jobs."),
				"total":      integerProp("Number of jobs returned."),
				"by_status":  objectSchema(map[string]any{}),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "string", "description": "Workspace project ID."},
					"branch":     map[string]any{"type": "string", "default": "main"},
					"status":     map[string]any{"type": "string", "enum": []string{"pending", "running", "completed", "failed", "dead_letter"}, "description": "Optional status filter."},
				},
				"required": []string{"project_id"},
			},
		},
		{
			Name:        "workspace_enqueue_index_job",
			Description: "Enqueue a durable async workspace reindex/reconcile job for the optional indexing worker. Duplicate payloads coalesce by idempotency key.",
			OutputSchema: objectSchema(map[string]any{
				"job": objectSchema(map[string]any{"id": stringProp("Job ID."), "status": stringProp("Job status."), "attempts": integerProp("Attempt count."), "next_run_at": stringProp("Next retry time, if scheduled."), "last_error": stringProp("Last error, if any.")}),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"operation":           map[string]any{"type": "string", "enum": []string{"reindex", "reconcile"}, "description": "Indexing operation to run."},
					"project_id":          map[string]any{"type": "string", "description": "Workspace project ID."},
					"branch":              map[string]any{"type": "string", "default": "main"},
					"generation":          map[string]any{"type": "string", "description": "Generation to build."},
					"collection":          map[string]any{"type": "string", "description": "Optional explicit collection."},
					"paths":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Markdown paths for reindex, or optional paths for reconcile."},
					"activate_generation": map[string]any{"type": "boolean", "default": true},
					"chunk_strategy":      map[string]any{"type": "string", "enum": []string{"merged", "paragraph", "sentence", "sliding"}, "default": "merged"},
					"min_chunk_chars":     map[string]any{"type": "integer", "default": 80},
					"max_chunk_chars":     map[string]any{"type": "integer", "default": 600},
					"delete_missing":      map[string]any{"type": "boolean", "default": false},
				},
				"required": []string{"operation", "project_id", "generation"},
			},
		},
		{
			Name:        "workspace_retry_index_job",
			Description: "Retry a durable failed workspace indexing job by replaying its saved reindex/reconcile request.",
			OutputSchema: objectSchema(map[string]any{
				"job":    objectSchema(map[string]any{"id": stringProp("Job ID."), "status": stringProp("Job status after retry."), "attempts": integerProp("Attempt count."), "last_error": stringProp("Last error, if any.")}),
				"result": objectSchema(map[string]any{}),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "string", "description": "Workspace project ID."},
					"branch":     map[string]any{"type": "string", "default": "main"},
					"job_id":     map[string]any{"type": "string", "description": "Durable job ID returned by workspace_index_jobs."},
				},
				"required": []string{"project_id", "job_id"},
			},
		},
		{
			Name:        "workspace_watch_status",
			Description: "Inspect workspace watcher status, lag counters, last reconciliation, and last error.",
			OutputSchema: objectSchema(map[string]any{
				"enabled":           booleanProp("Whether the watcher is currently running."),
				"scan_count":        integerProp("Number of polling scans completed."),
				"reconcile_count":   integerProp("Number of reconciliations completed."),
				"error_count":       integerProp("Number of watcher scan/reconcile errors."),
				"watched_branches":  integerProp("Number of project/branch trees with markdown files."),
				"pending_branches":  integerProp("Number of branches waiting for debounce."),
				"last_generation":   stringProp("Last auto-created generation."),
				"last_reconcile_at": stringProp("RFC3339Nano timestamp of last reconciliation."),
				"last_error":        stringProp("Last watcher error, if any."),
			}),
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "workspace_run_start",
			Description: "Create a durable markdown run artifact under the workspace /runs tree.",
			OutputSchema: objectSchema(map[string]any{
				"project_id": stringProp("Workspace project ID."),
				"branch":     stringProp("Workspace branch namespace."),
				"run_id":     stringProp("Run artifact ID."),
				"path":       stringProp("Durable run directory."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "string", "description": "Workspace project ID."},
					"branch":     map[string]any{"type": "string", "default": "main", "description": "Branch namespace."},
					"run_id":     map[string]any{"type": "string", "description": "Optional caller-provided run ID."},
					"prompt":     map[string]any{"type": "string", "description": "Prompt markdown to write as prompt.md."},
					"command":    map[string]any{"type": "string", "description": "Command markdown to write as command.md."},
					"result":     map[string]any{"type": "string", "description": "Initial result markdown to write as result.md."},
					"metadata":   map[string]any{"type": "object", "description": "Additional metadata frontmatter fields."},
				},
				"required": []string{"project_id"},
			},
		},
		{
			Name:        "workspace_run_get",
			Description: "Read durable markdown files for one workspace run artifact.",
			OutputSchema: objectSchema(map[string]any{
				"project_id": stringProp("Workspace project ID."),
				"branch":     stringProp("Workspace branch namespace."),
				"run_id":     stringProp("Run artifact ID."),
				"path":       stringProp("Durable run directory."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "string", "description": "Workspace project ID."},
					"branch":     map[string]any{"type": "string", "default": "main", "description": "Branch namespace."},
					"run_id":     map[string]any{"type": "string", "description": "Run artifact ID."},
				},
				"required": []string{"project_id", "run_id"},
			},
		},
		{
			Name:        "workspace_commit",
			Description: "Create a durable snapshot commit of the workspace truth layer.",
			OutputSchema: objectSchema(map[string]any{
				"project_id": stringProp("Workspace project ID."),
				"branch":     stringProp("Workspace branch namespace."),
				"commit_id":  stringProp("Snapshot commit ID."),
				"created_at": stringProp("Commit timestamp."),
				"files":      arrayOfObjectsProp(objectSchema(map[string]any{"path": stringProp("Relative file path."), "digest": stringProp("SHA-256 digest."), "size": integerProp("File size in bytes.")}), "Files captured in the snapshot."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "string", "description": "Workspace project ID."},
					"branch":     map[string]any{"type": "string", "default": "main", "description": "Branch namespace."},
					"message":    map[string]any{"type": "string", "description": "Commit message."},
					"author":     map[string]any{"type": "string", "description": "Human or agent author."},
				},
				"required": []string{"project_id"},
			},
		},
		{
			Name:        "workspace_log",
			Description: "List durable snapshot commits for a workspace branch.",
			OutputSchema: objectSchema(map[string]any{
				"project_id": stringProp("Workspace project ID."),
				"branch":     stringProp("Workspace branch namespace."),
				"commits":    arrayOfObjectsProp(objectSchema(map[string]any{"commit_id": stringProp("Snapshot commit ID."), "message": stringProp("Commit message."), "created_at": stringProp("Commit timestamp.")}), "Commits sorted newest first."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "string", "description": "Workspace project ID."},
					"branch":     map[string]any{"type": "string", "default": "main", "description": "Branch namespace."},
				},
				"required": []string{"project_id"},
			},
		},
		{
			Name:        "workspace_revert",
			Description: "Restore the workspace truth layer from a durable snapshot commit; optionally rebuild and activate a fresh markdown index generation.",
			OutputSchema: objectSchema(map[string]any{
				"project_id": stringProp("Workspace project ID."),
				"branch":     stringProp("Workspace branch namespace."),
				"commit_id":  stringProp("Restored commit ID."),
				"files":      arrayOfObjectsProp(objectSchema(map[string]any{"path": stringProp("Relative file path."), "digest": stringProp("SHA-256 digest."), "size": integerProp("File size in bytes.")}), "Files restored from the snapshot."),
				"indexed":    objectSchema(map[string]any{"active_generation": stringProp("Active generation after optional reindex.")}),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id":          map[string]any{"type": "string", "description": "Workspace project ID."},
					"branch":              map[string]any{"type": "string", "default": "main", "description": "Branch namespace."},
					"commit_id":           map[string]any{"type": "string", "description": "Commit ID to restore."},
					"reindex":             map[string]any{"type": "boolean", "default": false, "description": "After restoring files, reconcile markdown into a fresh index generation."},
					"generation":          map[string]any{"type": "string", "description": "Generation to create when reindex=true; defaults to revert-<commit_id>."},
					"collection":          map[string]any{"type": "string", "description": "Optional explicit vector/BM25 collection for the rebuilt generation."},
					"activate_generation": map[string]any{"type": "boolean", "default": true, "description": "Mark rebuilt generation active when reindex=true."},
					"chunk_strategy":      map[string]any{"type": "string", "enum": []string{"merged", "paragraph", "sentence", "sliding"}, "default": "merged"},
					"min_chunk_chars":     map[string]any{"type": "integer", "default": 80},
					"max_chunk_chars":     map[string]any{"type": "integer", "default": 600},
				},
				"required": []string{"project_id", "commit_id"},
			},
		},
		{
			Name:        "workspace_delete",
			Description: "Delete one markdown path from a workspace generation using exact manifest vector IDs.",
			OutputSchema: objectSchema(map[string]any{
				"project_id":         stringProp("Workspace project ID."),
				"branch":             stringProp("Workspace branch namespace."),
				"manifest_path":      stringProp("Durable manifest path."),
				"deleted_vector_ids": arrayOfStringsProp("Vector IDs deleted."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "string", "description": "Workspace project ID."},
					"branch":     map[string]any{"type": "string", "default": "main", "description": "Branch namespace."},
					"generation": map[string]any{"type": "string", "description": "Generation ID."},
					"collection": map[string]any{"type": "string", "description": "Optional explicit vector/BM25 collection."},
					"path":       map[string]any{"type": "string", "description": "Markdown path inside the workspace."},
				},
				"required": []string{"project_id", "generation", "path"},
			},
		},
		{
			Name:        "workspace_gc",
			Description: "Garbage-collect workspace generations marked gc_pending, including vector collections and BM25 sidecar entries.",
			OutputSchema: objectSchema(map[string]any{
				"project_id":        stringProp("Workspace project ID."),
				"branch":            stringProp("Workspace branch namespace."),
				"manifest_path":     stringProp("Durable manifest path."),
				"active_generation": stringProp("Active generation preserved by GC."),
				"result": objectSchema(map[string]any{
					"generations":           arrayOfStringsProp("Generations removed from the manifest."),
					"dropped_collections":   arrayOfStringsProp("Exclusive vector collections dropped."),
					"deleted_vector_ids":    arrayOfStringsProp("Vector IDs deleted from shared collections."),
					"dry_run":               booleanProp("True when this is a preview and no state was changed."),
					"shared_collections":    arrayOfStringsProp("Collections that require exact vector ID deletion."),
					"exclusive_collections": arrayOfStringsProp("Collections used only by GC-pending generations."),
				}),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "string", "description": "Workspace project ID."},
					"branch":     map[string]any{"type": "string", "default": "main", "description": "Branch namespace."},
					"dry_run":    map[string]any{"type": "boolean", "default": false, "description": "Preview GC impact without mutating manifest or vector/BM25 state."},
				},
				"required": []string{"project_id"},
			},
		},
		{
			Name:        "workspace_manifest",
			Description: "Read a workspace manifest with active generation, generation states, and chunk-to-vector records.",
			OutputSchema: objectSchema(map[string]any{
				"project_id":        stringProp("Workspace project ID."),
				"branch":            stringProp("Workspace branch namespace."),
				"manifest_path":     stringProp("Durable manifest path."),
				"active_generation": stringProp("Active generation."),
				"chunks_count":      integerProp("Number of chunk records."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "string", "description": "Workspace project ID."},
					"branch":     map[string]any{"type": "string", "default": "main", "description": "Branch namespace."},
				},
				"required": []string{"project_id"},
			},
		},
		{
			Name:        "check_drift",
			Description: "Check for embedding model drift across collections. Reports collections where the configured model/dimension doesn't match stored data.",
			OutputSchema: objectSchema(map[string]any{
				"current_model":   stringProp("Model name from server config."),
				"current_dim":     integerProp("Dimension from the first collection that reported one."),
				"current_version": stringProp("Current embedding-space fingerprint."),
				"drifted": arrayOfObjectsProp(objectSchema(map[string]any{
					"collection":       stringProp("Collection name."),
					"expected_model":   stringProp("Current configured model."),
					"expected_dim":     integerProp("Current representative dimension."),
					"expected_version": stringProp("Current embedding-space fingerprint."),
					"actual_model":     stringProp("Model recorded when the collection was created."),
					"actual_dim":       integerProp("Stored dimension."),
					"actual_version":   stringProp("Stored embedding-space fingerprint."),
					"is_drifted":       booleanProp("Whether this collection diverges."),
					"record_count":     integerProp("Vector count."),
				}), "Collections whose model or dimension diverges from current."),
			}),
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "prune_graph",
			Description: "Clean up old superseded graph edges and optionally orphaned nodes.",
			OutputSchema: objectSchema(map[string]any{
				"edges_deleted":      integerProp("Superseded edges removed."),
				"edges_would_delete": integerProp("Superseded edges that would be removed when dry_run=true."),
				"orphan_nodes":       integerProp("Orphan nodes removed or that would be removed."),
				"members_cleaned_up": integerProp("Community-member rows cleaned up."),
				"dry_run":            booleanProp("Echo of the request flag."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"max_age_days":         map[string]any{"type": "integer", "default": 90, "description": "Delete superseded edges older than N days."},
					"dry_run":              map[string]any{"type": "boolean", "default": true, "description": "Report what would be deleted without deleting."},
					"include_orphan_nodes": map[string]any{"type": "boolean", "default": false, "description": "Also delete orphaned nodes with no edges."},
				},
			},
		},
		{
			Name:        "list_data",
			Description: "List all datasets and their data items in the knowledge base. Optional tag/room filters narrow the result.",
			OutputSchema: objectSchema(map[string]any{
				"datasets": arrayOfObjectsProp(objectSchema(map[string]any{
					"id":         stringProp("Dataset or data UUID."),
					"name":       stringProp("Dataset, data item, or collection name."),
					"collection": stringProp("Vector collection name."),
					"type":       stringProp("vector_collection | dataset | data."),
					"extension":  stringProp("Data item extension."),
					"room":       stringProp("Data item room."),
					"tags":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Data item tags."},
				}), "Dataset records grouped by ID."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Return only data items with at least one of these tags."},
					"room": map[string]any{"type": "string", "description": "Return only data items in this room (sub-topic)."},
				},
			},
		},
		{
			Name:         "delete",
			Description:  "Delete a specific dataset by ID.",
			OutputSchema: statusMessageSchema(),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"dataset_id": map[string]any{"type": "string", "description": "UUID of the dataset to delete"},
				},
				"required": []string{"dataset_id"},
			},
		},
		{
			Name:         "prune",
			Description:  "Reset all data — removes all datasets, vectors, and graph data.",
			OutputSchema: statusMessageSchema(),
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:         "cognify_status",
			Description:  "Check the status of a running cognify pipeline by run ID.",
			OutputSchema: runStatusSchema(),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"run_id": map[string]any{"type": "string", "description": "Pipeline run ID returned by cognify"},
				},
				"required": []string{"run_id"},
			},
		},
		{
			Name:        "list_communities",
			Description: "List detected communities (entity clusters) with summaries. Communities are detected by Louvain algorithm during cognify full mode.",
			OutputSchema: objectSchema(map[string]any{
				"communities": arrayOfObjectsProp(objectSchema(map[string]any{
					"id":           stringProp("Community UUID."),
					"level":        integerProp("Hierarchy level (0=finest)."),
					"parent_id":    stringProp("Parent community ID."),
					"member_count": integerProp("Number of entities in the community."),
					"summary":      stringProp("LLM-generated summary."),
				}), "Community records sorted by size."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"level":       map[string]any{"type": "integer", "description": "Hierarchy level (0=finest). Omit for all levels."},
					"min_members": map[string]any{"type": "integer", "default": 2, "description": "Minimum member count"},
					"limit":       map[string]any{"type": "integer", "default": 20, "description": "Max communities to return"},
				},
			},
		},
		{
			Name:        "add",
			Description: "Ingest text data into the knowledge base for later cognification. Optional tags/room metadata enable filtered retrieval via list_data.",
			OutputSchema: objectSchema(map[string]any{
				"dataset_id": stringProp("Dataset receiving the new record."),
				"data_id":    stringProp("Newly-created record UUID."),
				"status":     stringProp("ingested | failed."),
				"message":    stringProp("Human-readable ingest summary."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"data":         map[string]any{"type": "string", "description": "Text content to ingest"},
					"dataset_name": map[string]any{"type": "string", "description": "Dataset name (default: 'default')"},
					"collection":   map[string]any{"type": "string", "description": "Collection to associate with added data."},
					"tags":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Tags attached to the data record."},
					"room":         map[string]any{"type": "string", "description": "Room (sub-topic) for the data record."},
				},
				"required": []string{"data"},
			},
		},

		// ── Git Commit Analyzer tools ──
		{
			Name:        "analyze_commits",
			Description: "Analyze git repository commits and build knowledge graph.",
			OutputSchema: objectSchema(map[string]any{
				"commits_analyzed": integerProp("Number of commits scanned."),
				"entities":         integerProp("Entities extracted across the commit history."),
				"edges":            integerProp("Graph edges added."),
				"summary":          stringProp("Truncated text summary of the commit window."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo_path": map[string]any{"type": "string", "description": "Path to git repository"},
					"since":     map[string]any{"type": "string", "description": "Date filter (e.g. 2024-01-01)"},
					"limit":     map[string]any{"type": "number", "description": "Max commits to analyze"},
				},
				"required": []string{"repo_path"},
			},
		},
		{
			Name:        "git_search",
			Description: "Search through analyzed git commits in the knowledge graph.",
			OutputSchema: objectSchema(map[string]any{
				"results": arrayOfObjectsProp(searchResultItemSchema(), "Commit-bearing hits ranked by relevance."),
				"message": stringProp("Optional empty-result explanation."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "Search query about commits"},
				},
				"required": []string{"query"},
			},
		},

		// ── Project Memory tools ──
		{
			Name:         "save_memory",
			Description:  "Save project/user memory key-value pair. Optional room (sub-topic within collection, e.g. auth/deploy/ocr) and hall (memory genre) enable structured retrieval.",
			OutputSchema: statusMessageSchema(),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"key":          map[string]any{"type": "string", "description": "Memory key"},
					"value":        map[string]any{"type": "string", "description": "Memory value"},
					"type":         map[string]any{"type": "string", "description": "Memory type: user, project, feedback"},
					"collection":   map[string]any{"type": "string", "description": "Collection name to scope memory to."},
					"room":         map[string]any{"type": "string", "description": "Sub-topic within collection (auth, deploy, mcp, ocr-bench)."},
					"hall":         map[string]any{"type": "string", "description": "Memory genre (controlled vocab): fact, event, decision, preference, advice, discovery."},
					"pin":          map[string]any{"type": "boolean", "description": "Pin as critical fact for wake_up. Default false."},
					"pin_priority": map[string]any{"type": "integer", "description": "Higher priority loaded first by wake_up. Default 0."},
				},
				"required": []string{"key", "value"},
			},
		},
		{
			Name:        "recall_memory",
			Description: "Search memories by query, optionally filtered by room/hall for higher precision.",
			OutputSchema: objectSchema(map[string]any{
				"results": arrayOfObjectsProp(objectSchema(map[string]any{
					"id":         stringProp("Memory row ID."),
					"key":        stringProp("Memory key."),
					"value":      stringProp("Memory value."),
					"type":       stringProp("Memory type."),
					"owner_id":   stringProp("Memory owner scope."),
					"hall":       stringProp("Memory genre: fact|event|decision|preference|advice|discovery."),
					"room":       stringProp("Sub-topic within the collection."),
					"created_at": stringProp("RFC3339 creation timestamp."),
					"updated_at": stringProp("RFC3339 update timestamp."),
				}), "Recalled memories ranked by relevance."),
				"message": stringProp("Optional empty-result explanation."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":      map[string]any{"type": "string", "description": "Search query for memories"},
					"collection": map[string]any{"type": "string", "description": "Collection name to filter memories."},
					"room":       map[string]any{"type": "string", "description": "Optional room filter (sub-topic)."},
					"hall":       map[string]any{"type": "string", "description": "Optional hall filter (fact|event|decision|preference|advice|discovery)."},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "list_memories",
			Description: "List all memories, optionally filtered by type/room/hall.",
			OutputSchema: objectSchema(map[string]any{
				"memories": arrayOfObjectsProp(objectSchema(map[string]any{
					"id":           stringProp("Memory row ID."),
					"key":          stringProp("Memory key."),
					"value":        stringProp("Memory value."),
					"type":         stringProp("user | project | feedback."),
					"owner_id":     stringProp("Memory owner scope."),
					"hall":         stringProp("Memory genre."),
					"room":         stringProp("Sub-topic."),
					"is_pinned":    booleanProp("True when pinned for wake_up."),
					"pin_priority": integerProp("Pinned memory priority."),
					"created_at":   stringProp("RFC3339."),
					"updated_at":   stringProp("RFC3339."),
				}), "Memories in insertion order."),
				"total": integerProp("Total count before any client-side paging."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"type":       map[string]any{"type": "string", "description": "Optional filter: user, project, feedback"},
					"collection": map[string]any{"type": "string", "description": "Collection name to filter memories."},
					"room":       map[string]any{"type": "string", "description": "Optional room filter."},
					"hall":       map[string]any{"type": "string", "description": "Optional hall filter."},
				},
			},
		},
		{
			Name:         "consolidate",
			Description:  "Consolidate near-duplicate/related memories in a collection: cluster, then merge (deterministic) or abstract (LLM). Reversible. dry_run defaults true.",
			OutputSchema: statusMessageSchema(),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"collection": map[string]any{"type": "string", "description": "Collection to consolidate. Use '_memories' to target the base memory store (records saved with no pinned context)."},
					"room":       map[string]any{"type": "string", "description": "Optional room scope."},
					"hall":       map[string]any{"type": "string", "description": "Optional hall scope."},
					"dry_run":    map[string]any{"type": "boolean", "description": "Preview only, no writes. Default true."},
				},
				"required": []string{"collection"},
			},
		},
		{
			Name:         "consolidation_revert",
			Description:  "Reverse a consolidation run: reactivate superseded source memories and delete generated semantic records. Full reversibility.",
			OutputSchema: statusMessageSchema(),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"run_id": map[string]any{"type": "string", "description": "Consolidation run ID to reverse (returned by consolidate)."},
				},
				"required": []string{"run_id"},
			},
		},
		{
			Name:        "wake_up",
			Description: "Load critical context at session start: pinned memories (priority-ordered) + top entities from knowledge graph (active edges only). Token budget enforced (~200 by default). Cheap alternative to get_project_context.",
			OutputSchema: objectSchema(map[string]any{
				"collection":   stringProp("Collection the snapshot was drawn from."),
				"max_tokens":   integerProp("Requested token budget."),
				"pinned":       arrayOfObjectsProp(objectSchema(map[string]any{"key": stringProp(""), "value": stringProp(""), "hall": stringProp(""), "room": stringProp(""), "priority": integerProp("")}), "Pinned memories in priority order."),
				"top_entities": arrayOfObjectsProp(objectSchema(map[string]any{"name": stringProp(""), "type": stringProp(""), "edge_count": integerProp("")}), "Top-k graph entities by active-edge degree."),
				"tokens_used":  integerProp("Approximate token count (chars/4)."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"collection":   map[string]any{"type": "string", "description": "Collection to wake up. Defaults to session context."},
					"max_tokens":   map[string]any{"type": "integer", "description": "Approximate token budget (chars/4). Default 200."},
					"top_entities": map[string]any{"type": "integer", "description": "How many top graph entities to include. Default 5."},
				},
			},
		},
		{
			Name:         "pin_memory",
			Description:  "Mark a memory as pinned (critical fact). It will be returned by wake_up. Optional priority for ordering.",
			OutputSchema: statusMessageSchema(),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"key":      map[string]any{"type": "string", "description": "Memory key to pin"},
					"priority": map[string]any{"type": "integer", "description": "Higher = loaded first. Default 1."},
				},
				"required": []string{"key"},
			},
		},
		{
			Name:         "unpin_memory",
			Description:  "Remove a memory from the pinned set (it stays in storage).",
			OutputSchema: statusMessageSchema(),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"key": map[string]any{"type": "string", "description": "Memory key to unpin"},
				},
				"required": []string{"key"},
			},
		},
		{
			Name:         "delete_memory",
			Description:  "Permanently delete a memory by key — removes both the stored row and its vector so it no longer surfaces in recall. Scoped to your own (or shared) memories. Optional collection narrows the delete to one pinned-context shard. Reports an error if no memory matched.",
			OutputSchema: statusMessageSchema(),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"key":        map[string]any{"type": "string", "description": "Memory key to delete"},
					"collection": map[string]any{"type": "string", "description": "Optional: only delete the row in this pinned-context collection."},
				},
				"required": []string{"key"},
			},
		},
		{
			Name:        "query_entity",
			Description: "Query all knowledge-graph facts about an entity. By default returns only currently-valid edges. Optional as_of returns the snapshot at a past time.",
			OutputSchema: objectSchema(map[string]any{
				"entity": stringProp("Entity name as stored."),
				"edges": arrayOfObjectsProp(objectSchema(map[string]any{
					"id":            stringProp("Edge ID."),
					"source_id":     stringProp("Source node ID."),
					"target_id":     stringProp("Target node ID."),
					"relationship":  stringProp("Relationship type (assigned_to, located_in, ...)."),
					"properties":    objectSchema(map[string]any{}),
					"valid_from":    stringProp("RFC3339 when the edge became active."),
					"valid_until":   stringProp("RFC3339 when the edge was superseded, empty for active."),
					"superseded_by": stringProp("UUID of the edge that replaced this one, empty for active."),
					"confidence":    numberProp("Confidence score."),
				}), "Edges sorted by recency."),
				"as_of":      stringProp("Snapshot timestamp applied (echo of request)."),
				"dataset_id": stringProp("Dataset scope echo."),
				"node_ids":   arrayOfStringsProp("Matched graph node IDs."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":       map[string]any{"type": "string", "description": "Entity name to query"},
					"as_of":      map[string]any{"type": "string", "description": "RFC3339 timestamp. If omitted, returns currently-valid edges."},
					"dataset_id": map[string]any{"type": "string", "description": "Optional tenant/project scope. When set, only edges from this dataset are returned."},
					"limit":      map[string]any{"type": "integer", "description": "Max edges to return. Default 50."},
				},
				"required": []string{"name"},
			},
		},
		{
			Name:         "diary_write",
			Description:  "Append an entry to a per-agent diary (isolated memory namespace under owner_id=agent:<name>). Use for specialized agents (reviewer, architect, oncall) to keep their own notes without polluting project memory.",
			OutputSchema: statusMessageSchema(),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent":      map[string]any{"type": "string", "description": "Agent name (reviewer, architect, oncall, ...)"},
					"key":        map[string]any{"type": "string", "description": "Diary entry key"},
					"value":      map[string]any{"type": "string", "description": "Diary entry content"},
					"collection": map[string]any{"type": "string", "description": "Optional collection scope"},
				},
				"required": []string{"agent", "key", "value"},
			},
		},
		{
			Name:        "diary_read",
			Description: "Read entries from a per-agent diary, optionally filtered by query.",
			OutputSchema: objectSchema(map[string]any{
				"entries": arrayOfObjectsProp(objectSchema(map[string]any{
					"key":        stringProp("Entry key."),
					"value":      stringProp("Entry text."),
					"created_at": stringProp("RFC3339."),
					"updated_at": stringProp("RFC3339."),
				}), "Diary entries in insertion order."),
				"message": stringProp("Optional empty-diary explanation."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent":      map[string]any{"type": "string", "description": "Agent name"},
					"query":      map[string]any{"type": "string", "description": "Optional substring to filter on. Empty = all."},
					"collection": map[string]any{"type": "string", "description": "Optional collection scope"},
				},
				"required": []string{"agent"},
			},
		},

		// ── Chat History tools ──
		{
			Name:         "save_chat",
			Description:  "Save chat session messages.",
			OutputSchema: statusMessageSchema(),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session_id": map[string]any{"type": "string", "description": "Chat session ID"},
					"collection": map[string]any{"type": "string", "description": "Collection to associate with chat session."},
					"messages": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"role":    map[string]any{"type": "string", "description": "Message role: user or assistant"},
								"content": map[string]any{"type": "string", "description": "Message content"},
							},
						},
						"description": "Array of chat messages",
					},
				},
				"required": []string{"session_id", "messages"},
			},
		},
		{
			Name:        "recall_chat",
			Description: "Recall chat history by session ID.",
			OutputSchema: objectSchema(map[string]any{
				"session_id": stringProp("Echo of the request."),
				"messages": arrayOfObjectsProp(objectSchema(map[string]any{
					"role":       stringProp("user | assistant"),
					"content":    stringProp("Message body."),
					"created_at": stringProp("Message timestamp."),
				}), "Messages in chronological order."),
				"message": stringProp("Optional empty-result explanation."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session_id": map[string]any{"type": "string", "description": "Chat session ID to recall"},
					"collection": map[string]any{"type": "string", "description": "Collection to filter chat recall."},
				},
				"required": []string{"session_id"},
			},
		},
		{
			Name:        "search_chats",
			Description: "Search across all chat sessions.",
			OutputSchema: objectSchema(map[string]any{
				"results": arrayOfObjectsProp(objectSchema(map[string]any{
					"session_id": stringProp("Source session."),
					"id":         stringProp("Interaction row ID."),
					"query":      stringProp("Stored user query."),
					"response":   stringProp("Stored assistant response."),
					"created_at": stringProp("Interaction timestamp."),
					"snippet":    stringProp("Matched snippet."),
					"role":       stringProp("user | assistant"),
					"score":      numberProp("Relevance score."),
				}), "Hits across all chat sessions."),
				"message": stringProp("Optional empty-result explanation."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":      map[string]any{"type": "string", "description": "Search query across chat history"},
					"collection": map[string]any{"type": "string", "description": "Collection to filter chat search."},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "get_project_context",
			Description: "Get full project context: memories, collection stats, key entities, recent interactions. Call at session start for maximum context awareness.",
			OutputSchema: objectSchema(map[string]any{
				"collection": stringProp("Project collection name."),
				"text":       stringProp("Markdown project context summary."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"collection":      map[string]any{"type": "string", "description": "Project collection name"},
					"include_related": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Additional project collections to include compact summaries for"},
				},
				"required": []string{"collection"},
			},
		},
		{
			Name:         "set_context",
			Description:  "Set the default project collection for this MCP session. All subsequent tool calls will use this collection unless explicitly overridden. Call this at session start or when switching projects.",
			OutputSchema: statusMessageSchema(),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"collection": map[string]any{"type": "string", "description": "Project collection name to set as default"},
				},
				"required": []string{"collection"},
			},
		},
		{
			Name:        "cross_search",
			Description: "Search across multiple project collections simultaneously. Results are tagged with their source collection. Use this to find information across projects without switching context. Max 5 collections.",
			OutputSchema: objectSchema(map[string]any{
				"results": arrayOfObjectsProp(objectSchema(map[string]any{
					"collection": stringProp("Source collection."),
					"vectors": arrayOfObjectsProp(objectSchema(map[string]any{
						"id":       stringProp("Vector hit ID."),
						"score":    numberProp("Vector score."),
						"metadata": stringProp("Serialized hit metadata."),
					}), "Vector hits for this collection."),
					"memories": arrayOfObjectsProp(objectSchema(map[string]any{
						"key":   stringProp("Memory key."),
						"value": stringProp("Truncated memory value."),
						"type":  stringProp("Memory type."),
					}), "Memory hits for this collection."),
				}), "Per-collection hit groups."),
				"collections": arrayOfStringsProp("Collections searched."),
				"query":       stringProp("Echo of the search query."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"search_query":     map[string]any{"type": "string", "description": "Natural language search query"},
					"collections":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "List of collection names to search (max 5)"},
					"top_k":            map[string]any{"type": "integer", "description": "Results per collection", "default": 5},
					"include_memories": map[string]any{"type": "boolean", "description": "Also search memories in these collections", "default": true},
				},
				"required": []string{"search_query", "collections"},
			},
		},
		{
			Name:        "sync",
			Description: "Synchronize data with a remote Levara instance. Syncs memories, interactions, graph, and vector collections (re-embedded with local model). Handles different embedding dimensions automatically.",
			OutputSchema: objectSchema(map[string]any{
				"direction": stringProp("pull | push."),
				"counts": map[string]any{
					"type":        "object",
					"description": "Per-type counts: {memories: {pushed, pulled, errors}, graph: {...}, ...}.",
				},
				"remote_manifest": map[string]any{
					"type":        "object",
					"description": "Remote-side manifest snapshot echoed for observability.",
				},
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"remote_url":  map[string]any{"type": "string", "description": "Remote Levara API URL (e.g., http://10.23.0.53:8080/api/v1)"},
					"direction":   map[string]any{"type": "string", "description": "pull (fetch from remote) or push (send to remote)", "default": "pull"},
					"types":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Data types: memories, interactions, graph, collections. Empty = all except collections (explicit opt-in).", "default": []string{"memories", "interactions", "graph"}},
					"collections": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Specific vector collection names to sync (requires re-embedding). Only used when types includes 'collections'."},
					"since":       map[string]any{"type": "string", "description": "Incremental sync: only records updated after this RFC3339 timestamp"},
				},
				"required": []string{"remote_url"},
			},
		},
		{
			Name:         "add_feedback",
			Description:  "Submit feedback on a search result to help improve search quality. Rate results 1-5.",
			OutputSchema: statusMessageSchema(),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":       map[string]any{"type": "string", "description": "The search query that was used"},
					"result_id":   map[string]any{"type": "string", "description": "ID of the result being rated"},
					"collection":  map[string]any{"type": "string", "description": "Collection that was searched"},
					"search_type": map[string]any{"type": "string", "description": "Search type used (CHUNKS, HYBRID, etc.)"},
					"rating":      map[string]any{"type": "integer", "description": "Rating 1-5 (1=irrelevant, 5=perfect)"},
					"comment":     map[string]any{"type": "string", "description": "Optional comment on why this rating"},
				},
				"required": []string{"query", "rating"},
			},
		},
		{
			Name:        "get_feedback_stats",
			Description: "Get aggregated feedback statistics: average rating, total count, worst queries.",
			OutputSchema: objectSchema(map[string]any{
				"total":       integerProp("Total feedback rows."),
				"avg_rating":  numberProp("Mean rating across all feedback."),
				"worst_query": stringProp("Query with the lowest average rating."),
				"collection":  stringProp("Collection filter echo."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"collection": map[string]any{"type": "string", "description": "Filter stats by collection (optional)"},
				},
			},
		},
		{
			Name:        "codify",
			Description: "Analyze source code and extract entities (functions, classes, imports) and relationships (CALLS, IMPORTS, EXTENDS). Supports Go and Python.",
			OutputSchema: objectSchema(map[string]any{
				"language":  stringProp("Detected language (go | python)."),
				"entities":  integerProp("Count of extracted entities."),
				"relations": integerProp("Count of extracted relations."),
				"text":      stringProp("Pretty-printed extraction summary."),
				"details":   objectSchema(map[string]any{}),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"code":       map[string]any{"type": "string", "description": "Source code to analyze"},
					"filename":   map[string]any{"type": "string", "description": "Filename for language detection (e.g., main.go, app.py)"},
					"collection": map[string]any{"type": "string", "description": "Collection to store extracted code entities"},
				},
				"required": []string{"code", "filename"},
			},
		},

		// ── System diagnostics tools ──
		{
			Name:        "doctor",
			Description: "Self-diagnose system health: service connectivity (postgres, embed, LLM, neo4j), embedding coverage, BM25 index coverage, graph connectivity, memory staleness. Returns structured checks with actionable remediation advice.",
			OutputSchema: objectSchema(map[string]any{
				"checks": arrayOfObjectsProp(objectSchema(map[string]any{
					"name":        stringProp("Check name (postgres, embed, neo4j, ...)."),
					"status":      map[string]any{"type": "string", "enum": []string{"ok", "warn", "fail"}},
					"message":     stringProp("Free-form explanation."),
					"remediation": stringProp("Remediation suggestion when status != ok."),
				}), "Per-dependency check results."),
				"status":  map[string]any{"type": "string", "enum": []string{"ok", "warn", "fail"}},
				"summary": stringProp("Short rollup of ok/warn/fail counts."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"verbose": map[string]any{"type": "boolean", "default": false, "description": "Include per-collection breakdown in coverage checks."},
				},
			},
		},
		{
			Name:        "levara_instructions",
			Description: "Versioned agent contract: memory model (room × hall), when-to-save rules, what-not-to-save, recall patterns, pin policy, observability toolkit, anti-patterns. Returns the embedded markdown plus a content hash for client-side caching. Call once per session and follow what it says — overrides any conflicting hint in the initialize response.",
			OutputSchema: objectSchema(map[string]any{
				"version":          stringProp("Contract revision string (bumped on meaningful changes)."),
				"content_sha":      stringProp("SHA-256 of the markdown body, hex-encoded."),
				"content_markdown": stringProp("Full agent contract as markdown."),
			}),
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "runtime_stats",
			Description: "Snapshot of live runtime state: per-collection record counts and embedding model, dependency configuration (embed/llm/rerank/neo4j), goroutine and heap stats. Use to answer 'what is this instance running right now?' without parsing /metrics.",
			OutputSchema: objectSchema(map[string]any{
				"collections": arrayOfObjectsProp(objectSchema(map[string]any{
					"name":            stringProp("Collection name."),
					"records":         integerProp("Vector count."),
					"dim":             integerProp("Embedding dimension."),
					"metric":          stringProp("cosine|l2|dot"),
					"embedding_model": stringProp("Model used to embed this collection."),
					"domain":          stringProp("Optional domain tag."),
				}), "Per-collection state."),
				"collection_count":  integerProp("Number of collections."),
				"total_records":     integerProp("Sum of records across all collections."),
				"embed_endpoint":    stringProp("Embedding service URL."),
				"embed_model":       stringProp("Default embedding model."),
				"llm_provider":      stringProp("'configured' when an LLM provider is wired, '' otherwise."),
				"llm_model":         stringProp("LLM model name."),
				"rerank_enabled":    map[string]any{"type": "boolean", "description": "Whether a reranker is wired."},
				"rerank_model":      stringProp("Reranker model name."),
				"neo4j_enabled":     map[string]any{"type": "boolean", "description": "Whether Neo4j graph backend is configured."},
				"goroutines":        integerProp("Live goroutine count."),
				"heap_alloc_bytes":  integerProp("Currently allocated heap bytes."),
				"heap_sys_bytes":    integerProp("Heap memory obtained from OS."),
				"num_gc":            integerProp("Completed GC cycles since start."),
				"snapshot_taken_at": stringProp("RFC3339 timestamp."),
			}),
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "ingestion_status",
			Description: "List in-flight and recently completed background pipeline runs (cognify, codify, analyze_commits) from the run registry, sorted newest-first. Use to debug stuck ingestion or confirm a long-running cognify finished.",
			OutputSchema: objectSchema(map[string]any{
				"summary": objectSchema(map[string]any{
					"total":     integerProp("Total runs currently retained."),
					"running":   integerProp("Runs in RUNNING state."),
					"completed": integerProp("Runs in COMPLETED state."),
					"failed":    integerProp("Runs in FAILED state."),
				}),
				"runs": arrayOfObjectsProp(objectSchema(map[string]any{
					"pipeline_run_id":    stringProp("Run UUID."),
					"status":             stringProp("RUNNING|COMPLETED|FAILED"),
					"stage":              stringProp("Current/last stage name."),
					"message":            stringProp("Free-form status message."),
					"chunks_created":     integerProp(""),
					"entities_extracted": integerProp(""),
					"edges_extracted":    integerProp(""),
					"elapsed_ms":         integerProp("Wall-clock duration."),
					"started_at":         stringProp("RFC3339."),
				}), "Filtered run list (newest first)."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"status": map[string]any{"type": "string", "description": "Filter by RUNNING|COMPLETED|FAILED. Empty = all."},
					"limit":  map[string]any{"type": "integer", "default": 20, "description": "Max runs to return (1-100)."},
				},
			},
		},
		{
			Name:        "recent_errors",
			Description: "Aggregated recent error signals: FAILED background runs (cognify/codify/analyze_commits) plus doctor heartbeats whose checks reported status=fail. Use as a single 'what's been going wrong lately?' query without grepping logs.",
			OutputSchema: objectSchema(map[string]any{
				"count": integerProp("Total entries returned."),
				"errors": arrayOfObjectsProp(objectSchema(map[string]any{
					"source":    stringProp("'pipeline_run' or 'doctor'."),
					"stage":     stringProp("Run stage or doctor check name."),
					"message":   stringProp("Free-form error message."),
					"reference": stringProp("run_id or heartbeat_id."),
					"at":        stringProp("RFC3339 timestamp."),
				}), "Error entries, newest-ish first."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{"type": "integer", "default": 20, "description": "Max entries (1-100)."},
				},
			},
		},
		{
			Name:        "reconcile_memory",
			Description: "Verify (and optionally repair) SQL↔vector consistency for the memory palace. The SQL memories table is the source of truth; each _memories_* sidecar should hold one vector per live row. Reports missing_vector (SQL row with no vector) and orphan_vector (vector with no live row). Dry-run by default; apply=true re-embeds missing vectors under their canonical id, and apply+delete_orphans removes orphan vectors. The durable backstop to per-write index verification.",
			OutputSchema: objectSchema(map[string]any{
				"apply":                booleanProp("Whether mutations were applied."),
				"delete_orphans":       booleanProp("Whether orphan vectors were deleted."),
				"sidecars_scanned":     integerProp("Number of _memories_* sidecars examined."),
				"total_missing":        integerProp("SQL rows with no vector."),
				"total_orphan":         integerProp("Vectors with no live SQL row."),
				"total_repaired":       integerProp("Missing vectors re-embedded and inserted."),
				"total_repair_failed":  integerProp("Repairs that failed (embed/insert error)."),
				"total_orphan_deleted": integerProp("Orphan vectors deleted."),
				"sidecars": arrayOfObjectsProp(objectSchema(map[string]any{
					"sidecar":        stringProp("Vector collection name."),
					"sql_rows":       integerProp("Live SQL rows for this context."),
					"vectors_before": integerProp("Vectors present before reconcile."),
					"missing_vector": integerProp("SQL rows lacking a vector."),
					"orphan_vector":  integerProp("Vectors lacking a SQL row."),
					"repaired":       integerProp("Vectors re-created this run."),
					"repair_failed":  integerProp("Failed repairs this run."),
					"orphan_deleted": integerProp("Orphans deleted this run."),
				}), "Per-sidecar findings."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"collection":     map[string]any{"type": "string", "description": "Limit to one logical context (e.g. 'levara'); empty = all sidecars."},
					"apply":          map[string]any{"type": "boolean", "default": false, "description": "Re-embed and insert missing vectors. Default false (dry-run)."},
					"delete_orphans": map[string]any{"type": "boolean", "default": false, "description": "With apply, also delete orphan vectors. Default false."},
				},
			},
		},
		{
			Name:        "sync_status",
			Description: "Summarize recent sync events per direction (push|pull) from heartbeats: count, last-seen-at, last remote URL, and the most recent N events. Sync emits a heartbeat on success only — answers 'did sync run lately?' rather than 'did sync fail?'.",
			OutputSchema: objectSchema(map[string]any{
				"by_direction": map[string]any{
					"type":        "object",
					"description": "Per-direction summary: count, last_at, last_remote.",
				},
				"events": arrayOfObjectsProp(objectSchema(map[string]any{
					"id":        stringProp("Heartbeat UUID."),
					"direction": stringProp("push|pull|unknown."),
					"remote":    stringProp("Remote URL."),
					"types":     map[string]any{"description": "Sync types payload as recorded."},
					"at":        stringProp("RFC3339."),
				}), "Recent sync events, newest first."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{"type": "integer", "default": 10, "description": "Max events (1-50)."},
				},
			},
		},
		{
			Name:        "heartbeat",
			Description: "Query recent system heartbeat events (doctor runs, sync, cognify completions, prune). Useful to understand system activity history and detect degradation patterns.",
			OutputSchema: objectSchema(map[string]any{
				"count": integerProp("Number of events returned."),
				"events": arrayOfObjectsProp(objectSchema(map[string]any{
					"id":         stringProp("Event UUID."),
					"event_type": stringProp("doctor | sync | cognify | prune | ..."),
					"payload":    map[string]any{"type": "object", "description": "Event-specific structured payload."},
					"created_at": stringProp("RFC3339."),
				}), "Recent events ordered by created_at desc."),
			}),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"event_type": map[string]any{"type": "string", "description": "Filter by event type: doctor, sync, cognify, prune. Empty = all."},
					"limit":      map[string]any{"type": "integer", "default": 10, "description": "Max events to return (1-100)."},
				},
			},
		},
	}
}
