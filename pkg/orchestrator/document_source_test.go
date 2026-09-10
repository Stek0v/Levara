package orchestrator

import (
	"encoding/json"
	"github.com/stek0v/levara/pkg/graph"
	"testing"
)

func TestDocumentGraphAssertionsKeepSourceGeneration(t *testing.T) {
	build := func(cfg Config) graph.DeduplicateResult {
		return scopeDocumentGraph(cfg, graph.Deduplicate([]graph.DedupNode{{ID: "person", Name: "Alice"}, {ID: "team", Name: "Team"}}, []graph.DedupEdge{{SourceID: "person", TargetID: "team", RelationshipName: "works_at", EdgeText: "Alice works at Team"}}))
	}
	base := Config{DatasetID: "a", DocumentID: "doc", ContentRevision: 1}
	first := build(base)
	for i, cfg := range []Config{base, {DatasetID: "b", DocumentID: "doc", ContentRevision: 1}, {DatasetID: "a", DocumentID: "other", ContentRevision: 1}, {DatasetID: "a", DocumentID: "doc", ContentRevision: 2}} {
		r := build(cfg)
		if (r.Nodes[0].ID == first.Nodes[0].ID) != (i == 0) {
			t.Fatalf("source collision: %+v", cfg)
		}
		if len(r.Triplets) != 1 || r.Edges[0].SourceID != r.Nodes[0].ID || r.Triplets[0].FromNodeID != r.Nodes[0].ID {
			t.Fatalf("broken assertion references: %+v", r)
		}
		if r.Nodes[0].SourceDocID != cfg.DocumentID || r.Nodes[0].ContentRevision != cfg.ContentRevision || r.Edges[0].SourceDocID != cfg.DocumentID || r.Edges[0].ContentRevision != cfg.ContentRevision {
			t.Fatalf("lost source: %+v", r)
		}
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(documentMetadata(base, map[string]any{"name": "quoted \" entity", "text": "private\ntext"})), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["document_id"] != "doc" || metadata["content_revision"] != float64(1) {
		t.Fatalf("lost metadata source: %+v", metadata)
	}
}
