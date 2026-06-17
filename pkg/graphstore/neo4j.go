package graphstore

import (
	"context"

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
	result, err := n.writer.ReadSubgraph(ctx, "", entityNames)
	if err != nil {
		return nil, err
	}
	out := make([]GraphContext, 0, len(result.Edges))
	nodes := map[string]graphdb.ReadNode{}
	for _, node := range result.Nodes {
		nodes[node.ID] = node
	}
	for _, edge := range result.Edges {
		src := nodes[edge.SourceID]
		tgt := nodes[edge.TargetID]
		out = append(out, GraphContext{
			SourceName:   propertyString(src.Properties, "name"),
			SourceType:   src.Label,
			Relationship: edge.RelationshipType,
			TargetName:   propertyString(tgt.Properties, "name"),
			TargetType:   tgt.Label,
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
