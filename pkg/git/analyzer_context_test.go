package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func gitContextFixture(t *testing.T) (string, func(...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("local git unavailable")
	}
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@test.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@test.invalid", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	run("config", "commit.gpgsign", "false")
	return repo, run
}
func TestParseLogContextRepositoryOutcomes(t *testing.T) {
	repo, run := gitContextFixture(t)
	empty, err := ParseLogContext(context.Background(), repo, "", 10)
	if err != nil || len(empty) != 0 {
		t.Fatalf("unborn: %v %v", empty, err)
	}
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "file.txt")
	run("commit", "-q", "-m", "fixture initial")
	first, err := ParseLogContext(context.Background(), repo, "", 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("committed: %+v %v", first, err)
	}
	again, err := ParseLog(repo, "", 10)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatalf("repeat adapter: %+v %v", again, err)
	}
	empty, err = ParseLogContext(context.Background(), repo, "2100-01-01", 10)
	if err != nil || len(empty) != 0 {
		t.Fatalf("filtered empty: %v %v", empty, err)
	}
	// Linked worktrees use a .git pointer file, not a directory.
	worktree := filepath.Join(t.TempDir(), "linked")
	run("worktree", "add", "-q", "--detach", worktree, "HEAD")
	linked, err := ParseLogContext(context.Background(), worktree, "", 10)
	if err != nil || !reflect.DeepEqual(first, linked) {
		t.Fatalf("worktree: %+v %v", linked, err)
	}
	// Missing commit objects must not be confused with an unborn branch.
	hash := run("rev-parse", "HEAD")
	if err := os.Remove(filepath.Join(repo, ".git", "objects", hash[:2], hash[2:])); err != nil {
		t.Fatal(err)
	}
	if got, err := ParseLogContext(context.Background(), repo, "", 10); err == nil {
		t.Fatalf("corrupt object accepted as empty: %+v", got)
	}
}
func TestParseLogContextInvalidAndCanceled(t *testing.T) {
	plain := t.TempDir()
	if _, err := ParseLogContext(context.Background(), plain, "", 10); err == nil {
		t.Fatal("plain directory accepted")
	}
	fake := t.TempDir()
	if err := os.Mkdir(filepath.Join(fake, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseLogContext(context.Background(), fake, "", 10); err == nil {
		t.Fatal("invalid .git directory accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ParseLogContext(ctx, plain, "", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("precanceled: %v", err)
	}
}
func TestParseLogContextCancellationDuringLog(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable")
	}
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "started")
	script := "#!/bin/sh\ncase \"$3\" in\n rev-parse) if [ \"$4\" = --git-dir ]; then printf '.git\\n'; else printf '1111111111111111111111111111111111111111\\n'; fi ;;\n log) printf started > \"$LEVARA_TEST_GIT_MARKER\"; while :; do :; done ;;\n *) exit 2 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("LEVARA_TEST_GIT_MARKER", marker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := ParseLogContext(ctx, repo, "", 10); done <- err }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
waiting:
	for {
		if _, err := os.Stat(marker); err == nil {
			break waiting
		}
		select {
		case err := <-done:
			t.Fatalf("log exited before cancellation: %v", err)
		case <-ticker.C:
		case <-timeout.C:
			cancel()
			<-done
			t.Fatal("log never entered deterministic executable")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("subprocess ignored cancellation")
	}
}
