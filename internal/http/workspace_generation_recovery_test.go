package http

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/workspace"
)

// This subprocess holds the same native project lock as a separate server.
func TestWorkspaceGenerationRecoveryLockChild(t *testing.T) {
	root := os.Getenv("LEVARA_RECOVERY_LOCK_ROOT")
	if root == "" {
		return
	}
	cfg := APIConfig{WorkspacePath: root}
	release, err := workspace.LockProject(context.Background(), root, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	fmt.Println("locked")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	path := workspaceIndexJobPath(cfg, "alpha", "main", "recovery")
	job, err := loadWorkspaceIndexJobConfined(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	job.Status = workspaceIndexJobCompleted
	job.Attempts = 9
	if err := saveWorkspaceIndexJobConfined(cfg, path, job); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceGenerationRecoveryNativeReload(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		cfg := f.cfg
		cfg.WorkspacePath, cfg.RequireAuth = t.TempDir(), true
		actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
		job := workspaceIndexJob{ID: "recovery", Status: workspaceIndexJobRunning, Attempts: 1,
			UpdatedAt: time.Now().Add(-time.Hour).Format(time.RFC3339Nano),
			Request:   workspaceIndexJobPayload{ProjectID: "alpha", Branch: "main", Operation: "reconcile", Generation: "recover"},
			authority: &workspaceIndexJobAuthority{Actor: &actor}}
		path := workspaceIndexJobPath(cfg, "alpha", "main", job.ID)
		if err := saveWorkspaceIndexJobConfined(cfg, path, job); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkspaceGenerationRecoveryLockChild$")
		cmd.Env = append(os.Environ(), "LEVARA_RECOVERY_LOCK_ROOT="+cfg.WorkspacePath)
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		output, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = input.Close()
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		}()
		if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "locked\n" {
			t.Fatalf("child admission: %q %v %s", line, err, stderr.String())
		}
		type result struct {
			job     workspaceIndexJob
			changed bool
			err     error
		}
		done := make(chan result, 1)
		go func() {
			j, c, e := recoverWorkspaceRunningJob(cfg, job, time.Now(), normalizeWorkspaceIndexWorkerOptions(WorkspaceIndexWorkerOptions{}))
			done <- result{j, c, e}
		}()
		// SQL admission is retained while recovery waits for the other server's FS lock.
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for f.db.Stats().InUse != 1 {
			select {
			case <-ctx.Done():
				t.Fatal("recovery did not reach native admission")
			case <-tick.C:
			}
		}
		select {
		case r := <-done:
			t.Fatalf("recovery escaped held project lock: %+v", r)
		default:
		}
		if _, err := fmt.Fprintln(input, "complete"); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatalf("child: %v %s", err, stderr.String())
		}
		select {
		case r := <-done:
			if r.err != nil || r.changed || r.job.Status != workspaceIndexJobCompleted || r.job.Attempts != 9 {
				t.Fatalf("recovery overwrote live completion: %+v", r)
			}
		case <-ctx.Done():
			t.Fatal("recovery did not finish")
		}
		persisted, err := loadWorkspaceIndexJobConfined(cfg, path)
		if err != nil || persisted.Status != workspaceIndexJobCompleted || persisted.Attempts != 9 {
			t.Fatalf("persisted=%+v error=%v", persisted, err)
		}

		job.ID = "expired_recovery"
		job.authority.Actor.Credential.ExpiresAt = time.Now().Add(-time.Hour).Unix()
		path = workspaceIndexJobPath(cfg, "alpha", "main", job.ID)
		if err := saveWorkspaceIndexJobConfined(cfg, path, job); err != nil {
			t.Fatal(err)
		}
		before, err := readWorkspaceFile(cfg, path)
		if err != nil {
			t.Fatal(err)
		}
		if _, changed, err := recoverWorkspaceRunningJob(cfg, job, time.Now(), normalizeWorkspaceIndexWorkerOptions(WorkspaceIndexWorkerOptions{})); err == nil || changed {
			t.Fatalf("expired recovery changed=%v error=%v", changed, err)
		}
		after, err := readWorkspaceFile(cfg, path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("expired delegation revived or rewrote job")
		}
	})
}

func TestWorkspaceGenerationRecoverySupersedesOnlyFullActive(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB, cfg.RequireAuth = f.db, true
		actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		sidecarFixtureWrite(t, filepath.Join(workspaceProjectRoot(cfg, "alpha", "main"), "note.md"), []byte(strings.Repeat("Native supersession publication. ", 20)))
		req := workspaceReconcileRequest{workspaceReindexRequest: workspaceReindexRequest{ProjectID: "alpha", Branch: "main", Generation: "active_a", ActivateGeneration: true, ChunkStrategy: "paragraph", MinChunkChars: 1}, DeleteMissing: true}
		if _, err := reconcileWorkspaceMarkdownAuthorized(ctx, cfg, req, actor); err != nil {
			t.Fatal(err)
		}
		stamp := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
		failed := workspaceIndexJob{ID: "scope_failure", Status: workspaceIndexJobDeadLetter, Attempts: 3, LastError: "original provider failure",
			CreatedAt: stamp, UpdatedAt: stamp, StartedAt: stamp, FinishedAt: stamp, DeadLetterAt: stamp,
			Request:   workspaceIndexJobPayload{ProjectID: "alpha", Branch: "main", Operation: "reconcile", Generation: "failed"},
			authority: &workspaceIndexJobAuthority{Actor: &actor}}
		path := workspaceIndexJobPath(cfg, "alpha", "main", failed.ID)
		if err := saveWorkspaceIndexJobConfined(cfg, path, failed); err != nil {
			t.Fatal(err)
		}
		assertHistory := func(marker string) {
			t.Helper()
			actual, err := loadWorkspaceIndexJobConfined(cfg, path)
			if err != nil {
				t.Fatal(err)
			}
			expected := failed
			expected.SupersededByGeneration = marker
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("publication changed failure history beyond marker: actual=%+v expected=%+v", actual, expected)
			}
		}
		req.Generation = "inactive_b"
		req.ActivateGeneration = false
		if _, err := reconcileWorkspaceMarkdownAuthorized(ctx, cfg, req, actor); err != nil {
			t.Fatal(err)
		}
		manifest, _, err := loadWorkspaceManifest(cfg, "alpha", "main")
		if err != nil || manifest.ActiveGeneration != "active_a" || manifest.Files["inactive_b"]["note.md"] == "" {
			t.Fatalf("inactive publication control failed: %v", err)
		}
		assertHistory("")
		req.Generation = "active_a"
		req.ActivateGeneration = true
		req.Paths = []string{"note.md"}
		if _, err := reconcileWorkspaceMarkdownAuthorized(ctx, cfg, req, actor); err != nil {
			t.Fatal(err)
		}
		manifest, _, err = loadWorkspaceManifest(cfg, "alpha", "main")
		if err != nil || manifest.ActiveGeneration != "active_a" {
			t.Fatalf("same-active partial control failed: %v", err)
		}
		assertHistory("")
		req.Generation = "active_c"
		req.Paths = nil
		if _, err := reconcileWorkspaceMarkdownAuthorized(ctx, cfg, req, actor); err != nil {
			t.Fatal(err)
		}
		manifest, _, err = loadWorkspaceManifest(cfg, "alpha", "main")
		if err != nil || manifest.ActiveGeneration != "active_c" || manifest.Files["active_c"]["note.md"] == "" {
			t.Fatalf("full active control failed: %v", err)
		}
		assertHistory("active_c")
	})
}
