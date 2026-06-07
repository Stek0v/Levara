package http

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
)

func newSyncGraphTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "sync-graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE graph_nodes (
			id TEXT PRIMARY KEY,
			name TEXT,
			type TEXT,
			description TEXT,
			properties TEXT,
			dataset_id TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE graph_edges (
			id TEXT PRIMARY KEY,
			source_id TEXT,
			target_id TEXT,
			relationship_name TEXT,
			properties TEXT,
			valid_from TEXT,
			valid_until TEXT,
			superseded_by TEXT NOT NULL DEFAULT '',
			confidence REAL NOT NULL DEFAULT 1.0,
			dataset_id TEXT NOT NULL DEFAULT ''
		);
	`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	SetDBProvider(DBSQLite)
	return db, func() {
		_ = db.Close()
		SetDBProvider(DBPostgres)
	}
}

func TestSyncExportGraphPreservesTemporalMetadata(t *testing.T) {
	db, cleanup := newSyncGraphTestDB(t)
	defer cleanup()
	if _, err := db.Exec(`
		INSERT INTO graph_nodes (id, name, type, description, properties, dataset_id)
		VALUES ('n1', 'Service', 'component', 'Payments service', '{}', 'ds-payments');
		INSERT INTO graph_edges (
			id, source_id, target_id, relationship_name, properties,
			valid_from, valid_until, superseded_by, confidence, dataset_id
		) VALUES (
			'e1', 'n1', 'n2', 'status_is', '{"edge_text":"old status"}',
			'2026-05-01T00:00:00Z', '2026-05-10T00:00:00Z', 'e2', 0.73, 'ds-payments'
		);
	`); err != nil {
		t.Fatal(err)
	}

	app := fiber.New()
	RegisterSyncAPI(app, APIConfig{DB: db})
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/sync/export/graph", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var g syncGraph
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes) != 1 || g.Nodes[0].DatasetID != "ds-payments" {
		t.Fatalf("node dataset not exported: %+v", g.Nodes)
	}
	if len(g.Edges) != 1 {
		t.Fatalf("edges len=%d, want 1", len(g.Edges))
	}
	edge := g.Edges[0]
	if edge.ValidFrom != "2026-05-01T00:00:00Z" ||
		edge.ValidUntil != "2026-05-10T00:00:00Z" ||
		edge.SupersededBy != "e2" ||
		edge.Confidence != 0.73 ||
		edge.DatasetID != "ds-payments" {
		t.Fatalf("edge temporal metadata lost: %+v", edge)
	}
}

func TestSyncImportGraphPreservesTemporalMetadata(t *testing.T) {
	db, cleanup := newSyncGraphTestDB(t)
	defer cleanup()

	app := fiber.New()
	RegisterSyncAPI(app, APIConfig{DB: db})
	payload, _ := json.Marshal(syncGraph{
		Nodes: []syncGraphNode{{
			ID: "n1", Name: "Service", Type: "component",
			Description: "Payments service", Properties: `{}`, DatasetID: "ds-payments",
		}},
		Edges: []syncGraphEdge{{
			ID: "e1", SourceID: "n1", TargetID: "n2", RelationshipName: "status_is",
			Properties: `{"edge_text":"old status"}`,
			ValidFrom:  "2026-05-01T00:00:00Z", ValidUntil: "2026-05-10T00:00:00Z",
			SupersededBy: "e2", Confidence: 0.73, DatasetID: "ds-payments",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/sync/import/graph", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}

	var nodeDataset string
	if err := db.QueryRow(`SELECT dataset_id FROM graph_nodes WHERE id = 'n1'`).Scan(&nodeDataset); err != nil {
		t.Fatal(err)
	}
	if nodeDataset != "ds-payments" {
		t.Fatalf("node dataset=%q, want ds-payments", nodeDataset)
	}

	var validFrom, validUntil, supersededBy, datasetID string
	var confidence float64
	if err := db.QueryRow(`
		SELECT valid_from, valid_until, superseded_by, confidence, dataset_id
		FROM graph_edges WHERE id = 'e1'
	`).Scan(&validFrom, &validUntil, &supersededBy, &confidence, &datasetID); err != nil {
		t.Fatal(err)
	}
	if validFrom != "2026-05-01T00:00:00Z" ||
		validUntil != "2026-05-10T00:00:00Z" ||
		supersededBy != "e2" ||
		confidence != 0.73 ||
		datasetID != "ds-payments" {
		t.Fatalf("imported edge metadata: valid_from=%q valid_until=%q superseded_by=%q confidence=%v dataset_id=%q",
			validFrom, validUntil, supersededBy, confidence, datasetID)
	}
}
