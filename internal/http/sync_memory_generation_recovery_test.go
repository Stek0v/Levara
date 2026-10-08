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
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/memoryindex"
)

// The first response is lost only after the real receiver has committed.
func TestSyncMemoryGenerationLostAcknowledgement(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			source, target := syncMemoryConflictConfig(t, dialect), syncMemoryConflictConfig(t, dialect)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, mcp.UserIDKey, "recovery-owner")
			result := mcp.ToolSaveMemory(ctx, NewMCPDeps(source), map[string]any{"key": "recovery-key", "value": "native committed content", "collection": "recovery-collection", "room": "memory", "hall": "fact"})
			if result.IsError {
				t.Fatalf("native save=%+v", result)
			}
			app := syncLifecycleWireApp(target)
			type commitResult struct {
				status int
				body   []byte
				err    error
			}
			committed := make(chan commitResult, 1)
			var requests atomic.Int32
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				req := httptest.NewRequest(r.Method, r.URL.RequestURI(), bytes.NewReader(raw))
				req.Header = r.Header.Clone()
				response, err := app.Test(req)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if requests.Add(1) == 1 {
					committed <- commitResult{response.StatusCode, body, err}
					conn, _, hijackErr := w.(http.Hijacker).Hijack()
					if hijackErr == nil {
						conn.Close()
					}
					return
				}
				for name, values := range response.Header {
					w.Header()[name] = values
				}
				w.WriteHeader(response.StatusCode)
				w.Write(body)
			}))
			defer peer.Close()
			first := syncPush(ctx, source, peer.URL, []string{"memories"}, "")
			if _, ok := first["memories_error"]; !ok {
				t.Fatalf("lost response reported success: %v", first)
			}
			var proof commitResult
			select {
			case proof = <-committed:
			case <-ctx.Done():
				t.Fatal("receiver commit was not observed")
			}
			var ack map[string]any
			if proof.err != nil || proof.status != 200 || json.Unmarshal(proof.body, &ack) != nil || ack["imported"] != float64(1) || ack["failed"] != float64(0) {
				t.Fatalf("actual receiver commit=%+v ack=%v", proof, ack)
			}
			rows := syncMemoryConflictStored(t, ctx, target)
			if len(rows) != 1 || rows[0].Value != "native committed content" {
				t.Fatalf("commit missing SQL row=%+v", rows)
			}
			jobs := syncMemoryConflictJobs(t, ctx, target)
			if len(jobs) != 1 || jobs[0].MemoryID != rows[0].ID || jobs[0].Operation != "upsert_vector" {
				t.Fatalf("commit missing canonical outbox=%+v", jobs)
			}
			before := syncGenerationSnapshot(t, ctx, target.DB)
			retry := syncPush(ctx, source, peer.URL, []string{"memories"}, "")
			retryAck, ok := retry["memories"].(map[string]any)
			if _, failed := retry["memories_error"]; failed || !ok || retryAck["imported"] != float64(0) || retryAck["skipped"] != float64(1) || retryAck["failed"] != float64(0) || requests.Load() != 2 {
				t.Fatalf("real retry=%v requests=%d", retry, requests.Load())
			}
			if !reflect.DeepEqual(before, syncGenerationSnapshot(t, ctx, target.DB)) {
				t.Fatal("committed lost-ACK retry changed SQL/history/aliases/outbox")
			}
			if source.DB.Stats().InUse != 0 || target.DB.Stats().InUse != 0 {
				t.Fatal("lost ACK leaked pool-one connection")
			}
		})
	}
}

func TestSyncMemoryGenerationDeleteRecreateClockSkew(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			source, target := syncMemoryConflictConfig(t, dialect), syncMemoryConflictConfig(t, dialect)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, mcp.UserIDKey, "skew-owner")
			save := func(cfg APIConfig, value string) syncMemory {
				t.Helper()
				result := mcp.ToolSaveMemory(ctx, NewMCPDeps(cfg), map[string]any{"key": "skew-key", "value": value, "collection": "skew-collection", "room": "memory", "hall": "fact"})
				if result.IsError {
					t.Fatalf("native save=%+v", result)
				}
				rows := syncMemoryConflictStored(t, ctx, cfg)
				if len(rows) != 1 {
					t.Fatalf("native active rows=%+v", rows)
				}
				return rows[0]
			}
			remote, local := save(source, "old native value"), save(target, "old native value")
			if remote.ID == local.ID {
				t.Fatal("requires independent canonical UUIDs")
			}
			// Simulate a peer wall clock far ahead; generations must still win.
			if _, err := source.DB.ExecContext(ctx, Q("UPDATE memories SET updated_at=$1 WHERE id=$2"), "2099-01-01T00:00:00Z", remote.ID); err != nil {
				t.Fatal(err)
			}
			sourceApp, targetApp := syncLifecycleWireApp(source), syncLifecycleWireApp(target)
			stale, _ := syncLifecycleWireExport(t, sourceApp, "")
			deleted, err := mcp.DeleteMemory(ctx, NewMCPDeps(target), mcp.DeleteMemoryRequest{MemoryID: local.ID})
			if err != nil || deleted.ID != local.ID {
				t.Fatalf("native delete=%+v err=%v", deleted, err)
			}
			importWire := func(appCfg APIConfig, raw []byte) map[string]any {
				t.Helper()
				status, ack := syncLifecycleWireImport(t, syncLifecycleWireApp(appCfg), raw)
				if status != 200 || ack["failed"] != float64(0) {
					t.Fatalf("public transfer=%d %v", status, ack)
				}
				return ack
			}
			if ack := importWire(target, stale); ack["imported"] != float64(0) || ack["skipped"] != float64(1) {
				t.Fatalf("future peer resurrected deleted generation: %v", ack)
			}
			var alias string
			if err := target.DB.QueryRowContext(ctx, Q("SELECT memory_id FROM memory_sync_aliases WHERE alias_id=$1"), remote.ID).Scan(&alias); err != nil || alias != local.ID {
				t.Fatalf("SKIP alias not retained: %q err=%v", alias, err)
			}
			fresh := save(target, "fresh native value")
			if fresh.ID == local.ID || fresh.ID == remote.ID {
				t.Fatal("recreate must allocate a distinct UUID")
			}
			if _, err := target.DB.ExecContext(ctx, Q("UPDATE memories SET updated_at=$1 WHERE id=$2"), "2001-01-01T00:00:00Z", fresh.ID); err != nil {
				t.Fatal(err)
			}
			if row := syncGenerationRow(t, ctx, target.DB, fresh.ID); row.Generation != 1 || row.State != "active" {
				t.Fatalf("fresh native generation=%+v", row)
			}
			restored, _ := syncLifecycleWireExport(t, targetApp, "")
			importWire(source, restored)
			for _, cfg := range []APIConfig{source, target} {
				rows := syncMemoryConflictStored(t, ctx, cfg)
				if len(rows) != 1 || rows[0].ID != fresh.ID || rows[0].Value != "fresh native value" {
					t.Fatalf("fresh generation lost to wallclock: %+v", rows)
				}
			}
			// Bind both roots' aliases before measuring the fixed point.
			raw, _ := syncLifecycleWireExport(t, sourceApp, "")
			importWire(target, raw)
			beforeSource, beforeTarget := syncGenerationSnapshot(t, ctx, source.DB), syncGenerationSnapshot(t, ctx, target.DB)
			for n := 0; n < 2; n++ {
				if ack := importWire(target, stale); ack["imported"] != float64(0) || ack["skipped"] != float64(1) {
					t.Fatalf("old future-clock snapshot reappeared=%v", ack)
				}
				for _, pair := range [][2]APIConfig{{source, target}, {target, source}} {
					raw, _ := syncLifecycleWireExport(t, syncLifecycleWireApp(pair[0]), "")
					if ack := importWire(pair[1], raw); ack["imported"] != float64(0) || ack["skipped"] != ack["total"] {
						t.Fatalf("replay not fixed point=%v", ack)
					}
				}
			}
			if !reflect.DeepEqual(beforeSource, syncGenerationSnapshot(t, ctx, source.DB)) || !reflect.DeepEqual(beforeTarget, syncGenerationSnapshot(t, ctx, target.DB)) {
				t.Fatal("generation replay changed SQL/history/aliases/outbox")
			}
			for _, pair := range []struct {
				cfg APIConfig
				old string
			}{{source, remote.ID}, {target, local.ID}} {
				// Reopen each persistent root with a fresh native pool and outbox.
				beforeReopen := syncGenerationSnapshot(t, ctx, pair.cfg.DB)
				var reopened *sql.DB
				if dialect == "sqlite" {
					reopened = independentMemoryIndexDB(t, &documentHTTPFixture{db: pair.cfg.DB, t: t})
				} else {
					var database, schema string
					if err := pair.cfg.DB.QueryRowContext(ctx, "SELECT current_database(), current_schema()").Scan(&database, &schema); err != nil {
						t.Fatal(err)
					}
					config, err := pgx.ParseConfig(os.Getenv("LEVARA_TEST_POSTGRES_DSN"))
					if err != nil {
						t.Fatal(err)
					}
					config.Database = database
					config.RuntimeParams["search_path"] = schema
					reopened = stdlib.OpenDB(*config)
					reopened.SetMaxOpenConns(1)
					t.Cleanup(func() { _ = reopened.Close() })
					if err := reopened.PingContext(ctx); err != nil {
						t.Fatal(err)
					}
				}
				if err := pair.cfg.DB.Close(); err != nil {
					t.Fatal(err)
				}
				pair.cfg.DB = reopened
				if err := MigrateSchema(reopened); err != nil {
					t.Fatal(err)
				}
				var err error
				pair.cfg.MemoryIndexOutbox, err = memoryindex.NewStore(reopened)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(beforeReopen, syncGenerationSnapshot(t, ctx, reopened)) {
					t.Fatal("persistent root reopen changed generations/aliases/SQL/outbox")
				}
				if ack := importWire(pair.cfg, stale); ack["imported"] != float64(0) || ack["skipped"] != float64(1) {
					t.Fatalf("reopened root accepted old future-clock snapshot: %v", ack)
				}
				if !reflect.DeepEqual(beforeReopen, syncGenerationSnapshot(t, ctx, reopened)) {
					t.Fatal("reopened stale replay changed generations/aliases/SQL/outbox")
				}
				if row := syncGenerationRow(t, ctx, pair.cfg.DB, pair.old); row.State != "deleted" || row.Generation != 0 {
					t.Fatalf("old generation ceased terminal=%+v", row)
				}
				if pair.cfg.DB.Stats().InUse != 0 {
					t.Fatal("generation replay leaked pool-one connection")
				}
			}
		})
	}
}

func TestSyncMemoryGenerationInvalidLineageRollback(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			source, target := syncMemoryConflictConfig(t, dialect), syncMemoryConflictConfig(t, dialect)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			save := func(cfg APIConfig, owner, key string) {
				t.Helper()
				nativeCtx := context.WithValue(ctx, mcp.UserIDKey, owner)
				result := mcp.ToolSaveMemory(nativeCtx, NewMCPDeps(cfg), map[string]any{"key": key, "value": "native lineage control", "collection": "lineage-collection", "room": "memory", "hall": "fact"})
				if result.IsError {
					t.Fatalf("native save=%+v", result)
				}
			}
			for _, cfg := range []APIConfig{source, target} {
				save(cfg, "lineage-owner", "lineage-key")
				save(cfg, "foreign-lineage-owner", "foreign-key")
			}
			sourceApp, targetApp := syncLifecycleWireApp(source), syncLifecycleWireApp(target)
			raw, batch := syncLifecycleWireExport(t, sourceApp, "")
			status, ack := syncLifecycleWireImport(t, targetApp, raw)
			if status != 200 || ack["failed"] != float64(0) {
				t.Fatalf("valid positive transfer=%d %v", status, ack)
			}
			var candidate int
			var foreignID string
			for i, m := range batch.Memories {
				if m.OwnerID == "lineage-owner" {
					candidate = i
				} else {
					foreignID = m.ID
				}
			}
			if foreignID == "" {
				t.Fatal("native foreign scope control missing")
			}
			for _, kind := range []string{"missing", "cross_scope", "canonical_self"} {
				t.Run(kind, func(t *testing.T) {
					var bad syncMemoryBatch
					if err := json.Unmarshal(raw, &bad); err != nil {
						t.Fatal(err)
					}
					ref := "00000000-0000-4000-8000-000000000099"
					if kind == "cross_scope" {
						ref = foreignID
					}
					if kind == "canonical_self" {
						ref = bad.Memories[candidate].ID
					}
					encoded, err := json.Marshal([]string{ref})
					if err != nil {
						t.Fatal(err)
					}
					bad.Memories[candidate].ConsolidatedFrom = string(encoded)
					bad.Memories[candidate].UpdatedAt = "2099-01-01T00:00:00Z"
					wire, err := json.Marshal(bad)
					if err != nil {
						t.Fatal(err)
					}
					before := syncGenerationSnapshot(t, ctx, target.DB)
					status, ack := syncLifecycleWireImport(t, targetApp, wire)
					failed, _ := ack["failed"].(float64)
					message, _ := ack["error"].(string)
					if status != http.StatusOK || ack["imported"] != float64(0) || ack["skipped"] != float64(0) || failed != float64(len(bad.Memories)+len(bad.Deletions)) || ack["total"] != failed || message == "" || ack["protocol_version"] != float64(syncMemoryProtocolVersion) {
						t.Fatalf("invalid %s lineage admitted=%d %v", kind, status, ack)
					}
					if !reflect.DeepEqual(before, syncGenerationSnapshot(t, ctx, target.DB)) {
						t.Fatalf("invalid %s lineage changed six-table SQL/history/aliases/outbox", kind)
					}
				})
			}
			if source.DB.Stats().InUse != 0 || target.DB.Stats().InUse != 0 {
				t.Fatal("lineage admission leaked pool-one connection")
			}
		})
	}
}
