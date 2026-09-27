package http

import (
	"os"
	"strings"

	"github.com/stek0v/levara/pkg/mcp"
)

// EffectiveMCPToolset resolves the advertised MCP toolset. Priority: explicit
// LEVARA_MCP_TOOLSET, then the personal-profile binding, then the historical
// full default. Ф1 binding for the 2026-09-27 functional-audit decision Р1
// (A+): the personal profile defaults to the `core` toolset so a fresh
// personal deployment advertises the 13-tool memory surface.
//
// The raw LEVARA_PROFILE value is compared on purpose: profile.Normalize maps
// the empty string to personal for validation defaults, and that validation
// default must never narrow the toolset of deployments that did not opt in.
// Unknown LEVARA_PROFILE values therefore keep the full surface.
func EffectiveMCPToolset() (mode, source string) {
	if ts := strings.ToLower(strings.TrimSpace(os.Getenv("LEVARA_MCP_TOOLSET"))); ts != "" {
		return ts, "env"
	}
	if strings.ToLower(strings.TrimSpace(os.Getenv("LEVARA_PROFILE"))) == "personal" {
		return "core", "profile:personal"
	}
	return "", "default"
}

// EffectiveMCPToolsetName returns the stable effective toolset name (as
// resolved by mcp.ToolsetName — empty and unknown values remain "full") plus
// the source that produced it: "env", "profile:personal" or "default".
func EffectiveMCPToolsetName() (name, source string) {
	mode, source := EffectiveMCPToolset()
	return mcp.ToolsetName(mode), source
}

func configuredMCPToolDescriptors() []mcp.Tool {
	mode, _ := EffectiveMCPToolset()
	return mcp.ToolDescriptorsForMode(mode)
}

// effectiveMCPToolsetName is the single-value form of EffectiveMCPToolsetName
// for audit rows: they must name the toolset the agent actually saw.
func effectiveMCPToolsetName() string {
	name, _ := EffectiveMCPToolsetName()
	return name
}
