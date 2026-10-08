package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
)

func sidecarFixtureWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceFilesystemSidecarsRoundTrip(t *testing.T) {
	cfg := APIConfig{WorkspacePath: t.TempDir()}
	job := workspaceIndexJob{ID: "job_fixture", Status: workspaceIndexJobPending,
		Request:   workspaceIndexJobPayload{ProjectID: "alpha", Branch: "main", Generation: "one", Operation: "reconcile"},
		authority: &workspaceIndexJobAuthority{Actor: &accesspkg.MetadataActor{Actor: accesspkg.Actor{UserID: "owner", TenantID: "a"}, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}}}
	path := workspaceIndexJobPath(cfg, "alpha", "main", job.ID)
	if err := saveWorkspaceIndexJobConfined(cfg, path, job); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadWorkspaceIndexJobConfined(cfg, path)
	if err != nil || loaded.authority == nil || loaded.authority.Actor == nil || loaded.authority.Actor.UserID != "owner" || loaded.authority.Actor.TenantID != "a" {
		t.Fatalf("round trip=%+v error=%v", loaded, err)
	}
	job.Status = workspaceIndexJobCompleted
	if err := saveWorkspaceIndexJobConfined(cfg, path, job); err != nil {
		t.Fatal(err)
	}
	jobs, err := listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{ProjectID: "alpha", Branch: "main"})
	if err != nil || len(jobs) != 1 || jobs[0].Status != workspaceIndexJobCompleted {
		t.Fatalf("jobs=%+v error=%v", jobs, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("temporary files: entries=%v error=%v", entries, err)
	}

	data := []byte("# Unicode\r\n\r\nПривет.\r\n")
	sidecarFixtureWrite(t, filepath.Join(workspaceProjectRoot(cfg, "alpha", "main"), "docs", "a.md"), data)
	registry := workspaceContextArtifactRegistry{Version: 1, Includes: []workspaceContextArtifactInclude{{ProjectID: "alpha", Branch: "main", Glob: "docs/*.md"}}}
	raw, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	sidecarFixtureWrite(t, workspaceContextArtifactRegistryPath(cfg), raw)
	artifacts, err := listWorkspaceContextArtifacts(context.Background(), cfg, workspaceContextArtifactsRequest{ProjectID: "alpha", Branch: "main"})
	if err != nil || len(artifacts.Artifacts) != 1 {
		t.Fatalf("artifacts=%+v error=%v", artifacts, err)
	}
	a := artifacts.Artifacts[0]
	if !a.Exists || a.Path != "docs/a.md" || a.Bytes != int64(len(data)) || a.Digest != digestBytes(data) {
		t.Fatalf("artifact=%+v", a)
	}
	got, err := readWorkspaceArtifactFile(cfg, "alpha", "main", a.Path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("bytes changed: %q %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = reindexWorkspaceContextArtifactsAuthorized(ctx, cfg, workspaceReindexArtifactsRequest{workspaceReindexRequest: workspaceReindexRequest{ProjectID: "alpha", Branch: "main", Generation: "cancelled"}}, accesspkg.MetadataActor{TrustedLocal: true})
	if err == nil {
		t.Fatal("canceled artifact indexing accepted")
	}
	got, err = readWorkspaceArtifactFile(cfg, "alpha", "main", a.Path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("cancelled operation changed authoritative file")
	}
	if _, err := os.Stat(workspaceManifestPath(cfg, "alpha", "main")); !os.IsNotExist(err) {
		t.Fatalf("cancelled operation created manifest: %v", err)
	}
}

func TestWorkspaceFilesystemSidecarsRejectRedirects(t *testing.T) {
	for _, kind := range []string{"jobs_namespace", "job_leaf", "registry_namespace", "registry_leaf", "project_namespace", "branch_namespace", "artifact_parent", "artifact_leaf", "artifact_directory"} {
		t.Run(kind, func(t *testing.T) {
			cfg := APIConfig{WorkspacePath: t.TempDir()}
			outside := t.TempDir()
			secret := filepath.Join(outside, "secret.json")
			const payload = "PRIVATE-EXTERNAL-PAYLOAD"
			sidecarFixtureWrite(t, secret, []byte(payload))
			target := secret
			var link string
			switch kind {
			case "jobs_namespace":
				link = filepath.Join(workspaceRoot(cfg), ".kb", "jobs")
				target = outside
			case "job_leaf":
				link = workspaceIndexJobPath(cfg, "alpha", "main", "fixture")
			case "registry_namespace":
				link = filepath.Join(workspaceRoot(cfg), ".kb")
				target = outside
			case "registry_leaf":
				link = workspaceContextArtifactRegistryPath(cfg)
			case "project_namespace":
				link = filepath.Join(workspaceRoot(cfg), "projects", "alpha")
				target = outside
			case "branch_namespace":
				link = workspaceProjectRoot(cfg, "alpha", "main")
				target = outside
			case "artifact_parent":
				link = filepath.Join(workspaceProjectRoot(cfg, "alpha", "main"), "docs")
				target = outside
			default:
				link = filepath.Join(workspaceProjectRoot(cfg, "alpha", "main"), "docs", "secret.json")
			}
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			if kind == "artifact_directory" {
				if err := os.Mkdir(link, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "jobs_namespace", "job_leaf":
				path := workspaceIndexJobPath(cfg, "alpha", "main", "fixture")
				if _, err = loadWorkspaceIndexJobConfined(cfg, path); err == nil {
					t.Fatal("redirected job read accepted")
				}
				if err = saveWorkspaceIndexJobConfined(cfg, path, workspaceIndexJob{ID: "fixture"}); err == nil {
					t.Fatal("redirected job write accepted")
				}
				if _, err = listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{ProjectID: "alpha", Branch: "main"}); err == nil {
					t.Fatal("redirected job listing accepted")
				}
			case "registry_namespace", "registry_leaf":
				_, _, err = loadWorkspaceContextArtifactRegistry(cfg)
			default:
				_, err = materializeWorkspaceArtifact(cfg, workspaceContextArtifactRequest{ProjectID: "alpha", Branch: "main", Path: "docs/secret.json"}, "explicit", false)
				if err == nil {
					t.Fatal("redirected artifact metadata accepted")
				}
				_, err = readWorkspaceArtifactFile(cfg, "alpha", "main", "docs/secret.json")
			}
			if err == nil {
				t.Fatal("unsafe access accepted")
			}
			got, readErr := os.ReadFile(secret)
			if readErr != nil || string(got) != payload {
				t.Fatalf("redirect target changed: %q %v", got, readErr)
			}
		})
	}
}

func TestWorkspaceFilesystemSidecarDenialBeforeMetadata(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg := f.cfg
		cfg.WorkspacePath = t.TempDir()
		cfg.RequireAuth = true
		outside := t.TempDir()
		sidecarFixtureWrite(t, filepath.Join(outside, "context-artifacts.json"), []byte("PRIVATE-REGISTRY-METADATA"))
		if err := os.MkdirAll(filepath.Join(cfg.WorkspacePath, ".kb"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "context-artifacts.json"), workspaceContextArtifactRegistryPath(cfg)); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(cfg.WorkspacePath, ".kb", "jobs")); err != nil {
			t.Fatal(err)
		}
		app := workspaceACLApp("foreign", cfg)
		for _, path := range []string{"/workspace/context/artifacts?project_id=alpha", "/workspace/jobs?project_id=alpha"} {
			body := workspaceTestGet(t, app, path, http.StatusForbidden)
			if bytes.Contains(body, []byte("PRIVATE")) || bytes.Contains(body, []byte(outside)) || bytes.Contains(body, []byte("symlink")) {
				t.Fatalf("denied operation leaked storage: %s", body)
			}
		}
		entries, err := os.ReadDir(outside)
		if err != nil || len(entries) != 1 {
			t.Fatalf("denial created storage: %v %v", entries, err)
		}
	})
}

func waitWorkspaceJobAdmission(t *testing.T, f *documentHTTPFixture, before int64) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for f.db.Stats().WaitCount <= before {
		select {
		case <-timer.C:
			t.Fatal("job did not reach SQL admission wait")
		case <-tick.C:
		}
	}
}

func TestWorkspaceJobsReloadAfterNativeFence(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		cfg := f.cfg
		cfg.WorkspacePath = t.TempDir()
		cfg.RequireAuth = true
		actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
		for _, operation := range []string{"worker", "retry"} {
			for _, change := range []string{"completed", "request", "credential"} {
				t.Run(operation+"_"+change, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
					defer cancel()
					job := workspaceIndexJob{ID: "job_" + operation + "_" + change, Status: workspaceIndexJobPending, Attempts: 1,
						Request:   workspaceIndexJobPayload{ProjectID: "alpha", Branch: "main", Operation: "reindex", Generation: "one", Paths: []string{"missing.md"}},
						authority: &workspaceIndexJobAuthority{Actor: &actor}}
					if operation == "retry" {
						job.Status = workspaceIndexJobFailed
					}
					path := workspaceIndexJobPath(cfg, "alpha", "main", job.ID)
					if err := saveWorkspaceIndexJobConfined(cfg, path, job); err != nil {
						t.Fatal(err)
					}
					_, release, err := f.p.BeginTransferFence(ctx, GetDBProvider() == DBSQLite)
					if err != nil {
						t.Fatal(err)
					}
					defer release()
					before := f.db.Stats().WaitCount
					type result struct {
						job workspaceIndexJob
						err error
					}
					done := make(chan result, 1)
					go func() {
						if operation == "worker" {
							j, _, err := runWorkspaceIndexJob(ctx, cfg, job, WorkspaceIndexWorkerOptions{})
							done <- result{j, err}
						} else {
							r, err := retryWorkspaceIndexJobAuthorized(ctx, cfg, workspaceRetryIndexJobRequest{ProjectID: "alpha", Branch: "main", JobID: job.ID}, actor)
							done <- result{r.Job, err}
						}
					}()
					waitWorkspaceJobAdmission(t, f, before)
					changed, err := loadWorkspaceIndexJobConfined(cfg, path)
					if err != nil {
						t.Fatal(err)
					}
					changed.Attempts = 7
					switch change {
					case "completed":
						changed.Status = workspaceIndexJobCompleted
					case "request":
						changed.Request.Paths = []string{"different.md"}
					case "credential":
						changed.authority.Actor.Credential.Epoch++
					}
					if err := saveWorkspaceIndexJobConfined(cfg, path, changed); err != nil {
						t.Fatal(err)
					}
					release()
					select {
					case result := <-done:
						if change == "completed" {
							if result.err != nil || result.job.Status != workspaceIndexJobCompleted || result.job.Attempts != 7 {
								t.Fatalf("terminal job replayed: %+v %v", result.job, result.err)
							}
						} else if result.err == nil {
							t.Fatal("changed admission executed")
						}
					case <-ctx.Done():
						t.Fatal("job did not finish after release")
					}
					persisted, err := loadWorkspaceIndexJobConfined(cfg, path)
					if err != nil || persisted.Attempts != 7 || persisted.Status != changed.Status {
						t.Fatalf("job mutated after stale wait: %+v %v", persisted, err)
					}
					if _, err := os.Stat(workspaceManifestPath(cfg, "alpha", "main")); !os.IsNotExist(err) {
						t.Fatalf("stale job created manifest: %v", err)
					}
				})
			}
		}
	})
}

func TestWorkspaceJobsEnqueueCredentialDeadline(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		cfg := f.cfg
		cfg.WorkspacePath = t.TempDir()
		cfg.RequireAuth = true
		actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(-time.Hour).Unix()}}
		payload := workspaceIndexJobPayload{ProjectID: "alpha", Branch: "main", Operation: "reindex", Generation: "one", Paths: []string{"missing.md"}}
		_, err := enqueueWorkspaceIndexJobFromPayloadAuthorized(context.Background(), cfg, payload, actor)
		var status *fiber.Error
		if !errors.As(err, &status) || status.Code != fiber.StatusForbidden {
			t.Fatalf("expired admission=%v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, release, err := f.p.BeginTransferFence(ctx, GetDBProvider() == DBSQLite)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		actor.Credential.ExpiresAt = time.Now().Add(2 * time.Second).Unix()
		before := f.db.Stats().WaitCount
		done := make(chan error, 1)
		go func() { _, err := enqueueWorkspaceIndexJobFromPayloadAuthorized(ctx, cfg, payload, actor); done <- err }()
		waitWorkspaceJobAdmission(t, f, before)
		deadline := time.NewTimer(time.Until(time.Unix(actor.Credential.ExpiresAt, 0)) + time.Second)
		defer deadline.Stop()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("expired queued enqueue accepted")
			}
		case <-deadline.C:
			t.Fatal("enqueue waited beyond credential expiry")
		}
		jobs, err := listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{ProjectID: "alpha", Branch: "main"})
		if err != nil || len(jobs) != 0 {
			t.Fatalf("expired enqueue created job: %+v %v", jobs, err)
		}
	})
}
