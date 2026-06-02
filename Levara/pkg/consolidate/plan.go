package consolidate

import "sort"

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
			actions = append(actions, Action{
				Kind:       ActionMerge,
				SurvivorID: survivor,
				SourceIDs:  sources,
				Room:       recs[survivor].Room,
				Hall:       recs[survivor].Hall,
			})
			continue
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
