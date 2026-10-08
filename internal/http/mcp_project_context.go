package http

import (
	"context"
	"encoding/json"
	"errors"

	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/mcp"
)

func (h *mcpHandler) ProjectContextAggregates(ctx context.Context, collection string) (mcp.ProjectContextAggregates, error) {
	var out mcp.ProjectContextAggregates
	out.EntityTypes = map[string]int{}
	if h.cfg.DB == nil {
		return out, errors.New("database not configured")
	}
	actor := workspaceActorFromMCP(ctx)

	type publication struct {
		dataset, document, generation, hash string
		revision, sourceRevision            int64
	}
	rows, err := h.cfg.DB.QueryContext(ctx, Q(`SELECT dataset_id,data_id,content_revision,generation,source_revision,raw_content_hash
		FROM document_index_publications WHERE collection_name=$1 AND lineage_verified=1`), collection)
	if err != nil {
		return out, err
	}
	var publications []publication
	for rows.Next() {
		var p publication
		if err := rows.Scan(&p.dataset, &p.document, &p.revision, &p.generation, &p.sourceRevision, &p.hash); err != nil {
			rows.Close()
			return out, err
		}
		publications = append(publications, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	for _, p := range publications {
		allowed, err := searchDocumentAllowed(ctx, h.cfg, actor, searchDocumentSource{DatasetID: p.dataset, DocumentID: p.document, Collection: collection, ContentRevision: p.revision, Generation: p.generation, SourceRevision: p.sourceRevision, RawContentHash: p.hash, Derived: true})
		if err != nil {
			return out, err
		}
		if allowed {
			out.PublishedDocuments++
		}
	}

	rows, err = h.cfg.DB.QueryContext(ctx, Q(`SELECT type,dataset_id,CAST(properties AS TEXT) FROM graph_nodes`))
	if err != nil {
		return out, err
	}
	type graphNode struct{ typ, dataset, properties string }
	var nodes []graphNode
	for rows.Next() {
		var node graphNode
		if err := rows.Scan(&node.typ, &node.dataset, &node.properties); err != nil {
			rows.Close()
			return out, err
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	for _, node := range nodes {
		source, err := decodeSearchDocumentSource(node.properties)
		if err != nil || source.Collection != collection || node.typ == "" {
			continue
		}
		if h.GraphAssertionsAllowed(ctx, []mcp.GraphAssertion{{DatasetID: node.dataset, Properties: []byte(node.properties)}}) {
			out.EntityTypes[node.typ]++
		}
	}

	type interaction struct {
		query, response, sources string
		requiresAdmin            int
	}
	rows, err = h.cfg.DB.QueryContext(ctx, Q(`SELECT i.query,i.response,p.sources,p.requires_admin
		FROM interaction_provenance p JOIN interactions i ON i.id=p.interaction_id
		WHERE p.owner_id=$1 AND p.tenant_id=$2 AND p.kind='server'
		ORDER BY p.created_at DESC,p.id DESC LIMIT 128`), actor.UserID, actor.TenantID)
	if err != nil {
		return out, err
	}
	var interactions []interaction
	for rows.Next() {
		var item interaction
		if err := rows.Scan(&item.query, &item.response, &item.sources, &item.requiresAdmin); err != nil {
			rows.Close()
			return out, err
		}
		interactions = append(interactions, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	for _, item := range interactions {
		var sources []searchDocumentSource
		if json.Unmarshal([]byte(item.sources), &sources) != nil || len(sources) == 0 {
			continue
		}
		if item.requiresAdmin != 0 {
			admin, err := (accesspkg.SQLPolicy{DB: h.cfg.DB, Q: Q, QA: QArgs}).IsSuperuser(ctx, actor.UserID)
			if err != nil {
				return out, err
			}
			if !admin || actor.TenantID != "" {
				continue
			}
		}
		allowed := true
		for _, source := range sources {
			if source.Collection != collection {
				allowed = false
				break
			}
			ok, err := searchDocumentAllowed(ctx, h.cfg, actor, source)
			if err != nil {
				return out, err
			}
			if !ok {
				allowed = false
				break
			}
		}
		if allowed {
			out.RecentInteractions = append(out.RecentInteractions, mcp.ProjectContextInteraction{Query: item.query, Response: item.response})
			if len(out.RecentInteractions) == 5 {
				break
			}
		}
	}
	return out, nil
}
