package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func processLockSupported() bool {
	switch runtime.GOOS {
	case "darwin", "dragonfly", "freebsd", "linux", "netbsd", "openbsd", "windows":
		return true
	}
	return false
}

func TestWorkspaceProcessLockChild(t *testing.T) {
	if os.Getenv("LEVARA_WORKSPACE_LOCK_CHILD") == "" {
		return
	}
	mode := os.Getenv("LEVARA_WORKSPACE_LOCK_MODE")
	deadline := 3 * time.Second
	if mode == "blocked" {
		deadline = 150 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	release, err := LockProject(ctx, os.Getenv("LEVARA_WORKSPACE_LOCK_BASE"), "alpha")
	if mode == "blocked" {
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("child lock escaped exclusion: %v", err)
		}
		fmt.Println("CHILD_BLOCKED")
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	release()
	release()
	fmt.Println("CHILD_RELEASED")
}

func TestWorkspaceProcessLockExclusionCancellationAndChild(t *testing.T) {
	if !processLockSupported() {
		t.Skip("native lock unavailable")
	}
	base := t.TempDir()
	release, err := LockProject(context.Background(), base, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if second, err := LockProject(ctx, base, "alpha"); !errors.Is(err, context.DeadlineExceeded) {
		if second != nil {
			second()
		}
		t.Fatalf("same-process exclusion failed: %v", err)
	}
	independent, err := LockProject(context.Background(), base, "beta")
	if err != nil {
		t.Fatal(err)
	}
	independent()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := func(mode string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "-test.run=^TestWorkspaceProcessLockChild$")
		cmd.Env = append(os.Environ(), "LEVARA_WORKSPACE_LOCK_CHILD=1", "LEVARA_WORKSPACE_LOCK_BASE="+base, "LEVARA_WORKSPACE_LOCK_MODE="+mode)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("child %s: %v %s", mode, err, out)
		}
		return string(out)
	}
	if out := child("blocked"); !strings.Contains(out, "CHILD_BLOCKED") {
		t.Fatalf("no child blocking evidence: %s", out)
	}
	release()
	release()
	if out := child("acquire"); !strings.Contains(out, "CHILD_RELEASED") {
		t.Fatalf("child failed after release: %s", out)
	}
	again, err := LockProject(context.Background(), base, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	again()
}

func TestWorkspaceProcessLockUnsafeNamespace(t *testing.T) {
	if !processLockSupported() {
		t.Skip("native lock unavailable")
	}
	base := t.TempDir()
	other := t.TempDir()
	if err := os.Symlink(other, filepath.Join(base, ".kb")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if release, err := LockProject(context.Background(), base, "alpha"); err == nil {
		release()
		t.Fatal("admitted redirected lock namespace")
	}
	entries, err := os.ReadDir(other)
	if err != nil || len(entries) != 0 {
		t.Fatal("modified redirected lock namespace")
	}
}

func TestWorkspaceProcessLockUnsupportedPlatform(t *testing.T) {
	if processLockSupported() {
		return
	}
	if release, err := LockProject(context.Background(), t.TempDir(), "alpha"); err == nil {
		release()
		t.Fatal("unsupported platform claimed lock")
	}
}
