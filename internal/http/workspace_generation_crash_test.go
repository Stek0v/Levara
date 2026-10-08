package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stek0v/levara/internal/store"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/vectorstore"
	"github.com/stek0v/levara/pkg/workspace"
)

type workspaceGenerationCrashInput struct {
	Root, Vectors, Mode, Phase, Attempt, Collection, PublishedID string
}

// This process persists the real intermediate state and exits without running
// the HTTP publisher's compensation. It models process death, not a Go panic.
func TestWorkspaceGenerationCrashChild(t *testing.T) {
	raw := os.Getenv("LEVARA_GENERATION_CRASH")
	if raw == "" {
		return
	}
	var input workspaceGenerationCrashInput
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatal(err)
	}
	cfg := APIConfig{WorkspacePath: input.Root}
	release, err := workspace.LockProject(context.Background(), input.Root, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if input.Mode == "history" {
		live := workspaceProjectRoot(cfg, "alpha", "main")
		parent, err := openWorkspaceDirectory(cfg, filepath.Dir(live), true)
		if err != nil {
			t.Fatal(err)
		}
		defer parent.Close()
		stage, backup := ".restore-"+uuid.NewString(), ".backup-"+uuid.NewString()
		if err := parent.Mkdir(stage, 0700); err != nil {
			t.Fatal(err)
		}
		staged, err := workspace.OpenDir(parent, stage, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"a.md", "nested/b.md"} {
			if err := workspace.WriteFile(context.Background(), staged, name, []byte("new "+name), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := staged.Close(); err != nil {
			t.Fatal(err)
		}
		journal := workspaceRestoreJournal{ProjectID: "alpha", Branch: "main", Stage: stage, Live: filepath.Base(live), Backup: backup, HadLive: true}
		data, err := json.Marshal(journal)
		if err != nil {
			t.Fatal(err)
		}
		if err := workspace.WriteFile(context.Background(), parent, ".restore-state-main.json", data, 0600); err != nil {
			t.Fatal(err)
		}
		if input.Phase != "journal" {
			if err := parent.Rename(journal.Live, backup); err != nil {
				t.Fatal(err)
			}
		}
		if input.Phase == "published" {
			if err := parent.Rename(stage, journal.Live); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	if input.Mode != "attempt" {
		t.Fatalf("unknown crash mode %q", input.Mode)
	}
	cm, err := store.NewCollectionManager(2, input.Vectors)
	if err != nil {
		t.Fatal(err)
	}
	defer cm.Close()
	db, err := cm.Get(input.Collection)
	if err != nil {
		t.Fatal(err)
	}
	vector, metadata, found := db.Get(input.PublishedID)
	if !found {
		t.Fatal("published control missing in independent process")
	}
	var source map[string]any
	if err := json.Unmarshal(metadata, &source); err != nil {
		t.Fatal(err)
	}
	source["attempt_id"] = input.Attempt
	// Recovery must protect IDs already bound by the committed manifest, even
	// when the attempt journal survived publication.
	if err := cm.Insert(input.Collection, input.PublishedID, vector, source); err != nil {
		t.Fatal(err)
	}
	source["vector_id"], source["chunk_id"] = "unpublished-attempt", "unpublished-attempt"
	if err := cm.Insert(input.Collection, "unpublished-attempt", []float32{1, 2}, source); err != nil {
		t.Fatal(err)
	}
	source["project_id"] = "other-project"
	if err := cm.Insert(input.Collection, "foreign-control", []float32{1, 2}, source); err != nil {
		t.Fatal(err)
	}
	attempt := workspaceGenerationAttempt{ID: input.Attempt, ProjectID: "alpha", Branch: "main", Generation: "stable", Collection: input.Collection}
	data, err := json.Marshal(attempt)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceFile(context.Background(), cfg, workspaceGenerationAttemptPath(cfg, "alpha", "main"), data); err != nil {
		t.Fatal(err)
	}
}

func workspaceGenerationRunCrashChild(t *testing.T, input workspaceGenerationCrashInput) {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkspaceGenerationCrashChild$")
	cmd.Env = append(os.Environ(), "LEVARA_GENERATION_CRASH="+string(data))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("independent crash-state process: %v\n%s", err, output)
	}
}

func TestWorkspaceGenerationHistoryProcessRestart(t *testing.T) {
	for _, phase := range []string{"journal", "backup", "published"} {
		t.Run(phase, func(t *testing.T) {
			workspaceIntegrityModes(t, func(t *testing.T, cfg APIConfig, actor accesspkg.MetadataActor) {
				for _, name := range []string{"a.md", "nested/b.md"} {
					workspaceIntegrityPut(t, cfg, name, []byte("old "+name))
				}
				workspaceGenerationRunCrashChild(t, workspaceGenerationCrashInput{Root: cfg.WorkspacePath, Mode: "history", Phase: phase})
				// Recovery is entered through an actual authorized read; both SQL dialects
				// retain actor admission and pool-one accounting, as does local mode.
				read, err := readWorkspaceMarkdownAuthorized(context.Background(), cfg, workspaceReadRequest{ProjectID: "alpha", Branch: "main", Path: "a.md"}, actor)
				if err != nil {
					t.Fatal(err)
				}
				prefix := "old "
				if phase == "published" {
					prefix = "new "
				}
				if read.Text != prefix+"a.md" {
					t.Fatalf("recovered read=%q", read.Text)
				}
				expected := map[string]string{"a.md": prefix + "a.md", "nested/b.md": prefix + "nested/b.md"}
				if tree := workspaceIntegrityTree(t, workspaceProjectRoot(cfg, "alpha", "main")); !reflect.DeepEqual(tree, expected) {
					t.Fatalf("incomplete recovered tree: %#v", tree)
				}
				entries, err := os.ReadDir(filepath.Dir(workspaceProjectRoot(cfg, "alpha", "main")))
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".restore-") || strings.HasPrefix(entry.Name(), ".backup-") {
						t.Fatalf("recovery evidence leaked: %s", entry.Name())
					}
				}
				if cfg.DB != nil && cfg.DB.Stats().InUse != 0 {
					t.Fatal("recovery leaked native SQL lease")
				}
			})
		})
	}
}

func TestWorkspaceGenerationAttemptProcessRestart(t *testing.T) {
	workspaceGenerationModes(t, func(t *testing.T, cfg APIConfig, actor accesspkg.MetadataActor, ctx context.Context) {
		workspaceIntegrityPut(t, cfg, "a.md", []byte("Copper published control."))
		req := workspaceGenerationRequest()
		req.Paths = []string{"a.md"}
		if _, err := reindexWorkspaceMarkdownAuthorized(ctx, cfg, req, actor); err != nil {
			t.Fatal(err)
		}
		manifest, before := workspaceGenerationManifest(t, cfg)
		records := workspaceGenerationRecords(t, cfg, manifest)
		if len(records) != 1 {
			t.Fatalf("published count=%d", len(records))
		}
		vectors := filepath.Join(filepath.Dir(cfg.WorkspacePath), "vectors")
		// No two CollectionManagers concurrently write the physical vector store.
		if err := cfg.Collections.Close(); err != nil {
			t.Fatal(err)
		}
		workspaceGenerationRunCrashChild(t, workspaceGenerationCrashInput{Root: cfg.WorkspacePath, Vectors: vectors, Mode: "attempt", Attempt: uuid.NewString(), Collection: req.Collection, PublishedID: records[0].ID})
		cm, err := store.NewCollectionManager(2, vectors)
		if err != nil {
			t.Fatal(err)
		}
		defer cm.Close()
		cfg.Collections = cm
		release, err := beginWorkspaceEffectFence(ctx, cfg, actor, "alpha", workspaceAccessWrite)
		if err != nil {
			t.Fatal(err)
		}
		err = recoverWorkspaceGenerationAttempt(cfg, manifest, vectorstore.NewHNSWStore(cm))
		release()
		if err != nil {
			t.Fatal(err)
		}
		db, err := cm.Get(req.Collection)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, found := db.Get("unpublished-attempt"); found {
			t.Fatal("unpublished attempt survived restart recovery")
		}
		for _, id := range []string{records[0].ID, "foreign-control"} {
			if _, _, found := db.Get(id); !found {
				t.Fatalf("exact recovery deleted protected ID %s", id)
			}
		}
		_, after := workspaceGenerationManifest(t, cfg)
		if !bytes.Equal(before, after) {
			t.Fatal("attempt cleanup mutated committed manifest")
		}
		if _, err := os.Stat(workspaceGenerationAttemptPath(cfg, "alpha", "main")); !os.IsNotExist(err) {
			t.Fatalf("attempt retained after successful cleanup: %v", err)
		}
		admitted, err := filterMCPDocumentResults(ctx, cfg, workspaceGenerationRecords(t, cfg, manifest))
		if err != nil || len(admitted) != 1 {
			t.Fatalf("published record lost eligibility: %d %v", len(admitted), err)
		}
		// A journal whose target cannot be verified is retained and fails closed.
		unknown := workspaceGenerationAttempt{ID: uuid.NewString(), ProjectID: "wrong", Branch: "main", Generation: "stable", Collection: req.Collection}
		raw, _ := json.Marshal(unknown)
		attemptPath := workspaceGenerationAttemptPath(cfg, "alpha", "main")
		if err := writeWorkspaceFile(ctx, cfg, attemptPath, raw); err != nil {
			t.Fatal(err)
		}
		if err := recoverWorkspaceGenerationAttempt(cfg, manifest, vectorstore.NewHNSWStore(cm)); err == nil {
			t.Fatal("unknown attempt target admitted")
		}
		retained, err := os.ReadFile(attemptPath)
		if err != nil || !bytes.Equal(retained, raw) {
			t.Fatal("invalid attempt evidence was discarded")
		}
	})
}

func TestWorkspaceGenerationManifestSaveFailurePreservesPublication(t *testing.T) {
	workspaceGenerationModes(t, func(t *testing.T, cfg APIConfig, actor accesspkg.MetadataActor, ctx context.Context) {
		workspaceIntegrityPut(t, cfg, "a.md", []byte("Original copper."))
		workspaceIntegrityPut(t, cfg, "b.md", []byte("Original zinc."))
		req := workspaceGenerationRequest()
		if _, err := reindexWorkspaceMarkdownAuthorized(ctx, cfg, req, actor); err != nil {
			t.Fatal(err)
		}
		manifest, before := workspaceGenerationManifest(t, cfg)
		records := workspaceGenerationRecords(t, cfg, manifest)
		manifestPath := workspaceManifestPath(cfg, "alpha", "main")
		diskBefore, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		backup := manifestPath + ".test-prior"
		var sabotaged atomic.Bool
		var sawPrepared atomic.Bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				Input []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			for _, text := range input.Input {
				if strings.Contains(text, "SaveFailure zinc") && sabotaged.CompareAndSwap(false, true) {
					all, err := vectorstore.NewHNSWStore(cfg.Collections).Scan(req.Collection)
					if err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
					sawPrepared.Store(len(all) > len(records))
					if err := os.Rename(manifestPath, backup); err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
					if err := os.Mkdir(manifestPath, 0700); err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
				}
			}
			values := make([]map[string]any, len(input.Input))
			for i := range values {
				values[i] = map[string]any{"index": i, "embedding": []float32{1, 2}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": values})
		}))
		defer server.Close()
		cfg.EmbedEndpoint = server.URL
		cfg.EmbedClient = nil
		workspaceIntegrityPut(t, cfg, "a.md", []byte("SaveFailure copper."))
		workspaceIntegrityPut(t, cfg, "b.md", []byte("SaveFailure zinc."))
		_, publishErr := reindexWorkspaceMarkdownAuthorized(ctx, cfg, req, actor)
		if sabotaged.Load() {
			if err := os.Remove(manifestPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(backup, manifestPath); err != nil {
				t.Fatal(err)
			}
		}
		if publishErr == nil || !sabotaged.Load() || !sawPrepared.Load() {
			t.Fatalf("save failure did not follow concrete vector insertion: err=%v sabotage=%v prepared=%v", publishErr, sabotaged.Load(), sawPrepared.Load())
		}
		diskAfter, err := os.ReadFile(manifestPath)
		if err != nil || !bytes.Equal(diskBefore, diskAfter) {
			t.Fatal("prior manifest bytes lost")
		}
		_, after := workspaceGenerationManifest(t, cfg)
		if !bytes.Equal(before, after) {
			t.Fatal("failed publication changed prior inventory/membership")
		}
		all, err := vectorstore.NewHNSWStore(cfg.Collections).Scan(req.Collection)
		if err != nil || len(all) != len(records) {
			t.Fatalf("failed preparation leaked vectors: %d want=%d err=%v", len(all), len(records), err)
		}
		admitted, err := filterMCPDocumentResults(ctx, cfg, workspaceGenerationRecords(t, cfg, manifest))
		if err != nil || len(admitted) != len(records) {
			t.Fatalf("previous publication ineligible: %d %v", len(admitted), err)
		}
	})
}
