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
		Name:        "cognify",
		Description: "Transform text data into a structured knowledge graph, or ingest in RAG mode (chunk+embed only, no LLM). Optional room/tags propagate to chunk metadata for filtered search.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"data":           map[string]any{"type": "string", "description": "Text data to process into knowledge graph"},
				"collection":     map[string]any{"type": "string", "description": "Target collection name (default: 'default')"},
				"custom_prompt":  map[string]any{"type": "string", "description": "Custom LLM prompt for entity extraction (ignored in rag mode)"},
				"room":           map[string]any{"type": "string", "description": "Sub-topic label attached to every chunk for filtered retrieval (auth, deploy, ocr-bench)."},
				"tags":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Tags attached to every chunk."},
				"mode":           map[string]any{"type": "string", "enum": []string{"rag", "full"}, "default": "full", "description": "Pipeline mode. 'rag': chunk+embed only (no LLM, no graph). 'full': complete pipeline with entity extraction."},
				"chunk_strategy": map[string]any{"type": "string", "enum": []string{"merged", "paragraph", "sentence", "row", "code", "sliding", "auto"}, "default": "merged", "description": "Chunking strategy. 'sliding' enables fixed-window with overlap."},
				"overlap_chars":      map[string]any{"type": "integer", "description": "Overlap in characters for sliding window chunking. Default: 20% of max_chunk_chars."},
				"snap_to_sentence":  map[string]any{"type": "boolean", "default": true, "description": "Snap sliding window boundaries to sentence/word ends (avoids cutting words)."},
				"parent_child":      map[string]any{"type": "boolean", "default": false, "description": "Enable parent-child chunking. Small chunks for search precision, large chunks for context."},
				"document_title":        map[string]any{"type": "string", "description": "Document title for attribution. Prepended as contextual header to chunk embeddings."},
				"document_id":           map[string]any{"type": "string", "description": "Stable document ID for metadata tracking."},
				"community_resolution":  map[string]any{"type": "number", "default": 1.0, "description": "Louvain resolution (γ). >1=finer, <1=coarser. Presets: 0.5=coarse, 1.0=medium, 2.0=fine."},
				"dedup_threshold":       map[string]any{"type": "number", "default": 0.95, "description": "Semantic entity dedup threshold (0.5-1.0). Higher=stricter."},
				"min_chunk_chars":       map[string]any{"type": "integer", "default": 80, "description": "Min chunk size in characters."},
				"max_chunk_chars":       map[string]any{"type": "integer", "default": 600, "description": "Max chunk size in characters."},
			},
			"required": []string{"data"},
		},
	},
	{
		Name:        "search",
		Description: "Search the knowledge graph using various strategies. Use AUTO (default) for intelligent routing that analyzes your query and selects the best strategy automatically. Optional room/tags filters narrow chunk results by metadata (overfetched ×3 then post-filtered).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"search_query": map[string]any{"type": "string", "description": "Natural language search query"},
				"search_type":  map[string]any{"type": "string", "description": "Search strategy: AUTO (intelligent routing), CHUNKS (vector), HYBRID (vector+BM25), RAG_COMPLETION (vector+LLM answer), GRAPH_COMPLETION (graph traversal+LLM), TEMPORAL (date-aware), SUMMARIES, CHUNKS_LEXICAL (BM25), CODING_RULES (code entities)", "default": "AUTO"},
				"top_k":        map[string]any{"type": "integer", "description": "Number of results to return", "default": 10},
				"collection":   map[string]any{"type": "string", "description": "Project collection name to search in. Leave empty to search all."},
				"room":         map[string]any{"type": "string", "description": "Filter chunks by room (sub-topic) attached during cognify."},
				"tags":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Filter chunks by tags (any-match)."},
				"rerank":       map[string]any{"type": "boolean", "default": false, "description": "Apply cross-encoder reranking to results. Requires RERANK_ENDPOINT configured."},
				"parent_child": map[string]any{"type": "boolean", "default": false, "description": "Search child chunks for precision, return parent chunks for context."},
				"multi_query":  map[string]any{"type": "boolean", "default": false, "description": "Generate query variants via LLM for broader retrieval. Requires LLM."},
				"dedup":        map[string]any{"type": "boolean", "default": true, "description": "Deduplicate overlapping results (useful for sliding window chunks)."},
				"mode":         map[string]any{"type": "string", "enum": []string{"rag", "graph", "full", "auto"}, "default": "auto", "description": "Search mode. rag: vector only. graph: graph traversal only. full: all backends. auto: router decides."},
				"graph_rerank": map[string]any{"type": "boolean", "default": false, "description": "Rerank results using graph proximity (entity hop distance)."},
			},
			"required": []string{"search_query"},
		},
	},
	{
		Name:        "check_drift",
		Description: "Check for embedding model drift across collections. Reports collections where the configured model/dimension doesn't match stored data.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{},
		},
	},
	{
		Name:        "prune_graph",
		Description: "Clean up old superseded graph edges and optionally orphaned nodes.",
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
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Return only data items with at least one of these tags."},
				"room": map[string]any{"type": "string", "description": "Return only data items in this room (sub-topic)."},
			},
		},
	},
	{
		Name:        "delete",
		Description: "Delete a specific dataset by ID.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"dataset_id": map[string]any{"type": "string", "description": "UUID of the dataset to delete"},
			},
			"required": []string{"dataset_id"},
		},
	},
	{
		Name:        "prune",
		Description: "Reset all data — removes all datasets, vectors, and graph data.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
	{
		Name:        "cognify_status",
		Description: "Check the status of a running cognify pipeline by run ID.",
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
		Name:        "save_memory",
		Description: "Save project/user memory key-value pair. Optional room (sub-topic within collection, e.g. auth/deploy/ocr) and hall (memory genre) enable structured retrieval.",
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
		Name:        "wake_up",
		Description: "Load critical context at session start: pinned memories (priority-ordered) + top entities from knowledge graph (active edges only). Token budget enforced (~200 by default). Cheap alternative to get_project_context.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"collection":  map[string]any{"type": "string", "description": "Collection to wake up. Defaults to session context."},
				"max_tokens":  map[string]any{"type": "integer", "description": "Approximate token budget (chars/4). Default 200."},
				"top_entities": map[string]any{"type": "integer", "description": "How many top graph entities to include. Default 5."},
			},
		},
	},
	{
		Name:        "pin_memory",
		Description: "Mark a memory as pinned (critical fact). It will be returned by wake_up. Optional priority for ordering.",
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
		Name:        "unpin_memory",
		Description: "Remove a memory from the pinned set (it stays in storage).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"key": map[string]any{"type": "string", "description": "Memory key to unpin"},
			},
			"required": []string{"key"},
		},
	},
	{
		Name:        "query_entity",
		Description: "Query all knowledge-graph facts about an entity. By default returns only currently-valid edges. Optional as_of returns the snapshot at a past time.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":  map[string]any{"type": "string", "description": "Entity name to query"},
				"as_of": map[string]any{"type": "string", "description": "RFC3339 timestamp. If omitted, returns currently-valid edges."},
				"limit": map[string]any{"type": "integer", "description": "Max edges to return. Default 50."},
			},
			"required": []string{"name"},
		},
	},
	{
		Name:        "diary_write",
		Description: "Append an entry to a per-agent diary (isolated memory namespace under owner_id=agent:<name>). Use for specialized agents (reviewer, architect, oncall) to keep their own notes without polluting project memory.",
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
		Name:        "save_chat",
		Description: "Save chat session messages.",
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
		Name:        "set_context",
		Description: "Set the default project collection for this MCP session. All subsequent tool calls will use this collection unless explicitly overridden. Call this at session start or when switching projects.",
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
		Name:        "add_feedback",
		Description: "Submit feedback on a search result to help improve search quality. Rate results 1-5.",
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
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"verbose": map[string]any{"type": "boolean", "default": false, "description": "Include per-collection breakdown in coverage checks."},
			},
		},
	},
	{
		Name:        "heartbeat",
		Description: "Query recent system heartbeat events (doctor runs, sync, cognify completions, prune). Useful to understand system activity history and detect degradation patterns.",
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
