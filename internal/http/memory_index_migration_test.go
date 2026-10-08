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

	"github.com/stek0v/levara/pkg/embcontract"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/memoryindex"
)

func memoryIndexMigrationFixture(t *testing.T, f *documentHTTPFixture, sqlOwner string) (APIConfig, <-chan struct{}, func()) {
	t.Helper()
	cfg, cleanup := newWorkspaceTestConfig(t)
	t.Cleanup(cleanup)
	cfg.DB = f.db
	cfg.StoragePath = t.TempDir()
	cfg.EmbedClient = embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, 1, 1)
	entered, resume := make(chan struct{}), make(chan struct{})
	var once, release sync.Once
	unblock := func() { release.Do(func() { close(resume) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		select {
		case <-resume:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[1,0]}]}`)
	}))
	t.Cleanup(func() { unblock(); srv.Close() })
	contract := embcontract.Contract{Encoder: "shadow-model", Dim: 2, Metric: "cosine"}
	if err := ensureMigrationTargetCollection(cfg, "shadow", 2, "cosine", contract); err != nil {
		t.Fatal(err)
	}
	persistEmbeddingDualWriteRule(cfg, embeddingDualWriteRule{SourceCollection: "_memories_main", TargetCollection: "shadow", TargetEndpoint: srv.URL, TargetModel: "shadow-model", TargetContract: contract, Enabled: true})
	installEmbeddingMigrationDualWriteHook(cfg)
	f.exec(`INSERT INTO memories(id,key,value,type,owner_id,collection_name) VALUES('dual','key','value','user',$1,'main'),('unrelated','unrelated','before','user','peer','main')`, sqlOwner)
	return cfg, entered, unblock
}

func TestMemoryIndexMigrationEmbeddingDoesNotHoldSQLFence(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		writerDB := independentMemoryIndexDB(t, f)
		cfg, entered, unblock := memoryIndexMigrationFixture(t, f, "peer")
		defer unblock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done := make(chan error, 1)
		job := memoryindex.Job{MemoryID: "dual", Operation: "upsert_vector", OwnerID: "peer", Collection: "main", Digest: fmt.Sprintf("%x", sha256.Sum256([]byte("key\x00value")))}
		drained := make(chan struct{})
		t.Cleanup(func() {
			unblock()
			select {
			case <-drained:
			case <-time.After(5 * time.Second):
				t.Error("index job did not drain before fixture cleanup")
			}
		})
		go func() { defer close(drained); done <- executeMemoryIndexJob(ctx, cfg, job) }()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("migration embedding did not start")
		}
		writerCtx, writerCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer writerCancel()
		if _, err := writerDB.ExecContext(writerCtx, "UPDATE memories SET value='after' WHERE id='unrelated'"); err != nil {
			t.Fatalf("network hook held memory SQL protection: %v", err)
		}
		unblock()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("index/hook did not return")
		}
		if !cfg.Collections.HasRecord("_memories_main", "dual") || !cfg.Collections.HasRecord("shadow", "dual") {
			t.Fatal("valid native publication/dual write was lost")
		}
	})
}

func TestMemoryIndexMigrationRejectsSourceRetiredDuringEmbed(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		cfg, entered, unblock := memoryIndexMigrationFixture(t, f, "peer")
		defer unblock()
		done := make(chan error, 1)
		drained := make(chan struct{})
		t.Cleanup(func() {
			unblock()
			select {
			case <-drained:
			case <-time.After(5 * time.Second):
				t.Error("migration hook did not drain before fixture cleanup")
			}
		})
		go func() {
			defer close(drained)
			done <- cfg.Collections.Insert("_memories_main", "dual", []float32{1, 0}, map[string]any{"memory_id": "dual", "key": "key", "value": "value", "type": "user", "collection": "main"})
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("migration embedding did not start")
		}
		f.exec(`UPDATE memories SET superseded_by='abstract' WHERE id='dual'`)
		unblock()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("migration hook did not return")
		}
		if cfg.Collections.HasRecord("shadow", "dual") {
			t.Fatal("late migration embedding published retired memory into shadow")
		}
	})
}

func TestMemoryIndexMigrationOwnerPresence(t *testing.T) {
	for _, tc := range []struct {
		name          string
		sqlOwner      string
		metadataOwner any
		omitted       bool
		want          bool
	}{
		{"explicit_shared_against_private", "peer", "", false, false},
		{"explicit_null", "peer", nil, false, false},
		{"non_string", "peer", 42, false, false},
		{"legacy_absent", "peer", nil, true, true},
		{"matching_private", "peer", "peer", false, true},
		{"matching_shared", "", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
				cfg, entered, unblock := memoryIndexMigrationFixture(t, f, tc.sqlOwner)
				unblock()
				meta := map[string]any{"memory_id": "dual", "key": "key", "value": "value", "type": "user", "collection": "main"}
				if !tc.omitted {
					meta["owner_id"] = tc.metadataOwner
				}
				if err := cfg.Collections.Insert("_memories_main", "dual", []float32{1, 0}, meta); err != nil {
					t.Fatal(err)
				}
				called := false
				select {
				case <-entered:
					called = true
				default:
				}
				if called != tc.want || cfg.Collections.HasRecord("shadow", "dual") != tc.want {
					t.Fatalf("owner validation: provider called=%v, shadow present=%v, want=%v", called, cfg.Collections.HasRecord("shadow", "dual"), tc.want)
				}
			})
		})
	}
}
