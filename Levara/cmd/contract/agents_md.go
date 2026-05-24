package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stek0v/levara/internal/contract"
)

const (
	mcpBegin = "<!-- BEGIN: contract-mcp -->"
	mcpEnd   = "<!-- END: contract-mcp -->"
)

func rewriteAgentsMD(c contract.Contract, repoRoot string) error {
	path := filepath.Join(repoRoot, "AGENTS.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	s := string(raw)
	i := strings.Index(s, mcpBegin)
	j := strings.Index(s, mcpEnd)
	if i < 0 || j < 0 || j < i {
		return errors.New("AGENTS.md missing contract-mcp markers")
	}

	var body strings.Builder
	body.WriteString(mcpBegin + "\n\n")
	body.WriteString("| Tool | Group | Status |\n|---|---|---|\n")
	for _, t := range c.MCP {
		fmt.Fprintf(&body, "| %s | %s | %s |\n", t.Name, t.Group, t.Status)
	}
	body.WriteString("\n")

	out := s[:i] + body.String() + s[j:]
	return atomicWrite(path, []byte(out))
}
