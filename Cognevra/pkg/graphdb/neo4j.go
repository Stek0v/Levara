// Package graphdb provides batch write operations for Neo4j,
// mirroring Cognee's Neo4j adapter Cypher patterns (UNWIND + MERGE + APOC).
package graphdb

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

const baseLabel = "__Node__"

// NodeRecord represents a node to write to Neo4j.
type NodeRecord struct {
	ID         string            // UUID string
	Label      string            // Dynamic label (class name, e.g. "Entity")
	Properties map[string]any    // All serialized properties
}

// EdgeRecord represents an edge to write to Neo4j.
type EdgeRecord struct {
	SourceID         string
	TargetID         string
	RelationshipName string
	Properties       map[string]any
}

// BatchWriteResult holds the outcome of a batch write operation.
type BatchWriteResult struct {
	NodesWritten int
	EdgesWritten int
	Errors       []string
}

// Writer handles batch writes to Neo4j.
type Writer struct {
	driver   neo4j.DriverWithContext
	database string
}

// NewWriter creates a Neo4j writer. url is bolt:// or neo4j:// URI.
func NewWriter(ctx context.Context, url, username, password, database string) (*Writer, error) {
	var auth neo4j.AuthToken
	if username != "" && password != "" {
		auth = neo4j.BasicAuth(username, password, "")
	} else {
		auth = neo4j.NoAuth()
	}

	driver, err := neo4j.NewDriverWithContext(url, auth,
		func(config *neo4j.Config) {
			config.MaxConnectionLifetime = 120_000_000_000 // 120s in nanoseconds
		},
	)
	if err != nil {
		return nil, fmt.Errorf("neo4j driver: %w", err)
	}

	// Verify connectivity
	if err := driver.VerifyConnectivity(ctx); err != nil {
		driver.Close(ctx)
		return nil, fmt.Errorf("neo4j connectivity: %w", err)
	}

	w := &Writer{driver: driver, database: database}

	// Ensure unique constraint
	if err := w.ensureConstraint(ctx); err != nil {
		driver.Close(ctx)
		return nil, err
	}

	return w, nil
}

// Close releases the Neo4j driver.
func (w *Writer) Close(ctx context.Context) error {
	return w.driver.Close(ctx)
}

func (w *Writer) ensureConstraint(ctx context.Context) error {
	session := w.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: w.database})
	defer session.Close(ctx)

	_, err := session.Run(ctx,
		fmt.Sprintf("CREATE CONSTRAINT IF NOT EXISTS FOR (n:`%s`) REQUIRE n.id IS UNIQUE", baseLabel),
		nil,
	)
	return err
}

// BatchWrite writes nodes and edges to Neo4j in batch using UNWIND.
// Mirrors Cognee's add_nodes + add_edges Cypher patterns.
func (w *Writer) BatchWrite(ctx context.Context, nodes []NodeRecord, edges []EdgeRecord) BatchWriteResult {
	result := BatchWriteResult{}

	if len(nodes) > 0 {
		n, err := w.writeNodes(ctx, nodes)
		result.NodesWritten = n
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("nodes: %v", err))
		}
	}

	if len(edges) > 0 {
		n, err := w.writeEdges(ctx, edges)
		result.EdgesWritten = n
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("edges: %v", err))
		}
	}

	return result
}

func (w *Writer) writeNodes(ctx context.Context, nodes []NodeRecord) (int, error) {
	session := w.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: w.database})
	defer session.Close(ctx)

	// Build batch payload
	batch := make([]map[string]any, len(nodes))
	for i, n := range nodes {
		props := serializeProperties(n.Properties)
		props["id"] = n.ID
		batch[i] = map[string]any{
			"node_id":    n.ID,
			"label":      n.Label,
			"properties": props,
		}
	}

	// UNWIND + MERGE + APOC addLabels (same as Cognee)
	query := fmt.Sprintf(`
		UNWIND $nodes AS node
		MERGE (n: `+"`%s`"+` {id: node.node_id})
		ON CREATE SET n += node.properties, n.updated_at = timestamp()
		ON MATCH SET n += node.properties, n.updated_at = timestamp()
		WITH n, node.label AS label
		CALL apoc.create.addLabels(n, [label]) YIELD node AS labeledNode
		RETURN count(labeledNode) AS cnt
	`, baseLabel)

	res, err := session.Run(ctx, query, map[string]any{"nodes": batch})
	if err != nil {
		return 0, fmt.Errorf("write nodes: %w", err)
	}

	if res.Next(ctx) {
		cnt, _ := res.Record().Get("cnt")
		if c, ok := cnt.(int64); ok {
			return int(c), nil
		}
	}
	return len(nodes), nil
}

func (w *Writer) writeEdges(ctx context.Context, edges []EdgeRecord) (int, error) {
	session := w.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: w.database})
	defer session.Close(ctx)

	batch := make([]map[string]any, len(edges))
	for i, e := range edges {
		props := flattenEdgeProperties(e.Properties)
		props["source_node_id"] = e.SourceID
		props["target_node_id"] = e.TargetID
		batch[i] = map[string]any{
			"from_node":         e.SourceID,
			"to_node":           e.TargetID,
			"relationship_name": e.RelationshipName,
			"properties":        props,
		}
	}

	// UNWIND + MATCH + apoc.merge.relationship (same as Cognee)
	query := fmt.Sprintf(`
		UNWIND $edges AS edge
		MATCH (from_node: `+"`%s`"+` {id: edge.from_node})
		MATCH (to_node: `+"`%s`"+` {id: edge.to_node})
		CALL apoc.merge.relationship(
			from_node,
			edge.relationship_name,
			{source_node_id: edge.from_node, target_node_id: edge.to_node},
			edge.properties,
			to_node
		) YIELD rel
		RETURN count(rel) AS cnt
	`, baseLabel, baseLabel)

	res, err := session.Run(ctx, query, map[string]any{"edges": batch})
	if err != nil {
		return 0, fmt.Errorf("write edges: %w", err)
	}

	if res.Next(ctx) {
		cnt, _ := res.Record().Get("cnt")
		if c, ok := cnt.(int64); ok {
			return int(c), nil
		}
	}
	return len(edges), nil
}

// serializeProperties converts Go types to Neo4j-compatible property types.
// Mirrors Cognee's serialize_properties: UUID→string, dict→JSON string.
func serializeProperties(props map[string]any) map[string]any {
	if props == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(props))
	for k, v := range props {
		switch v.(type) {
		case map[string]any:
			b, _ := json.Marshal(v)
			out[k] = string(b)
		case []any:
			b, _ := json.Marshal(v)
			out[k] = string(b)
		default:
			out[k] = v
		}
	}
	return out
}

// flattenEdgeProperties mirrors Cognee's _flatten_edge_properties:
// weights dict → weight_X prefixed keys, other dicts/lists → JSON strings.
func flattenEdgeProperties(props map[string]any) map[string]any {
	if props == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(props))
	for k, v := range props {
		switch val := v.(type) {
		case map[string]any:
			if k == "weights" {
				for wk, wv := range val {
					out["weight_"+wk] = wv
				}
			} else {
				b, _ := json.Marshal(val)
				out[k+"_json"] = string(b)
			}
		case []any:
			b, _ := json.Marshal(val)
			out[k+"_json"] = string(b)
		default:
			out[k] = v
		}
		_ = strings.TrimSpace("") // keep strings import used
	}
	return out
}
