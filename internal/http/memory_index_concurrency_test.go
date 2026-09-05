package http

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/memoryindex"
)

func TestMemoryIndexDoesNotResurrectRetiredMemory(t *testing.T) {
	for _, change := range []string{"supersede", "delete"} {
		t.Run(change, func(t *testing.T) {
			db := newMCPMemoryBehaviorDB(t)
			cfg, cleanup := newWorkspaceTestConfig(t)
			defer cleanup()
			cfg.DB = db
			if _, err := db.Exec(`INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES('old','key','value','owner','test')`); err != nil {
				t.Fatal(err)
			}
			if err := cfg.Collections.Insert("_memories_test", "old", []float32{1, 0}, nil); err != nil {
				t.Fatal(err)
			}
			entered, resume := make(chan struct{}), make(chan struct{})
			var once sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				once.Do(func() { close(entered) })
				<-resume
				io.WriteString(w, `{"data":[{"index":0,"embedding":[1,0]}]}`)
			}))
			defer server.Close()
			var release sync.Once
			defer release.Do(func() { close(resume) })
			cfg.EmbedClient = embed.NewClient(server.URL, "test", 1, 1)
			job := memoryindex.Job{MemoryID: "old", Operation: "upsert_vector", OwnerID: "owner", Collection: "test", Digest: fmt.Sprintf("%x", sha256.Sum256([]byte("key\x00value")))}
			done := make(chan error, 1)
			go func() { done <- executeMemoryIndexJob(context.Background(), cfg, job) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("embedding did not start")
			}
			query := `UPDATE memories SET key='key#superseded:old',superseded_by='new' WHERE id='old'`
			if change == "delete" {
				query = `DELETE FROM memories WHERE id='old'`
			}
			if _, err := db.Exec(query); err != nil {
				t.Fatal(err)
			}
			if err := executeMemoryIndexJob(context.Background(), cfg, memoryindex.Job{MemoryID: "old", Operation: "delete_vector", Collection: "test"}); err != nil {
				t.Fatal(err)
			}
			release.Do(func() { close(resume) })
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if cfg.Collections.HasRecord("_memories_test", "old") {
				t.Fatal("late embedding resurrected retired memory vector")
			}
		})
	}
}
