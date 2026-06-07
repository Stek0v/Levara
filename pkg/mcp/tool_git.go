package mcp

// Git-focused tools: analyze_commits + git_search.
// Migrated in F-4 wave 3m. Together because they share the
// "git_commits" collection convention and are small enough that
// one file keeps them co-located.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/git"
	"github.com/stek0v/levara/pkg/runreg"
)

const (
	// analyzeCommitsDefaultLimit caps git.ParseLog when the caller
	// omits "limit". Matches pre-refactor.
	analyzeCommitsDefaultLimit = 100
	// analyzeCommitsCollection is the vector collection name where
	// commit text gets indexed. Shared with git_search which reads
	// from the same collection. Historic naming — don't change
	// without migrating existing data.
	analyzeCommitsCollection = "git_commits"
	// analyzeCommitsCognifiedPreview caps the human-readable preview
	// in the cognify-enabled response (text continues in the vector
	// index).
	analyzeCommitsCognifiedPreview = 2000
	// analyzeCommitsTextOnlyPreview caps the response when the embed
	// service is missing — the entire text goes in the response.
	analyzeCommitsTextOnlyPreview = 4000
	// gitSearchTopK caps git_search results per call. Matches
	// pre-refactor hard-coded 10.
	gitSearchTopK = 10
)

// ToolAnalyzeCommits parses git log for a repository and — when the
// embed service is configured — kicks off a cognify pipeline to
// index the commit text into the git_commits collection. Returns a
// preview of the commit text regardless of the embed state, so the
// tool is useful in read-only deployments too.
//
// Error branches:
//   - Missing repo_path → IsError.
//   - git.ParseLog error → IsError with the parse-log message.
//
// Non-error branches:
//   - len(commits) == 0 → "No commits found." (not IsError — empty is
//     a valid query outcome).
//   - Embed configured → registry gets a RUNNING run, goroutine
//     drives the pipeline; tool returns immediately with a preview.
//   - Embed not configured → text-only preview, no pipeline.
func ToolAnalyzeCommits(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	repoPath, _ := args["repo_path"].(string)
	if repoPath == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'repo_path' required"}},
			IsError: true,
		}
	}
	since, _ := args["since"].(string)
	limit := analyzeCommitsDefaultLimit
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	commits, err := git.ParseLog(repoPath, since, limit)
	if err != nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Error parsing git log: %s", err.Error())}},
			IsError: true,
		}
	}

	if len(commits) == 0 {
		return ToolResult{Content: []Content{{Type: "text", Text: "No commits found."}}}
	}

	text := git.CommitsToText(commits)

	pipeCfg := deps.BaseCognifyConfig()
	if pipeCfg.EmbedEndpoint == "" {
		return ToolResult{Content: []Content{{
			Type: "text",
			Text: fmt.Sprintf("Analyzed %d commits (no embedding service — text only):\n%s", len(commits), Truncate(text, analyzeCommitsTextOnlyPreview)),
		}}}
	}

	// AnalyzeCommits historically built its pipeline config without
	// LLMProvider or BM25Indexes. Zero both here so the refactor
	// preserves the pre-refactor code path byte-for-byte:
	//   - LLMProvider nil → entity extraction uses the legacy
	//     raw-HTTP path rather than the multi-provider Provider API.
	//   - BM25Indexes nil → orchestrator skips BM25 indexing of
	//     commit chunks (even when a git_commits BM25 index exists).
	// Both are conservative overrides; when someone later decides the
	// tool should benefit from these features, the lines can be
	// dropped. Document in the release note when flipping.
	pipeCfg.LLMProvider = nil
	pipeCfg.BM25Indexes = nil

	runID := uuid.New().String()
	pipeCfg.Collection = analyzeCommitsCollection
	pipeCfg.DatasetID = runID
	pipeCfg.GenerateTriplets = true

	status := &runreg.Status{
		RunID: runID, Status: "RUNNING", Stage: "starting", StartedAt: time.Now(),
	}
	deps.Runs().Store(runID, status)

	go func() {
		runPipelineWithStatus(deps, []string{text}, pipeCfg, status)
		// Heartbeat so long-running tool observability matches cognify.
		// Added in wave 3m — pre-refactor skipped it. No other
		// consumer assumes its absence so the semantic change is safe.
		deps.LogHeartbeat("analyze_commits", map[string]any{
			"run_id":     runID,
			"collection": analyzeCommitsCollection,
			"status":     status.Status,
			"commits":    len(commits),
			"elapsed_ms": status.ElapsedMs,
		})
	}()

	return ToolResult{Content: []Content{{
		Type: "text",
		Text: fmt.Sprintf("Analyzed %d commits. Cognify pipeline started (run_id: %s). Use cognify_status to track.\n\nPreview:\n%s",
			len(commits), runID, Truncate(text, analyzeCommitsCognifiedPreview)),
	}}}
}

// ToolGitSearch runs a vector search against the git_commits
// collection (populated by ToolAnalyzeCommits). Always uses the
// default SearchByText branch — no rerank / multi-query / parent-
// child. Hard-coded topK=10 matches pre-refactor.
//
// Error branches:
//   - Missing query → IsError.
//   - SearchByText error → IsError with "Search error: ..."
//
// Non-error branches:
//   - Embed not configured → plain "No results ..." text.
//   - Zero results → "No matching commits found."
//   - Hits → JSON array of {id, score, metadata}.
func ToolGitSearch(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'query' required"}},
			IsError: true,
		}
	}

	sp := deps.NewSearchPipeline(false)
	if sp == nil {
		return ToolResult{Content: []Content{{
			Type: "text",
			Text: "No results (embedding service not configured)",
		}}}
	}

	res, err := sp.SearchByText(ctx, analyzeCommitsCollection, query, gitSearchTopK)
	if err != nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Search error: %s", err.Error())}},
			IsError: true,
		}
	}

	if len(res) == 0 {
		return ToolResult{Content: []Content{{Type: "text", Text: "No matching commits found."}}}
	}

	var results []map[string]any
	for _, r := range res {
		results = append(results, map[string]any{
			"id":       r.ID,
			"score":    r.Score,
			"metadata": string(r.Metadata),
		})
	}
	out, _ := json.MarshalIndent(results, "", "  ")
	return ToolResult{Content: []Content{{Type: "text", Text: string(out)}}}
}
