package aggregator

import (
	"fmt"
	"sort"
	"strings"
)

type ScoredEdge struct {
	SourceID, SourceName, SourceText string
	SourceDist                       float32
	TargetID, TargetName, TargetText string
	TargetDist                       float32
	RelationshipName                 string
	EdgeDist                         float32
}

type RankedEdge struct {
	SourceID, SourceName string
	TargetID, TargetName string
	RelationshipName     string
	Score                float32
}

type AggregateResult struct {
	RankedEdges      []RankedEdge
	FormattedContext string
	UniqueNodeCount  int
}

func Aggregate(edges []ScoredEdge, topK int) AggregateResult {
	if topK <= 0 {
		topK = 10
	}

	// 1. Rank by score (lower = better)
	type scored struct {
		edge  ScoredEdge
		score float32
	}
	ranked := make([]scored, len(edges))
	for i, e := range edges {
		ranked[i] = scored{edge: e, score: e.SourceDist + e.TargetDist + e.EdgeDist}
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score < ranked[j].score })
	if len(ranked) > topK {
		ranked = ranked[:topK]
	}

	// 2. Deduplicate nodes
	type nodeInfo struct {
		name, text string
	}
	nodes := make(map[string]nodeInfo)
	for _, r := range ranked {
		if _, ok := nodes[r.edge.SourceID]; !ok {
			nodes[r.edge.SourceID] = nodeInfo{name: r.edge.SourceName, text: r.edge.SourceText}
		}
		if _, ok := nodes[r.edge.TargetID]; !ok {
			nodes[r.edge.TargetID] = nodeInfo{name: r.edge.TargetName, text: r.edge.TargetText}
		}
	}

	// 3. Format context
	var sb strings.Builder
	sb.WriteString("Nodes:\n")
	for _, n := range nodes {
		title := createTitle(n.text)
		fmt.Fprintf(&sb, "Node: %s\n__node_content_start__\n%s\n__node_content_end__\n\n", title, n.text)
	}
	sb.WriteString("Connections:\n")
	for _, r := range ranked {
		fmt.Fprintf(&sb, "%s --[%s]--> %s\n", r.edge.SourceName, r.edge.RelationshipName, r.edge.TargetName)
	}

	// 4. Build result
	result := AggregateResult{
		FormattedContext: sb.String(),
		UniqueNodeCount:  len(nodes),
	}
	for _, r := range ranked {
		result.RankedEdges = append(result.RankedEdges, RankedEdge{
			SourceID:         r.edge.SourceID,
			SourceName:       r.edge.SourceName,
			TargetID:         r.edge.TargetID,
			TargetName:       r.edge.TargetName,
			RelationshipName: r.edge.RelationshipName,
			Score:            r.score,
		})
	}
	return result
}

func createTitle(text string) string {
	words := strings.Fields(text)
	if len(words) <= 7 {
		return text
	}
	title := strings.Join(words[:7], " ")
	if len(words) > 30 {
		// Add top 3 frequent words
		freq := make(map[string]int)
		for _, w := range words {
			w = strings.ToLower(w)
			if len(w) > 3 { // skip short words
				freq[w]++
			}
		}
		type wf struct {
			w string
			f int
		}
		var sorted []wf
		for w, f := range freq {
			sorted = append(sorted, wf{w, f})
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].f > sorted[j].f })
		var top []string
		for i := 0; i < 3 && i < len(sorted); i++ {
			top = append(top, sorted[i].w)
		}
		if len(top) > 0 {
			title += " [" + strings.Join(top, ", ") + "]"
		}
	}
	return title
}
