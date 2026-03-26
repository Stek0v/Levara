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
)
