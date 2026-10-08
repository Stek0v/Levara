package graphstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/stek0v/levara/pkg/graphdb"
)

// Neo4jGraphStore adapts the legacy graphdb.Writer to the GraphStore contract.
type Neo4jGraphStore struct {
	writer *graphdb.Writer
}

var _ GraphStore = (*Neo4jGraphStore)(nil)

func NewNeo4jGraphStore(writer *graphdb.Writer) *Neo4jGraphStore {
	return &Neo4jGraphStore{writer: writer}
}

func (n *Neo4jGraphStore) Close() error {
	return nil
}

func (n *Neo4jGraphStore) Query1Hop(ctx context.Context, entityNames []string) ([]GraphContext, error) {
	return n.QueryNHop(ctx, entityNames, 1)
}

func (n *Neo4jGraphStore) Query2Hop(ctx context.Context, entityNames []string) ([]GraphContext, error) {
	return n.QueryNHop(ctx, entityNames, 2)
}

func (n *Neo4jGraphStore) QueryNHop(ctx context.Context, entityNames []string, hops int) ([]GraphContext, error) {
	if n == nil || n.writer == nil || len(entityNames) == 0 {
		return nil, nil
	}
	if hops <= 0 {
		hops = 1
	}
	if hops > 8 {
		hops = 8
	}
	names := make([]string, len(entityNames))
	for i, name := range entityNames {
		names[i] = strings.ToLower(name)
	}
	// Frontier nodes contribute their incident edges, matching native SQL depth.
	query := fmt.Sprintf(`
		MATCH (seed:__Node__) WHERE toLower(seed.name) IN $names
		MATCH p=(seed)-[*0..%d]-(source:__Node__)
		WHERE all(node IN nodes(p) WHERE node:__Node__)
		WITH DISTINCT source
		MATCH (source)-[r]-(target:__Node__)
		WITH DISTINCT source, type(r) AS relationship, target
		ORDER BY source.id, relationship, target.id
		LIMIT 100
		RETURN source.name AS source_name,
		       coalesce(source.type, head([label IN labels(source) WHERE label <> '__Node__']), '') AS source_type,
		       relationship,
		       target.name AS target_name,
		       coalesce(target.type, head([label IN labels(target) WHERE label <> '__Node__']), '') AS target_type
	`, hops-1)
	rows, err := n.writer.Query(ctx, query, map[string]any{"names": names})
	if err != nil {
		return nil, err
	}
	out := make([]GraphContext, 0, len(rows))
	for _, row := range rows {
		out = append(out, GraphContext{
			SourceName:   propertyString(row, "source_name"),
			SourceType:   propertyString(row, "source_type"),
			Relationship: propertyString(row, "relationship"),
			TargetName:   propertyString(row, "target_name"),
			TargetType:   propertyString(row, "target_type"),
		})
	}
	return out, nil
}

func (n *Neo4jGraphStore) ReadFullGraph(ctx context.Context) (graphdb.GraphReadResult, error) {
	if n == nil || n.writer == nil {
		return graphdb.GraphReadResult{}, nil
	}
	return n.writer.ReadFullGraph(ctx)
}

func (n *Neo4jGraphStore) PathBetween(ctx context.Context, q graphdb.PathQuery) (graphdb.PathResult, error) {
	if n == nil || n.writer == nil {
		return graphdb.PathResult{AsOf: q.AsOf}, nil
	}
	return n.writer.PathBetween(ctx, q)
}

func (n *Neo4jGraphStore) WriteGraph(ctx context.Context, datasetID string, nodes []NodeRecord, edges []EdgeRecord) BatchWriteResult {
	if n == nil || n.writer == nil || (len(nodes) == 0 && len(edges) == 0) {
		return BatchWriteResult{}
	}
	neoNodes := make([]graphdb.NodeRecord, len(nodes))
	for i, node := range nodes {
		props := cloneMap(node.Properties)
		if node.Name != "" {
			props["name"] = node.Name
		}
		if node.Description != "" {
			props["description"] = node.Description
		}
		if node.Type != "" {
			props["type"] = node.Type
		}
		if datasetID != "" {
			props["dataset_id"] = datasetID
		}
		neoNodes[i] = graphdb.NodeRecord{ID: node.ID, Label: node.Type, Properties: props}
	}
	neoEdges := make([]graphdb.EdgeRecord, len(edges))
	for i, edge := range edges {
		props := cloneMap(edge.Properties)
		if edge.ID != "" {
			if propertyID, exists := props["id"]; exists && propertyID != nil && propertyID != edge.ID {
				return BatchWriteResult{Errors: []string{fmt.Sprintf("edge ID conflicts with property ID: %s", edge.ID)}}
			}
			props["id"] = edge.ID
		}
		if datasetID != "" {
			props["dataset_id"] = datasetID
		}
		neoEdges[i] = graphdb.EdgeRecord{
			SourceID:         edge.SourceID,
			TargetID:         edge.TargetID,
			RelationshipName: edge.RelationshipName,
			Properties:       props,
		}
	}
	res := n.writer.BatchWrite(ctx, neoNodes, neoEdges)
	return BatchWriteResult{NodesWritten: res.NodesWritten, EdgesWritten: res.EdgesWritten, Errors: res.Errors}
}
