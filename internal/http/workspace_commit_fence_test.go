package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"time"

	accesspkg "github.com/stek0v/levara/pkg/access"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceRevertReservedDestinationPreservesTree(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB = f.db
		path, _, err := workspaceFilePath(cfg, "alpha", "main", "ledger.md")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("Snapshot before current edits."), 0600); err != nil {
			t.Fatal(err)
		}
		commit, err := commitWorkspace(cfg, workspaceCommitRequest{ProjectID: "alpha"})
		if err != nil {
			t.Fatal(err)
		}
		const current = "Current edits must survive rejected reindex."
		if err := os.WriteFile(path, []byte(current), 0600); err != nil {
			t.Fatal(err)
		}
		_, err = revertWorkspace(context.Background(), cfg, workspaceRevertRequest{ProjectID: "alpha", CommitID: commit.CommitID, Reindex: true, Collection: "_memories"})
		if err == nil || !strings.Contains(err.Error(), "reserved memory collection") {
			t.Fatalf("destination error=%v", err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != current {
			t.Fatal("rejected reindex restored tree before destination validation")
		}
	})
}

func TestWorkspaceCommitRestoreRunAuthorityFence(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB, cfg.RequireAuth = f.db, true
		f.db.SetMaxOpenConns(1)
		actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
		path, _, err := workspaceFilePath(cfg, "alpha", "main", "ledger.md")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		snapshot := strings.Repeat("Copper seventeen. Zinc twenty-three. Total forty units. ", 20)
		if err := os.WriteFile(path, []byte(snapshot), 0600); err != nil {
			t.Fatal(err)
		}
		commit, err := commitWorkspaceAuthorized(context.Background(), cfg, workspaceCommitRequest{ProjectID: "alpha"}, actor)
		if err != nil || len(commit.Files) != 1 || f.db.Stats().InUse != 0 {
			t.Fatalf("native commit error=%v files=%d", err, len(commit.Files))
		}
		const current = "Current edits must survive denied or invalid restore."
		if err := os.WriteFile(path, []byte(current), 0600); err != nil {
			t.Fatal(err)
		}
		viewer := actor
		viewer.UserID = "viewer"
		expired := actor
		expired.Credential.ExpiresAt = time.Now().Add(-time.Hour).Unix()
		for _, admission := range []string{"viewer", "expired", "cancelled"} {
			for _, effect := range []string{"commit", "revert", "run"} {
				t.Run(admission+"_"+effect, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					caller := actor
					switch admission {
					case "viewer":
						caller = viewer
					case "expired":
						caller = expired
					case "cancelled":
						cancel()
					}
					var runErr error
					switch effect {
					case "commit":
						_, runErr = commitWorkspaceAuthorized(ctx, cfg, workspaceCommitRequest{ProjectID: "alpha"}, caller)
					case "revert":
						_, runErr = revertWorkspaceAuthorized(ctx, cfg, workspaceRevertRequest{ProjectID: "alpha", CommitID: commit.CommitID}, caller)
					case "run":
						_, runErr = startWorkspaceRunAuthorized(ctx, cfg, workspaceRunStartRequest{ProjectID: "alpha", RunID: "denied", Result: "must not appear"}, caller)
					}
					if runErr == nil {
						t.Fatal("unavailable authority accepted effect")
					}
					body, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					log, err := logWorkspaceCommits(cfg, workspaceCommitRequest{ProjectID: "alpha"})
					if err != nil {
						t.Fatal(err)
					}
					runDir, err := workspaceRunDir(cfg, "alpha", "main", "denied")
					if err != nil {
						t.Fatal(err)
					}
					_, runDirErr := os.Stat(runDir)
					if string(body) != current || len(log.Commits) != 1 || !os.IsNotExist(runDirErr) || f.db.Stats().InUse != 0 {
						t.Fatal("denied effect mutated tree/commits/run or leaked SQL")
					}
				})
			}
		}
		for _, invalid := range []string{"project", "branch", "id", "path_nul", "path_escape"} {
			t.Run("saved_"+invalid, func(t *testing.T) {
				bad := commit
				bad.Files = append([]workspaceCommitFile(nil), commit.Files...)
				switch invalid {
				case "project":
					bad.ProjectID = "beta"
				case "branch":
					bad.Branch = "other"
				case "id":
					bad.CommitID = "other"
				case "path_nul":
					bad.Files[0].Path = "invalid\x00.md"
				case "path_escape":
					bad.Files[0].Path = "../invalid.md"
				}
				if err := saveWorkspaceCommitRecord(commit.Path, bad); err != nil {
					t.Fatal(err)
				}
				if _, err := revertWorkspaceAuthorized(context.Background(), cfg, workspaceRevertRequest{ProjectID: "alpha", CommitID: commit.CommitID}, actor); err == nil {
					t.Fatal("invalid persisted target/path accepted")
				}
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(body) != current || f.db.Stats().InUse != 0 {
					t.Fatal("invalid restore destroyed current tree or leaked SQL")
				}
				if err := saveWorkspaceCommitRecord(commit.Path, commit); err != nil {
					t.Fatal(err)
				}
			})
		}
		t.Run("restore_without_reindex", func(t *testing.T) {
			resp, err := revertWorkspaceAuthorized(context.Background(), cfg, workspaceRevertRequest{ProjectID: "alpha", CommitID: commit.CommitID}, actor)
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != snapshot || resp.Indexed != nil || f.db.Stats().InUse != 0 {
				t.Fatal("native restore without indexing failed")
			}
		})
		target, err := url.Parse(cfg.EmbedEndpoint)
		if err != nil {
			t.Fatal(err)
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if f.db.Stats().InUse != 1 {
				t.Error("restore embedding ran without authority transaction")
			}
			proxy.ServeHTTP(w, r)
		}))
		defer server.Close()
		cfg.EmbedEndpoint = server.URL
		t.Run("restore_with_native_reindex", func(t *testing.T) {
			resp, err := revertWorkspaceAuthorized(context.Background(), cfg, workspaceRevertRequest{ProjectID: "alpha", CommitID: commit.CommitID, Reindex: true, Generation: "restored"}, actor)
			if err != nil {
				t.Fatal(err)
			}
			if resp.Indexed == nil || len(resp.Indexed.Results) != 1 || len(resp.Indexed.Results[0].VectorIDs) == 0 || calls.Load() == 0 || f.db.Stats().InUse != 0 {
				t.Fatal("native fenced restore reindex failed or leaked SQL")
			}
			jobs, err := listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{ProjectID: "alpha"})
			if err != nil {
				t.Fatal(err)
			}
			if len(jobs) != 1 || jobs[0].authority == nil || jobs[0].authority.Actor == nil || *jobs[0].authority.Actor != actor {
				t.Fatal("restore reindex lost submitting authority")
			}
		})
		t.Run("native_run_artifacts", func(t *testing.T) {
			resp, err := startWorkspaceRunAuthorized(context.Background(), cfg, workspaceRunStartRequest{ProjectID: "alpha", RunID: "accepted", Prompt: "Explain", Command: "go test", Result: "passed"}, actor)
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.Files) != 4 {
				t.Fatal("run omitted native artifacts")
			}
			for name, content := range resp.Files {
				body, err := os.ReadFile(filepath.Join(resp.Path, name))
				if err != nil || string(body) != content {
					t.Fatalf("artifact %s error=%v", name, err)
				}
			}
			if f.db.Stats().InUse != 0 {
				t.Fatal("run retained SQL")
			}
		})
	})
}

func saveWorkspaceCommitRecord(commitDir string, record workspaceCommitRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(commitDir, "commit.json"), data, 0644)
}
