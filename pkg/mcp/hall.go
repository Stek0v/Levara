package mcp

import (
	"encoding/json"

	"github.com/stek0v/levara/pkg/memoryhall"
)

// ValidHalls returns a defensive copy of the controlled vocabulary.
func ValidHalls() []string { return memoryhall.ValidHalls() }

// IsValidHall reports exact membership; empty and unknown halls are invalid.
func IsValidHall(h string) bool { return memoryhall.IsValidHall(h) }

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
