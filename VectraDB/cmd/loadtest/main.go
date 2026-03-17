package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

const (
	InsertCount = 5_000 // Enough to see the graph climb
	SearchCount = 1_000 // High volume to test read speed
	Concurrency = 10

	BaseURL = "http://localhost:8080/api/v1"
)

// Data Structures matching your Server API
type InsertRequest struct {
	ID       string            `json:"id"`
	Vector   []float32         `json:"vector"`
	Metadata map[string]string `json:"metadata"`
}

type SearchRequest struct {
	Vector []float32 `json:"vector"`
	K      int       `json:"k"`
}

func main() {
	fmt.Println("🔥 Starting VectraDB HTTP Load Generator")
	fmt.Printf("Target: %s | Workers: %d\n", BaseURL, Concurrency)

	// --- Phase 1: Ingestion ---
	fmt.Println("\n📝 Phase 1: Ingestion (Writing to Raft)...")
	runTest("insert", InsertCount, func(workerID, i int) error {
		reqBody := InsertRequest{
			ID:       fmt.Sprintf("load-%d-%d", workerID, i),
			Vector:   randomVector(128),
			Metadata: map[string]string{"source": "loadtest"},
		}
		return sendRequest("POST", "/insert", reqBody)
	})

	// --- Phase 2: Search ---
	fmt.Println("\n🔍 Phase 2: Search (Reading from Memory)...")
	runTest("search", SearchCount, func(workerID, i int) error {
		reqBody := SearchRequest{
			Vector: randomVector(128),
			K:      10, // Top 10 results
		}
		return sendRequest("POST", "/search", reqBody)
	})

	fmt.Println("\n✅ Load Test Complete!")
}

// Generic Test Runner to handle Concurrency and Timing
func runTest(name string, totalOps int, opFunc func(workerID, i int) error) {
	var wg sync.WaitGroup
	start := time.Now()

	opsPerWorker := totalOps / Concurrency

	for w := 0; w < Concurrency; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < opsPerWorker; i++ {
				if err := opFunc(workerID, i); err != nil {
					fmt.Printf("❌ %s error: %v\n", name, err)
				}
				// Optional: Slight delay to prevent local port exhaustion
				// time.Sleep(1 * time.Millisecond)
			}
		}(w)
	}

	wg.Wait()
	duration := time.Since(start)
	qps := float64(totalOps) / duration.Seconds()

	fmt.Printf("⏱️ %s Duration: %s\n", name, duration)
	fmt.Printf("📈 %s QPS: %.2f\n", name, qps)
}

// Helper to send HTTP requests
func sendRequest(method, endpoint string, body interface{}) error {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
		},
	}

	jsonBody, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, BaseURL+endpoint, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func randomVector(dim int) []float32 {
	vec := make([]float32, dim)
	for i := 0; i < dim; i++ {
		vec[i] = rand.Float32()
	}
	return vec
}
