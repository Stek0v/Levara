package contract

import (
	"encoding/json"
	"sort"
	"testing"
)

func TestStatusValues(t *testing.T) {
	for _, s := range []Status{StatusCanonical, StatusLegacy, StatusAlias, StatusOps, StatusDeprecated} {
		if s == "" {
			t.Fatalf("empty Status constant")
		}
	}
}

func TestContractJSONRoundTrip(t *testing.T) {
	in := Contract{
		GeneratedAt: "2026-05-24T00:00:00Z",
		GitRev:      "deadbeef",
		REST:        []RESTRoute{{Method: "GET", Path: "/health", Status: StatusOps, Group: "ops"}},
		GRPC:        []GRPCMethod{{Service: "levara.v1.LevaraService", Method: "Search", Status: StatusCanonical}},
		MCP:         []MCPTool{{Name: "search", Status: StatusCanonical, Group: "search"}},
		Schema:      []SchemaObject{{Provider: "postgres", Kind: "table", Name: "users"}},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Contract
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.REST[0].Path != "/health" || out.GRPC[0].Method != "Search" {
		t.Fatalf("round-trip lost fields: %+v", out)
	}
}

func TestRESTRouteSortStable(t *testing.T) {
	rs := []RESTRoute{
		{Method: "POST", Path: "/a"},
		{Method: "GET", Path: "/a"},
		{Method: "GET", Path: "/b"},
	}
	sort.Sort(ByRESTRoute(rs))
	if rs[0].Method != "GET" || rs[0].Path != "/a" {
		t.Fatalf("wrong order: %+v", rs)
	}
}
