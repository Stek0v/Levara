package mcp

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stek0v/levara/pkg/memoryindex"
)

func TestToolSupersedeMemoryAtomicIndexIntents(t *testing.T) {
	for _, pg := range []bool{false, true} {
		for _, mode := range []string{"success", "second-enqueue-failure", "foreign-pending-job", "validity-retired", "legacy-inline"} {
			t.Run(fmt.Sprintf("postgres=%t/%s", pg, mode), func(t *testing.T) {
				d, ctx := memoryCommitEvidenceFixture(t, pg)
				var base *fakeDeps
				if pg {
					base = d.Deps.(*postgresMemoryDeps).fakeDeps
				} else {
					base = d.Deps.(*fakeDeps)
				}
				// Seed through the native save path before enabling the outbox.
				if got := ToolSaveMemory(ctx, d, map[string]any{"key": "atomic-supersede", "value": "old", "collection": "levara", "room": "memory", "hall": "fact"}); got.IsError {
					t.Fatal(got.Content)
				}
				var oldID string
				if err := d.DB().QueryRow("SELECT id FROM memories WHERE key='atomic-supersede'").Scan(&oldID); err != nil {
					t.Fatal(err)
				}
				base.hasColls, base.embedAvailable = true, true
				base.baseCfg.EmbedModel = "supersede-native-model"
				if mode != "legacy-inline" {
					var err error
					base.memoryIndexOutbox, err = memoryindex.NewStore(d.DB())
					if err != nil {
						t.Fatal(err)
					}
				}
				if mode == "second-enqueue-failure" {
					ddl := []string{`CREATE TRIGGER supersede_reject_upsert BEFORE INSERT ON memory_index_jobs WHEN NEW.operation='upsert_vector' BEGIN SELECT RAISE(ABORT,'injected successor enqueue failure'); END`}
					if pg {
						ddl = []string{
							`CREATE FUNCTION supersede_reject_upsert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.operation='upsert_vector' THEN RAISE EXCEPTION 'injected successor enqueue failure'; END IF; RETURN NEW; END $$`,
							`CREATE TRIGGER supersede_reject_upsert BEFORE INSERT ON memory_index_jobs FOR EACH ROW EXECUTE FUNCTION supersede_reject_upsert()`,
						}
					}
					for _, q := range ddl {
						if _, err := d.DB().Exec(q); err != nil {
							t.Fatal(err)
						}
					}
				}
				if mode == "foreign-pending-job" {
					if _, err := base.memoryIndexOutbox.Enqueue(ctx, memoryindex.Job{MemoryID: oldID, Operation: "delete_vector", OwnerID: "owner-b", Collection: "other", Digest: "delete:" + oldID}); err != nil {
						t.Fatal(err)
					}
				}
				var retiredBefore, outboxBefore string
				snapshot := func(query string) string {
					t.Helper()
					rows, err := d.DB().Query(query)
					if err != nil {
						t.Fatal(err)
					}
					defer rows.Close()
					columns, err := rows.Columns()
					if err != nil {
						t.Fatal(err)
					}
					var all [][]any
					for rows.Next() {
						values := make([]any, len(columns))
						pointers := make([]any, len(columns))
						for i := range values {
							pointers[i] = &values[i]
						}
						if err := rows.Scan(pointers...); err != nil {
							t.Fatal(err)
						}
						all = append(all, values)
					}
					if err := rows.Err(); err != nil {
						t.Fatal(err)
					}
					encoded, err := json.Marshal(all)
					if err != nil {
						t.Fatal(err)
					}
					return string(encoded)
				}
				if mode == "validity-retired" {
					if _, err := d.DB().Exec(d.Q("UPDATE memories SET valid_until=$1 WHERE id=$2"), "2026-10-06T12:00:00Z", oldID); err != nil {
						t.Fatal(err)
					}
					retiredBefore = snapshot("SELECT * FROM memories ORDER BY id")
					outboxBefore = snapshot("SELECT * FROM memory_index_jobs ORDER BY id")
				}
				got := ToolSupersedeMemory(ctx, d, map[string]any{"old_memory_id": oldID, "new_value": "replacement", "reason": "native outbox regression"})
				wantFailure := mode == "second-enqueue-failure" || mode == "foreign-pending-job" || mode == "validity-retired"
				if got.IsError != wantFailure {
					t.Fatalf("result=%+v want failure=%t", got, wantFailure)
				}
				if mode == "validity-retired" {
					if got.Content[0].Text != "Error: active memory not found" || retiredBefore != snapshot("SELECT * FROM memories ORDER BY id") || outboxBefore != snapshot("SELECT * FROM memory_index_jobs ORDER BY id") {
						t.Fatal("validity-only retired row changed SQL or outbox state")
					}
				}
				if wantFailure {
					var key, value, child string
					if err := d.DB().QueryRow(d.Q("SELECT key,value,superseded_by FROM memories WHERE id=$1"), oldID).Scan(&key, &value, &child); err != nil {
						t.Fatal(err)
					}
					var count, jobs int
					if err := d.DB().QueryRow("SELECT COUNT(*) FROM memories").Scan(&count); err != nil {
						t.Fatal(err)
					}
					if err := d.DB().QueryRow("SELECT COUNT(*) FROM memory_index_jobs").Scan(&jobs); err != nil {
						t.Fatal(err)
					}
					wantJobs := 0
					if mode == "foreign-pending-job" {
						wantJobs = 1
					}
					if key != "atomic-supersede" || value != "old" || child != "" || count != 1 || jobs != wantJobs {
						t.Fatalf("rollback key=%q value=%q child=%q rows=%d jobs=%d", key, value, child, count, jobs)
					}
				} else {
					var result struct {
						NewID string `json:"new_memory_id"`
					}
					if err := json.Unmarshal([]byte(got.Content[0].Text), &result); err != nil || result.NewID == "" {
						t.Fatalf("success result=%+v error=%v", got, err)
					}
					if mode == "success" {
						jobs, err := base.memoryIndexOutbox.List(ctx, "owner-a", 10)
						if err != nil || len(jobs) != 2 {
							t.Fatalf("jobs=%+v error=%v", jobs, err)
						}
						for _, job := range jobs {
							if job.ID == "" || job.OwnerID != "owner-a" || job.Collection != "levara" || job.Status != memoryindex.Pending {
								t.Fatalf("incorrect scope/status: %+v", job)
							}
							if job.Operation == "delete_vector" {
								if job.MemoryID != oldID || job.Digest != "delete:"+oldID || job.Model != "" {
									t.Fatalf("retirement: %+v", job)
								}
							} else if job.Operation != "upsert_vector" || job.MemoryID != result.NewID || job.Digest != fmt.Sprintf("%x", sha256.Sum256([]byte("atomic-supersede\x00replacement"))) || job.Model != base.EmbedModel() {
								t.Fatalf("successor: %+v", job)
							}
						}
					}
				}
				base.insertedMu.Lock()
				defer base.insertedMu.Unlock()
				wantInline := 0
				if mode == "legacy-inline" {
					wantInline = 1
				}
				if len(base.insertedRows) != wantInline || len(base.deletedRows) != wantInline {
					t.Fatalf("inline side effects inserts=%d deletes=%d want=%d", len(base.insertedRows), len(base.deletedRows), wantInline)
				}
			})
		}
	}
}
