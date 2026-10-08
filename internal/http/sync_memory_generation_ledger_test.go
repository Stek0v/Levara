package http

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/mcp"
)

type syncGenerationFixtureRow struct {
	ID, Owner, Collection, Key, State string
	Resolved                          int
	Generation, Revision              int64
}

func syncGenerationRow(t *testing.T, ctx context.Context, db *sql.DB, id string) syncGenerationFixtureRow {
	t.Helper()
	var row syncGenerationFixtureRow
	err := db.QueryRowContext(ctx, Q("SELECT memory_id,owner_id,collection_name,logical_key,state,original_key_resolved,generation,state_revision FROM memory_sync_incarnations WHERE memory_id=$1"), id).
		Scan(&row.ID, &row.Owner, &row.Collection, &row.Key, &row.State, &row.Resolved, &row.Generation, &row.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func syncGenerationSnapshot(t *testing.T, ctx context.Context, db *sql.DB) map[string][][]any {
	t.Helper()
	result := map[string][][]any{}
	for _, table := range []string{"memories", "memory_sync_heads", "memory_sync_incarnations", "memory_sync_aliases", "memory_sync_deletions", "memory_index_jobs"} {
		order := "1"
		switch table {
		case "memory_sync_heads", "memory_sync_deletions":
			order = "1,2,3"
		}
		rows, err := db.QueryContext(ctx, "SELECT * FROM "+table+" ORDER BY "+order)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for i, value := range values {
				if b, ok := value.([]byte); ok {
					values[i] = string(b)
				}
			}
			result[table] = append(result[table], values)
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	return result
}

// Native trusted-local mutations and real SQL trigger transactions, pool one.
// No HTTP owner-hint or sync-generation convergence claim.
func TestSyncMemoryNativeGenerationLedger(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			cfg := syncMemoryConflictConfig(t, dialect)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, mcp.UserIDKey, "generation-owner")
			deps := NewMCPDeps(cfg)
			if !deps.MetadataActor(ctx).TrustedLocal || cfg.DB.Stats().MaxOpenConnections != 1 {
				t.Fatal("requires trusted native pool-one fixture")
			}
			check := func(r mcp.ToolResult) {
				t.Helper()
				if r.IsError || len(r.Content) == 0 {
					t.Fatalf("native mutation=%+v", r)
				}
			}
			save := func(value string) {
				t.Helper()
				check(mcp.ToolSaveMemory(ctx, deps, map[string]any{"key": "generation-key", "value": value, "type": "project", "collection": "generation-collection", "room": "memory", "hall": "fact"}))
			}
			save("first")
			first := syncMemoryConflictStored(t, ctx, cfg)[0]
			row := syncGenerationRow(t, ctx, cfg.DB, first.ID)
			if row.Key != "generation-key" || row.Owner != "generation-owner" || row.Collection != "generation-collection" || row.Resolved != 1 || row.Generation != 0 || row.Revision != 0 || row.State != "active" {
				t.Fatalf("first incarnation=%+v", row)
			}
			save("second")
			save("third")
			var count int
			for _, table := range []string{"memory_sync_heads", "memory_sync_incarnations", "memory_sync_aliases"} {
				if err := cfg.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 1 {
					t.Fatalf("upsert minted phantom identities: %s=%d err=%v", table, count, err)
				}
			}
			if updated := syncGenerationRow(t, ctx, cfg.DB, first.ID); updated != row {
				t.Fatalf("content upsert changed lifecycle: %+v", updated)
			}
			if _, err := cfg.DB.ExecContext(ctx, Q("INSERT INTO memories(id,key,value,owner_id,collection_name,supersedes_memory_id) VALUES($1,$2,$3,$4,$5,$6)"), "provenance-only-id", "provenance-only-key", "hint", "generation-owner", "generation-collection", first.ID); err != nil {
				t.Fatal(err)
			}
			hint := syncGenerationRow(t, ctx, cfg.DB, "provenance-only-id")
			if hint.Generation != 0 || hint.State != "active" {
				t.Fatalf("hint did not start independent slot: %+v", hint)
			}
			if after := syncGenerationRow(t, ctx, cfg.DB, first.ID); after != row {
				t.Fatal("provenance hint retired/advanced predecessor")
			}
			check(mcp.ToolSupersedeMemory(ctx, deps, map[string]any{"old_memory_id": first.ID, "new_value": "replacement", "reason": "generation ledger fixture"}))
			var successor string
			if err := cfg.DB.QueryRowContext(ctx, Q("SELECT superseded_by FROM memories WHERE id=$1"), first.ID).Scan(&successor); err != nil {
				t.Fatal(err)
			}
			old := syncGenerationRow(t, ctx, cfg.DB, first.ID)
			next := syncGenerationRow(t, ctx, cfg.DB, successor)
			if old.Key != "generation-key" || old.Generation != 0 || old.State != "retired" || old.Revision != 1 || next.Key != old.Key || next.Generation != 1 || next.State != "active" || next.Revision != 0 {
				t.Fatalf("supersede identity=%+v successor=%+v", old, next)
			}
			// Consolidation restores a retained ID using the same native UPDATE shape;
			// the archive key remains separate from the original logical key.
			if _, err := cfg.DB.ExecContext(ctx, Q("UPDATE memories SET superseded_by='',valid_until=NULL WHERE id=$1"), first.ID); err != nil {
				t.Fatal(err)
			}
			restored := syncGenerationRow(t, ctx, cfg.DB, first.ID)
			if restored.Key != old.Key || restored.Generation != 0 || restored.Revision != 2 || restored.State != "active" {
				t.Fatalf("restore=%+v", restored)
			}
			if _, err := cfg.DB.ExecContext(ctx, Q("UPDATE memories SET superseded_by=$1,valid_until=CURRENT_TIMESTAMP WHERE id=$2"), successor, first.ID); err != nil {
				t.Fatal(err)
			}
			if retired := syncGenerationRow(t, ctx, cfg.DB, first.ID); retired.Revision != 3 || retired.State != "retired" {
				t.Fatalf("repeat retirement=%+v", retired)
			}
			before := syncGenerationSnapshot(t, ctx, cfg.DB)
			tx, err := cfg.DB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, Q("DELETE FROM memories WHERE id=$1"), successor); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			var pendingState string
			var pendingRevision int64
			if err := tx.QueryRowContext(ctx, Q("SELECT state,state_revision FROM memory_sync_incarnations WHERE memory_id=$1"), successor).Scan(&pendingState, &pendingRevision); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if pendingState != "deleted" || pendingRevision != 1 {
				tx.Rollback()
				t.Fatalf("pending delete=%s/%d", pendingState, pendingRevision)
			}
			if _, err := tx.ExecContext(ctx, Q("INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES($1,$2,$3,$4,$5)"), "rollback-generation-id", "generation-key", "rollback", "generation-owner", "generation-collection"); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			var pendingGeneration int64
			if err := tx.QueryRowContext(ctx, "SELECT generation FROM memory_sync_incarnations WHERE memory_id='rollback-generation-id'").Scan(&pendingGeneration); err != nil || pendingGeneration != 2 {
				tx.Rollback()
				t.Fatalf("pending generation=%d err=%v", pendingGeneration, err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if after := syncGenerationSnapshot(t, ctx, cfg.DB); !reflect.DeepEqual(before, after) {
				t.Fatal("rollback changed memory, generation/head, alias, deletion journal or outbox")
			}
			deleted, err := mcp.DeleteMemory(ctx, deps, mcp.DeleteMemoryRequest{MemoryID: successor})
			if err != nil || deleted.ID != successor {
				t.Fatalf("native delete=%+v err=%v", deleted, err)
			}
			terminal := syncGenerationRow(t, ctx, cfg.DB, successor)
			if terminal.State != "deleted" || terminal.Revision != 1 || terminal.Generation != 1 {
				t.Fatalf("delete=%+v", terminal)
			}
			save("recreated")
			var recreated string
			if err := cfg.DB.QueryRowContext(ctx, Q("SELECT id FROM memories WHERE key=$1 AND owner_id=$2 AND collection_name=$3"), "generation-key", "generation-owner", "generation-collection").Scan(&recreated); err != nil {
				t.Fatal(err)
			}
			fresh := syncGenerationRow(t, ctx, cfg.DB, recreated)
			if recreated == successor || fresh.Generation != 2 || fresh.Revision != 0 || fresh.State != "active" {
				t.Fatalf("recreate=%+v", fresh)
			}
			before = syncGenerationSnapshot(t, ctx, cfg.DB)
			for i := 0; i < 2; i++ {
				if err := MigrateSchema(cfg.DB); err != nil {
					t.Fatal(err)
				}
			}
			if after := syncGenerationSnapshot(t, ctx, cfg.DB); !reflect.DeepEqual(before, after) {
				t.Fatalf("repeated migration changed retained lifecycle: before=%#v after=%#v", before, after)
			}
			for _, statement := range []string{
				fmt.Sprintf("UPDATE memory_sync_incarnations SET state='active' WHERE memory_id='%s'", successor),
				fmt.Sprintf("UPDATE memory_sync_aliases SET memory_id='%s' WHERE alias_id='%s'", recreated, successor),
				fmt.Sprintf("DELETE FROM memory_sync_aliases WHERE alias_id='%s'", successor),
				fmt.Sprintf("UPDATE memories SET owner_id='foreign-owner' WHERE id='%s'", recreated),
			} {
				if _, err := cfg.DB.ExecContext(ctx, statement); err == nil {
					t.Fatalf("identity/terminal mutation accepted: %s", statement)
				}
			}
			if after := syncGenerationSnapshot(t, ctx, cfg.DB); !reflect.DeepEqual(before, after) {
				t.Fatal("rejected mutation changed SQL")
			}
			check(mcp.ToolSupersedeMemory(ctx, deps, map[string]any{"old_memory_id": recreated, "key": "different-successor-key", "new_value": "changed key", "reason": "cross-slot lineage"}))
			var crossSlotID string
			if err := cfg.DB.QueryRowContext(ctx, Q("SELECT superseded_by FROM memories WHERE id=$1"), recreated).Scan(&crossSlotID); err != nil {
				t.Fatal(err)
			}
			previousSlot := syncGenerationRow(t, ctx, cfg.DB, recreated)
			crossSlot := syncGenerationRow(t, ctx, cfg.DB, crossSlotID)
			if previousSlot.Key != "generation-key" || previousSlot.Generation != 2 || previousSlot.State != "retired" || crossSlot.Key != "different-successor-key" || crossSlot.Generation != 0 || crossSlot.State != "active" {
				t.Fatalf("cross-slot supersede: %+v %+v", previousSlot, crossSlot)
			}
			if cfg.DB.Stats().InUse != 0 {
				t.Fatalf("connection leak: %+v", cfg.DB.Stats())
			}
		})
	}
}

func TestSyncMemoryGenerationPreregistration(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			cfg := syncMemoryConflictConfig(t, dialect)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			tx, err := cfg.DB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.ExecContext(ctx, Q("INSERT INTO memory_sync_incarnations(memory_id,owner_id,collection_name,logical_key,original_key_resolved,generation,state_revision,state) VALUES($1,$2,$3,$4,1,7,9,'active')"), "registered-id", "registered-owner", "registered-collection", "registered-key"); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, Q("INSERT INTO memory_sync_aliases(alias_id,memory_id) VALUES($1,$2)"), "registered-id", "registered-id"); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, Q("INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES($1,$2,$3,$4,$5)"), "registered-id", "registered-key", "registered", "registered-owner", "registered-collection"); err != nil {
				t.Fatal(err)
			}
			var generation, revision int64
			if err := tx.QueryRowContext(ctx, "SELECT generation,state_revision FROM memory_sync_incarnations WHERE memory_id='registered-id'").Scan(&generation, &revision); err != nil || generation != 7 || revision != 9 {
				t.Fatalf("insert double increment: %d/%d err=%v", generation, revision, err)
			}
			// Validated importer preregisters retirement before its native row write.
			if _, err := tx.ExecContext(ctx, "UPDATE memory_sync_incarnations SET state='retired',state_revision=10 WHERE memory_id='registered-id'"); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, "UPDATE memories SET superseded_by='registered-successor',valid_until=CURRENT_TIMESTAMP WHERE id='registered-id'"); err != nil {
				t.Fatal(err)
			}
			if err := tx.QueryRowContext(ctx, "SELECT state_revision FROM memory_sync_incarnations WHERE memory_id='registered-id'").Scan(&revision); err != nil || revision != 10 {
				t.Fatalf("retirement double increment: %d err=%v", revision, err)
			}
			if _, err := tx.ExecContext(ctx, "UPDATE memory_sync_incarnations SET state='deleted',state_revision=11 WHERE memory_id='registered-id'"); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM memories WHERE id='registered-id'"); err != nil {
				t.Fatal(err)
			}
			if err := tx.QueryRowContext(ctx, "SELECT state_revision FROM memory_sync_incarnations WHERE memory_id='registered-id'").Scan(&revision); err != nil || revision != 11 {
				t.Fatalf("deletion double increment: %d err=%v", revision, err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			var head int64
			if err := cfg.DB.QueryRowContext(ctx, "SELECT generation FROM memory_sync_heads WHERE owner_id='registered-owner' AND collection_name='registered-collection' AND logical_key='registered-key'").Scan(&head); err != nil || head != 7 {
				t.Fatalf("head=%d err=%v", head, err)
			}
		})
	}
}

func TestSyncMemoryGenerationHistoricalBackfill(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			cfg := syncMemoryConflictConfig(t, dialect)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			// Remove only this isolated fixture's new ledger to model a pre-upgrade DB.
			for _, name := range []string{"memory_sync_generation_insert", "memory_sync_generation_update", "memory_sync_generation_delete"} {
				statement := "DROP TRIGGER " + name
				if dialect == "postgres" {
					statement += " ON memories"
				}
				if _, err := cfg.DB.ExecContext(ctx, statement); err != nil {
					t.Fatal(err)
				}
			}
			for _, table := range []string{"memory_sync_aliases", "memory_sync_incarnations", "memory_sync_heads"} {
				if _, err := cfg.DB.ExecContext(ctx, "DROP TABLE "+table); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := cfg.DB.ExecContext(ctx, "INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES('historical-active','active-key','active','historical-active-owner','historical-collection')"); err != nil {
				t.Fatal(err)
			}
			if _, err := cfg.DB.ExecContext(ctx, "INSERT INTO memories(id,key,value,owner_id,collection_name,superseded_by,valid_until) VALUES('historical-retired','opaque#superseded:key','retired','historical-owner','historical-collection','unknown-successor',CURRENT_TIMESTAMP)"); err != nil {
				t.Fatal(err)
			}
			if _, err := cfg.DB.ExecContext(ctx, "INSERT INTO memory_sync_deletions(memory_id,owner_id,collection_name,key,deleted_at) VALUES('historical-deleted','historical-owner','historical-collection','possibly-archived-key','2026-10-07T00:00:00Z')"); err != nil {
				t.Fatal(err)
			}
			if err := MigrateSchema(cfg.DB); err != nil {
				t.Fatal(err)
			}
			active := syncGenerationRow(t, ctx, cfg.DB, "historical-active")
			retired := syncGenerationRow(t, ctx, cfg.DB, "historical-retired")
			deleted := syncGenerationRow(t, ctx, cfg.DB, "historical-deleted")
			if active.Resolved != 1 || active.Generation != 0 || active.State != "active" || retired.Resolved != 0 || retired.Key != "opaque#superseded:key" || retired.State != "retired" || deleted.Resolved != 0 || deleted.State != "deleted" {
				t.Fatalf("historical identities: %+v %+v %+v", active, retired, deleted)
			}
			before := syncGenerationSnapshot(t, ctx, cfg.DB)
			if err := MigrateSchema(cfg.DB); err != nil {
				t.Fatal(err)
			}
			if after := syncGenerationSnapshot(t, ctx, cfg.DB); !reflect.DeepEqual(before, after) {
				t.Fatal("repeat backfill changed historical identity")
			}
			if _, err := cfg.DB.ExecContext(ctx, "INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES('unproved-generation','unrelated-key','new','historical-owner','historical-collection')"); err == nil {
				t.Fatal("unresolved historical scope silently minted generation")
			}
			if after := syncGenerationSnapshot(t, ctx, cfg.DB); !reflect.DeepEqual(before, after) {
				t.Fatal("failed historical generation allocation changed SQL")
			}
		})
	}
}

func TestSyncMemoryGenerationPreregistrationGuards(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			cfg := syncMemoryConflictConfig(t, dialect)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := cfg.DB.ExecContext(ctx, "INSERT INTO memory_sync_incarnations(memory_id,owner_id,collection_name,logical_key,original_key_resolved,generation,state_revision,state) VALUES('guard-id','guard-owner','guard-collection','slot-a',1,0,0,'active')"); err != nil {
				t.Fatal(err)
			}
			before := syncGenerationSnapshot(t, ctx, cfg.DB)
			if _, err := cfg.DB.ExecContext(ctx, "INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES('guard-id','slot-b','invalid slot','guard-owner','guard-collection')"); err == nil {
				t.Fatal("active preregistration accepted different logical slot")
			}
			if after := syncGenerationSnapshot(t, ctx, cfg.DB); !reflect.DeepEqual(before, after) {
				t.Fatalf("invalid active slot changed SQL: before=%#v after=%#v", before, after)
			}
			// Remove-wins ties are valid imports, but restoration needs a newer revision.
			if _, err := cfg.DB.ExecContext(ctx, "UPDATE memory_sync_incarnations SET state='retired' WHERE memory_id='guard-id'"); err != nil {
				t.Fatalf("same-counter retirement rejected: %v", err)
			}
			before = syncGenerationSnapshot(t, ctx, cfg.DB)
			if _, err := cfg.DB.ExecContext(ctx, "UPDATE memory_sync_incarnations SET state='active' WHERE memory_id='guard-id'"); err == nil {
				t.Fatal("same-counter restoration accepted")
			}
			if after := syncGenerationSnapshot(t, ctx, cfg.DB); !reflect.DeepEqual(before, after) {
				t.Fatal("rejected restoration changed SQL")
			}
			// A retained retirement may legitimately carry an archived physical key.
			if _, err := cfg.DB.ExecContext(ctx, "INSERT INTO memories(id,key,value,owner_id,collection_name,superseded_by,valid_until) VALUES('guard-id','opaque-archive-key','retired','guard-owner','guard-collection','successor',CURRENT_TIMESTAMP)"); err != nil {
				t.Fatalf("retired archived-key preregistration rejected: %v", err)
			}
			if row := syncGenerationRow(t, ctx, cfg.DB, "guard-id"); row.Key != "slot-a" || row.Revision != 0 || row.State != "retired" {
				t.Fatalf("retired insert changed registered original identity: %+v", row)
			}
			if _, err := cfg.DB.ExecContext(ctx, "UPDATE memories SET superseded_by='',valid_until=NULL WHERE id='guard-id'"); err != nil {
				t.Fatalf("native newer restoration rejected: %v", err)
			}
			if row := syncGenerationRow(t, ctx, cfg.DB, "guard-id"); row.Revision != 1 || row.State != "active" {
				t.Fatalf("native restoration=%+v", row)
			}
			if _, err := cfg.DB.ExecContext(ctx, "UPDATE memory_sync_incarnations SET state='deleted' WHERE memory_id='guard-id'"); err != nil {
				t.Fatalf("same-counter deletion rejected: %v", err)
			}
		})
	}
}

func TestSyncMemoryGenerationMigrationRejectsTerminalCoexistence(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, terminal := range []string{"deletion_journal", "deleted_incarnation"} {
				t.Run(terminal, func(t *testing.T) {
					cfg := syncMemoryConflictConfig(t, dialect)
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if _, err := cfg.DB.ExecContext(ctx, "INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES('coexist-id','coexist-key','active','coexist-owner','coexist-collection')"); err != nil {
						t.Fatal(err)
					}
					if terminal == "deletion_journal" {
						if _, err := cfg.DB.ExecContext(ctx, "INSERT INTO memory_sync_deletions(memory_id,owner_id,collection_name,key,deleted_at) VALUES('coexist-id','coexist-owner','coexist-collection','coexist-key','2026-10-07T00:00:00Z')"); err != nil {
							t.Fatal(err)
						}
					} else {
						if _, err := cfg.DB.ExecContext(ctx, "UPDATE memory_sync_incarnations SET state='deleted' WHERE memory_id='coexist-id'"); err != nil {
							t.Fatal(err)
						}
					}
					before := syncGenerationSnapshot(t, ctx, cfg.DB)
					if err := MigrateSchema(cfg.DB); err == nil {
						t.Fatal("migration silently accepted physical row with terminal identity")
					}
					if after := syncGenerationSnapshot(t, ctx, cfg.DB); !reflect.DeepEqual(before, after) {
						t.Fatalf("failed terminal coexistence migration changed SQL: before=%#v after=%#v", before, after)
					}
				})
			}
		})
	}
}
