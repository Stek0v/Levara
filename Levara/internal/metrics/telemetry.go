package metrics

import (
	"context"
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// 1. Throughput (Counters)
	SearchRequests = promauto.NewCounter(prometheus.CounterOpts{
		Name: "levara_search_requests_total",
		Help: "Total number of search requests received",
	})

	InsertRequests = promauto.NewCounter(prometheus.CounterOpts{
		Name: "levara_insert_requests_total",
		Help: "Total number of insert requests received",
	})

	// 2. Latency (Histograms) - Crucial for measuring P99
	SearchDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "levara_search_duration_seconds",
		Help:    "Time taken to process search requests",
		Buckets: prometheus.DefBuckets, // []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
	})

	InsertDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "levara_insert_duration_seconds",
		Help:    "Time taken to process insert requests (including Raft consensus)",
		Buckets: []float64{.01, .05, .1, .5, 1, 2.5, 5}, // Slower buckets for Raft writes
	})

	// 3. State (Gauges)
	TotalVectors = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "levara_vectors_total",
		Help: "Current number of vectors in the Arena",
	})

	RaftState = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "levara_raft_state",
		Help: "Current Raft state (0=Follower, 1=Candidate, 2=Leader)",
	})

	// 4. Search routing
	SearchRequestsByType = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "levara_search_requests_by_type_total",
		Help: "Search requests by type and source (explicit vs routed)",
	}, []string{"search_type", "source"})

	RouterDecisionDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "levara_router_decision_seconds",
		Help:    "Time taken for search router to select strategy",
		Buckets: []float64{.00005, .0001, .0005, .001, .005},
	})

	// 5. MCP tool metrics
	MCPToolDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "levara_mcp_tool_duration_seconds",
		Help:    "MCP tool call latency by tool name",
		Buckets: []float64{.005, .01, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
	}, []string{"tool"})

	MCPToolRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "levara_mcp_tool_requests_total",
		Help: "MCP tool calls by tool name and status (ok/error)",
	}, []string{"tool", "status"})

	MCPSessionsActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "levara_mcp_sessions_active",
		Help: "Number of active MCP sessions",
	})

	// 6. Embedding metrics
	EmbedDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "levara_embed_duration_seconds",
		Help:    "Embedding API call latency",
		Buckets: []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10},
	})

	EmbedRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "levara_embed_requests_total",
		Help: "Embedding API calls by model and status",
	}, []string{"model", "status"})

	// 7. LLM metrics
	LLMDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "levara_llm_duration_seconds",
		Help:    "LLM completion call latency",
		Buckets: []float64{.1, .25, .5, 1, 2.5, 5, 10, 30, 60},
	})

	LLMRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "levara_llm_requests_total",
		Help: "LLM completion calls by model and status",
	}, []string{"model", "status"})

	// 8. Sync metrics
	SyncOperations = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "levara_sync_operations_total",
		Help: "Sync operations by direction, data type, and status",
	}, []string{"direction", "type", "status"})

	// 9. Data state
	MemoriesTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "levara_memories_total",
		Help: "Current number of memories in SQL",
	})

	CollectionRecords = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "levara_collection_records_total",
		Help: "Number of records per collection",
	}, []string{"collection"})

	// 10. Pipeline reliability
	CognifyPanics = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "levara_cognify_panics_total",
		Help: "Panics recovered in cognify/memify pipeline goroutines, by stage",
	}, []string{"stage"})

	// 11. Rate limiting (T2)
	RateLimitRejected = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "levara_rate_limit_rejected_total",
		Help: "Requests rejected by rate limiter, by channel and bucket",
	}, []string{"channel", "bucket"})

	// 12. Generic HTTP operation counters (T17). user_id cardinality is
	// bounded by UserBucket (top-50 + "other" + "anon") so the series
	// count stays predictable under load.
	HTTPRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "levara_http_requests_total",
		Help: "HTTP requests by logical operation, result, and bucketed user_id",
	}, []string{"operation", "status", "user_id"})

	HTTPDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "levara_http_duration_seconds",
		Help:    "HTTP handler latency by logical operation and bucketed user_id",
		Buckets: prometheus.DefBuckets,
	}, []string{"operation", "user_id"})

	UserBucketSize = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "levara_user_bucket_size",
		Help: "Number of user_ids currently promoted to real labels (top-N whitelist)",
	})

	// 13. WAL recovery (T16). Eagerly registered so the series appears at 0
	// on a clean start — operators alert on any non-zero, restart-bumped value.
	WALRecoveriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "levara_wal_recoveries_total",
		Help: "WAL recovery passes by result (ok|fail), summed across shards",
	}, []string{"result"})

	// 14. RAG verify-stack observability. Default thresholds are 0 (disabled)
	// so these metrics let an operator observe live confidence distributions
	// before opting in to abstain/verify behavior.
	RAGConfidence = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "levara_rag_confidence",
		Help:    "Distribution of combined RAG confidence scores by search type",
		Buckets: []float64{.05, .1, .2, .3, .4, .5, .6, .7, .8, .9, .95},
	}, []string{"search_type"})

	RAGAbstainTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "levara_rag_abstain_total",
		Help: "RAG completions that abstained, by search type and reason (low_confidence|no_evidence)",
	}, []string{"search_type", "reason"})

	RAGVerifyDroppedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "levara_rag_verify_dropped_total",
		Help: "Result rows dropped by verify stage, by search type and reason (low_score|bad_metadata)",
	}, []string{"search_type", "reason"})

	// 15. External calls (T2-B). Unified observability across every outbound
	// dependency the request path touches. {target} identifies the system
	// (neo4j, postgres-graph, embed, llm, rerank), {op} the logical action
	// (read, write, query, embed, complete, rerank), and for the duration
	// histogram {result} captures the outcome (ok, error, timeout, cancelled).
	// Eager-init in init() materializes the cells so dashboards see zero
	// series instead of "no data".
	ExternalCallDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "levara_external_call_duration_seconds",
		Help:    "Outbound dependency call latency by target, operation, and result",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
	}, []string{"target", "op", "result"})

	ExternalCallTimeouts = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "levara_external_call_timeouts_total",
		Help: "Outbound dependency calls that exceeded their request-scoped deadline",
	}, []string{"target", "op"})

	// 16. Markdown workspace operational health. These gauges are refreshed by
	// workspace ops/status and by indexing job transitions; audit events are
	// counted at write time.
	WorkspaceIndexJobs = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "levara_workspace_index_jobs",
		Help: "Durable workspace indexing jobs by status",
	}, []string{"status"})

	WorkspaceIndexJobMaxLagSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "levara_workspace_index_job_max_lag_seconds",
		Help: "Maximum age in seconds among pending/failed workspace indexing jobs",
	})

	WorkspaceIndexDeadLetters = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "levara_workspace_index_dead_letters",
		Help: "Number of workspace indexing jobs currently in dead_letter status",
	})

	WorkspaceWatcherPendingBranches = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "levara_workspace_watcher_pending_branches",
		Help: "Number of workspace project/branch trees with pending watcher reconciliation",
	})

	WorkspaceWatcherErrors = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "levara_workspace_watcher_errors",
		Help: "Workspace watcher error count recorded in the current process/status file",
	})

	WorkspaceAuditEventsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "levara_workspace_audit_events_total",
		Help: "Workspace audit events by source, operation, and result",
	}, []string{"source", "operation", "result"})

	WorkspaceAuditStoredEvents = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "levara_workspace_audit_stored_events",
		Help: "Number of sanitized workspace audit events found on disk by the last ops/status refresh",
	})
)

func init() {
	// Force "ok" and "fail" series to materialize at 0 so /metrics never
	// hides them just because no recovery has happened in this process.
	WALRecoveriesTotal.WithLabelValues("ok")
	WALRecoveriesTotal.WithLabelValues("fail")

	// Eagerly materialize RAG verify-stack series at 0 across the known
	// search types and reasons so dashboards/alerts can reference a stable
	// label set instead of waiting for a real abstain/drop to appear.
	for _, st := range []string{"RAG_COMPLETION", "GRAPH_COMPLETION", "GRAPH_COMPLETION_CONTEXT_EXTENSION"} {
		RAGAbstainTotal.WithLabelValues(st, "low_confidence")
		RAGAbstainTotal.WithLabelValues(st, "strict_grounded_no_evidence")
		RAGVerifyDroppedTotal.WithLabelValues(st, "low_score")
		RAGVerifyDroppedTotal.WithLabelValues(st, "bad_metadata")
	}

	// Eager-init external-call cells so /metrics shows the full label space
	// from process start. Lazy promauto leaves un-fired combinations
	// invisible — dashboards and alerts then read "no data" instead of
	// recognizing a healthy zero. Keep this list in sync with ObserveExternalCall
	// callers.
	for _, ts := range [][2]string{
		{"neo4j", "read"},
		{"neo4j", "write"},
		{"neo4j", "query"},
		{"neo4j", "migrate_valid_from"},
		{"postgres-graph", "read"},
		{"postgres-graph", "write"},
		{"embed", "embed"},
		{"llm", "complete"},
		{"rerank", "rerank"},
	} {
		for _, result := range []string{"ok", "error", "timeout", "cancelled"} {
			ExternalCallDuration.WithLabelValues(ts[0], ts[1], result)
		}
		ExternalCallTimeouts.WithLabelValues(ts[0], ts[1])
	}

	for _, status := range []string{"pending", "running", "completed", "failed", "dead_letter"} {
		WorkspaceIndexJobs.WithLabelValues(status)
	}
	for _, source := range []string{"rest", "mcp"} {
		for _, operation := range []string{
			"access_check", "audit", "audit_log", "commit", "conflicts", "context", "delete", "enqueue_index_job", "gc",
			"context_artifacts", "index", "index_jobs", "log", "manifest", "ops_status", "read", "reindex",
			"reindex_artifacts", "reconcile", "retry_index_job", "revert", "run_get", "run_start", "search",
			"watch_status", "write",
		} {
			for _, result := range []string{"success", "failure", "denied"} {
				WorkspaceAuditEventsTotal.WithLabelValues(source, operation, result)
			}
		}
	}
}

// ObserveExternalCall records duration and outcome of an outbound dependency
// call. err==nil → "ok"; ctx-deadline hits → "timeout" plus a counter bump;
// ctx-cancellation → "cancelled"; anything else → "error". Designed to be
// invoked via `defer metrics.ObserveExternalCall(target, op, time.Now(), &err)`
// so the named return propagates the call's err verbatim.
func ObserveExternalCall(target, op string, start time.Time, err *error) {
	result := "ok"
	if err != nil && *err != nil {
		switch {
		case errors.Is(*err, context.DeadlineExceeded):
			result = "timeout"
			ExternalCallTimeouts.WithLabelValues(target, op).Inc()
		case errors.Is(*err, context.Canceled):
			result = "cancelled"
		default:
			result = "error"
		}
	}
	ExternalCallDuration.WithLabelValues(target, op, result).Observe(time.Since(start).Seconds())
}
