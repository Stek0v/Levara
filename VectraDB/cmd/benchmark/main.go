package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"time"

	"log"
	"net/http"

	"github.com/hashicorp/raft"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rupamthxt/vectradb/internal/cluster"
	"github.com/rupamthxt/vectradb/internal/metrics"
	"github.com/rupamthxt/vectradb/internal/store"
)

const (
	RaftBasePort = 19000
)

var (
	dimension    = 128
	totalVectors = 5_000 // default, overridden via flags
	numQueries   = 1000
	numShards    = 3
	metricsPort  = 9091
)

func main() {
	// allow customization via flags
	dimPtr := flag.Int("dim", dimension, "vector dimension")
	itemsPtr := flag.Int("items", totalVectors, "total vectors to insert")
	queriesPtr := flag.Int("queries", numQueries, "number of search queries")
	shardsPtr := flag.Int("shards", numShards, "number of raft shards")
	metricsPtr := flag.Int("metrics-port", metricsPort, "port for Prometheus metrics")
	flag.Parse()

	dimension = *dimPtr
	totalVectors = *itemsPtr
	numQueries = *queriesPtr
	numShards = *shardsPtr
	metricsPort = *metricsPtr

	fmt.Println("🔥 Starting VectraDB Distributed Benchmark (Raft + IVF)")
	fmt.Printf("Config: Dim=%d | Items=%d | Shards=%d\n", dimension, totalVectors, numShards)

	baseDir := "data_bench"
	os.RemoveAll(baseDir)
	os.MkdirAll(baseDir, 0755)
	defer os.RemoveAll(baseDir)

	var shards []store.ShardHandler
	nodeID := "bench_node"

	fmt.Println("⚡ Initializing Raft Groups...")

	// start prometheus metrics endpoint
	go func() {
		http.Handle("/metrics", promhttp.Handler())
		addr := fmt.Sprintf(":%d", metricsPort)
		log.Printf("metrics listening on %s", addr)
		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Fatalf("metrics server failed: %v", err)
		}
	}()

	for i := 0; i < numShards; i++ {
		shardDir := fmt.Sprintf("%s/shard_%d", baseDir, i)
		os.MkdirAll(shardDir, 0755)

		dbPath := fmt.Sprintf("%s/meta.bin", shardDir)
		db, _ := store.NewVectraDB(dimension, dbPath)

		raftPort := RaftBasePort + i
		raftNode, _ := cluster.NewRaftNode(i, nodeID, baseDir, raftPort, db)

		cfg := raft.Configuration{
			Servers: []raft.Server{
				{
					ID:      raft.ServerID(fmt.Sprintf("%s-shard-%d", nodeID, i)),
					Address: raft.ServerAddress(fmt.Sprintf("127.0.0.1:%d", raftPort)),
				},
			},
		}
		raftNode.Raft.BootstrapCluster(cfg)
		shards = append(shards, raftNode)
	}

	time.Sleep(3 * time.Second) // Wait for elections
	c := store.NewCluster(shards)

	// --- Phase 1: Ingestion ---
	fmt.Println("\n--- Phase 1: Ingestion (Raft Log Replication) ---")
	start := time.Now()

	// Use 10 concurrent workers for ingestion
	var wg sync.WaitGroup
	workers := 10
	batch := totalVectors / workers
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			offset := idx * batch
			for i := 0; i < batch; i++ {
				metrics.InsertRequests.Inc()
				startIns := time.Now()
				c.Insert(fmt.Sprintf("vec-%d", offset+i), randomVector(dimension), nil)
				metrics.InsertDuration.Observe(time.Since(startIns).Seconds())
				metrics.TotalVectors.Inc()
			}
		}(w)
	}
	wg.Wait()
	fmt.Printf("✅ Ingestion Complete: %.2fs\n", time.Since(start).Seconds())

	// --- Phase 2: Search ---
	fmt.Println("\n--- Phase 2: Search (HNSW) ---")
	startSearch := time.Now()
	wgSearch := sync.WaitGroup{}
	wgSearch.Add(numQueries)

	for i := 0; i < numQueries; i++ {
		go func() {
			defer wgSearch.Done()
			metrics.SearchRequests.Inc()
			startSearchLoop := time.Now()
			c.Search(randomVector(dimension), 10)
			metrics.SearchDuration.Observe(time.Since(startSearchLoop).Seconds())
		}()
	}
	wgSearch.Wait()

	qps := float64(numQueries) / time.Since(startSearch).Seconds()
	fmt.Printf("🚀 HNSW QPS: %.2f\n", qps)

	// keep process running so prometheus can scrape metrics after benchmark completes
	fmt.Println("🔋 benchmark complete – metrics remain available at :9091/metrics until you stop the program")
	select {}
}

func randomVector(dim int) []float32 {
	vec := make([]float32, dim)
	for i := 0; i < dim; i++ {
		vec[i] = rand.Float32()
	}
	return vec
}
