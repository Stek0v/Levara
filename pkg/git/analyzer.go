// Package git provides structured git log parsing for commit analysis.
package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Commit represents a single parsed git commit.
type Commit struct {
	Hash    string
	Author  string
	Date    time.Time
	Message string
	Files   []string
	Diff    string // short diff summary
}

// ParseLog retains the legacy background-context adapter.
func ParseLog(repoPath string, since string, limit int) ([]Commit, error) {
	return ParseLogContext(context.Background(), repoPath, since, limit)
}

// ParseLogContext verifies the repository before recognizing an unborn HEAD.
// Repeated calls return matching commits again; no global deduplication is done.
func ParseLogContext(ctx context.Context, repoPath, since string, limit int) ([]Commit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Stat(repoPath)
	if err != nil {
		return nil, fmt.Errorf("repo path %q: %w", repoPath, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("repo path %q: not a directory", repoPath)
	}
	// .git may be a directory or a worktree pointer file.
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		return nil, fmt.Errorf("repo path %q: not a git repository (.git not found)", repoPath)
	}
	run := func(args ...string) ([]byte, error) {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", repoPath}, args...)...).Output()
		if canceled := ctx.Err(); canceled != nil {
			return nil, canceled
		}
		return out, err
	}
	if _, err := run("rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("not a git repository: %w", err)
	}
	if _, headErr := run("rev-parse", "--verify", "HEAD^{commit}"); headErr != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ref, err := run("symbolic-ref", "--quiet", "HEAD")
		if err != nil {
			return nil, fmt.Errorf("git HEAD: %w", headErr)
		}
		name := strings.TrimSpace(string(ref))
		if !strings.HasPrefix(name, "refs/heads/") {
			return nil, fmt.Errorf("git HEAD: %w", headErr)
		}
		_, err = run("show-ref", "--verify", "--quiet", name)
		if err == nil {
			return nil, fmt.Errorf("git HEAD: %w", headErr)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return []Commit{}, nil
		}
		return nil, fmt.Errorf("git HEAD reference: %w", err)
	}
	if limit <= 0 {
		limit = 100
	}
	args := []string{"log", "--format=%H|%an|%aI|%s", "--name-only", fmt.Sprintf("-n%d", limit)}
	if since != "" {
		if exactDate, parseErr := time.Parse("2006-01-02", since); parseErr == nil {
			since = "@" + strconv.FormatInt(exactDate.Unix(), 10)
		}
		args = append(args, "--since="+since)
	}
	out, err := run(args...)
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	commits, err := parseGitOutput(string(out))
	if canceled := ctx.Err(); canceled != nil {
		return nil, canceled
	}
	return commits, err
}

// parseGitOutput parses the combined format of hash|author|date|message
// followed by file names until the next commit line.
func parseGitOutput(raw string) ([]Commit, error) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	var commits []Commit
	var current *Commit

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, "|", 4)
		if len(parts) == 4 && len(parts[0]) == 40 {
			// New commit header line
			if current != nil {
				commits = append(commits, *current)
			}
			t, _ := time.Parse(time.RFC3339, parts[2])
			current = &Commit{
				Hash:    parts[0],
				Author:  parts[1],
				Date:    t,
				Message: parts[3],
			}
		} else if current != nil {
			// File name line
			current.Files = append(current.Files, line)
		}
	}
	if current != nil {
		commits = append(commits, *current)
	}

	return commits, nil
}

// CommitsToText converts commits to a text block for cognify ingestion.
func CommitsToText(commits []Commit) string {
	var sb strings.Builder
	for _, c := range commits {
		fmt.Fprintf(&sb, "Commit %s by %s on %s: %s\n",
			c.Hash[:min(8, len(c.Hash))], c.Author, c.Date.Format("2006-01-02"), c.Message)
		if len(c.Files) > 0 {
			sb.WriteString("  Files: " + strings.Join(c.Files, ", ") + "\n")
		}
	}
	return sb.String()
}
