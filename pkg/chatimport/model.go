// Package chatimport normalizes external chat transcripts (Codex, Claude
// Code, Cursor) into a common per-message model and writes them idempotently
// into the chat_import_* tables.
package chatimport

// Platform identifies the transcript source. Values are stored in the DB and
// used in deterministic message IDs, so they are frozen once shipped.
type Platform string

const (
	PlatformCodex      Platform = "codex"
	PlatformClaudeCode Platform = "claude-code"
	PlatformCursor     Platform = "cursor"
)

// Kind classifies a normalized message. The vocabulary is enforced here, not
// by a DB CHECK, so new kinds do not require a schema migration.
type Kind string

const (
	KindText       Kind = "text"        // user or assistant prose
	KindReasoning  Kind = "reasoning"   // model thinking/summary available in plaintext
	KindToolCall   Kind = "tool_call"   // assistant invoked a tool
	KindToolResult Kind = "tool_result" // tool output
	KindSystem     Kind = "system"      // harness boilerplate (app-context, env)
)

// Message is one normalized transcript item. ExternalID must be stable for
// the same source item: it is a third of the idempotency key
// (platform, session_id, external_id).
type Message struct {
	ExternalID string            `json:"external_id"`
	Ordinal    int               `json:"ordinal"`
	Role       string            `json:"role"` // user|assistant|developer|system|tool
	Kind       Kind              `json:"kind"`
	Model      string            `json:"model,omitempty"`
	Content    string            `json:"content"`
	CreatedAt  string            `json:"created_at,omitempty"` // ISO-8601 from the source
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// Conversation groups messages from one source session.
type Conversation struct {
	Platform  Platform          `json:"platform"`
	SessionID string            `json:"session_id"`
	Title     string            `json:"title,omitempty"`
	Model     string            `json:"model,omitempty"`
	CreatedAt string            `json:"created_at,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Messages  []Message         `json:"messages"`
}

// ParseStats reports what an adapter saw, for run-ledger accounting and for
// the "skip, don't abort" contract on foreign/broken records.
type ParseStats struct {
	Messages int      // normalized messages produced
	Skipped  int      // records seen but not normalized (noise types, encrypted reasoning)
	Warnings []string // human-readable warnings; secrets findings land here too
}

// AddWarning appends a warning (deduplicated by exact text).
func (s *ParseStats) AddWarning(w string) {
	for _, existing := range s.Warnings {
		if existing == w {
			return
		}
	}
	s.Warnings = append(s.Warnings, w)
}
