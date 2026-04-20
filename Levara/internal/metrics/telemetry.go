package metrics

import (
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
)
