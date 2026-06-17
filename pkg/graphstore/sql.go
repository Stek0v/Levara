package graphstore

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stek0v/levara/internal/metrics"
	"github.com/stek0v/levara/pkg/graphdb"
)

// SQLGraphStore implements GraphStore on top of Levara's graph_nodes and
// graph_edges tables. It is intentionally dialect-light: the read queries avoid
// placeholders so the same code works for SQLite and PostgreSQL.
type SQLGraphStore struct {
	db *sql.DB
}

var _ GraphStore = (*SQLGraphStore)(nil)
var _ GraphStore = (*PostgresGraphStore)(nil)

// NewSQLGraphStore creates a graph store backed by Levara SQL graph tables.
func NewSQLGraphStore(db *sql.DB) *SQLGraphStore {
	return &SQLGraphStore{db: db}
}

func (s *SQLGraphStore) Close() error { return nil }

// WriteGraph upserts nodes and edges into graph_nodes/graph_edges in one
// transaction. It accepts both graphstore-native records and Neo4j-style records
// projected into Properties.
func (s *SQLGraphStore) WriteGraph(ctx context.Context, datasetID string, nodes []NodeRecord, edges []EdgeRecord) (result BatchWriteResult) {
	if s == nil || s.db == nil || (len(nodes) == 0 && len(edges) == 0) {
		return result
	}
	var err error
	defer metrics.ObserveExternalCall("sql-graph", "write", time.Now(), &err)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		result.Errors = append(result.Errors, "begin tx: "+err.Error())
		return result
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	for _, n := range nodes {
		name := firstString(n.Name, propertyString(n.Properties, "name"))
		typ := firstString(n.Type, propertyString(n.Properties, "type"))
		desc := firstString(n.Description, propertyString(n.Properties, "description"))
		props := cloneMap(n.Properties)
		props["name"] = name
		props["type"] = typ
		props["description"] = desc
		if datasetID != "" {
			props["dataset_id"] = datasetID
		}
		propsJSON, _ := json.Marshal(props)

		_, err = tx.ExecContext(ctx,
			`INSERT INTO graph_nodes (id, name, type, description, properties, dataset_id, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			 ON CONFLICT (id) DO UPDATE SET
				name = EXCLUDED.name,
				type = EXCLUDED.type,
				description = EXCLUDED.description,
				properties = EXCLUDED.properties,
				dataset_id = EXCLUDED.dataset_id,
				updated_at = EXCLUDED.updated_at`,
			n.ID, name, typ, desc, string(propsJSON), datasetID, now, now)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("upsert node %s: %v", n.ID, err))
			return result
		}
		result.NodesWritten++
	}

	for _, e := range edges {
		id := e.ID
		if id == "" {
			id = e.SourceID + "_" + e.RelationshipName + "_" + e.TargetID
		}
		props := cloneMap(e.Properties)
		if datasetID != "" {
			props["dataset_id"] = datasetID
		}
		propsJSON, _ := json.Marshal(props)
		validFrom := now
		if raw, ok := props["valid_from"].(string); ok && raw != "" {
			if ts := parseSQLTime(raw); ts != 0 {
				validFrom = time.Unix(ts, 0).UTC()
			}
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO graph_edges (id, source_id, target_id, relationship_name, properties, valid_from, dataset_id, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $6, $6)
			 ON CONFLICT (id) DO UPDATE SET
				source_id = EXCLUDED.source_id,
				target_id = EXCLUDED.target_id,
				relationship_name = EXCLUDED.relationship_name,
				properties = EXCLUDED.properties,
				dataset_id = EXCLUDED.dataset_id,
				updated_at = EXCLUDED.updated_at`,
			id, e.SourceID, e.TargetID, e.RelationshipName, string(propsJSON), validFrom, datasetID)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("upsert edge %s: %v", id, err))
			return result
		}
		result.EdgesWritten++
	}

	if err = tx.Commit(); err != nil {
		result.Errors = append(result.Errors, "commit: "+err.Error())
		return result
	}
	return result
}

func (s *SQLGraphStore) Query1Hop(ctx context.Context, entityNames []string) ([]GraphContext, error) {
	return s.QueryNHop(ctx, entityNames, 1)
}

func (s *SQLGraphStore) Query2Hop(ctx context.Context, entityNames []string) ([]GraphContext, error) {
	return s.QueryNHop(ctx, entityNames, 2)
}

func (s *SQLGraphStore) QueryNHop(ctx context.Context, entityNames []string, hops int) (_ []GraphContext, err error) {
	if s == nil || s.db == nil || len(entityNames) == 0 {
		return nil, nil
	}
	defer metrics.ObserveExternalCall("sql-graph", "read", time.Now(), &err)

	if hops <= 0 {
		hops = 1
	}
	if hops > 8 {
		hops = 8
	}

	graph, err := s.readGraphRows(ctx)
	if err != nil {
		return nil, err
	}
	nameSet := make(map[string]bool, len(entityNames))
	for _, name := range entityNames {
		nameSet[strings.ToLower(name)] = true
	}

	type queueItem struct {
		id    string
		depth int
	}
	seen := make(map[string]bool)
	var queue []queueItem
	for _, n := range graph.nodes {
		if nameSet[strings.ToLower(n.name)] {
			queue = append(queue, queueItem{id: n.id})
			seen[n.id] = true
		}
	}

	outSeen := make(map[string]bool)
	var out []GraphContext
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= hops {
			continue
		}
		for _, e := range graph.adj[cur.id] {
			src := graph.nodes[e.sourceID]
			tgt := graph.nodes[e.targetID]
			if src.id == "" || tgt.id == "" {
				continue
			}
			key := src.id + "\x00" + e.relationship + "\x00" + tgt.id
			if !outSeen[key] {
				outSeen[key] = true
				out = append(out, GraphContext{
					SourceName:   src.name,
					SourceType:   src.typ,
					Relationship: e.relationship,
					TargetName:   tgt.name,
					TargetType:   tgt.typ,
				})
			}
			if !seen[tgt.id] {
				seen[tgt.id] = true
				queue = append(queue, queueItem{id: tgt.id, depth: cur.depth + 1})
			}
		}
	}
	if len(out) > 100 {
		out = out[:100]
	}
	return out, nil
}

// ReadFullGraph returns all graph rows in the existing graphdb DTO.
func (s *SQLGraphStore) ReadFullGraph(ctx context.Context) (_ graphdb.GraphReadResult, err error) {
	if s == nil || s.db == nil {
		return graphdb.GraphReadResult{}, nil
	}
	defer metrics.ObserveExternalCall("sql-graph", "read_full", time.Now(), &err)

	graph, err := s.readGraphRows(ctx)
	if err != nil {
		return graphdb.GraphReadResult{}, err
	}
	result := graphdb.GraphReadResult{
		Nodes: make([]graphdb.ReadNode, 0, len(graph.nodes)),
		Edges: make([]graphdb.ReadEdge, 0, len(graph.edges)),
	}
	nodeIDs := make([]string, 0, len(graph.nodes))
	for id := range graph.nodes {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Strings(nodeIDs)
	for _, id := range nodeIDs {
		n := graph.nodes[id]
		props := cloneMap(n.properties)
		props["name"] = n.name
		props["type"] = n.typ
		props["description"] = n.description
		if n.datasetID != "" {
			props["dataset_id"] = n.datasetID
		}
		result.Nodes = append(result.Nodes, graphdb.ReadNode{ID: n.id, Label: n.typ, Properties: props})
	}
	for _, e := range graph.edges {
		props := cloneMap(e.properties)
		props["id"] = e.id
		props["valid_from"] = e.validFrom
		if e.validUntil != nil {
			props["valid_until"] = *e.validUntil
		}
		if e.datasetID != "" {
			props["dataset_id"] = e.datasetID
		}
		result.Edges = append(result.Edges, graphdb.ReadEdge{
			SourceID:         e.sourceID,
			TargetID:         e.targetID,
			RelationshipType: e.relationship,
			Properties:       props,
		})
	}
	return result, nil
}

// PathBetween returns a flat union of shortest-path edges. It loads the bounded
// SQL graph and performs BFS in-process, avoiding dialect-specific recursive CTE
// differences while preserving the public API contract.
func (s *SQLGraphStore) PathBetween(ctx context.Context, q graphdb.PathQuery) (_ graphdb.PathResult, err error) {
	if s == nil || s.db == nil {
		return graphdb.PathResult{AsOf: q.AsOf}, nil
	}
	defer metrics.ObserveExternalCall("sql-graph", "path", time.Now(), &err)

	if q.From == "" || q.To == "" {
		return graphdb.PathResult{}, fmt.Errorf("from and to required")
	}
	maxHops := q.MaxHops
	if maxHops <= 0 {
		maxHops = 4
	}
	if maxHops > 8 {
		maxHops = 8
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	cur, err := decodePathCursor(q.Cursor)
	if err != nil {
		return graphdb.PathResult{}, err
	}

	graph, err := s.readGraphRows(ctx)
	if err != nil {
		return graphdb.PathResult{}, err
	}
	if graph.nodes[q.From].id == "" || graph.nodes[q.To].id == "" {
		return graphdb.PathResult{AsOf: q.AsOf}, nil
	}

	edges := shortestPathEdges(graph, q.From, q.To, maxHops, q.AsOf)
	if cur.Offset >= len(edges) {
		return graphdb.PathResult{AsOf: q.AsOf}, nil
	}
	end := cur.Offset + limit
	out := graphdb.PathResult{AsOf: q.AsOf}
	if end < len(edges) {
		out.Edges = edges[cur.Offset:end]
		out.NextCursor = encodePathCursor(pathCursor{Offset: end})
	} else {
		out.Edges = edges[cur.Offset:]
	}
	return out, nil
}

type sqlNode struct {
	id          string
	name        string
	typ         string
	description string
	properties  map[string]any
	datasetID   string
}

type sqlEdge struct {
	id           string
	sourceID     string
	targetID     string
	relationship string
	properties   map[string]any
	validFrom    int64
	validUntil   *int64
	datasetID    string
}

type sqlGraph struct {
	nodes map[string]sqlNode
	edges []sqlEdge
	adj   map[string][]sqlEdge
}

func (s *SQLGraphStore) readGraphRows(ctx context.Context) (sqlGraph, error) {
	g := sqlGraph{nodes: map[string]sqlNode{}, adj: map[string][]sqlEdge{}}

	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, COALESCE(description,''), COALESCE(properties,'{}'), COALESCE(dataset_id,'') FROM graph_nodes`)
	if err != nil {
		return g, fmt.Errorf("read graph nodes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var n sqlNode
		var props string
		if err := rows.Scan(&n.id, &n.name, &n.typ, &n.description, &props, &n.datasetID); err != nil {
			return g, fmt.Errorf("scan graph node: %w", err)
		}
		n.properties = parseProperties(props)
		g.nodes[n.id] = n
	}
	if err := rows.Err(); err != nil {
		return g, fmt.Errorf("iterate graph nodes: %w", err)
	}

	erows, err := s.db.QueryContext(ctx, `SELECT id, source_id, target_id, relationship_name, COALESCE(properties,'{}'), valid_from, valid_until, COALESCE(dataset_id,'') FROM graph_edges`)
	if err != nil {
		return g, fmt.Errorf("read graph edges: %w", err)
	}
	defer erows.Close()
	for erows.Next() {
		var e sqlEdge
		var props string
		var vf, vu sql.NullString
		if err := erows.Scan(&e.id, &e.sourceID, &e.targetID, &e.relationship, &props, &vf, &vu, &e.datasetID); err != nil {
			return g, fmt.Errorf("scan graph edge: %w", err)
		}
		e.properties = parseProperties(props)
		e.validFrom = parseSQLTime(vf.String)
		if vu.Valid {
			v := parseSQLTime(vu.String)
			e.validUntil = &v
		}
		g.edges = append(g.edges, e)
		g.adj[e.sourceID] = append(g.adj[e.sourceID], e)
		rev := e
		rev.sourceID, rev.targetID = e.targetID, e.sourceID
		g.adj[rev.sourceID] = append(g.adj[rev.sourceID], rev)
	}
	if err := erows.Err(); err != nil {
		return g, fmt.Errorf("iterate graph edges: %w", err)
	}

	sort.Slice(g.edges, func(i, j int) bool {
		a, b := g.edges[i], g.edges[j]
		return a.sourceID+"\x00"+a.targetID+"\x00"+a.relationship < b.sourceID+"\x00"+b.targetID+"\x00"+b.relationship
	})
	return g, nil
}

type pathState struct {
	id    string
	path  []sqlEdge
	depth int
}

func shortestPathEdges(g sqlGraph, from, to string, maxHops int, asOf int64) []graphdb.PathEdge {
	queue := []pathState{{id: from}}
	bestDepth := -1
	seenDepth := map[string]int{from: 0}
	edgeSet := map[string]graphdb.PathEdge{}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if bestDepth >= 0 && cur.depth >= bestDepth {
			continue
		}
		if cur.depth >= maxHops {
			continue
		}
		for _, e := range g.adj[cur.id] {
			if !edgeVisibleAt(e, asOf) {
				continue
			}
			nextDepth := cur.depth + 1
			nextPath := append(append([]sqlEdge{}, cur.path...), e)
			if e.targetID == to {
				if bestDepth == -1 || nextDepth < bestDepth {
					bestDepth = nextDepth
					edgeSet = map[string]graphdb.PathEdge{}
				}
				if nextDepth == bestDepth {
					for _, pe := range nextPath {
						addPathEdge(edgeSet, pe)
					}
				}
				continue
			}
			if d, ok := seenDepth[e.targetID]; ok && d < nextDepth {
				continue
			}
			seenDepth[e.targetID] = nextDepth
			queue = append(queue, pathState{id: e.targetID, path: nextPath, depth: nextDepth})
		}
	}

	keys := make([]string, 0, len(edgeSet))
	for k := range edgeSet {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]graphdb.PathEdge, 0, len(keys))
	for _, k := range keys {
		out = append(out, edgeSet[k])
	}
	return out
}

func addPathEdge(edgeSet map[string]graphdb.PathEdge, e sqlEdge) {
	key := e.sourceID + "\x00" + e.targetID + "\x00" + e.relationship
	edgeSet[key] = graphdb.PathEdge{
		SourceID:   e.sourceID,
		TargetID:   e.targetID,
		Type:       e.relationship,
		ValidFrom:  e.validFrom,
		ValidUntil: e.validUntil,
		Properties: cloneMap(e.properties),
	}
}

func edgeVisibleAt(e sqlEdge, asOf int64) bool {
	if asOf == 0 {
		return true
	}
	if e.validFrom > asOf {
		return false
	}
	return e.validUntil == nil || *e.validUntil >= asOf
}

func parseProperties(raw string) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func propertyString(props map[string]any, key string) string {
	if props == nil {
		return ""
	}
	switch v := props[key].(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	}
	return ""
}

func firstString(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func parseSQLTime(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix()
		}
	}
	return 0
}

type pathCursor struct {
	Offset int `json:"o"`
}

func encodePathCursor(c pathCursor) string {
	b, _ := json.Marshal(c)
	return base64.URLEncoding.EncodeToString(b)
}

func decodePathCursor(s string) (pathCursor, error) {
	if s == "" {
		return pathCursor{}, nil
	}
	b, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return pathCursor{}, fmt.Errorf("cursor decode: %w", err)
	}
	var c pathCursor
	if err := json.Unmarshal(b, &c); err != nil {
		return pathCursor{}, fmt.Errorf("cursor parse: %w", err)
	}
	if c.Offset < 0 {
		return pathCursor{}, fmt.Errorf("cursor offset must be non-negative")
	}
	return c, nil
}
