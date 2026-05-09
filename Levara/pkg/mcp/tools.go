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
		OutputSchema: objectSchema(map[string]any{
			"results":     arrayOfObjectsProp(searchResultItemSchema(), "Ranked hits from the selected strategy."),
			"search_type": stringProp("Strategy actually used (may differ from the request under AUTO routing)."),
			"total":       integerProp("Total matches before top_k truncation (may be estimated)."),
		}),
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
		OutputSchema: objectSchema(map[string]any{
			"current_model": stringProp("Model name from server config."),
			"current_dim":   integerProp("Dimension from the first collection that reported one."),
			"drifted": arrayOfObjectsProp(objectSchema(map[string]any{
				"collection":     stringProp("Collection name."),
				"stored_model":   stringProp("Model recorded when the collection was created."),
				"stored_dim":     integerProp("Stored dimension."),
				"reason":         stringProp("Why this collection counts as drifted."),
			}), "Collections whose model or dimension diverges from current."),
		}),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{},
		},
	},
	{
		Name:        "prune_graph",
		Description: "Clean up old superseded graph edges and optionally orphaned nodes.",
		OutputSchema: objectSchema(map[string]any{
			"edges_pruned": integerProp("Superseded edges removed (or that would be removed when dry_run=true)."),
			"nodes_pruned": integerProp("Orphan nodes removed (only when include_orphan_nodes=true)."),
			"dry_run":      booleanProp("Echo of the request flag."),
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
				"id":         stringProp("Dataset UUID."),
				"name":       stringProp("Dataset name."),
				"item_count": integerProp("Number of data items."),
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
				"id":      stringProp("Community UUID."),
				"level":   integerProp("Hierarchy level (0=finest)."),
				"members": integerProp("Number of entities in the community."),
				"summary": stringProp("LLM-generated summary."),
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
				"key":   stringProp("Memory key."),
				"value": stringProp("Memory value (possibly truncated)."),
				"hall":  stringProp("Memory genre: fact|event|decision|preference|advice|discovery."),
				"room":  stringProp("Sub-topic within the collection."),
				"score": numberProp("Relevance score when the recall was semantic."),
			}), "Recalled memories ranked by relevance."),
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
				"key":        stringProp("Memory key."),
				"value":      stringProp("Memory value."),
				"type":       stringProp("user | project | feedback."),
				"hall":       stringProp("Memory genre."),
				"room":       stringProp("Sub-topic."),
				"pinned":     booleanProp("True when pinned for wake_up."),
				"created_at": stringProp("RFC3339."),
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
		Name:        "wake_up",
		Description: "Load critical context at session start: pinned memories (priority-ordered) + top entities from knowledge graph (active edges only). Token budget enforced (~200 by default). Cheap alternative to get_project_context.",
		OutputSchema: objectSchema(map[string]any{
			"collection":   stringProp("Collection the snapshot was drawn from."),
			"pinned":       arrayOfObjectsProp(objectSchema(map[string]any{"key": stringProp(""), "value": stringProp(""), "priority": integerProp("")}), "Pinned memories in priority order."),
			"top_entities": arrayOfObjectsProp(objectSchema(map[string]any{"name": stringProp(""), "type": stringProp(""), "edge_count": integerProp("")}), "Top-k graph entities by active-edge degree."),
			"tokens_used":  integerProp("Approximate token count (chars/4)."),
		}),
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
		Name:        "query_entity",
		Description: "Query all knowledge-graph facts about an entity. By default returns only currently-valid edges. Optional as_of returns the snapshot at a past time.",
		OutputSchema: objectSchema(map[string]any{
			"entity": stringProp("Entity name as stored."),
			"edges": arrayOfObjectsProp(objectSchema(map[string]any{
				"relation":        stringProp("Relationship type (assigned_to, located_in, ...)."),
				"target":          stringProp("Target entity name."),
				"valid_from":      stringProp("RFC3339 when the edge became active."),
				"valid_until":     stringProp("RFC3339 when the edge was superseded, empty for active."),
				"superseded_by":   stringProp("UUID of the edge that replaced this one, empty for active."),
			}), "Edges sorted by recency."),
			"as_of": stringProp("Snapshot timestamp applied (echo of request)."),
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
			}), "Diary entries in insertion order."),
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
		Name:        "save_chat",
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
				"role":    stringProp("user | assistant"),
				"content": stringProp("Message body."),
			}), "Messages in chronological order."),
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
				"snippet":    stringProp("Matched snippet."),
				"role":       stringProp("user | assistant"),
				"score":      numberProp("Relevance score."),
			}), "Hits across all chat sessions."),
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
			"collection":   stringProp("Project collection name."),
			"records":      integerProp("Total records in the collection."),
			"key_entities": arrayOfStringsProp("Top entities by graph degree."),
			"memories":     arrayOfObjectsProp(objectSchema(map[string]any{"key": stringProp(""), "value": stringProp("")}), "Recent project memories."),
			"interactions": integerProp("Recent interaction count."),
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
			"results": arrayOfObjectsProp(searchResultItemSchema(), "Merged hits; each carries its source collection."),
			"per_collection": map[string]any{
				"type":        "object",
				"description": "Per-collection hit counts keyed on collection name.",
			},
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
				"name":   stringProp("Check name (postgres, embed, neo4j, ...)."),
				"status": map[string]any{"type": "string", "enum": []string{"ok", "warn", "fail"}},
				"detail": stringProp("Free-form explanation."),
				"hint":   stringProp("Remediation suggestion when status != ok."),
			}), "Per-dependency check results."),
			"overall": map[string]any{"type": "string", "enum": []string{"ok", "warn", "fail"}},
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
