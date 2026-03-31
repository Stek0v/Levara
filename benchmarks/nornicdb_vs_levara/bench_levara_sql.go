// Levara SQL Graph Benchmark — direct SQLite queries matching NornicDB test.
// Run: go run bench_levara_sql.go
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

const dbPath = "/tmp/levara_graph_bench.db"

var scales = []int{500, 1000, 5000}
var edgesPerNode = 5

type Stats struct {
	P50ms      float64 `json:"p50_ms"`
	P95ms      float64 `json:"p95_ms"`
	P99ms      float64 `json:"p99_ms"`
	MeanMs     float64 `json:"mean_ms"`
	AvgResults float64 `json:"avg_results,omitempty"`
}

func percentile(data []float64, p float64) float64 {
	if len(data) == 0 {
		return 0
	}
	sort.Float64s(data)
	k := float64(len(data)-1) * p / 100
	f := int(k)
	c := f + 1
	if c >= len(data) {
		return data[len(data)-1]
	}
	return data[f] + (k-float64(f))*(data[c]-data[f])
}

func bench(label string, fn func(j int) int, iters int) Stats {
	lats := make([]float64, 0, iters)
	counts := make([]int, 0, iters)
	for j := 0; j < iters; j++ {
		t0 := time.Now()
		cnt := fn(j)
		elapsed := time.Since(t0).Seconds() * 1000 // ms
		lats = append(lats, elapsed)
		counts = append(counts, cnt)
	}
	s := Stats{
		P50ms:  percentile(lats, 50),
		P95ms:  percentile(lats, 95),
		P99ms:  percentile(lats, 99),
		MeanMs: avg(lats),
	}
	if len(counts) > 0 {
		total := 0
		for _, c := range counts {
			total += c
		}
		s.AvgResults = float64(total) / float64(len(counts))
	}
	fmt.Printf("    %s: p50=%.1fms  p95=%.1fms  p99=%.1fms  avg_results=%.0f\n",
		label, s.P50ms, s.P95ms, s.P99ms, s.AvgResults)
	return s
}

func avg(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range data {
		sum += v
	}
	return sum / float64(len(data))
}

func randomName() string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, 8)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return "ent_" + string(b)
}

type Node struct {
	ID   string
	Name string
	Type string
	Desc string
}

type Edge struct {
	ID       string
	SourceID string
	TargetID string
	RelName  string
}

func generateData(n, epn int) ([]Node, []Edge) {
	rels := []string{"RELATES_TO", "CALLS", "IMPORTS", "CONTAINS", "DEPENDS_ON"}
	nodes := make([]Node, n)
	for i := 0; i < n; i++ {
		nodes[i] = Node{
			ID:   fmt.Sprintf("n-%d", i),
			Name: randomName(),
			Type: "Entity",
			Desc: fmt.Sprintf("Node %d", i),
		}
	}
	var edges []Edge
	for i := 0; i < n; i++ {
		perm := rand.Perm(n)
		count := 0
		for _, t := range perm {
			if t != i && count < epn {
				edges = append(edges, Edge{
					ID:       fmt.Sprintf("e-%d-%d", i, t),
					SourceID: fmt.Sprintf("n-%d", i),
					TargetID: fmt.Sprintf("n-%d", t),
					RelName:  rels[rand.Intn(len(rels))],
				})
				count++
			}
		}
	}
	return nodes, edges
}

func main() {
	fmt.Println(strings.Repeat("=", 70))
	fmt.Println("Levara SQL Graph Benchmark (SQLite direct)")
	fmt.Println(strings.Repeat("=", 70))

	os.Remove(dbPath)
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		fmt.Printf("Failed to open DB: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	// Enable WAL mode
	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA synchronous=NORMAL")

	// Create tables (same as Levara schema.go)
	db.Exec(`CREATE TABLE IF NOT EXISTS graph_nodes (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		type TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		properties TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS graph_edges (
		id TEXT PRIMARY KEY,
		source_id TEXT NOT NULL,
		target_id TEXT NOT NULL,
		relationship_name TEXT NOT NULL DEFAULT '',
		properties TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	// Indexes (same as Levara)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_graph_nodes_name ON graph_nodes(name)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_graph_nodes_type ON graph_nodes(type)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_graph_edges_source ON graph_edges(source_id)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_graph_edges_target ON graph_edges(target_id)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_graph_edges_rel ON graph_edges(relationship_name)`)

	allResults := map[string]any{}

	for _, N := range scales {
		fmt.Printf("\n%s\n", strings.Repeat("─", 70))
		fmt.Printf("Scale: %d nodes, ~%d edges\n", N, N*edgesPerNode)
		fmt.Printf("%s\n", strings.Repeat("─", 70))

		result := map[string]any{"nodes": N}

		// Clear
		fmt.Println("[1] Clearing...")
		db.Exec("DELETE FROM graph_edges")
		db.Exec("DELETE FROM graph_nodes")

		// Generate
		fmt.Printf("[2] Generating %d nodes...\n", N)
		nodes, edges := generateData(N, edgesPerNode)

		// Insert nodes
		fmt.Printf("[3] Inserting %d nodes...\n", N)
		t0 := time.Now()
		tx, _ := db.Begin()
		stmt, _ := tx.Prepare("INSERT INTO graph_nodes(id, name, type, description) VALUES(?, ?, ?, ?)")
		for _, n := range nodes {
			stmt.Exec(n.ID, n.Name, n.Type, n.Desc)
		}
		stmt.Close()
		tx.Commit()
		tNodes := time.Since(t0).Seconds()
		result["insert_nodes_sec"] = int(float64(N) / tNodes)
		fmt.Printf("    %d nodes in %.2fs (%d nodes/sec)\n", N, tNodes, int(float64(N)/tNodes))

		// Insert edges
		fmt.Printf("[4] Inserting %d edges...\n", len(edges))
		t0 = time.Now()
		tx, _ = db.Begin()
		stmt, _ = tx.Prepare("INSERT INTO graph_edges(id, source_id, target_id, relationship_name) VALUES(?, ?, ?, ?)")
		for _, e := range edges {
			stmt.Exec(e.ID, e.SourceID, e.TargetID, e.RelName)
		}
		stmt.Close()
		tx.Commit()
		tEdges := time.Since(t0).Seconds()
		result["insert_edges_sec"] = int(float64(len(edges)) / tEdges)
		result["edge_crashes"] = 0
		fmt.Printf("    %d edges in %.2fs (%d edges/sec)\n", len(edges), tEdges, int(float64(len(edges))/tEdges))

		// Traversals
		fmt.Println("[5] Traversal benchmarks...")

		// 1-hop: same as graphContextFromPostgres
		query1hop := `SELECT gn.name AS source, ge.relationship_name AS rel, gn2.name AS target
			FROM graph_edges ge
			JOIN graph_nodes gn ON ge.source_id = gn.id
			JOIN graph_nodes gn2 ON ge.target_id = gn2.id
			WHERE gn.name = ? LIMIT 50`
		result["1hop"] = bench("1-hop", func(j int) int {
			name := nodes[j%N].Name
			rows, err := db.Query(query1hop, name)
			if err != nil {
				return 0
			}
			defer rows.Close()
			count := 0
			var s, r, t string
			for rows.Next() {
				rows.Scan(&s, &r, &t)
				count++
			}
			return count
		}, min(100, N))

		// 2-hop: two JOINs (like contextExtensionSearch)
		query2hop := `SELECT DISTINCT gn3.name
			FROM graph_edges ge1
			JOIN graph_nodes gn ON ge1.source_id = gn.id
			JOIN graph_nodes gn2 ON ge1.target_id = gn2.id
			JOIN graph_edges ge2 ON ge2.source_id = gn2.id
			JOIN graph_nodes gn3 ON ge2.target_id = gn3.id
			WHERE gn.name = ? LIMIT 100`
		result["2hop"] = bench("2-hop", func(j int) int {
			name := nodes[j%N].Name
			rows, err := db.Query(query2hop, name)
			if err != nil {
				return 0
			}
			defer rows.Close()
			count := 0
			var t string
			for rows.Next() {
				rows.Scan(&t)
				count++
			}
			return count
		}, min(50, N))

		// 3-hop: three JOINs
		query3hop := `SELECT DISTINCT gn4.name
			FROM graph_edges ge1
			JOIN graph_nodes gn ON ge1.source_id = gn.id
			JOIN graph_nodes gn2 ON ge1.target_id = gn2.id
			JOIN graph_edges ge2 ON ge2.source_id = gn2.id
			JOIN graph_nodes gn3 ON ge2.target_id = gn3.id
			JOIN graph_edges ge3 ON ge3.source_id = gn3.id
			JOIN graph_nodes gn4 ON ge3.target_id = gn4.id
			WHERE gn.name = ? LIMIT 200`
		result["3hop"] = bench("3-hop", func(j int) int {
			name := nodes[j%N].Name
			rows, err := db.Query(query3hop, name)
			if err != nil {
				return 0
			}
			defer rows.Close()
			count := 0
			var t string
			for rows.Next() {
				rows.Scan(&t)
				count++
			}
			return count
		}, min(20, N))

		// Count
		result["count"] = bench("count", func(j int) int {
			var cnt int
			db.QueryRow("SELECT count(*) FROM graph_nodes").Scan(&cnt)
			return cnt
		}, 50)

		allResults[fmt.Sprintf("N=%d", N)] = result
	}

	// Summary
	fmt.Printf("\n%s\n", strings.Repeat("=", 70))
	fmt.Println("SUMMARY")
	fmt.Printf("%s\n", strings.Repeat("=", 70))
	fmt.Printf("%-10s %-12s %-12s %-12s %-12s %-12s\n", "Scale", "Insert N/s", "Insert E/s", "1-hop p50", "2-hop p50", "3-hop p50")
	for _, N := range scales {
		key := fmt.Sprintf("N=%d", N)
		r := allResults[key].(map[string]any)
		h1 := r["1hop"].(Stats).P50ms
		h2 := r["2hop"].(Stats).P50ms
		h3 := r["3hop"].(Stats).P50ms
		fmt.Printf("%-10s %-12d %-12d %-12.1f %-12.1f %-12.1f\n",
			key, r["insert_nodes_sec"], r["insert_edges_sec"], h1, h2, h3)
	}

	data, _ := json.MarshalIndent(allResults, "", "  ")
	os.WriteFile("results_levara_graph.json", data, 0644)
	fmt.Println("\nSaved to results_levara_graph.json")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
