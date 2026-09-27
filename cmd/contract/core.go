package main

import (
	"os"
	"path/filepath"

	"github.com/stek0v/levara/internal/contract"
	"github.com/stek0v/levara/pkg/mcp"
)

// coreContract derives the personal (toolset `core`) variant of the
// contract: canonical REST/gRPC/schema inventories with the MCP list
// narrowed to what a personal deployment actually advertises (Р1 A+,
// Ф1 T6). The live registry is the only input, so generation is
// deterministic; flag-gated tools (memory_commit_*, task_*) can never
// appear because the core set does not contain them.
func coreContract(c contract.Contract) contract.Contract {
	advertised := make(map[string]bool, 16)
	for _, t := range mcp.ToolDescriptorsForMode("core") {
		advertised[t.Name] = true
	}
	out := c
	out.MCP = make([]contract.MCPTool, 0, len(advertised))
	for _, t := range c.MCP {
		if advertised[t.Name] {
			out.MCP = append(out.MCP, t)
		}
	}
	return out
}

func writeCore(c contract.Contract, outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	b, err := renderJSONBytes(coreContract(c))
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(outDir, "contract-core.json"), b)
}

func validateCore(c contract.Contract, outDir string) error {
	cmp := coreContract(c)
	cmp.GitRev = readContractField(outDir, "contract-core.json", "git_rev", c.GitRev)
	cmp.GeneratedAt = readContractField(outDir, "contract-core.json", "generated_at", c.GeneratedAt)
	return compareFile(cmp, outDir, "contract-core.json", renderJSONBytes)
}
