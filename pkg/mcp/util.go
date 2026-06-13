package mcp

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
)

// RandomHex returns n random hex characters. Panics if crypto/rand is
// unavailable (which would indicate a system-level failure where panicking
// is preferable to silently emitting predictable IDs). Used for MCP session
// IDs and any other "must be unguessable" tokens.
func RandomHex(n int) string {
	b := make([]byte, n/2+1)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return fmt.Sprintf("%x", b)[:n]
}

// DiaryOwnerPrefix is the namespace prefix for per-agent diary entries
// stored in the memory palace. Combined with the agent name it forms an
// owner_id like "agent:reviewer".
const DiaryOwnerPrefix = "agent:"

// DiaryOwner builds the owner_id used to scope diary memories to a specific
// subagent (Explore, Plan, code-review, ...). Trims whitespace from agent so
// "  reviewer " and "reviewer" produce the same owner.
func DiaryOwner(agent string) string {
	return DiaryOwnerPrefix + strings.TrimSpace(agent)
}

// Truncate shortens s to at most maxLen runes, replacing the tail with
// "..." (3 bytes) when the cut happens. Used by tool implementations
// to keep log/message echoes bounded. Operates on bytes, not runes —
// acceptable for the short ASCII-heavy strings the tools emit.
func Truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func jsonResult(v any) ToolResult {
	out, _ := json.MarshalIndent(v, "", "  ")
	return ToolResult{Content: []Content{{Type: "text", Text: string(out)}}}
}

func statusResult(ok bool, message string) ToolResult {
	return jsonResult(map[string]any{"ok": ok, "message": message})
}
