package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"github.com/stek0v/levara/pkg/mcp"
)

func syncLifecycleWireSQLSnapshot(t *testing.T, cfg APIConfig) [][]any {
	t.Helper()
	rows, err := cfg.DB.Query("SELECT * FROM memories ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	result := [][]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		for i, value := range values {
			if bytesValue, ok := value.([]byte); ok {
				values[i] = append([]byte(nil), bytesValue...)
			}
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func syncLifecycleWireApp(cfg APIConfig) *fiber.App {
	app := fiber.New()
	RegisterSyncAPI(app, cfg)
	return app
}

func syncLifecycleWireExport(t *testing.T, app *fiber.App, since string) ([]byte, syncMemoryBatch) {
	t.Helper()
	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/sync/export/memories"+since, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("export status=%d", response.StatusCode)
	}
	var raw json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	var batch syncMemoryBatch
	if err := json.Unmarshal(raw, &batch); err != nil {
		t.Fatal(err)
	}
	if err := batch.validate(); err != nil {
		t.Fatal(err)
	}
	return raw, batch
}

func syncLifecycleWireImport(t *testing.T, app *fiber.App, raw []byte) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/sync/import/memories", bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var ack map[string]any
	if err := json.NewDecoder(response.Body).Decode(&ack); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, ack
}

// Native mutations and real HTTP JSON transfer between independent SQL stores.
// Trusted local mode is explicit; JWT and independent OS processes are covered
// elsewhere. Physical-ID history is bounded: unseen divergent IDs remain open.
func TestSyncMemoryLifecycleWireNativeDeletion(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			source, target := syncMemoryConflictConfig(t, dialect), syncMemoryConflictConfig(t, dialect)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, mcp.UserIDKey, "wire-owner")
			deps := NewMCPDeps(source)
			actor := deps.MetadataActor(ctx)
			if !actor.TrustedLocal || actor.UserID != "wire-owner" || actor.TenantID != "" || actor.Credential.Kind != "" {
				t.Fatalf("native authority=%+v", actor)
			}
			result := mcp.ToolSaveMemory(ctx, deps, map[string]any{"key": "wire-key", "value": "native content", "collection": "wire-collection", "room": "memory", "hall": "fact"})
			if result.IsError || len(result.Content) == 0 {
				t.Fatalf("native save=%+v", result)
			}
			sourceApp, targetApp := syncLifecycleWireApp(source), syncLifecycleWireApp(target)
			// A future since boundary must not truncate lifecycle dependencies.
			predelete, batch := syncLifecycleWireExport(t, sourceApp, "?since=2099-01-01T00:00:00Z")
			if len(batch.Memories) != 1 || len(batch.Deletions) != 0 {
				t.Fatalf("native save snapshot=%+v", batch)
			}
			memory := batch.Memories[0]
			if memory.OwnerID != "wire-owner" || memory.CollectionName != "wire-collection" || memory.Key != "wire-key" {
				t.Fatalf("native identity=%+v", memory)
			}
			status, ack := syncLifecycleWireImport(t, targetApp, predelete)
			if status != 200 || ack["protocol_version"] != float64(syncMemoryProtocolVersion) || ack["imported"] != float64(1) || ack["total"] != float64(1) {
				t.Fatalf("initial transfer=%d %v", status, ack)
			}
			deleted, err := mcp.DeleteMemory(ctx, deps, mcp.DeleteMemoryRequest{MemoryID: memory.ID})
			if err != nil || deleted.ID != memory.ID || deleted.OwnerID != memory.OwnerID || deleted.Collection != memory.CollectionName {
				t.Fatalf("native deletion=%+v %v", deleted, err)
			}
			deletionWire, batch := syncLifecycleWireExport(t, sourceApp, "?since=2099-01-01T00:00:00Z")
			if len(batch.Memories) != 0 || len(batch.Deletions) != 1 {
				t.Fatalf("deletion-only export=%+v", batch)
			}
			tombstone := batch.Deletions[0]
			if tombstone.ID != memory.ID || tombstone.Key != memory.Key || tombstone.OwnerID != memory.OwnerID || tombstone.CollectionName != memory.CollectionName || tombstone.DeletedAt == "" {
				t.Fatalf("deletion identity=%+v", tombstone)
			}
			var legacy []syncMemory
			if err := json.Unmarshal(deletionWire, &legacy); err == nil {
				t.Fatal("old array decoder accepted lifecycle object")
			}
			status, ack = syncLifecycleWireImport(t, targetApp, deletionWire)
			if status != 200 || ack["protocol_version"] != float64(syncMemoryProtocolVersion) || ack["imported"] != float64(1) || ack["skipped"] != float64(0) || ack["total"] != float64(1) {
				t.Fatalf("deletion transfer=%d %v", status, ack)
			}
			if rows := syncMemoryConflictStored(t, ctx, target); len(rows) != 0 {
				t.Fatalf("deleted target remained=%+v", rows)
			}
			jobs := syncMemoryConflictJobs(t, ctx, target)
			deletionJobs := 0
			for _, job := range jobs {
				if job.Operation == "delete_vector" && job.MemoryID == memory.ID && job.OwnerID == memory.OwnerID && job.Collection == memory.CollectionName {
					deletionJobs++
				}
			}
			if deletionJobs != 1 {
				t.Fatalf("canonical deletion intent=%+v", jobs)
			}
			for _, raw := range [][]byte{deletionWire, predelete} {
				status, ack = syncLifecycleWireImport(t, targetApp, raw)
				if status != 200 || ack["imported"] != float64(0) || ack["skipped"] != float64(1) || ack["total"] != float64(1) {
					t.Fatalf("known replay=%d %v", status, ack)
				}
				if rows := syncMemoryConflictStored(t, ctx, target); len(rows) != 0 {
					t.Fatalf("known stale ID resurrected=%+v", rows)
				}
				if after := syncMemoryConflictJobs(t, ctx, target); !reflect.DeepEqual(jobs, after) {
					t.Fatal("known replay changed outbox")
				}
			}
			for _, cfg := range []APIConfig{source, target} {
				if cfg.DB.Stats().InUse != 0 {
					t.Fatal("wire transaction leaked pool-one connection")
				}
			}
		})
	}
}

func TestSyncMemoryLifecycleWireRejectsUnsupportedEnvelope(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			cfg := syncMemoryConflictConfig(t, dialect)
			ctx := context.Background()
			seed := syncMemoryConflictPayload("sentinel", "unchanged")
			if _, _, err := importSyncMemories(ctx, cfg, []syncMemory{seed}); err != nil {
				t.Fatal(err)
			}
			rows, jobs := syncMemoryConflictStored(t, ctx, cfg), syncMemoryConflictJobs(t, ctx, cfg)
			var journalBefore int
			if err := cfg.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_sync_deletions").Scan(&journalBefore); err != nil {
				t.Fatal(err)
			}
			for _, raw := range []string{
				`[{"id":"new","key":"new","value":"legacy"}]`,
				`{"protocol_version":4,"memories":[],"deletions":[],"incarnations":[],"aliases":[]}`, `{"protocol_version":3,"memories":[],"deletions":[]}`,
				`{"protocol_version":2,"memories":[]}`,
				`{"protocol_version":2,"deletions":[]}`,
				`{"protocol_version":2,"memories":null,"deletions":[]}`,
				`{"protocol_version":2,"memories":[],"deletions":null}`,
			} {
				status, _ := syncLifecycleWireImport(t, syncLifecycleWireApp(cfg), []byte(raw))
				if status != 400 {
					t.Fatalf("unsupported envelope status=%d payload=%s", status, raw)
				}
				if after := syncMemoryConflictStored(t, ctx, cfg); !reflect.DeepEqual(rows, after) {
					t.Fatal("protocol rejection changed SQL")
				}
				if after := syncMemoryConflictJobs(t, ctx, cfg); !reflect.DeepEqual(jobs, after) {
					t.Fatal("protocol rejection changed outbox")
				}
				var journalAfter int
				if err := cfg.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_sync_deletions").Scan(&journalAfter); err != nil || journalAfter != journalBefore {
					t.Fatalf("protocol rejection journal=%d %v", journalAfter, err)
				}
			}
		})
	}
}

func TestSyncMemoryCanonicalLifecycleSelfReferenceRejectsAtomically(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, field := range []string{"superseded_by", "supersedes_memory_id"} {
				t.Run(field, func(t *testing.T) {
					cfg := syncMemoryConflictConfig(t, dialect)
					ctx := context.Background()
					original := syncMemoryConflictPayload("local-canonical", "original")
					if _, _, err := importSyncMemories(ctx, cfg, []syncMemory{original}); err != nil {
						t.Fatal(err)
					}
					before, jobs := syncLifecycleWireSQLSnapshot(t, cfg), syncMemoryConflictJobs(t, ctx, cfg)
					incoming := original
					incoming.ID = "foreign-id"
					incoming.Value = "new content"
					incoming.UpdatedAt = "2026-09-06T00:00:00Z"
					if field == "superseded_by" {
						incoming.SupersededBy = original.ID
						incoming.ValidUntil = "2026-09-06T00:00:00Z"
					} else {
						incoming.SupersedesMemoryID = original.ID
					}
					counts, accepted, err := importSyncMemories(ctx, cfg, []syncMemory{incoming})
					if err == nil || !strings.Contains(err.Error(), "self reference") || counts["failed"] != 1 || counts["imported"] != 0 || len(accepted) != 0 {
						t.Fatalf("canonical self-reference=%v %+v %v", counts, accepted, err)
					}
					if after := syncLifecycleWireSQLSnapshot(t, cfg); !reflect.DeepEqual(before, after) {
						t.Fatal("self-reference changed authoritative SQL row")
					}
					if after := syncMemoryConflictJobs(t, ctx, cfg); !reflect.DeepEqual(jobs, after) {
						t.Fatal("self-reference changed outbox")
					}
					if cfg.DB.Stats().InUse != 0 {
						t.Fatal("self-reference leaked transaction")
					}
				})
			}
		})
	}
}

func TestSyncMemoryLifecycleStandalonePullAndLegacyPeerPush(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			source, target := syncMemoryConflictConfig(t, dialect), syncMemoryConflictConfig(t, dialect)
			ctx := context.Background()
			seed := syncMemoryConflictPayload("wire-positive", "content")
			if _, _, err := importSyncMemories(ctx, source, []syncMemory{seed}); err != nil {
				t.Fatal(err)
			}
			remote := httptest.NewServer(adaptor.FiberApp(syncLifecycleWireApp(source)))
			defer remote.Close()
			result := SyncPull(target, remote.URL, []string{"memories"}, "")
			if result["memories_error"] != nil {
				t.Fatalf("standalone valid pull=%v", result)
			}
			if rows := syncMemoryConflictStored(t, ctx, target); len(rows) != 1 || rows[0].ID != seed.ID {
				t.Fatalf("standalone pull rows=%+v", rows)
			}
			receivedBatches := make(chan syncMemoryBatch, 1)
			legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Model the old decoder: receiving an object is a hard failure.
				var old []syncMemory
				var raw json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if err := json.Unmarshal(raw, &old); err == nil {
					t.Error("old peer decoded lifecycle envelope")
				}
				var received syncMemoryBatch
				if err := json.Unmarshal(raw, &received); err != nil {
					t.Error(err)
				}
				receivedBatches <- received
				w.WriteHeader(400)
				fmt.Fprint(w, `{"detail":"array required"}`)
			}))
			defer legacy.Close()
			before, jobs := syncMemoryConflictStored(t, ctx, source), syncMemoryConflictJobs(t, ctx, source)
			pushed := syncPush(ctx, source, legacy.URL, []string{"memories"}, "")
			var received syncMemoryBatch
			select {
			case received = <-receivedBatches:
			case <-time.After(time.Second):
				t.Fatal("legacy peer did not receive lifecycle object")
			}
			if pushed["memories_error"] == nil || received.validate() != nil || len(received.Memories) != 1 {
				t.Fatalf("legacy push rejection=%v received=%+v", pushed, received)
			}
			if after := syncMemoryConflictStored(t, ctx, source); !reflect.DeepEqual(before, after) {
				t.Fatal("failed legacy push changed SQL")
			}
			if after := syncMemoryConflictJobs(t, ctx, source); !reflect.DeepEqual(jobs, after) {
				t.Fatal("failed legacy push changed outbox")
			}
		})
	}
}
