package mcp

import "encoding/json"

// hallVocab is the controlled vocabulary for the "hall" field on a memory
// record. Extending this list is a deliberate code change so downstream
// consumers (search filters, dashboards) stay in sync — adding a value here
// without updating those consumers leaks unfiltered memories into UI.
//
// Vocabulary semantics:
//   fact       — objective characteristic (version, dimension, IP, path)
//   event      — something happened at a moment (deploy, merge, incident)
//   decision   — architectural/project choice with justification
//   preference — user preference about style, tools, workflow
//   advice     — reusable rule of thumb ("before X, do Y")
//   discovery  — non-obvious insight worth recalling months later
var hallVocab = []string{
	"fact",
	"event",
	"decision",
	"preference",
	"advice",
	"discovery",
}

// ValidHalls returns the controlled hall vocabulary. Returned slice should
// be treated as read-only by callers.
func ValidHalls() []string { return hallVocab }

// IsValidHall reports whether h is a member of the controlled vocabulary.
// Empty string is invalid (callers must explicitly pick a hall).
func IsValidHall(h string) bool {
	for _, v := range hallVocab {
		if v == h {
			return true
		}
	}
	return false
}

// ChunkMetaMatches returns true when the chunk metadata blob (JSON, as
// written by the orchestrator pipeline) satisfies room and tag filters.
//
// Empty roomFilter or empty tagFilters means "no filter on that dimension".
// Tag filtering uses OR semantics: a chunk matches if it has ANY of the
// wanted tags. This makes recall easier and matches user expectation when
// listing related topics.
//
// If raw can't be unmarshalled (older chunks without room/tags metadata),
// the function returns false when any filter is requested. The caller is
// expected to skip the filter call entirely when no filter is set.
func ChunkMetaMatches(raw []byte, roomFilter string, tagFilters []string) bool {
	if roomFilter == "" && len(tagFilters) == 0 {
		return true
	}
	var meta struct {
		Room string   `json:"room"`
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return false
	}
	if roomFilter != "" && meta.Room != roomFilter {
		return false
	}
	if len(tagFilters) > 0 {
		hit := false
		for _, want := range tagFilters {
			for _, have := range meta.Tags {
				if want == have {
					hit = true
					break
				}
			}
			if hit {
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}
