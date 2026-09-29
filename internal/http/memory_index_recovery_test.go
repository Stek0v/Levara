package http

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/memoryindex"
)

func TestMemoryIndexBreakerDefersWithoutLosingAttemptOrCause(t *testing.T) {
	db := newMCPMemoryBehaviorDB(t)
	cfg, cleanup := newWorkspaceTestConfig(t)
	defer cleanup()
	cfg.DB = db
	var err error
	cfg.MemoryIndexOutbox, err = memoryindex.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES('recover','key','value','owner','test')`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = cfg.MemoryIndexOutbox.Enqueue(ctx, memoryindex.Job{ID: "job", MemoryID: "recover", Operation: "upsert_vector", OwnerID: "owner", Collection: "test", Digest: fmt.Sprintf("%x", sha256.Sum256([]byte("key\x00value")))})
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	var healthy atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !healthy.Load() {
			http.Error(w, "provider temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, `{"data":[{"index":0,"embedding":[1,0]}]}`)
	}))
	defer server.Close()
	cfg.EmbedClient = embed.NewClient(server.URL, "test", 1, 1)
	due := func() {
		t.Helper()
		if _, err := db.Exec(`UPDATE memory_index_jobs SET next_run_at='' WHERE id='job'`); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		due()
		if !runMemoryIndexJob(ctx, cfg) {
			t.Fatal("job was not run")
		}
	}
	jobs, err := cfg.MemoryIndexOutbox.List(ctx, "owner", 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%v err=%v", jobs, err)
	}
	original := jobs[0].LastError
	if !strings.Contains(original, "503") || hits.Load() != 3 {
		t.Fatalf("original provider failure absent: jobs=%v hits=%d", jobs, hits.Load())
	}
	due()
	if !runMemoryIndexJob(ctx, cfg) {
		t.Fatal("breaker rejection was not handled")
	}
	jobs, err = cfg.MemoryIndexOutbox.List(ctx, "owner", 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%v err=%v", jobs, err)
	}
	job := jobs[0]
	next, parseErr := time.Parse(time.RFC3339Nano, job.NextRunAt)
	if job.Attempts != 3 || job.Status != memoryindex.Pending || job.LastError != original || parseErr != nil || time.Until(next) < 25*time.Second || hits.Load() != 3 {
		t.Fatalf("breaker must defer without HTTP, spending attempts or replacing cause: job=%+v hits=%d", job, hits.Load())
	}
	if runMemoryIndexJob(ctx, cfg) || cfg.MemoryIndexOutbox.WaitReady(ctx, "test", "owner", 0) {
		t.Fatal("deferred job must remain pending and unclaimable until cooldown")
	}
	// Advance queue time and reset the client as on recovery/restart. Breaker's
	// actual cooldown admission is checked separately in pkg/embed.
	healthy.Store(true)
	cfg.EmbedClient = embed.NewClient(server.URL, "test", 1, 1)
	due()
	if !runMemoryIndexJob(ctx, cfg) {
		t.Fatal("recovered job was not run")
	}
	jobs, err = cfg.MemoryIndexOutbox.List(ctx, "owner", 10)
	if err != nil || len(jobs) != 1 || jobs[0].Status != memoryindex.Completed || jobs[0].Attempts != 4 || jobs[0].LastError != "" || jobs[0].NextRunAt != "" || hits.Load() != 4 || !cfg.Collections.HasRecord("_memories_test", "recover") {
		t.Fatalf("recovery must complete with original attempts retained: jobs=%v hits=%d err=%v", jobs, hits.Load(), err)
	}
}
