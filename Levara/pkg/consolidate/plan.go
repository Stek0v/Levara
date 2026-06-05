package consolidate

import (
	"regexp"
	"sort"
	"strings"
)

// Plan classifies each cluster into a consolidation Action.
// A cluster whose every internal edge >= TauHigh is a mechanical merge
// (keep newest record, supersede the rest). Otherwise it is an LLM abstraction
// (supersede all sources; NewValue is left empty for the caller to fill).
func Plan(recs map[string]MemoryRecord, clusters []Cluster, cfg Config) []Action {
	var actions []Action
	for _, c := range clusters {
		if len(c.IDs) < 2 {
			continue
		}
		ids := append([]string(nil), c.IDs...)
		sort.Strings(ids) // deterministic ordering

		if allTight(c.Edges, cfg.TauHigh) {
			survivor := newest(ids, recs)
			var sources []string
			for _, id := range ids {
				if id != survivor {
					sources = append(sources, id)
				}
			}
			// A high cosine alone is not enough to mechanically supersede a
			// source: potion envelope-collapse scores records sharing a long
			// header >= TauHigh despite distinct bodies. Only merge when the
			// survivor actually subsumes each source's content; otherwise fall
			// through to a content-preserving abstraction (findings P2.3).
			if mergeSafe(recs[survivor].Value, sources, recs, cfg.MaxMergeLossFraction) {
				actions = append(actions, Action{
					Kind:       ActionMerge,
					SurvivorID: survivor,
					SourceIDs:  sources,
					Room:       recs[survivor].Room,
					Hall:       recs[survivor].Hall,
				})
				continue
			}
		}

		actions = append(actions, Action{
			Kind:      ActionAbstract,
			SourceIDs: ids,
			Room:      dominantRoom(ids, recs),
			Hall:      "semantic",
		})
	}
	return actions
}

var wordRe = regexp.MustCompile(`[a-z0-9]+`)

func contentTokens(s string) map[string]bool {
	set := map[string]bool{}
	for _, t := range wordRe.FindAllString(strings.ToLower(s), -1) {
		set[t] = true
	}
	return set
}

// mergeSafe reports whether a mechanical merge would preserve the cluster's
// content. A merge keeps only the survivor's value and supersedes every source,
// so any source token absent from the survivor is information the merge would
// drop. If any source loses more than maxLoss of its tokens, the merge is
// unsafe (suspected envelope-collapse) and the caller abstracts instead.
// maxLoss <= 0 disables the guard (preserves cosine-only merge behavior).
func mergeSafe(survivorVal string, sources []string, recs map[string]MemoryRecord, maxLoss float64) bool {
	if maxLoss <= 0 {
		return true
	}
	sv := contentTokens(survivorVal)
	for _, id := range sources {
		src := contentTokens(recs[id].Value)
		if len(src) == 0 {
			continue
		}
		var lost int
		for tok := range src {
			if !sv[tok] {
				lost++
			}
		}
		if float64(lost)/float64(len(src)) > maxLoss {
			return false
		}
	}
	return true
}

func allTight(edges []SimEdge, tauHigh float64) bool {
	if len(edges) == 0 {
		return false
	}
	for _, e := range edges {
		if e.Score < tauHigh {
			return false
		}
	}
	return true
}

func newest(ids []string, recs map[string]MemoryRecord) string {
	best := ids[0]
	for _, id := range ids[1:] {
		if recs[id].CreatedAt.After(recs[best].CreatedAt) {
			best = id
		}
	}
	return best
}

func dominantRoom(ids []string, recs map[string]MemoryRecord) string {
	count := map[string]int{}
	for _, id := range ids {
		count[recs[id].Room]++
	}
	best, bestN := "", -1
	for room, n := range count {
		if n > bestN {
			best, bestN = room, n
		}
	}
	return best
}
