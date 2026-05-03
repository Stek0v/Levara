// Package graphdb provides batch write operations for Neo4j,
// mirroring Levara's Neo4j adapter Cypher patterns (UNWIND + MERGE + APOC).
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
	ID         string         // UUID string
	Label      string         // Dynamic label (class name, e.g. "Entity")
	Properties map[string]any // All serialized properties
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

	return &Writer{driver: driver, database: database}, nil
}

// NewWriterWithSchema creates a writer and ensures required schema objects.
// Use this for one-time bootstrap paths, not per-request query handlers.
func NewWriterWithSchema(ctx context.Context, url, username, password, database string) (*Writer, error) {
	w, err := NewWriter(ctx, url, username, password, database)
	if err != nil {
		return nil, err
	}
	if err := w.EnsureSchema(ctx); err != nil {
		w.Close(ctx)
		return nil, err
	}
	return w, nil
}

// Close releases the Neo4j driver.
func (w *Writer) Close(ctx context.Context) error {
	return w.driver.Close(ctx)
}

// EnsureSchema creates required constraints/indexes if they do not exist.
func (w *Writer) EnsureSchema(ctx context.Context) error {
	session := w.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: w.database})
	defer session.Close(ctx)

	for _, stmt := range requiredNeo4jSchemaStatements(baseLabel) {
		if _, err := session.Run(ctx, stmt, nil); err != nil {
			return err
		}
	}
	return nil
}

func requiredNeo4jSchemaStatements(label string) []string {
	return []string{
		fmt.Sprintf("CREATE CONSTRAINT IF NOT EXISTS FOR (n:`%s`) REQUIRE n.id IS UNIQUE", label),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS FOR (n:`%s`) ON (n.name)", label),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS FOR (n:`%s`) ON (n.dataset_id)", label),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS FOR (n:`%s`) ON (n.type)", label),
	}
}

// BatchWrite writes nodes and edges to Neo4j in batch using UNWIND.
// Mirrors Levara's add_nodes + add_edges Cypher patterns.
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

	// UNWIND + MERGE + APOC addLabels (same as Levara)
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
	if err := res.Err(); err != nil {
		return 0, fmt.Errorf("write nodes cypher: %w", err)
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

	// UNWIND + MATCH + apoc.merge.relationship (same as Levara)
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
	if err := res.Err(); err != nil {
		return 0, fmt.Errorf("write edges cypher: %w", err)
	}
	return len(edges), nil
}

// serializeProperties converts Go types to Neo4j-compatible property types.
// Mirrors Levara's serialize_properties: UUID→string, dict→JSON string.
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

// flattenEdgeProperties mirrors Levara's _flatten_edge_properties:
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

// GraphReadResult holds nodes and edges from a graph read query.
type GraphReadResult struct {
	Nodes []ReadNode
	Edges []ReadEdge
}

// ReadNode is a node returned from a graph read query.
type ReadNode struct {
	ID         string
	Label      string
	Properties map[string]any
}

// ReadEdge is an edge returned from a graph read query.
type ReadEdge struct {
	SourceID         string
	TargetID         string
	RelationshipType string
	Properties       map[string]any
}

// ReadFullGraph returns all nodes and edges. Mirrors Levara's get_graph_data().
func (w *Writer) ReadFullGraph(ctx context.Context) (GraphReadResult, error) {
	session := w.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: w.database})
	defer session.Close(ctx)

	var result GraphReadResult

	res, err := session.Run(ctx,
		fmt.Sprintf("MATCH (n:`%s`) RETURN n.id AS id, labels(n) AS labels, properties(n) AS props", baseLabel), nil)
	if err != nil {
		return result, fmt.Errorf("read nodes: %w", err)
	}
	for res.Next(ctx) {
		rec := res.Record()
		id, _ := rec.Get("id")
		labels, _ := rec.Get("labels")
		props, _ := rec.Get("props")
		label := ""
		if ls, ok := labels.([]any); ok {
			for _, l := range ls {
				if s, ok := l.(string); ok && s != baseLabel {
					label = s
					break
				}
			}
		}
		result.Nodes = append(result.Nodes, ReadNode{
			ID: fmt.Sprint(id), Label: label, Properties: toStringMap(props),
		})
	}

	res, err = session.Run(ctx,
		fmt.Sprintf("MATCH (n:`%s`)-[r]->(m:`%s`) RETURN n.id AS src, m.id AS tgt, TYPE(r) AS typ, properties(r) AS props",
			baseLabel, baseLabel), nil)
	if err != nil {
		return result, fmt.Errorf("read edges: %w", err)
	}
	for res.Next(ctx) {
		rec := res.Record()
		src, _ := rec.Get("src")
		tgt, _ := rec.Get("tgt")
		typ, _ := rec.Get("typ")
		props, _ := rec.Get("props")
		result.Edges = append(result.Edges, ReadEdge{
			SourceID: fmt.Sprint(src), TargetID: fmt.Sprint(tgt),
			RelationshipType: fmt.Sprint(typ), Properties: toStringMap(props),
		})
	}

	return result, nil
}

// ReadIDFiltered returns nodes/edges touching the given IDs.
func (w *Writer) ReadIDFiltered(ctx context.Context, ids []string) (GraphReadResult, error) {
	session := w.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: w.database})
	defer session.Close(ctx)

	var result GraphReadResult
	query := fmt.Sprintf(`
		MATCH (a:`+"`%s`"+`)-[r]-(b:`+"`%s`"+`)
		WHERE a.id IN $ids OR b.id IN $ids
		WITH DISTINCT r, startNode(r) AS a, endNode(r) AS b
		RETURN properties(a) AS a_props, properties(b) AS b_props,
		       TYPE(r) AS typ, properties(r) AS r_props
	`, baseLabel, baseLabel)

	res, err := session.Run(ctx, query, map[string]any{"ids": ids})
	if err != nil {
		return result, fmt.Errorf("id-filtered read: %w", err)
	}

	nodesSeen := make(map[string]bool)
	for res.Next(ctx) {
		rec := res.Record()
		aProps, _ := rec.Get("a_props")
		bProps, _ := rec.Get("b_props")
		typ, _ := rec.Get("typ")
		rProps, _ := rec.Get("r_props")

		am := toStringMap(aProps)
		bm := toStringMap(bProps)
		aID := fmt.Sprint(am["id"])
		bID := fmt.Sprint(bm["id"])

		if !nodesSeen[aID] {
			nodesSeen[aID] = true
			result.Nodes = append(result.Nodes, ReadNode{ID: aID, Properties: am})
		}
		if !nodesSeen[bID] {
			nodesSeen[bID] = true
			result.Nodes = append(result.Nodes, ReadNode{ID: bID, Properties: bm})
		}

		result.Edges = append(result.Edges, ReadEdge{
			SourceID: aID, TargetID: bID,
			RelationshipType: fmt.Sprint(typ), Properties: toStringMap(rProps),
		})
	}

	return result, nil
}

// ReadNeighbours returns direct neighbors of a node.
func (w *Writer) ReadNeighbours(ctx context.Context, nodeID string) (GraphReadResult, error) {
	return w.ReadIDFiltered(ctx, []string{nodeID})
}

// ReadSubgraph returns nodes matching label+names with their neighbors.
func (w *Writer) ReadSubgraph(ctx context.Context, label string, names []string) (GraphReadResult, error) {
	session := w.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: w.database})
	defer session.Close(ctx)

	var result GraphReadResult
	query := fmt.Sprintf(`
		UNWIND $names AS wantedName
		MATCH (n:`+"`%s`"+`)
		WHERE n.name = wantedName
		WITH collect(DISTINCT n) AS primary
		UNWIND primary AS p
		OPTIONAL MATCH (p)--(nbr)
		WITH primary, collect(DISTINCT nbr) AS nbrs
		WITH primary + nbrs AS nodelist
		UNWIND nodelist AS node
		WITH collect(DISTINCT node) AS nodes
		OPTIONAL MATCH (a)-[r]-(b)
		WHERE a IN nodes AND b IN nodes
		WITH nodes, collect(DISTINCT r) AS rels
		RETURN
		  [n IN nodes | {id: n.id, properties: properties(n)}] AS rawNodes,
		  [r IN rels | {type: TYPE(r), properties: properties(r)}] AS rawRels
	`, label)

	res, err := session.Run(ctx, query, map[string]any{"names": names})
	if err != nil {
		return result, fmt.Errorf("subgraph read: %w", err)
	}

	if res.Next(ctx) {
		rawNodes, _ := res.Record().Get("rawNodes")
		rawRels, _ := res.Record().Get("rawRels")

		if nodes, ok := rawNodes.([]any); ok {
			for _, n := range nodes {
				if nm, ok := n.(map[string]any); ok {
					props := toStringMap(nm["properties"])
					result.Nodes = append(result.Nodes, ReadNode{
						ID: fmt.Sprint(nm["id"]), Properties: props,
					})
				}
			}
		}

		if rels, ok := rawRels.([]any); ok {
			for _, r := range rels {
				if rm, ok := r.(map[string]any); ok {
					props := toStringMap(rm["properties"])
					srcID := fmt.Sprint(props["source_node_id"])
					tgtID := fmt.Sprint(props["target_node_id"])
					result.Edges = append(result.Edges, ReadEdge{
						SourceID: srcID, TargetID: tgtID,
						RelationshipType: fmt.Sprint(rm["type"]), Properties: props,
					})
				}
			}
		}
	}

	return result, nil
}

// Query executes an arbitrary Cypher query and returns rows as []map[string]any.
func (w *Writer) Query(ctx context.Context, cypher string, params map[string]any) ([]map[string]any, error) {
	session := w.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: w.database})
	defer session.Close(ctx)

	res, err := session.Run(ctx, cypher, params)
	if err != nil {
		return nil, fmt.Errorf("cypher query: %w", err)
	}

	var rows []map[string]any
	for res.Next(ctx) {
		rec := res.Record()
		row := make(map[string]any, len(rec.Keys))
		for _, key := range rec.Keys {
			val, _ := rec.Get(key)
			row[key] = val
		}
		rows = append(rows, row)
	}
	if err := res.Err(); err != nil {
		return rows, fmt.Errorf("cypher iterate: %w", err)
	}
	return rows, nil
}

func toStringMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}
