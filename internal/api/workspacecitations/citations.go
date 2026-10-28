// citations.go — workspace source-citation construction for agent answers.
//
// Extracted verbatim from internal/http/workspace_citations.go (Phase-2
// workspace campaign, leaf 1; code-modernization brief §3). Pure library:
// no HTTP handlers, no APIConfig — only DTO construction over
// pkg/workspace chunk records. The search-target/freshness metadata types
// moved with it from workspace.go; internal/http keeps type aliases and
// one-line delegates so workspace.go and the publication test are
// unchanged.
package workspacecitations

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/stek0v/levara/pkg/workspace"
)

// SearchFreshness carries generation/watcher freshness facts for a search
// result, mirrored into citations.
type SearchFreshness struct {
	Stale                      bool   `json:"stale"`
	PotentiallyStale           bool   `json:"potentially_stale"`
	Reason                     string `json:"reason,omitempty"`
	ActiveGeneration           string `json:"active_generation,omitempty"`
	RequestedGeneration        string `json:"requested_generation,omitempty"`
	ResolvedGeneration         string `json:"resolved_generation,omitempty"`
	LastIndexedAt              string `json:"last_indexed_at,omitempty"`
	ActiveChunkCount           int    `json:"active_chunk_count"`
	ActivePathCount            int    `json:"active_path_count"`
	WatcherEnabled             bool   `json:"watcher_enabled"`
	WatcherPending             int    `json:"watcher_pending_branches"`
	WatcherLastReconcile       string `json:"watcher_last_reconcile_at,omitempty"`
	WatcherLastError           string `json:"watcher_last_error,omitempty"`
	WatcherBranchPending       bool   `json:"watcher_branch_pending"`
	WatcherBranchLastScan      string `json:"watcher_branch_last_scan_at,omitempty"`
	WatcherBranchLastChange    string `json:"watcher_branch_last_change_at,omitempty"`
	WatcherBranchLastReconcile string `json:"watcher_branch_last_reconcile_at,omitempty"`
	WatcherBranchLastError     string `json:"watcher_branch_last_error,omitempty"`
}

// SearchTarget identifies the resolved workspace branch a search ran
// against.
type SearchTarget struct {
	Manifest     *workspace.Manifest
	ManifestPath string
	Branch       string
	Generation   string
	Collection   string
	Chunks       []workspace.ChunkRecord
}

// SourceCitation is the citation object attached to workspace search
// results and required by the answer contract.
type SourceCitation struct {
	SourceID         string         `json:"source_id"`
	ProjectID        string         `json:"project_id"`
	Branch           string         `json:"branch"`
	Path             string         `json:"path"`
	HeadingPath      []string       `json:"heading_path,omitempty"`
	HeadingAnchor    string         `json:"heading_anchor,omitempty"`
	Generation       string         `json:"generation,omitempty"`
	Collection       string         `json:"collection,omitempty"`
	ChunkID          string         `json:"chunk_id,omitempty"`
	VectorID         string         `json:"vector_id,omitempty"`
	FileDigest       string         `json:"file_digest,omitempty"`
	SourceURI        string         `json:"source_uri"`
	ReadTool         string         `json:"read_tool"`
	ReadArgs         map[string]any `json:"read_args"`
	Stale            bool           `json:"stale"`
	PotentiallyStale bool           `json:"potentially_stale"`
}

// AnswerContract is the advertised citation requirement for
// workspace_search consumers.
type AnswerContract struct {
	Required          bool     `json:"required"`
	SearchTool        string   `json:"search_tool"`
	ReadTool          string   `json:"read_tool"`
	CitationField     string   `json:"citation_field"`
	RequiredFields    []string `json:"required_fields"`
	ExactReadRequired bool     `json:"exact_read_required"`
	Rule              string   `json:"rule"`
}

// SearchAnswerContract returns the contract for workspace_search answers.
func SearchAnswerContract() AnswerContract {
	return AnswerContract{
		Required:          true,
		SearchTool:        "workspace_search",
		ReadTool:          "workspace_read",
		CitationField:     "citation",
		ExactReadRequired: true,
		RequiredFields: []string{
			"project_id", "branch", "path", "generation", "collection",
			"heading_path", "chunk_id", "vector_id", "source_uri",
		},
		Rule: "Every factual answer derived from workspace_search must cite the result citation and first verify the exact Markdown source with workspace_read.",
	}
}

// CitationFromSearchResult builds a citation from a raw search-result map,
// falling back to the resolved target for missing identity fields.
func CitationFromSearchResult(result map[string]any, target SearchTarget, freshness SearchFreshness) SourceCitation {
	projectID := firstString(result, "project_id", "dataset_id")
	if projectID == "" {
		projectID = target.Manifest.ProjectID
	}
	branch := firstString(result, "branch")
	if branch == "" {
		branch = target.Branch
	}
	sourcePath := firstString(result, "path")
	headingPath := anyStringSlice(result["heading_path"])
	generation := firstString(result, "generation")
	if generation == "" {
		generation = target.Generation
	}
	collection := firstString(result, "collection")
	if collection == "" {
		collection = target.Collection
	}
	vectorID := firstString(result, "vector_id", "id")
	chunkID := firstString(result, "chunk_id")
	fileDigest := firstString(result, "file_digest")
	return SourceCitation{
		SourceID:         CitationSourceID(projectID, branch, sourcePath, generation, chunkID, vectorID),
		ProjectID:        projectID,
		Branch:           branch,
		Path:             sourcePath,
		HeadingPath:      headingPath,
		HeadingAnchor:    HeadingAnchor(headingPath),
		Generation:       generation,
		Collection:       collection,
		ChunkID:          chunkID,
		VectorID:         vectorID,
		FileDigest:       fileDigest,
		SourceURI:        SourceURI(projectID, branch, sourcePath, headingPath),
		ReadTool:         "workspace_read",
		ReadArgs:         ReadArgs(projectID, branch, sourcePath),
		Stale:            freshness.Stale,
		PotentiallyStale: freshness.PotentiallyStale,
	}
}

// CitationsFromChunks builds one citation per chunk record.
func CitationsFromChunks(projectID, branch, sourcePath string, chunks []workspace.ChunkRecord) []SourceCitation {
	out := make([]SourceCitation, 0, len(chunks))
	for _, ch := range chunks {
		out = append(out, SourceCitation{
			SourceID:      CitationSourceID(projectID, branch, sourcePath, ch.Generation, ch.ChunkID, ch.VectorID),
			ProjectID:     projectID,
			Branch:        branch,
			Path:          sourcePath,
			HeadingPath:   append([]string(nil), ch.HeadingPath...),
			HeadingAnchor: HeadingAnchor(ch.HeadingPath),
			Generation:    ch.Generation,
			Collection:    ch.Collection,
			ChunkID:       ch.ChunkID,
			VectorID:      ch.VectorID,
			FileDigest:    ch.FileDigest,
			SourceURI:     SourceURI(projectID, branch, sourcePath, ch.HeadingPath),
			ReadTool:      "workspace_read",
			ReadArgs:      ReadArgs(projectID, branch, sourcePath),
		})
	}
	return out
}

// FileCitation builds a file-level citation without chunk identity.
func FileCitation(projectID, branch, sourcePath string) SourceCitation {
	return SourceCitation{
		SourceID:  CitationSourceID(projectID, branch, sourcePath, "", "", ""),
		ProjectID: projectID,
		Branch:    branch,
		Path:      sourcePath,
		SourceURI: SourceURI(projectID, branch, sourcePath, nil),
		ReadTool:  "workspace_read",
		ReadArgs:  ReadArgs(projectID, branch, sourcePath),
	}
}

// ReadArgs is the argument shape the advertised read tool expects.
func ReadArgs(projectID, branch, sourcePath string) map[string]any {
	return map[string]any{
		"project_id": projectID,
		"branch":     branch,
		"path":       sourcePath,
	}
}

// SourceURI renders the canonical workspace:// URI with heading anchor.
func SourceURI(projectID, branch, sourcePath string, headingPath []string) string {
	uri := fmt.Sprintf("workspace://%s/%s/%s", projectID, branch, sourcePath)
	if anchor := HeadingAnchor(headingPath); anchor != "" {
		uri += "#" + anchor
	}
	return uri
}

// CitationSourceID joins non-empty identity parts with "::".
func CitationSourceID(projectID, branch, sourcePath, generation, chunkID, vectorID string) string {
	parts := []string{projectID, branch, sourcePath}
	for _, part := range []string{generation, chunkID, vectorID} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "::")
}

var anchorUnsafeRe = regexp.MustCompile(`[^a-z0-9\- ]+`)

// HeadingAnchor slugifies the deepest heading of a heading path.
func HeadingAnchor(headingPath []string) string {
	if len(headingPath) == 0 {
		return ""
	}
	last := strings.ToLower(strings.TrimSpace(headingPath[len(headingPath)-1]))
	last = anchorUnsafeRe.ReplaceAllString(last, "")
	last = strings.Join(strings.Fields(last), "-")
	return strings.Trim(last, "-")
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func anyStringSlice(v any) []string {
	switch xs := v.(type) {
	case []string:
		return append([]string(nil), xs...)
	case []any:
		out := make([]string, 0, len(xs))
		for _, x := range xs {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
