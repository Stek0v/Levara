package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/memoryindex"
)

func TestSyncMemoryCanonicalIndexRejectsLatePublication(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := syncConvergenceDB(t, dialect)
			cfg, cleanup := newWorkspaceTestConfig(t)
			defer cleanup()
			cfg.DB = db
			outbox, err := memoryindex.NewStore(db)
			if err != nil {
				t.Fatal(err)
			}
			cfg.MemoryIndexOutbox = outbox
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			old := syncMemory{ID: "local", Key: "key", Value: "old", Type: "project", OwnerID: "owner", CollectionName: "test", CreatedAt: "2026-10-01T00:00:00Z", UpdatedAt: "2026-10-01T00:00:00Z"}
			if counts, _, err := importSyncMemories(ctx, cfg, []syncMemory{old}); err != nil || counts["imported"] != 1 {
				t.Fatalf("seed %v %v", counts, err)
			}
			stale, ok, err := outbox.Claim(ctx)
			if err != nil || !ok || stale.MemoryID != "local" {
				t.Fatalf("claim %v %v %v", stale, ok, err)
			}
			entered, resume := make(chan struct{}), make(chan struct{})
			var once, release sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				once.Do(func() { close(entered) })
				<-resume
				io.WriteString(w, `{"data":[{"index":0,"embedding":[1,0]}]}`)
			}))
			defer server.Close()
			defer release.Do(func() { close(resume) })
			cfg.EmbedClient = embed.NewClient(server.URL, "test", 1, 1)
			done := make(chan error, 1)
			go func() { done <- executeMemoryIndexJob(ctx, cfg, stale) }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("old embedding did not enter")
			}
			newer := old
			newer.ID = "remote"
			newer.Value = "new"
			newer.UpdatedAt = "2026-10-02T00:00:00Z"
			counts, accepted, err := importSyncMemories(ctx, cfg, []syncMemory{newer})
			if err != nil || counts["imported"] != 1 || len(accepted) != 1 || accepted[0].ID != "local" {
				t.Fatalf("replacement %v %v %v", counts, accepted, err)
			}
			release.Do(func() { close(resume) })
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if cfg.Collections.HasRecord("_memories_test", "local") || cfg.Collections.HasRecord("_memories_test", "remote") {
				t.Fatal("stale or remote-ID publication escaped guard")
			}
			if err := outbox.Finish(ctx, stale, nil, 5, time.Second); err != nil {
				t.Fatal(err)
			}
			current, ok, err := outbox.Claim(ctx)
			if err != nil || !ok || current.MemoryID != "local" {
				t.Fatalf("current claim %v %v %v", current, ok, err)
			}
			if err := executeMemoryIndexJob(ctx, cfg, current); err != nil {
				t.Fatal(err)
			}
			ids, _, metas, err := cfg.Collections.AllRecords("_memories_test")
			if err != nil || len(ids) != 1 || ids[0] != "local" {
				t.Fatalf("physical identities %v %v", ids, err)
			}
			var meta map[string]any
			if err := json.Unmarshal(metas[0], &meta); err != nil || meta["memory_id"] != "local" || meta["value"] != "new" || meta["owner_id"] != "owner" || meta["collection"] != "test" {
				t.Fatalf("native metadata %v %v", meta, err)
			}
		})
	}
}

func TestSyncMemoryTypeOnlyIndexRace(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := syncConvergenceDB(t, dialect)
			cfg, cleanup := newWorkspaceTestConfig(t)
			defer cleanup()
			cfg.DB = db
			outbox, err := memoryindex.NewStore(db)
			if err != nil {
				t.Fatal(err)
			}
			cfg.MemoryIndexOutbox = outbox
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			old := syncMemory{ID: "local", Key: "key", Value: "unchanged", Type: "project", OwnerID: "owner", CollectionName: "test", CreatedAt: "2026-10-01T00:00:00Z", UpdatedAt: "2026-10-01T00:00:00Z"}
			if counts, _, err := importSyncMemories(ctx, cfg, []syncMemory{old}); err != nil || counts["imported"] != 1 {
				t.Fatalf("seed %v %v", counts, err)
			}
			job, ok, err := outbox.Claim(ctx)
			if err != nil || !ok {
				t.Fatalf("claim %v %v", ok, err)
			}
			entered, resume := make(chan struct{}), make(chan struct{})
			var release sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				<-resume
				io.WriteString(w, `{"data":[{"index":0,"embedding":[1,0]}]}`)
			}))
			defer server.Close()
			defer release.Do(func() { close(resume) })
			cfg.EmbedClient = embed.NewClient(server.URL, "test", 1, 1)
			done := make(chan error, 1)
			go func() { done <- executeMemoryIndexJob(ctx, cfg, job) }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("provider not entered")
			}
			newer := old
			newer.ID = "remote"
			newer.Type = "preference"
			newer.UpdatedAt = "2026-10-02T00:00:00Z"
			if counts, _, err := importSyncMemories(ctx, cfg, []syncMemory{newer}); err != nil || counts["imported"] != 1 {
				t.Fatalf("replacement %v %v", counts, err)
			}
			release.Do(func() { close(resume) })
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if err := outbox.Finish(ctx, job, nil, 5, time.Second); err != nil {
				t.Fatal(err)
			}
			ids, _, metas, err := cfg.Collections.AllRecords("_memories_test")
			if err != nil || len(ids) != 1 || ids[0] != "local" {
				t.Fatalf("current metadata publication lost: %v %v", ids, err)
			}
			var meta map[string]any
			if err := json.Unmarshal(metas[0], &meta); err != nil || meta["type"] != "preference" {
				t.Fatalf("stale vector type %v %v", meta, err)
			}
			if _, ok, err := outbox.Claim(ctx); err != nil || ok {
				t.Fatalf("completed publication leaves work %v %v", ok, err)
			}
		})
	}
}
