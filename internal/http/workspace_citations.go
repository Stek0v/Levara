// workspace_citations.go — adapter delegates. The citation library lives
// in internal/api/workspacecitations (Phase-2 workspace campaign, leaf 1;
// code-modernization brief §3). Package-local names are preserved as
// aliases/delegates so workspace.go, task_executor.go and the publication
// test are unchanged.
package http

import (
	"github.com/stek0v/levara/internal/api/workspacecitations"
	"github.com/stek0v/levara/pkg/workspace"
)

type workspaceSourceCitation = workspacecitations.SourceCitation
type workspaceAnswerContract = workspacecitations.AnswerContract
type workspaceSearchFreshness = workspacecitations.SearchFreshness
type workspaceSearchTarget = workspacecitations.SearchTarget

func workspaceSearchAnswerContract() workspaceAnswerContract {
	return workspacecitations.SearchAnswerContract()
}

func workspaceCitationFromSearchResult(result map[string]any, target workspaceSearchTarget, freshness workspaceSearchFreshness) workspaceSourceCitation {
	return workspacecitations.CitationFromSearchResult(result, target, freshness)
}

func workspaceCitationsFromChunks(projectID, branch, sourcePath string, chunks []workspace.ChunkRecord) []workspaceSourceCitation {
	return workspacecitations.CitationsFromChunks(projectID, branch, sourcePath, chunks)
}

func workspaceFileCitation(projectID, branch, sourcePath string) workspaceSourceCitation {
	return workspacecitations.FileCitation(projectID, branch, sourcePath)
}

func workspaceSourceURI(projectID, branch, sourcePath string, headingPath []string) string {
	return workspacecitations.SourceURI(projectID, branch, sourcePath, headingPath)
}

func workspaceCitationSourceID(projectID, branch, sourcePath, generation, chunkID, vectorID string) string {
	return workspacecitations.CitationSourceID(projectID, branch, sourcePath, generation, chunkID, vectorID)
}

func workspaceHeadingAnchor(headingPath []string) string {
	return workspacecitations.HeadingAnchor(headingPath)
}
