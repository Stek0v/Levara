// search_test_helpers.go — shared fixtures for search-handler tests.
//
// Wave A of the internal/http test coverage push (fix_tasks.md FIX-2).
// This file sets up a deterministic environment for every search path:
//
//   - CollectionManager rooted in t.TempDir so vector search is in-process
//   - embed-server httptest replacement that returns a fixed vector per input
//   - in-memory SQLite with the graph_nodes/graph_edges schema the PG-fallback
//     paths query against
//   - recordingLLM implementing llm.Provider so tests can assert prompt shape
//     without touching a real LLM
//
// Tests customise the APIConfig on the returned env, then call env.start()
// to freeze it into the mounted fiber.App — APIConfig is captured by value
// inside the handler factories, so post-start mutations would be invisible.
package http

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/stek0v/levara/pkg/embed"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/llm"
)

// recordingLLM implements llm.Provider for tests. Each ChatCompletion call
// pushes the user-message content to prompts and pops the next scripted
// response. Exhausted scripts fall back to empty string (not an error) to
// match production "LLM returned nothing" behaviour.
type recordingLLM struct {
	mu        sync.Mutex
	prompts   []string
	responses []string
	err       error
}

func (r *recordingLLM) ChatCompletion(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var prompt string
	if len(req.Messages) > 0 {
		prompt = req.Messages[0].Content
	}
	r.prompts = append(r.prompts, prompt)

	if r.err != nil {
		return nil, r.err
	}

	var content string
	if len(r.responses) > 0 {
		content = r.responses[0]
		r.responses = r.responses[1:]
	}
	return &llm.CompletionResponse{Content: content}, nil
}

func (r *recordingLLM) Name() string { return "recording" }

// promptsSnapshot returns a copy of captured prompts. Safe to call from
// a test goroutine after the handler has completed.
func (r *recordingLLM) promptsSnapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.prompts))
	copy(out, r.prompts)
	return out
}

// newEmbedServer returns an httptest server that returns fixedVec for every
// input in an OpenAI-compatible embedding response.
func newEmbedServer(t testing.TB, fixedVec []float32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		data := make([]item, len(req.Input))
		for i := range req.Input {
			data[i] = item{Index: i, Embedding: fixedVec}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
}

// graphSchemaSQL creates the subset of tables graphContextFromPostgres,
// community*, and RBAC paths query against. Mirrors the relevant shape of
// schema.go + the community/RBAC tables defined elsewhere. `dataset_id`
// on graph_nodes is the RBAC post-filter key.
const graphSchemaSQL = `
CREATE TABLE graph_nodes (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '',
	type TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	properties TEXT NOT NULL DEFAULT '{}',
	dataset_id TEXT,
	created_at TEXT,
	updated_at TEXT
);
CREATE TABLE graph_edges (
	id TEXT PRIMARY KEY,
	source_id TEXT NOT NULL,
	target_id TEXT NOT NULL,
	relationship_name TEXT NOT NULL DEFAULT '',
	properties TEXT NOT NULL DEFAULT '{}',
	valid_from TEXT,
	valid_until TEXT,
	superseded_by TEXT NOT NULL DEFAULT '',
	confidence REAL NOT NULL DEFAULT 1.0
);
CREATE TABLE graph_communities (
	id TEXT PRIMARY KEY,
	level INTEGER NOT NULL DEFAULT 0,
	parent_id TEXT NOT NULL DEFAULT '',
	member_count INTEGER NOT NULL DEFAULT 0,
	summary TEXT NOT NULL DEFAULT ''
);
CREATE TABLE community_members (
	id TEXT PRIMARY KEY,
	community_id TEXT NOT NULL,
	node_id TEXT NOT NULL,
	level INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE users (
	id TEXT PRIMARY KEY,
	email TEXT,
	is_superuser INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE datasets (
	id TEXT PRIMARY KEY,
	owner_id TEXT
);
CREATE TABLE dataset_shares (
	id TEXT PRIMARY KEY,
	dataset_id TEXT NOT NULL,
	user_id TEXT NOT NULL,
	role TEXT NOT NULL DEFAULT 'viewer',
	granted_by TEXT,
	created_at TEXT
);
`

// searchTestEnv bundles a pre-wired environment for exercising searchHandler
// and its per-type handlers. Fields are exported for tests to customise cfg
// (e.g. set LLMProvider) before calling start().
type searchTestEnv struct {
	t     testing.TB
	dir   string
	cm    *store.CollectionManager
	db    *sql.DB
	embed *httptest.Server
	cfg   APIConfig
	app   *fiber.App
}

// newSearchTestEnv constructs the fixture. Caller must customise env.cfg
// then call env.start() before issuing requests. Cleanup is registered
// via t.Cleanup so tests don't need to defer.
func newSearchTestEnv(t testing.TB) *searchTestEnv {
	t.Helper()
	dir, err := os.MkdirTemp("", "levara-search-test-*")
	if err != nil {
		t.Fatal(err)
	}

	const dim = 4
	cm, err := store.NewCollectionManager(dim, dir)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}

	dbPath := filepath.Join(dir, "graph.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		cm.Close()
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	if _, err := db.Exec(graphSchemaSQL); err != nil {
		db.Close()
		cm.Close()
		os.RemoveAll(dir)
		t.Fatalf("create graph schema: %v", err)
	}

	// Handler code uses $N placeholders — switch global dialect to SQLite
	// for the test. Restored in cleanup.
	SetDBProvider(DBSQLite)

	// Fixed unit-length vector so HNSW scoring is deterministic for the
	// dim-4 test collection. Any non-zero vector works; all inserts and
	// queries use this same vector so similarity is 1.0.
	vec := []float32{1, 0, 0, 0}
	embedSrv := newEmbedServer(t, vec)

	cfg := APIConfig{
		EmbedEndpoint: embedSrv.URL,
		EmbedModel:    "test-model",
		EmbedClient:   embed.NewClient(embedSrv.URL, "test-model", 16, 1),
		Collections:   cm,
		DB:            db,
	}

	env := &searchTestEnv{
		t:     t,
		dir:   dir,
		cm:    cm,
		db:    db,
		embed: embedSrv,
		cfg:   cfg,
	}

	t.Cleanup(env.cleanup)
	return env
}

// start mounts searchHandler on a fresh fiber app with the current cfg
// snapshot. Must be called exactly once per env after customisation.
func (e *searchTestEnv) start() {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/search/text", searchHandler(e.cfg))
	e.app = app
}

// startWithUser is the same as start() but injects a middleware that sets
// Locals("user_id") = userID before the handler. Use for RBAC tests that
// depend on GetAllowedDatasetIDs resolving to a concrete user.
func (e *searchTestEnv) startWithUser(userID string) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user_id", userID)
		return c.Next()
	})
	app.Post("/search/text", searchHandler(e.cfg))
	e.app = app
}

// insertUser seeds the users table. isSuperuser=true grants the dataset-
// filter bypass inside GetAllowedDatasetIDs.
func (e *searchTestEnv) insertUser(id, email string, isSuperuser bool) {
	e.t.Helper()
	su := 0
	if isSuperuser {
		su = 1
	}
	_, err := e.db.Exec(
		`INSERT INTO users(id, email, is_superuser) VALUES (?, ?, ?)`,
		id, email, su,
	)
	if err != nil {
		e.t.Fatalf("insert user %q: %v", id, err)
	}
}

// insertDataset seeds datasets.
func (e *searchTestEnv) insertDataset(id, ownerID string) {
	e.t.Helper()
	_, err := e.db.Exec(
		`INSERT INTO datasets(id, owner_id) VALUES (?, ?)`,
		id, ownerID,
	)
	if err != nil {
		e.t.Fatalf("insert dataset %q: %v", id, err)
	}
}

// shareDataset grants userID read access to datasetID via dataset_shares.
func (e *searchTestEnv) shareDataset(datasetID, userID, role string) {
	e.t.Helper()
	_, err := e.db.Exec(
		`INSERT INTO dataset_shares(id, dataset_id, user_id, role, granted_by, created_at)
		 VALUES (?, ?, ?, ?, '', '')`,
		"share-"+datasetID+"-"+userID, datasetID, userID, role,
	)
	if err != nil {
		e.t.Fatalf("share dataset %q→%q: %v", datasetID, userID, err)
	}
}

// insertCommunity seeds graph_communities + community_members for a single
// community at the given level.
func (e *searchTestEnv) insertCommunity(commID string, level int, summary string, memberNodeIDs []string) {
	e.t.Helper()
	_, err := e.db.Exec(
		`INSERT INTO graph_communities(id, level, parent_id, member_count, summary)
		 VALUES (?, ?, '', ?, ?)`,
		commID, level, len(memberNodeIDs), summary,
	)
	if err != nil {
		e.t.Fatalf("insert community %q: %v", commID, err)
	}
	for i, nodeID := range memberNodeIDs {
		_, err := e.db.Exec(
			`INSERT INTO community_members(id, community_id, node_id, level) VALUES (?, ?, ?, ?)`,
			commID+"-m"+strconv.Itoa(i), commID, nodeID, level,
		)
		if err != nil {
			e.t.Fatalf("insert community member: %v", err)
		}
	}
}

func (e *searchTestEnv) cleanup() {
	if e.app != nil {
		_ = e.app.Shutdown()
	}
	if e.embed != nil {
		e.embed.Close()
	}
	if e.cm != nil {
		_ = e.cm.Close()
	}
	if e.db != nil {
		_ = e.db.Close()
	}
	os.RemoveAll(e.dir)
	SetDBProvider(DBPostgres)
}

// insertVector seeds one record into collection (creates it if missing).
func (e *searchTestEnv) insertVector(collection, id string, vec []float32, meta map[string]any) {
	e.t.Helper()
	if err := e.cm.CreateWithDim(collection, len(vec), "test-model", "cosine"); err != nil {
		e.t.Fatalf("create collection %q: %v", collection, err)
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		e.t.Fatalf("marshal meta: %v", err)
	}
	if err := e.cm.Insert(collection, id, vec, json.RawMessage(metaBytes)); err != nil {
		e.t.Fatalf("insert vector: %v", err)
	}
}

// insertNode seeds graph_nodes. datasetID is optional (pass "" to skip).
func (e *searchTestEnv) insertNode(id, name, nodeType, datasetID string) {
	e.t.Helper()
	_, err := e.db.Exec(
		`INSERT INTO graph_nodes(id, name, type, properties, dataset_id) VALUES (?, ?, ?, '{}', ?)`,
		id, name, nodeType, datasetID,
	)
	if err != nil {
		e.t.Fatalf("insert node %q: %v", id, err)
	}
}

// insertEdge seeds graph_edges.
func (e *searchTestEnv) insertEdge(id, srcID, tgtID, rel string) {
	e.t.Helper()
	_, err := e.db.Exec(
		`INSERT INTO graph_edges(id, source_id, target_id, relationship_name) VALUES (?, ?, ?, ?)`,
		id, srcID, tgtID, rel,
	)
	if err != nil {
		e.t.Fatalf("insert edge %q: %v", id, err)
	}
}

// postSearch issues POST /search/text and returns status + decoded body.
func (e *searchTestEnv) postSearch(body map[string]any) (int, map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/search/text", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.app.Test(req, -1)
	if err != nil {
		e.t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}
