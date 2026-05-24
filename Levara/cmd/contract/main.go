package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/stek0v/levara/internal/contract"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: contract <generate|validate> [flags]")
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	outDir := fs.String("out", "docs", "output directory")
	repoRoot := fs.String("repo", ".", "repo root (for AGENTS.md, deployment-matrix)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fail(err.Error())
	}

	gitRev := gitCommit()
	generatedAt := gitCommitTime()
	c := collect(gitRev, generatedAt)

	switch cmd {
	case "generate":
		if err := writeAll(c, *outDir, *repoRoot); err != nil {
			fail(err.Error())
		}
	case "validate":
		if err := validate(c, *outDir, *repoRoot); err != nil {
			fail(err.Error())
		}
	default:
		fail("unknown command: " + cmd)
	}
}

func fail(msg string) { fmt.Fprintln(os.Stderr, msg); os.Exit(1) }

func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func gitCommitTime() string {
	out, err := exec.Command("git", "log", "-1", "--format=%cI").Output()
	if err != nil {
		return "1970-01-01T00:00:00Z"
	}
	return strings.TrimSpace(string(out))
}

func writeAll(c contract.Contract, outDir, repoRoot string) error {
	if err := writeJSON(c, outDir); err != nil {
		return err
	}
	if err := writeMarkdown(c, outDir); err != nil {
		return err
	}
	return rewriteAgentsMD(c, repoRoot)
}

// Stub — filled in by task 11.
func validate(c contract.Contract, outDir, repoRoot string) error { return nil }
