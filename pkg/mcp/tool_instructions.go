// tool_instructions.go — Versioned agent contract served via MCP.
//
// The contract markdown (agent_contract.md) is embedded in the binary so
// the document ships with the server and cannot drift from the runtime
// it describes. Clients receive the markdown plus a content hash so they
// can cache and detect changes across deploys.
package mcp

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed agent_contract.md
var agentContractMD string

// AgentContractVersion is the human-readable contract revision. Bump
// whenever the embedded markdown changes meaningfully (not for typos).
const AgentContractVersion = "v1"

// AgentContractMarkdown returns the embedded contract verbatim. Exposed
// for callers (e.g. the HTTP `initialize` handler) that want to hint at
// the contract without going through tools/call.
func AgentContractMarkdown() string { return agentContractMD }

// AgentContractSHA returns the hex-encoded SHA-256 of the contract
// markdown. Stable across builds with identical content; clients use it
// as a cache key.
func AgentContractSHA() string {
	sum := sha256.Sum256([]byte(agentContractMD))
	return hex.EncodeToString(sum[:])
}

// ToolLevaraInstructions returns the embedded agent contract. No
// dependencies, no DB access — pure read of the binary's own bytes.
func ToolLevaraInstructions(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	out := map[string]any{
		"version":          AgentContractVersion,
		"content_sha":      AgentContractSHA(),
		"content_markdown": agentContractMD,
	}
	return jsonResult(out)
}
