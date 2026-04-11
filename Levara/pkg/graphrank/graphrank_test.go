package graphrank

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func setupTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	f, _ := os.CreateTemp("", "graphrank-test-*.db")
	path := f.Name()
	f.Close()

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	// Create graph tables
	db.Exec(`CREATE TABLE graph_nodes (id TEXT PRIMARY KEY, name TEXT, type TEXT, description TEXT)`)
	db.Exec(`CREATE TABLE graph_edges (
		id TEXT PRIMARY KEY, source_id TEXT, target_id TEXT,
		relationship_name TEXT, valid_until TEXT, confidence REAL DEFAULT 1.0
	)`)

	cleanup := func() {
		db.Close()
		os.Remove(path)
	}
	return db, cleanup
}

func insertNode(db *sql.DB, id, name string) {
	db.Exec("INSERT INTO graph_nodes (id, name, type, description) VALUES (?, ?, 'Entity', '')", id, name)
}

func insertEdge(db *sql.DB, src, dst string) {
	db.Exec("INSERT INTO graph_edges (id, source_id, target_id, relationship_name) VALUES (?, ?, ?, 'related')",
		src+"_"+dst, src, dst)
}

func TestGraphProximity_SameEntity(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	insertNode(db, "A", "A")

	score := GraphProximity(context.Background(), db, []string{"A"}, []string{"A"}, DefaultConfig())
	if score != 1.0 {
		t.Errorf("Same entity: expected 1.0, got %f", score)
	}
}

func TestGraphProximity_OneHop(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	insertNode(db, "A", "A")
	insertNode(db, "B", "B")
	insertEdge(db, "A", "B")

	cfg := DefaultConfig()
	score := GraphProximity(context.Background(), db, []string{"A"}, []string{"B"}, cfg)
	if score != cfg.DecayFactor {
		t.Errorf("1-hop: expected %f, got %f", cfg.DecayFactor, score)
	}
}

func TestGraphProximity_TwoHop(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	insertNode(db, "A", "A")
	insertNode(db, "B", "B")
	insertNode(db, "C", "C")
	insertEdge(db, "A", "B")
	insertEdge(db, "B", "C")

	cfg := DefaultConfig()
	score := GraphProximity(context.Background(), db, []string{"A"}, []string{"C"}, cfg)
	expected := cfg.DecayFactor * cfg.DecayFactor
	if score != expected {
		t.Errorf("2-hop: expected %f, got %f", expected, score)
	}
}

func TestGraphProximity_Unreachable(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	insertNode(db, "A", "A")
	insertNode(db, "Z", "Z")
	// No edge between A and Z

	score := GraphProximity(context.Background(), db, []string{"A"}, []string{"Z"}, DefaultConfig())
	if score != 0 {
		t.Errorf("Unreachable: expected 0, got %f", score)
	}
}

func TestGraphProximity_MultipleQueryEntities(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	insertNode(db, "A", "A")
	insertNode(db, "B", "B")
	insertNode(db, "C", "C")
	insertEdge(db, "B", "C") // B→C is 1-hop

	cfg := DefaultConfig()
	// Query: A and B. Result: C.
	// A→C: unreachable. B→C: 1-hop. Max = 1-hop.
	score := GraphProximity(context.Background(), db, []string{"A", "B"}, []string{"C"}, cfg)
	if score != cfg.DecayFactor {
		t.Errorf("Multi-query: expected %f (1-hop via B), got %f", cfg.DecayFactor, score)
	}
}

func TestGraphProximity_EmptyGraph(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	score := GraphProximity(context.Background(), db, []string{"A"}, []string{"B"}, DefaultConfig())
	if score != 0 {
		t.Errorf("Empty graph: expected 0, got %f", score)
	}
}

func TestGraphProximity_NoDB(t *testing.T) {
	score := GraphProximity(context.Background(), nil, []string{"A"}, []string{"B"}, DefaultConfig())
	if score != 0 {
		t.Errorf("Nil DB: expected 0, got %f", score)
	}
}

func TestRerankWithGraph_ReordersResults(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	insertNode(db, "Einstein", "Einstein")
	insertNode(db, "Princeton", "Princeton")
	insertNode(db, "Redis", "Redis")
	insertEdge(db, "Einstein", "Princeton") // Einstein→Princeton: 1-hop

	cfg := DefaultConfig()
	cfg.Alpha = 0.5
	cfg.Beta = 0.5

	results := []ScoredResult{
		{ID: "r1", Score: 0.9, Metadata: json.RawMessage(`{"name":"Redis"}`)},       // high vector, no graph connection
		{ID: "r2", Score: 0.5, Metadata: json.RawMessage(`{"name":"Princeton"}`)},    // low vector, 1-hop connection
	}

	reranked := RerankWithGraph(context.Background(), db, []string{"Einstein"}, results, cfg)

	if len(reranked) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(reranked))
	}

	// Princeton should rank higher due to graph proximity
	// r1: 0.5*0.9 + 0.5*0.0 = 0.45
	// r2: 0.5*0.5 + 0.5*0.5 = 0.50  ← higher
	if reranked[0].ID != "r2" {
		t.Errorf("Expected Princeton (r2) first after graph rerank, got %s", reranked[0].ID)
	}

	t.Logf("Reranked: %s (%.2f), %s (%.2f)", reranked[0].ID, reranked[0].Score, reranked[1].ID, reranked[1].Score)
}

func TestRerankWithGraph_FallbackNoGraph(t *testing.T) {
	// Nil DB → original order
	results := []ScoredResult{
		{ID: "r1", Score: 0.9},
		{ID: "r2", Score: 0.5},
	}
	reranked := RerankWithGraph(context.Background(), nil, []string{"A"}, results, DefaultConfig())
	if reranked[0].ID != "r1" {
		t.Errorf("Fallback: expected original order, got %s first", reranked[0].ID)
	}
}

func TestRerankWithGraph_EmptyQueryEntities(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	results := []ScoredResult{{ID: "r1", Score: 0.9}}
	reranked := RerankWithGraph(context.Background(), db, nil, results, DefaultConfig())
	if len(reranked) != 1 || reranked[0].ID != "r1" {
		t.Error("Empty query entities: expected original order")
	}
}
