package http

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/stek0v/levara/pkg/workspace"
)

type workspaceSourceCitation struct {
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

type workspaceAnswerContract struct {
	Required          bool     `json:"required"`
	SearchTool        string   `json:"search_tool"`
	ReadTool          string   `json:"read_tool"`
	CitationField     string   `json:"citation_field"`
	RequiredFields    []string `json:"required_fields"`
	ExactReadRequired bool     `json:"exact_read_required"`
	Rule              string   `json:"rule"`
}

func workspaceSearchAnswerContract() workspaceAnswerContract {
	return workspaceAnswerContract{
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

func workspaceCitationFromSearchResult(result map[string]any, target workspaceSearchTarget, freshness workspaceSearchFreshness) workspaceSourceCitation {
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
	return workspaceSourceCitation{
		SourceID:         workspaceCitationSourceID(projectID, branch, sourcePath, generation, chunkID, vectorID),
		ProjectID:        projectID,
		Branch:           branch,
		Path:             sourcePath,
		HeadingPath:      headingPath,
		HeadingAnchor:    workspaceHeadingAnchor(headingPath),
		Generation:       generation,
		Collection:       collection,
		ChunkID:          chunkID,
		VectorID:         vectorID,
		FileDigest:       fileDigest,
		SourceURI:        workspaceSourceURI(projectID, branch, sourcePath, headingPath),
		ReadTool:         "workspace_read",
		ReadArgs:         workspaceReadArgs(projectID, branch, sourcePath),
		Stale:            freshness.Stale,
		PotentiallyStale: freshness.PotentiallyStale,
	}
}

func workspaceCitationsFromChunks(projectID, branch, sourcePath string, chunks []workspace.ChunkRecord) []workspaceSourceCitation {
	out := make([]workspaceSourceCitation, 0, len(chunks))
	for _, ch := range chunks {
		out = append(out, workspaceSourceCitation{
			SourceID:      workspaceCitationSourceID(projectID, branch, sourcePath, ch.Generation, ch.ChunkID, ch.VectorID),
			ProjectID:     projectID,
			Branch:        branch,
			Path:          sourcePath,
			HeadingPath:   append([]string(nil), ch.HeadingPath...),
			HeadingAnchor: workspaceHeadingAnchor(ch.HeadingPath),
			Generation:    ch.Generation,
			Collection:    ch.Collection,
			ChunkID:       ch.ChunkID,
			VectorID:      ch.VectorID,
			FileDigest:    ch.FileDigest,
			SourceURI:     workspaceSourceURI(projectID, branch, sourcePath, ch.HeadingPath),
			ReadTool:      "workspace_read",
			ReadArgs:      workspaceReadArgs(projectID, branch, sourcePath),
		})
	}
	return out
}

func workspaceFileCitation(projectID, branch, sourcePath string) workspaceSourceCitation {
	return workspaceSourceCitation{
		SourceID:  workspaceCitationSourceID(projectID, branch, sourcePath, "", "", ""),
		ProjectID: projectID,
		Branch:    branch,
		Path:      sourcePath,
		SourceURI: workspaceSourceURI(projectID, branch, sourcePath, nil),
		ReadTool:  "workspace_read",
		ReadArgs:  workspaceReadArgs(projectID, branch, sourcePath),
	}
}

func workspaceReadArgs(projectID, branch, sourcePath string) map[string]any {
	return map[string]any{
		"project_id": projectID,
		"branch":     branch,
		"path":       sourcePath,
	}
}

func workspaceSourceURI(projectID, branch, sourcePath string, headingPath []string) string {
	uri := fmt.Sprintf("workspace://%s/%s/%s", projectID, branch, sourcePath)
	if anchor := workspaceHeadingAnchor(headingPath); anchor != "" {
		uri += "#" + anchor
	}
	return uri
}

func workspaceCitationSourceID(projectID, branch, sourcePath, generation, chunkID, vectorID string) string {
	parts := []string{projectID, branch, sourcePath}
	for _, part := range []string{generation, chunkID, vectorID} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "::")
}

var workspaceAnchorUnsafeRe = regexp.MustCompile(`[^a-z0-9\- ]+`)

func workspaceHeadingAnchor(headingPath []string) string {
	if len(headingPath) == 0 {
		return ""
	}
	last := strings.ToLower(strings.TrimSpace(headingPath[len(headingPath)-1]))
	last = workspaceAnchorUnsafeRe.ReplaceAllString(last, "")
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
