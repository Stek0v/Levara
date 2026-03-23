// Package git provides structured git log parsing for commit analysis.
package git

import (
	"context"
	"fmt"
	"os/exec"
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

// ParseLog runs git log and returns structured commits.
func ParseLog(repoPath string, since string, limit int) ([]Commit, error) {
	if limit <= 0 {
		limit = 100
	}
	args := []string{"-C", repoPath, "log",
		"--format=%H|%an|%aI|%s",
		"--name-only",
		fmt.Sprintf("-n%d", limit),
	}
	if since != "" {
		args = append(args, "--since="+since)
	}

	out, err := exec.CommandContext(context.Background(), "git", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}

	return parseGitOutput(string(out))
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
		sb.WriteString(fmt.Sprintf("Commit %s by %s on %s: %s\n",
			c.Hash[:min(8, len(c.Hash))], c.Author, c.Date.Format("2006-01-02"), c.Message))
		if len(c.Files) > 0 {
			sb.WriteString("  Files: " + strings.Join(c.Files, ", ") + "\n")
		}
	}
	return sb.String()
}
