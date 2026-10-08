package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/memoryindex"
)

func syncMemoryConflictConfig(t *testing.T, dialect string) APIConfig {
	t.Helper()
	db := syncConvergenceDB(t, dialect)
	outbox, err := memoryindex.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return APIConfig{DB: db, MemoryIndexOutbox: outbox, EmbedModel: "fixture-model"}
}
func syncMemoryConflictPayload(id, value string) syncMemory {
	return syncMemory{ID: id, Key: "shared-key", Value: value, Type: "project", OwnerID: "owner", CollectionName: "collection", Room: "memory", Hall: "fact", CreatedAt: "2020-01-01T00:00:00Z", UpdatedAt: "2026-09-05T00:00:00.1231Z"}
}
func syncMemoryConflictStored(t *testing.T, ctx context.Context, cfg APIConfig) []syncMemory {
	t.Helper()
	rows, err := exportSyncMemories(ctx, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
func syncMemoryConflictJobs(t *testing.T, ctx context.Context, cfg APIConfig) []memoryindex.Job {
	t.Helper()
	jobs, err := cfg.MemoryIndexOutbox.List(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	return jobs
}

func TestSyncMemoryCanonicalConflictFixedPoint(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			left, right := syncMemoryConflictConfig(t, dialect), syncMemoryConflictConfig(t, dialect)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			low := syncMemoryConflictPayload("local-left", "a")
			high := syncMemoryConflictPayload("remote-high", "z")
			// Offset and sub-microsecond spelling differ but represent one canonical
			// native precision timestamp. Incoming creation time never replaces local.
			high.UpdatedAt = "2026-09-05T03:00:00.123100499+03:00"
			high.CreatedAt = "2024-01-01T00:00:00Z"
			low.UpdatedAt = "2026-09-05T00:00:00.123100500Z"
			counts, accepted, err := importSyncMemories(ctx, left, []syncMemory{low, high})
			if err != nil || counts["imported"] != 2 || len(accepted) != 2 {
				t.Fatalf("left=%v accepted=%v err=%v", counts, accepted, err)
			}
			if accepted[1].ID != "local-left" || accepted[1].CreatedAt != low.CreatedAt {
				t.Fatalf("remote identity replaced canonical target: %+v", accepted[1])
			}
			high.ID = "local-right"
			counts, _, err = importSyncMemories(ctx, right, []syncMemory{high, low})
			if err != nil || counts["imported"] != 1 || counts["skipped"] != 1 {
				t.Fatalf("right=%v %v", counts, err)
			}
			for round := 0; round < 3; round++ {
				l, r := syncMemoryConflictStored(t, ctx, left), syncMemoryConflictStored(t, ctx, right)
				if len(l) != 1 || len(r) != 1 || l[0].Value != "z" || r[0].Value != "z" || l[0].UpdatedAt != "2026-09-05T00:00:00.1231Z" || r[0].UpdatedAt != l[0].UpdatedAt {
					t.Fatalf("memory fixed point: %#v %#v", l, r)
				}
				if l[0].ID != "local-left" || r[0].ID != "local-right" || l[0].CreatedAt != low.CreatedAt || r[0].CreatedAt != high.CreatedAt {
					t.Fatalf("immutable local IDs/creation: %#v %#v", l, r)
				}
				for index, cfg := range []APIConfig{left, right} {
					incoming := r
					if index == 1 {
						incoming = l
					}
					before := syncMemoryConflictJobs(t, ctx, cfg)
					counts, accepted, err = importSyncMemories(ctx, cfg, incoming)
					if err != nil || counts["imported"] != 0 || counts["skipped"] != 1 || len(accepted) != 0 {
						t.Fatalf("repeat=%v %v %v", counts, accepted, err)
					}
					if after := syncMemoryConflictJobs(t, ctx, cfg); !reflect.DeepEqual(before, after) {
						t.Fatal("repeat changed outbox")
					}
					rows := syncMemoryConflictStored(t, ctx, cfg)
					canonical := rows[0]
					var winning bool
					for _, job := range before {
						if job.MemoryID != canonical.ID || job.OwnerID != canonical.OwnerID || job.Collection != canonical.CollectionName {
							t.Fatalf("noncanonical outbox identity: %+v", job)
						}
						if job.Digest == fmt.Sprintf("%x", sha256.Sum256([]byte(canonical.Key+"\x00"+canonical.Value))) {
							winning = true
						}
					}
					if !winning {
						t.Fatal("winner lacks durable index intent while embedding endpoint is disabled")
					}
					if cfg.DB.Stats().InUse != 0 {
						t.Fatal("fixed point leaked pool-one connection")
					}
				}
			}
			// Timestamp ordering uses instants before canonical content ranking.
			newer := low
			newer.ID = "newer-remote"
			newer.Value = "a newer version"
			newer.UpdatedAt = "2026-09-05T00:00:00.123101Z"
			counts, accepted, err = importSyncMemories(ctx, left, []syncMemory{newer})
			if err != nil || counts["imported"] != 1 || accepted[0].ID != "local-left" {
				t.Fatalf("newer instant lost=%v %v %v", counts, accepted, err)
			}
		})
	}
}

func TestSyncMemoryForeignPhysicalIDFailsWithoutEffects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			cfg := syncMemoryConflictConfig(t, dialect)
			ctx := context.Background()
			target := syncMemoryConflictPayload("canonical-target", "target")
			foreign := syncMemoryConflictPayload("foreign-id", "foreign")
			foreign.Key = "foreign-key"
			foreign.OwnerID = "foreign-owner"
			foreign.CollectionName = "foreign-collection"
			if _, _, err := importSyncMemories(ctx, cfg, []syncMemory{target, foreign}); err != nil {
				t.Fatal(err)
			}
			before := syncMemoryConflictStored(t, ctx, cfg)
			jobs := syncMemoryConflictJobs(t, ctx, cfg)
			incoming := target
			incoming.ID = foreign.ID
			incoming.Value = "z stolen"
			incoming.UpdatedAt = "2026-09-06T00:00:00Z"
			counts, accepted, err := importSyncMemories(ctx, cfg, []syncMemory{incoming})
			if err == nil || counts["failed"] != 1 || counts["imported"] != 0 || len(accepted) != 0 {
				t.Fatalf("foreign ID collision=%v %v %v", counts, accepted, err)
			}
			if after := syncMemoryConflictStored(t, ctx, cfg); !reflect.DeepEqual(before, after) {
				t.Fatal("physical ID collision changed SQL")
			}
			if after := syncMemoryConflictJobs(t, ctx, cfg); !reflect.DeepEqual(jobs, after) {
				t.Fatal("physical ID collision queued index effect")
			}
			if cfg.DB.Stats().InUse != 0 {
				t.Fatal("collision leaked transaction")
			}
		})
	}
}

func TestSyncMemoryProvenanceTracksMatchingContent(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			cfg := syncMemoryConflictConfig(t, dialect)
			ctx := context.Background()
			initial := syncMemoryConflictPayload("canonical", "content")
			if _, _, err := importSyncMemories(ctx, cfg, []syncMemory{initial}); err != nil {
				t.Fatal(err)
			}
			provenance := func() (string, string, string) {
				t.Helper()
				var task, receipts, status string
				if err := cfg.DB.QueryRowContext(ctx, Q(`SELECT source_task_id,source_receipt_ids,verification_status FROM memories WHERE id=$1`), "canonical").Scan(&task, &receipts, &status); err != nil {
					t.Fatal(err)
				}
				return task, receipts, status
			}
			if task, receipts, status := provenance(); task != "" || receipts != "[]" || status != "unverified" {
				t.Fatalf("new import proof=%q %q %q", task, receipts, status)
			}
			// Fixture proof metadata tests inheritance; it does not claim a real Task
			// verification event or create/promote Task Runtime records.
			query, args := QArgs(`UPDATE memories SET source_task_id=$2,source_receipt_ids=$3,verification_status=$4 WHERE id=$1`, "canonical", "fixture-task", `["fixture-receipt"]`, "verified")
			result, err := cfg.DB.ExecContext(ctx, query, args...)
			if err != nil {
				t.Fatal(err)
			}
			if changed, err := result.RowsAffected(); err != nil || changed != 1 {
				t.Fatalf("proof fixture did not update canonical row: %d %v", changed, err)
			}
			if task, receipts, status := provenance(); task != "fixture-task" || receipts != `["fixture-receipt"]` || status != "verified" {
				t.Fatalf("proof fixture not seeded: %q %q %q", task, receipts, status)
			}
			metadata := initial
			metadata.ID = "metadata-remote"
			metadata.Room = "deploy"
			metadata.Hall = "decision"
			metadata.IsPinned = true
			metadata.PinPriority = 8
			metadata.UpdatedAt = "2026-09-06T00:00:00Z"
			counts, accepted, err := importSyncMemories(ctx, cfg, []syncMemory{metadata})
			if err != nil || counts["imported"] != 1 || accepted[0].ID != "canonical" {
				t.Fatalf("metadata update=%v %v %v", counts, accepted, err)
			}
			if task, receipts, status := provenance(); task != "fixture-task" || receipts != `["fixture-receipt"]` || status != "verified" {
				t.Fatalf("matching-content proof lost=%q %q %q", task, receipts, status)
			}
			replacement := metadata
			replacement.ID = "content-remote"
			replacement.Value = "changed content"
			replacement.UpdatedAt = "2026-09-07T00:00:00Z"
			counts, accepted, err = importSyncMemories(ctx, cfg, []syncMemory{replacement})
			if err != nil || counts["imported"] != 1 || accepted[0].ID != "canonical" {
				t.Fatalf("content update=%v %v %v", counts, accepted, err)
			}
			if task, receipts, status := provenance(); task != "" || receipts != "[]" || status != "unverified" {
				t.Fatalf("remote content inherited local proof=%q %q %q", task, receipts, status)
			}
			if rows := syncMemoryConflictStored(t, ctx, cfg); len(rows) != 1 || rows[0].CreatedAt != initial.CreatedAt {
				t.Fatalf("creation changed=%+v", rows)
			}
			if cfg.DB.Stats().InUse != 0 {
				t.Fatal("provenance update leaked pool-one connection")
			}
		})
	}
}

func TestSyncMemoryOutboxFailureRollsBackSQL(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, operation := range []string{"insert", "replace"} {
				t.Run(operation, func(t *testing.T) {
					cfg := syncMemoryConflictConfig(t, dialect)
					ctx := context.Background()
					incoming := syncMemoryConflictPayload("canonical", "original")
					if operation == "replace" {
						if _, _, err := importSyncMemories(ctx, cfg, []syncMemory{incoming}); err != nil {
							t.Fatal(err)
						}
						incoming.ID = "remote"
						incoming.Value = "replacement"
						incoming.UpdatedAt = "2026-09-06T00:00:00Z"
					}
					before := syncMemoryConflictStored(t, ctx, cfg)
					if _, err := cfg.DB.ExecContext(ctx, "DROP TABLE memory_index_jobs"); err != nil {
						t.Fatal(err)
					}
					counts, accepted, err := importSyncMemories(ctx, cfg, []syncMemory{incoming})
					if err == nil || counts["failed"] != 1 || counts["imported"] != 0 || len(accepted) != 0 {
						t.Fatalf("outbox failure=%v %v %v", counts, accepted, err)
					}
					if after := syncMemoryConflictStored(t, ctx, cfg); !reflect.DeepEqual(before, after) {
						t.Fatal("outbox failure committed authoritative SQL")
					}
					if cfg.DB.Stats().InUse != 0 {
						t.Fatal("outbox failure leaked transaction")
					}
				})
			}
		})
	}
}

func TestSyncMemoryRetiredTargetCannotBeReplaced(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, retirement := range []string{"superseded", "valid_until"} {
				t.Run(retirement, func(t *testing.T) {
					cfg := syncMemoryConflictConfig(t, dialect)
					ctx := context.Background()
					original := syncMemoryConflictPayload("canonical", "original")
					if _, _, err := importSyncMemories(ctx, cfg, []syncMemory{original}); err != nil {
						t.Fatal(err)
					}
					update := `UPDATE memories SET superseded_by='successor' WHERE id='canonical'`
					if retirement == "valid_until" {
						update = `UPDATE memories SET valid_until='2099-01-01T00:00:00Z' WHERE id='canonical'`
					}
					if _, err := cfg.DB.ExecContext(ctx, update); err != nil {
						t.Fatal(err)
					}
					before := syncMemoryConflictStored(t, ctx, cfg)
					jobs := syncMemoryConflictJobs(t, ctx, cfg)
					incoming := original
					incoming.ID = "remote"
					incoming.Value = "z new active content"
					incoming.UpdatedAt = "2026-09-06T00:00:00Z"
					counts, accepted, err := importSyncMemories(ctx, cfg, []syncMemory{incoming})
					if err == nil || counts["failed"] != 1 || counts["imported"] != 0 || len(accepted) != 0 {
						t.Fatalf("retired replacement=%v %v %v", counts, accepted, err)
					}
					if after := syncMemoryConflictStored(t, ctx, cfg); !reflect.DeepEqual(before, after) {
						t.Fatal("retired SQL row overwritten")
					}
					var superseded, until string
					if err := cfg.DB.QueryRowContext(ctx, `SELECT superseded_by,COALESCE(CAST(valid_until AS TEXT),'') FROM memories WHERE id='canonical'`).Scan(&superseded, &until); err != nil {
						t.Fatal(err)
					}
					if retirement == "superseded" && superseded != "successor" || retirement == "valid_until" && until == "" {
						t.Fatal("retirement marker cleared")
					}
					if after := syncMemoryConflictJobs(t, ctx, cfg); !reflect.DeepEqual(jobs, after) {
						t.Fatal("retired replacement queued index job")
					}
					if cfg.DB.Stats().InUse != 0 {
						t.Fatal("retirement denial leaked connection")
					}
				})
			}
		})
	}
}

func TestSyncMemoryAbsentIdentityRaceUsesCanonicalOutboxID(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			cfg := syncMemoryConflictConfig(t, dialect)
			cfg.DB.SetMaxOpenConns(2)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			start := make(chan struct{})
			failures := make(chan error, 8)
			var workers sync.WaitGroup
			for i := 0; i < 8; i++ {
				workers.Add(1)
				go func(i int) {
					defer workers.Done()
					<-start
					payload := syncMemoryConflictPayload(fmt.Sprintf("remote-%d", i), fmt.Sprintf("%02d", i))
					_, _, err := importSyncMemories(ctx, cfg, []syncMemory{payload})
					failures <- err
				}(i)
			}
			close(start)
			workers.Wait()
			close(failures)
			for err := range failures {
				if err != nil {
					t.Fatal(err)
				}
			}
			stored := syncMemoryConflictStored(t, ctx, cfg)
			if len(stored) != 1 || stored[0].Value != "07" {
				t.Fatalf("race winner=%+v", stored)
			}
			jobs := syncMemoryConflictJobs(t, ctx, cfg)
			if len(jobs) == 0 {
				t.Fatal("race produced no durable jobs")
			}
			for _, job := range jobs {
				if job.MemoryID != stored[0].ID || job.OwnerID != "owner" || job.Collection != "collection" {
					t.Fatalf("race job identity=%+v canonical=%s", job, stored[0].ID)
				}
			}
			if cfg.DB.Stats().InUse != 0 {
				t.Fatal("absent identity race leaked transaction")
			}
		})
	}
}

func TestSyncMemoryQueuedAckAndMissingOutbox(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			cfg := syncMemoryConflictConfig(t, dialect)
			payload, _ := json.Marshal(syncMemoryFixtureBatch([]syncMemory{syncMemoryConflictPayload("queued", "content")}))
			app := fiber.New()
			app.Post("/import", syncImportMemoriesHandler(cfg))
			request := httptest.NewRequest("POST", "/import", bytes.NewReader(payload))
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
			if response.StatusCode != 200 || ack["imported"] != float64(1) || ack["embedding"] != true {
				t.Fatalf("queued ACK=%d %#v", response.StatusCode, ack)
			}
			if len(syncMemoryConflictJobs(t, context.Background(), cfg)) != 1 {
				t.Fatal("ACK lacks durable job")
			}
			enabled, cleanup := newWorkspaceTestConfig(t)
			defer cleanup()
			enabled.DB = cfg.DB
			enabled.MemoryIndexOutbox = nil
			incoming := syncMemoryConflictPayload("without-outbox", "different")
			incoming.Key = "different"
			counts, accepted, err := importSyncMemories(context.Background(), enabled, []syncMemory{incoming})
			if err == nil || counts["failed"] != 1 || counts["imported"] != 0 || len(accepted) != 0 {
				t.Fatalf("missing outbox=%v %v %v", counts, accepted, err)
			}
			if rows := syncMemoryConflictStored(t, context.Background(), cfg); len(rows) != 1 {
				t.Fatalf("unqueueable row committed=%+v", rows)
			}
			if cfg.DB.Stats().InUse != 0 {
				t.Fatal("queued/missing-outbox test leaked pool-one connection")
			}
		})
	}
}
