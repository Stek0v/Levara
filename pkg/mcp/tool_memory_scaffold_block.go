package mcp

import (
	"context"
	"fmt"
	"strings"
)

const memoryScaffoldBlockMaxProposals = 6

type memoryScaffoldBlockProposal struct {
	ID, Summary, ProposedChange string
}

// ToolMemoryScaffoldBlock renders approved scaffold proposals as a manual
// AGENTS.md or CLAUDE.md preview. It never changes a proposal or a file.
func ToolMemoryScaffoldBlock(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	collection, _ := args["collection"].(string)
	if strings.TrimSpace(collection) == "" {
		return toolError("'collection' required")
	}
	targetFile, _ := args["target_file"].(string)
	if targetFile != "AGENTS.md" && targetFile != "CLAUDE.md" {
		return toolError("'target_file' must be AGENTS.md or CLAUDE.md")
	}
	proposalIDs, err := memoryScaffoldBlockIDs(args["proposal_ids"])
	if err != nil {
		return toolError(err.Error())
	}
	if deps.DB() == nil {
		return toolError("database unavailable")
	}

	placeholders := make([]string, len(proposalIDs))
	params := make([]any, 0, len(proposalIDs)+1)
	params = append(params, collection)
	for i, id := range proposalIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+2)
		params = append(params, id)
	}
	rows, err := deps.DB().QueryContext(ctx, deps.Q(fmt.Sprintf(`SELECT id,summary,proposed_change
		FROM memory_scaffold_proposals WHERE collection_name=$1 AND status='approved' AND id IN (%s)`, strings.Join(placeholders, ","))), params...)
	if err != nil {
		return toolError(err.Error())
	}
	defer rows.Close()
	byID := make(map[string]memoryScaffoldBlockProposal, len(proposalIDs))
	for rows.Next() {
		var proposal memoryScaffoldBlockProposal
		if err := rows.Scan(&proposal.ID, &proposal.Summary, &proposal.ProposedChange); err != nil {
			return toolError(err.Error())
		}
		byID[proposal.ID] = proposal
	}
	if err := rows.Err(); err != nil {
		return toolError(err.Error())
	}
	proposals := make([]memoryScaffoldBlockProposal, 0, len(proposalIDs))
	for _, id := range proposalIDs {
		proposal, ok := byID[id]
		if !ok {
			return toolError("every selected proposal must be approved in this collection")
		}
		proposals = append(proposals, proposal)
	}
	return jsonResult(map[string]any{
		"collection": collection, "target_file": targetFile, "proposal_ids": proposalIDs,
		"block": memoryScaffoldBlockMarkdown(proposals),
	})
}

func memoryScaffoldBlockIDs(raw any) ([]string, error) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 || len(items) > memoryScaffoldBlockMaxProposals {
		return nil, fmt.Errorf("'proposal_ids' must contain 1-%d proposal IDs", memoryScaffoldBlockMaxProposals)
	}
	seen := make(map[string]bool, len(items))
	ids := make([]string, 0, len(items))
	for _, item := range items {
		id, ok := item.(string)
		id = strings.TrimSpace(id)
		if !ok || id == "" {
			return nil, fmt.Errorf("'proposal_ids' must contain non-empty strings")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func memoryScaffoldBlockMarkdown(proposals []memoryScaffoldBlockProposal) string {
	var out strings.Builder
	out.WriteString("<!-- Levara scaffold preview: review and apply manually. -->\n## Levara memory workflow\n\n")
	for _, proposal := range proposals {
		text := proposal.ProposedChange
		if strings.TrimSpace(text) == "" {
			text = proposal.Summary
		}
		fmt.Fprintf(&out, "- %s\n", memoryScaffoldBlockText(text))
	}
	return out.String()
}

func memoryScaffoldBlockText(value string) string {
	value = strings.NewReplacer("\r", " ", "\n", " ", "<", "&lt;", ">", "&gt;").Replace(strings.TrimSpace(value))
	runes := []rune(value)
	if len(runes) > 280 {
		return string(runes[:280]) + "…"
	}
	return value
}
