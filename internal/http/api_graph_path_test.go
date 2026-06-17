package http

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
)

func graphPathTestApp(cfg APIConfig) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/graph/path", graphPathHandler(cfg))
	return app
}

// When no graph store is configured the handler should answer 503 immediately
// rather than try to dial anything.
func TestGraphPath_GraphStoreUnconfigured(t *testing.T) {
	app := graphPathTestApp(APIConfig{})
	req := httptest.NewRequest("GET", "/graph/path?from=a&to=b", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
}

func TestGraphPath_MissingArgs(t *testing.T) {
	app := graphPathTestApp(APIConfig{})

	cases := []string{
		"/graph/path",
		"/graph/path?from=a",
		"/graph/path?to=b",
	}
	for _, url := range cases {
		req := httptest.NewRequest("GET", url, nil)
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("Test %q: %v", url, err)
		}
		if resp.StatusCode != fiber.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", url, resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		if parsed["error"] == nil {
			t.Errorf("%s: expected error field, got %s", url, string(body))
		}
	}
}

func TestGraphPath_SQLFallback(t *testing.T) {
	db := newGraphPathSQLiteDB(t)
	app := graphPathTestApp(APIConfig{DB: db})

	req := httptest.NewRequest("GET", "/graph/path?from=a&to=c&as_of=150&limit=1", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, string(body))
	}
	var first struct {
		Edges      []map[string]any `json:"edges"`
		NextCursor string           `json:"next_cursor"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&first); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	if len(first.Edges) != 1 || first.NextCursor == "" {
		t.Fatalf("first page=%+v, want one edge and next cursor", first)
	}

	req = httptest.NewRequest("GET", "/graph/path?from=a&to=c&as_of=150&limit=1&cursor="+first.NextCursor, nil)
	resp, err = app.Test(req, -1)
	if err != nil {
		t.Fatalf("Test page 2: %v", err)
	}
	var second struct {
		Edges      []map[string]any `json:"edges"`
		NextCursor string           `json:"next_cursor"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&second); err != nil {
		t.Fatalf("decode second page: %v", err)
	}
	if len(second.Edges) != 1 || second.NextCursor != "" {
		t.Fatalf("second page=%+v, want final one edge", second)
	}
}

func newGraphPathSQLiteDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+t.TempDir()+"/path.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE graph_nodes (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			properties TEXT NOT NULL DEFAULT '{}',
			dataset_id TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE graph_edges (
			id TEXT PRIMARY KEY,
			source_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			relationship_name TEXT NOT NULL DEFAULT '',
			properties TEXT NOT NULL DEFAULT '{}',
			valid_from TEXT,
			valid_until TEXT,
			dataset_id TEXT NOT NULL DEFAULT ''
		);
		INSERT INTO graph_nodes(id,name,type,dataset_id) VALUES ('a','A','Service','ds1'),('b','B','Service','ds1'),('c','C','Service','ds1');
		INSERT INTO graph_edges(id,source_id,target_id,relationship_name,valid_from,valid_until,dataset_id) VALUES
			('ab','a','b','calls','0',NULL,'ds1'),
			('bc','b','c','calls','0',NULL,'ds1'),
			('ac','a','c','expired','0','99','ds1');
	`); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}
	return db
}

func TestParseIntDefault(t *testing.T) {
	if parseIntDefault("", 7) != 7 {
		t.Error("empty should yield default")
	}
	if parseIntDefault("nope", 5) != 5 {
		t.Error("garbage should yield default")
	}
	if parseIntDefault("42", 0) != 42 {
		t.Error("number should parse")
	}
}

func TestParseInt64Default(t *testing.T) {
	if parseInt64Default("", 9) != 9 {
		t.Error("empty should yield default")
	}
	if parseInt64Default("123456789012", 0) != 123456789012 {
		t.Error("large int should parse")
	}
}
