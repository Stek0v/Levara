// Router probe: run the production heuristic router over the gate's routing
// queries and dump decisions as JSON for comparison with FRIDA-Decisions.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/stek0v/levara/pkg/router"
)

type item struct {
	Query string `json:"query"`
	Label string `json:"label"`
}

func main() {
	var items []item
	if err := json.NewDecoder(os.Stdin).Decode(&items); err != nil {
		fmt.Fprintln(os.Stderr, "stdin:", err)
		os.Exit(1)
	}
	caps := router.Capabilities{HasEmbedding: true, HasBM25: true, HasNeo4j: true,
		HasLLM: true, HasPostgres: true, AllowCypher: true, HasCommunities: true}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		d := router.Route(it.Query, caps)
		out = append(out, map[string]any{
			"query": it.Query, "label": it.Label,
			"route": d.SearchType, "confidence": d.Confidence, "reason": d.Reason,
		})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "")
	_ = enc.Encode(out)
}
