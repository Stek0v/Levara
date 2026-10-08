package http

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/mcp"
)

// Native SQL/explicit trusted-local MCP fixture; no HTTP authentication or
// independent-process claim. Scope-variant sync payloads are admin replication
// fixtures, never evidence that callers may choose an authorization owner.
func TestSyncMemoryNativeDeletionJournal(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			cfg := syncMemoryConflictConfig(t, dialect)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, mcp.UserIDKey, "journal-owner")
			deps := NewMCPDeps(cfg)
			actor := deps.MetadataActor(ctx)
			if !actor.TrustedLocal || actor.UserID != "journal-owner" || actor.TenantID != "" || actor.Credential.Kind != "" {
				t.Fatalf("fixture authority=%+v", actor)
			}
			if cfg.DB.Stats().MaxOpenConnections != 1 {
				t.Fatalf("fixture is not pool-one: %+v", cfg.DB.Stats())
			}
			const key = "journal-key"
			const collection = "journal-collection"
			save := func() {
				t.Helper()
				result := mcp.ToolSaveMemory(ctx, deps, map[string]any{
					"key": key, "value": "native journal source", "type": "project",
					"collection": collection, "room": "memory", "hall": "fact",
				})
				if result.IsError || len(result.Content) == 0 {
					t.Fatalf("native save=%+v", result)
				}
			}
			save()
			before := syncMemoryConflictStored(t, ctx, cfg)
			if len(before) != 1 || before[0].ID == "" || before[0].OwnerID != "journal-owner" || before[0].CollectionName != collection || before[0].Key != key {
				t.Fatalf("native source identity=%+v", before)
			}
			original := before[0]
			jobsBefore := syncMemoryConflictJobs(t, ctx, cfg)
			memoryTable, journalTable := "memories", "memory_sync_deletions"
			if dialect == "postgres" {
				var current, memorySchema, journalSchema string
				if err := cfg.DB.QueryRowContext(ctx, `SELECT current_schema(),
					(SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.oid='memories'::regclass),
					(SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.oid='memory_sync_deletions'::regclass)`).Scan(&current, &memorySchema, &journalSchema); err != nil {
					t.Fatal(err)
				}
				if current == "" || memorySchema != current || journalSchema != current {
					t.Fatalf("migration namespaces: current=%q memories=%q journal=%q", current, memorySchema, journalSchema)
				}
				quote := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
				memoryTable = quote(memorySchema) + ".memories"
				journalTable = quote(journalSchema) + ".memory_sync_deletions"
			}
			tx, err := cfg.DB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			// Trigger function must resolve its own table schema even when the
			// deleting connection's search_path no longer contains that schema.
			if dialect == "postgres" {
				if _, err := tx.ExecContext(ctx, "SET LOCAL search_path = pg_catalog"); err != nil {
					t.Fatal(err)
				}
			}
			result, err := tx.ExecContext(ctx, Q("DELETE FROM "+memoryTable+" WHERE id=$1 AND owner_id=$2 AND collection_name=$3"), original.ID, original.OwnerID, collection)
			if err != nil {
				t.Fatal(err)
			}
			if n, err := result.RowsAffected(); err != nil || n != 1 {
				t.Fatalf("transaction delete=%d err=%v", n, err)
			}
			var pending int
			if err := tx.QueryRowContext(ctx, Q("SELECT COUNT(*) FROM "+journalTable+" WHERE memory_id=$1 AND owner_id=$2 AND collection_name=$3 AND key=$4"), original.ID, original.OwnerID, collection, key).Scan(&pending); err != nil || pending != 1 {
				t.Fatalf("journal missing inside deleting transaction: count=%d err=%v", pending, err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if after := syncMemoryConflictStored(t, ctx, cfg); !reflect.DeepEqual(before, after) {
				t.Fatalf("rollback changed memory: before=%+v after=%+v", before, after)
			}
			if after := syncMemoryConflictJobs(t, ctx, cfg); !reflect.DeepEqual(jobsBefore, after) {
				t.Fatal("rollback changed outbox")
			}
			var journalCount int
			if err := cfg.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_sync_deletions").Scan(&journalCount); err != nil || journalCount != 0 {
				t.Fatalf("rollback retained journal: count=%d err=%v", journalCount, err)
			}
			// A failed native index-intent write must roll back both the physical
			// deletion and its trigger journal. Rename only this fixture's table.
			if _, err := cfg.DB.ExecContext(ctx, "ALTER TABLE memory_index_jobs RENAME TO memory_index_jobs_journal_fixture"); err != nil {
				t.Fatal(err)
			}
			_, deleteErr := mcp.DeleteMemory(ctx, deps, mcp.DeleteMemoryRequest{MemoryID: original.ID})
			if _, err := cfg.DB.ExecContext(ctx, "ALTER TABLE memory_index_jobs_journal_fixture RENAME TO memory_index_jobs"); err != nil {
				t.Fatal(err)
			}
			if deleteErr == nil {
				t.Fatal("native deletion ignored failed index-intent write")
			}
			if after := syncMemoryConflictStored(t, ctx, cfg); !reflect.DeepEqual(before, after) {
				t.Fatalf("failed outbox deletion changed memory: %+v", after)
			}
			if after := syncMemoryConflictJobs(t, ctx, cfg); !reflect.DeepEqual(jobsBefore, after) {
				t.Fatal("failed outbox deletion changed jobs")
			}
			if err := cfg.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_sync_deletions").Scan(&journalCount); err != nil || journalCount != 0 {
				t.Fatalf("failed outbox deletion retained journal: count=%d err=%v", journalCount, err)
			}
			deleted, err := mcp.DeleteMemory(ctx, deps, mcp.DeleteMemoryRequest{MemoryID: original.ID})
			if err != nil || deleted.ID != original.ID || deleted.Key != key || deleted.OwnerID != original.OwnerID || deleted.Collection != collection {
				t.Fatalf("native delete=%+v err=%v", deleted, err)
			}
			readJournal := func() [5]string {
				t.Helper()
				var row [5]string
				if err := cfg.DB.QueryRowContext(ctx, Q(`SELECT memory_id,owner_id,collection_name,key,deleted_at FROM memory_sync_deletions WHERE memory_id=$1 AND owner_id=$2 AND collection_name=$3`), original.ID, original.OwnerID, collection).Scan(&row[0], &row[1], &row[2], &row[3], &row[4]); err != nil {
					t.Fatal(err)
				}
				return row
			}
			committed := readJournal()
			if committed[0] != original.ID || committed[1] != original.OwnerID || committed[2] != collection || committed[3] != key {
				t.Fatalf("journal scope=%v", committed)
			}
			if _, err := time.Parse(time.RFC3339Nano, committed[4]); err != nil {
				t.Fatalf("display timestamp=%q err=%v", committed[4], err)
			}
			if rows := syncMemoryConflictStored(t, ctx, cfg); len(rows) != 0 {
				t.Fatalf("committed deletion retained row: %+v", rows)
			}
			var retirementJobs int
			for _, job := range syncMemoryConflictJobs(t, ctx, cfg) {
				if job.MemoryID == original.ID && job.OwnerID == original.OwnerID && job.Collection == collection && job.Operation == "delete_vector" {
					retirementJobs++
				}
			}
			if retirementJobs != 1 {
				t.Fatalf("native committed retirement jobs=%d", retirementJobs)
			}
			for attempt := 0; attempt < 2; attempt++ {
				if err := MigrateSchema(cfg.DB); err != nil {
					t.Fatal(err)
				}
				if after := readJournal(); after != committed {
					t.Fatalf("repeat migration rewrote journal: before=%v after=%v", committed, after)
				}
			}
			// Deleted physical IDs retain their historical identity. Another scope
			// can use the same logical key with a fresh ID, never claim the old ID.
			for _, axis := range []string{"owner", "collection"} {
				incoming := original
				if axis == "owner" {
					incoming.OwnerID = "journal-other-owner"
				} else {
					incoming.CollectionName = "journal-other-collection"
				}
				jobs := syncMemoryConflictJobs(t, ctx, cfg)
				counts, accepted, err := importSyncMemories(ctx, cfg, []syncMemory{incoming})
				if err == nil || counts["failed"] != 1 || counts["imported"] != 0 || len(accepted) != 0 {
					t.Fatalf("other %s scope claimed deleted ID: counts=%v accepted=%+v err=%v", axis, counts, accepted, err)
				}
				if rows := syncMemoryConflictStored(t, ctx, cfg); len(rows) != 0 {
					t.Fatalf("foreign scoped replay changed SQL: %+v", rows)
				}
				if after := syncMemoryConflictJobs(t, ctx, cfg); !reflect.DeepEqual(jobs, after) {
					t.Fatal("foreign scoped replay changed outbox")
				}
				incoming.ID = original.ID + "-" + axis
				counts, accepted, err = importSyncMemories(ctx, cfg, []syncMemory{incoming})
				if err != nil || counts["imported"] != 1 || len(accepted) != 1 || accepted[0].ID != incoming.ID {
					t.Fatalf("fresh other %s scope blocked: counts=%v accepted=%+v err=%v", axis, counts, accepted, err)
				}
				otherCtx := context.WithValue(ctx, mcp.UserIDKey, incoming.OwnerID)
				otherDeleted, err := mcp.DeleteMemory(otherCtx, deps, mcp.DeleteMemoryRequest{MemoryID: incoming.ID})
				if err != nil || otherDeleted.OwnerID != incoming.OwnerID || otherDeleted.Collection != incoming.CollectionName {
					t.Fatalf("other %s scoped delete=%+v err=%v", axis, otherDeleted, err)
				}
				if after := readJournal(); after != committed {
					t.Fatalf("other scope rewrote original journal: before=%v after=%v", committed, after)
				}
			}
			if err := cfg.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_sync_deletions").Scan(&journalCount); err != nil || journalCount != 3 {
				t.Fatalf("scoped journal cardinality=%d err=%v", journalCount, err)
			}
			save()
			fresh := syncMemoryConflictStored(t, ctx, cfg)
			if len(fresh) != 1 || fresh[0].ID == "" || fresh[0].ID == original.ID || fresh[0].Key != key || fresh[0].OwnerID != original.OwnerID || fresh[0].CollectionName != collection {
				t.Fatalf("fresh native incarnation blocked/changed: %+v", fresh)
			}
			if after := readJournal(); after != committed {
				t.Fatalf("fresh save rewrote old journal: before=%v after=%v", committed, after)
			}
			if stats := cfg.DB.Stats(); stats.MaxOpenConnections != 1 || stats.InUse != 0 {
				t.Fatalf("pool-one transaction leaked: %+v", stats)
			}
		})
	}
}
