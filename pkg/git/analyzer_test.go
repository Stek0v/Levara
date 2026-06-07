package git

import (
	"strings"
	"testing"
	"time"
)

// T-9b: pure-fn smoke tests for the git log parser.
// ParseLog itself shells out to `git`, but parseGitOutput is the parser
// that interprets git's stdout — it's the part that's worth locking in.

func TestParseGitOutput_Empty(t *testing.T) {
	commits, err := parseGitOutput("")
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 0 {
		t.Errorf("expected 0 commits from empty input, got %d", len(commits))
	}
}

func TestParseGitOutput_SingleCommit(t *testing.T) {
	raw := strings.Join([]string{
		"abcdef0123456789abcdef0123456789abcdef01|Alice|2026-04-15T10:00:00+00:00|Initial commit",
		"README.md",
		"go.mod",
	}, "\n")
	commits, err := parseGitOutput(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 1 {
		t.Fatalf("got %d commits, want 1", len(commits))
	}
	c := commits[0]
	if c.Hash != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Errorf("Hash = %q", c.Hash)
	}
	if c.Author != "Alice" {
		t.Errorf("Author = %q", c.Author)
	}
	if c.Message != "Initial commit" {
		t.Errorf("Message = %q", c.Message)
	}
	want, _ := time.Parse(time.RFC3339, "2026-04-15T10:00:00+00:00")
	if !c.Date.Equal(want) {
		t.Errorf("Date = %v, want %v", c.Date, want)
	}
	if len(c.Files) != 2 || c.Files[0] != "README.md" || c.Files[1] != "go.mod" {
		t.Errorf("Files = %v", c.Files)
	}
}

func TestParseGitOutput_MultipleCommits(t *testing.T) {
	raw := strings.Join([]string{
		"1111111111111111111111111111111111111111|Alice|2026-04-15T10:00:00+00:00|First",
		"file1.go",
		"2222222222222222222222222222222222222222|Bob|2026-04-15T11:00:00+00:00|Second",
		"file2.go",
		"file3.go",
	}, "\n")
	commits, err := parseGitOutput(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 {
		t.Fatalf("got %d commits, want 2", len(commits))
	}
	if commits[0].Author != "Alice" || len(commits[0].Files) != 1 {
		t.Errorf("commit[0] = %+v", commits[0])
	}
	if commits[1].Author != "Bob" || len(commits[1].Files) != 2 {
		t.Errorf("commit[1] = %+v", commits[1])
	}
}

func TestParseGitOutput_PipeInMessage(t *testing.T) {
	// Subjects can contain "|"; SplitN(_, 4) preserves the rest.
	raw := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef|Carol|2026-04-15T10:00:00+00:00|fix: split a|b in regex"
	commits, _ := parseGitOutput(raw)
	if len(commits) != 1 {
		t.Fatalf("got %d, want 1", len(commits))
	}
	if commits[0].Message != "fix: split a|b in regex" {
		t.Errorf("Message = %q (pipe should be preserved)", commits[0].Message)
	}
}

func TestParseLog_NonexistentDir(t *testing.T) {
	_, err := ParseLog("/nonexistent/path/does/not/exist", "", 10)
	if err == nil {
		t.Fatal("expected error on missing path")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("err should name the path: %v", err)
	}
}

func TestParseLog_NotAGitRepo(t *testing.T) {
	dir := t.TempDir()
	_, err := ParseLog(dir, "", 10)
	if err == nil {
		t.Fatal("expected error on non-git directory")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("err should mention 'not a git repository': %v", err)
	}
}

func TestCommitsToText(t *testing.T) {
	c := Commit{
		Hash:    "abcdef0123",
		Author:  "Alice",
		Date:    time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
		Message: "Initial",
		Files:   []string{"README.md"},
	}
	out := CommitsToText([]Commit{c})
	if !strings.Contains(out, "abcdef01") {
		t.Errorf("output missing short hash: %s", out)
	}
	if !strings.Contains(out, "Alice") {
		t.Errorf("output missing author: %s", out)
	}
	if !strings.Contains(out, "2026-04-15") {
		t.Errorf("output missing date: %s", out)
	}
	if !strings.Contains(out, "README.md") {
		t.Errorf("output missing files: %s", out)
	}
}

func TestCommitsToText_ShortHashTruncation(t *testing.T) {
	// CommitsToText uses min(8, len(Hash)) so a short hash shouldn't crash.
	c := Commit{Hash: "abc", Author: "X", Message: "y"}
	out := CommitsToText([]Commit{c})
	if !strings.Contains(out, "abc") {
		t.Errorf("short hash dropped: %s", out)
	}
}
