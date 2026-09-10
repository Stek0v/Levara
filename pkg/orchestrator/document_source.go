package orchestrator

import (
	"encoding/json"
	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/graph"
)

func documentScopedID(cfg Config, id string) string {
	scope, _ := json.Marshal([]any{cfg.DatasetID, cfg.DocumentID, cfg.ContentRevision, cfg.Generation, id})
	return uuid.NewSHA1(uuid.NameSpaceOID, scope).String()
}

// Keep assertions from different source generations separate, even when the
// extracted entity has the same name. Retrieval combines authorized assertions.
func scopeDocumentGraph(cfg Config, result graph.DeduplicateResult) graph.DeduplicateResult {
	if cfg.DocumentID == "" {
		return result
	}
	for i := range result.Nodes {
		n := &result.Nodes[i]
		n.ID = documentScopedID(cfg, n.ID)
		n.SourceDocID = cfg.DocumentID
		n.ContentRevision = cfg.ContentRevision
		n.Generation, n.Collection = cfg.Generation, cfg.Collection
	}
	for i := range result.Edges {
		e := &result.Edges[i]
		e.SourceID = documentScopedID(cfg, e.SourceID)
		e.TargetID = documentScopedID(cfg, e.TargetID)
		e.SourceDocID = cfg.DocumentID
		e.ContentRevision = cfg.ContentRevision
		e.Generation, e.Collection = cfg.Generation, cfg.Collection
	}
	return graph.Deduplicate(result.Nodes, result.Edges)
}

func documentMetadata(cfg Config, fields map[string]any) string {
	fields["dataset_id"] = cfg.DatasetID
	if cfg.DocumentID != "" {
		fields["document_id"] = cfg.DocumentID
		fields["content_revision"] = cfg.ContentRevision
		fields["generation"], fields["collection"] = cfg.Generation, cfg.Collection
	}
	b, _ := json.Marshal(fields)
	return string(b)
}
