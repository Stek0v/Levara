package http

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/runreg"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// mcp_reconcile_memory_test.go — coverage for the reconcile_memory MCP
// tool, the durable backstop that verifies SQL↔vector consistency across
// the _memories_* sidecars. The SQL memories row is the source of truth;
// each sidecar should hold one vector per live row. We pin two findings:
//   - missing_vector: a SQL row with no vector in its sidecar
//   - orphan_vector:  a sidecar vector with no live SQL row
// Dry-run (apply=false) must detect both without mutating anything.

func reconcileTestHandler(t *testing.T) (*mcpHandler, *sql.DB, *store.CollectionManager) {
	t.Helper()
	prev := GetDBProvider()
	SetDBProvider(DBSQLite)
	t.Cleanup(func() { SetDBProvider(prev) })

	dir := t.TempDir()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, "rec.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(`CREATE TABLE memories (
		id TEXT PRIMARY KEY,
		key TEXT NOT NULL,
		value TEXT NOT NULL DEFAULT '',
		type TEXT NOT NULL DEFAULT 'project',
		owner_id TEXT NOT NULL DEFAULT '',
		collection_name TEXT NOT NULL DEFAULT '',
		room TEXT NOT NULL DEFAULT '',
		hall TEXT NOT NULL DEFAULT '',
		is_pinned INTEGER NOT NULL DEFAULT 0,
		pin_priority INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatalf("schema: %v", err)
	}

	cm, err := store.NewCollectionManager(8, filepath.Join(dir, "vec"))
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}
	t.Cleanup(func() { _ = cm.Close() })

	h := &mcpHandler{cfg: APIConfig{DB: db, Runs: runreg.New(), Collections: cm}}
	return h, db, cm
}

func TestToolReconcileMemory_DryRunDetectsMissingAndOrphan(t *testing.T) {
	h, db, cm := reconcileTestHandler(t)

	// Two SQL rows in context 'levara' → sidecar _memories_levara.
	mustExec(t, db, `INSERT INTO memories (id, key, value, collection_name) VALUES ('row-have','k1','v1','levara')`)
	mustExec(t, db, `INSERT INTO memories (id, key, value, collection_name) VALUES ('row-missing','k2','v2','levara')`)

	vec := []float32{1, 0, 0, 0, 0, 0, 0, 0}
	if err := cm.Insert("_memories_levara", "row-have", vec, map[string]string{"memory_id": "row-have"}); err != nil {
		t.Fatalf("Insert have: %v", err)
	}
	// An orphan: a vector with no matching SQL row.
	if err := cm.Insert("_memories_levara", "row-orphan", vec, map[string]string{"memory_id": "row-orphan"}); err != nil {
		t.Fatalf("Insert orphan: %v", err)
	}

	out := decodeText(t, h.toolReconcileMemory(context.Background(), map[string]any{}))

	if got := out["apply"]; got != false {
		t.Errorf("apply = %v, want false (dry-run default)", got)
	}
	if got := numVal(out["total_missing"]); got != 1 {
		t.Errorf("total_missing = %v, want 1", got)
	}
	if got := numVal(out["total_orphan"]); got != 1 {
		t.Errorf("total_orphan = %v, want 1", got)
	}
	if got := numVal(out["total_repaired"]); got != 0 {
		t.Errorf("total_repaired = %v, want 0 in dry-run", got)
	}

	// Dry-run must not mutate the sidecar: still 2 vectors (have + orphan).
	ids, _, _, err := cm.AllRecords("_memories_levara")
	if err != nil {
		t.Fatalf("AllRecords: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("sidecar mutated in dry-run: %d vectors, want 2", len(ids))
	}
}

func TestToolReconcileMemory_DeleteOrphansRemovesUnbacked(t *testing.T) {
	h, db, cm := reconcileTestHandler(t)

	mustExec(t, db, `INSERT INTO memories (id, key, value, collection_name) VALUES ('row-have','k1','v1','levara')`)
	vec := []float32{1, 0, 0, 0, 0, 0, 0, 0}
	_ = cm.Insert("_memories_levara", "row-have", vec, map[string]string{"memory_id": "row-have"})
	_ = cm.Insert("_memories_levara", "row-orphan", vec, map[string]string{"memory_id": "row-orphan"})

	out := decodeText(t, h.toolReconcileMemory(context.Background(), map[string]any{
		"apply": true, "delete_orphans": true,
	}))
	if got := numVal(out["total_orphan_deleted"]); got != 1 {
		t.Errorf("total_orphan_deleted = %v, want 1", got)
	}

	if cm.HasRecord("_memories_levara", "row-orphan") {
		t.Errorf("orphan vector still present after delete_orphans")
	}
	if !cm.HasRecord("_memories_levara", "row-have") {
		t.Errorf("backed vector wrongly deleted")
	}
}

func numVal(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return -1
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
