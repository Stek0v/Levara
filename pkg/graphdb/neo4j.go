// Package graphdb provides batch write operations for Neo4j,
// mirroring Levara's Neo4j adapter Cypher patterns (UNWIND + MERGE + APOC).
package graphdb

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stek0v/levara/pkg/graph"

	"github.com/stek0v/levara/internal/metrics"
)

const baseLabel = "__Node__"

// safeLabel validates s as a Cypher identifier safe to interpolate into a
// query. Neo4j allows labels containing arbitrary characters when quoted
// with backticks, but a backtick inside the label would let a malicious
// caller break out and inject arbitrary Cypher. We therefore restrict
// labels to the conservative ASCII identifier subset.
//
// Returns the validated label on success, or an error explaining why the
// input is unsafe. Callers should propagate the error rather than panic so
// the request boundary can decide between 400 (user input), 500 (internal
// bug), or fallback behavior.
func safeLabel(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("empty label")
	}
	if len(s) > 64 {
		return "", fmt.Errorf("label too long: %d > 64", len(s))
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r == '_':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return "", fmt.Errorf("label %q contains invalid character %q at offset %d (allowed: [A-Za-z_][A-Za-z0-9_]*)", s, r, i)
		}
	}
	return s, nil
}

// safeRelType validates s as a Cypher relationship type safe to interpolate.
// Same character class as safeLabel — Cypher does not distinguish syntactically
// between labels and relationship types for backtick-quoted identifiers, but
// having a separate name documents intent at call sites.
func safeRelType(s string) (string, error) {
	if _, err := safeLabel(s); err != nil {
		return "", fmt.Errorf("relationship type: %w", err)
	}
	return s, nil
}

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

	// lazyValidFromOnce gates Stage-5 lazy temporal migration: the first read
	// after process start backfills valid_from=0 (epoch) on legacy edges that
	// pre-date the temporal model. Subsequent reads skip the no-op MATCH.
	lazyValidFromOnce  sync.Once
	episodeSchemaMu    sync.Mutex
	episodeSchemaReady bool
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
		func(config *neo4j.Config) { //nolint:staticcheck // SA1019: neo4j.Config deprecation; upstream upgrade out of scope
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

// ensureValidFromBackfill is the lazy Stage-5 temporal migration: any edge
// missing valid_from gets epoch 0 written, so range queries (as_of) treat
// pre-temporal edges as valid since the dawn of time. Runs at most once per
// process via lazyValidFromOnce. Best-effort: errors are logged via the
// observed metric channel but never fail the read that triggered it.
func (w *Writer) ensureValidFromBackfill(ctx context.Context) {
	w.lazyValidFromOnce.Do(func() {
		var err error
		defer metrics.ObserveExternalCall("neo4j", "migrate_valid_from", time.Now(), &err)
		_, err = RunWriteTx(ctx, w, func(ctx context.Context, tx neo4j.ManagedTransaction) (struct{}, error) {
			_, runErr := tx.Run(ctx,
				"MATCH ()-[r]->() WHERE r.valid_from IS NULL SET r.valid_from = 0",
				nil)
			return struct{}{}, runErr
		})
	})
}

// EnsureSchema creates required constraints/indexes if they do not exist.
func (w *Writer) EnsureSchema(ctx context.Context) error {
	_, err := RunWriteTx(ctx, w, func(ctx context.Context, tx neo4j.ManagedTransaction) (struct{}, error) {
		for _, stmt := range append(requiredNeo4jSchemaStatements(baseLabel), episodeIdentityConstraint) {
			res, err := tx.Run(ctx, stmt, nil)
			if err != nil {
				return struct{}{}, err
			}
			if _, err := res.Consume(ctx); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	})
	return err
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
//
// Atomicity: nodes and edges share a single Neo4j transaction. If the edge
// MERGE fails after the node MERGE has run, the entire transaction rolls
// back — orphaned nodes cannot leak to the database (Stage 3 invariant).
// On error both NodesWritten and EdgesWritten in the returned result are 0.
func (w *Writer) BatchWrite(ctx context.Context, nodes []NodeRecord, edges []EdgeRecord) BatchWriteResult {
	if len(nodes) == 0 && len(edges) == 0 {
		return BatchWriteResult{}
	}
	var obsErr error
	defer metrics.ObserveExternalCall("neo4j", "write", time.Now(), &obsErr)

	nodeBatch := buildNodeBatch(nodes)
	edgeBatch, err := prepareEpisodeBatch(edges)
	if err != nil {
		return BatchWriteResult{Errors: []string{err.Error()}}
	}
	for _, node := range nodes {
		if node.ID == "" {
			return BatchWriteResult{Errors: []string{"node ID required"}}
		}
		if node.Label == "__EpisodeIdentity__" {
			return BatchWriteResult{Errors: []string{"reserved internal node label"}}
		}
		if node.Label == "" {
			return BatchWriteResult{Errors: []string{"node label required"}}
		}
	}
	if len(edges) > 0 {
		if err := w.ensureEpisodeIdentitySchema(ctx); err != nil {
			return BatchWriteResult{Errors: []string{err.Error()}}
		}
	}

	type counts struct{ nodes, edges int }
	out, err := RunWriteTx(ctx, w, func(ctx context.Context, tx neo4j.ManagedTransaction) (counts, error) {
		var c counts
		if err := lockEpisodeBatch(ctx, tx, nodes, edges, edgeBatch); err != nil {
			return counts{}, err
		}
		if len(nodeBatch) > 0 {
			n, err := runNodeMerge(ctx, tx, nodeBatch)
			if err != nil {
				return counts{}, fmt.Errorf("nodes: %w", err)
			}
			c.nodes = n
		}
		if len(edgeBatch) > 0 {
			n, err := runEdgeMerge(ctx, tx, edgeBatch)
			if err != nil {
				return counts{}, fmt.Errorf("edges: %w", err)
			}
			c.edges = n
		}
		return c, nil
	})

	result := BatchWriteResult{NodesWritten: out.nodes, EdgesWritten: out.edges}
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		obsErr = err
	}
	return result
}

func buildNodeBatch(nodes []NodeRecord) []map[string]any {
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
	return batch
}

func buildEdgeBatch(edges []EdgeRecord) []map[string]any {
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
	return batch
}

// nodeMergeCypher returns the UNWIND+MERGE+APOC addLabels query for nodes.
// Exposed as a package var so tests can assert the wire format.
var nodeMergeCypher = fmt.Sprintf(`
	UNWIND $nodes AS node
	MERGE (n: `+"`%s`"+` {id: node.node_id})
	ON CREATE SET n += node.properties, n.updated_at = timestamp()
	ON MATCH SET n += node.properties, n.updated_at = timestamp()
	WITH n, node.label AS label
	CALL apoc.create.addLabels(n, [label]) YIELD node AS labeledNode
	RETURN count(labeledNode) AS cnt
`, baseLabel)

func runNodeMerge(ctx context.Context, tx neo4j.ManagedTransaction, batch []map[string]any) (int, error) {
	res, err := tx.Run(ctx, nodeMergeCypher, map[string]any{"nodes": batch})
	if err != nil {
		return 0, err
	}
	if res.Next(ctx) {
		if c, ok := res.Record().Get("cnt"); ok {
			if n, ok := c.(int64); ok {
				return int(n), nil
			}
		}
	}
	if err := res.Err(); err != nil {
		return 0, fmt.Errorf("cypher: %w", err)
	}
	return len(batch), nil
}

const episodeIdentityConstraint = "CREATE CONSTRAINT IF NOT EXISTS FOR (n:__EpisodeIdentity__) REQUIRE n.id IS UNIQUE"

func (w *Writer) ensureEpisodeIdentitySchema(ctx context.Context) error {
	w.episodeSchemaMu.Lock()
	defer w.episodeSchemaMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.episodeSchemaReady {
		return nil
	}
	_, err := RunWriteTx(ctx, w, func(ctx context.Context, tx neo4j.ManagedTransaction) (struct{}, error) {
		for _, statement := range []string{requiredNeo4jSchemaStatements(baseLabel)[0], episodeIdentityConstraint} {
			res, err := tx.Run(ctx, statement, nil)
			if err != nil {
				return struct{}{}, err
			}
			if _, err := res.Consume(ctx); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	})
	if err == nil {
		w.episodeSchemaReady = true
	}
	return err
}

func episodeSecond(value any) (int64, bool, error) {
	if value == nil {
		return 0, false, nil
	}
	var n int64
	switch v := value.(type) {
	case int:
		n = int64(v)
	case int8:
		n = int64(v)
	case int16:
		n = int64(v)
	case int32:
		n = int64(v)
	case int64:
		n = v
	case uint:
		if uint64(v) > math.MaxInt64 {
			return 0, false, fmt.Errorf("timestamp overflow")
		}
		n = int64(v)
	case uint64:
		if v > math.MaxInt64 {
			return 0, false, fmt.Errorf("timestamp overflow")
		}
		n = int64(v)
	case uint8:
		n = int64(v)
	case uint16:
		n = int64(v)
	case uint32:
		n = int64(v)
	case float32:
		return episodeSecond(float64(v))
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v || v >= 9223372036854775808.0 || v < -9223372036854775808.0 {
			return 0, false, fmt.Errorf("invalid timestamp number")
		}
		n = int64(v)
	case json.Number:
		raw := string(v)
		if !json.Valid([]byte(raw)) {
			return 0, false, fmt.Errorf("invalid timestamp number")
		}
		mantissa := strings.SplitN(strings.ToLower(raw), "e", 2)[0]
		if strings.Trim(mantissa, "-0.") == "" {
			n = 0
			break
		}
		// Bound magnitude before exact parsing to avoid enormous exponent allocation.
		approximate, err := v.Float64()
		if err != nil || math.Abs(approximate) < 1 || math.Abs(approximate) > 9223372036854775808.0 {
			return 0, false, fmt.Errorf("invalid timestamp number")
		}
		exact, ok := new(big.Rat).SetString(raw)
		if !ok || !exact.IsInt() || !exact.Num().IsInt64() {
			return 0, false, fmt.Errorf("invalid timestamp number")
		}
		n = exact.Num().Int64()
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err == nil {
			n = parsed
		} else {
			stamp, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				return 0, false, fmt.Errorf("invalid timestamp: %q", v)
			}
			n = stamp.Unix()
		}
	default:
		return 0, false, fmt.Errorf("invalid timestamp type %T", value)
	}
	return n, true, nil
}

func prepareEpisodeBatch(edges []EdgeRecord) ([]map[string]any, error) {
	for _, edge := range edges {
		for _, key := range []string{"id", "dataset_id", "superseded_by"} {
			if value := edge.Properties[key]; value != nil {
				if _, ok := value.(string); !ok {
					return nil, fmt.Errorf("%s must be a string", key)
				}
			}
		}
		for _, key := range []string{"valid_from", "valid_until"} {
			if _, _, err := episodeSecond(edge.Properties[key]); err != nil {
				return nil, err
			}
		}
	}
	batch := buildEdgeBatch(edges)
	for _, edge := range batch {
		if edge["from_node"] == "" || edge["to_node"] == "" {
			return nil, fmt.Errorf("edge endpoints required")
		}
		if edge["relationship_name"] == "" {
			return nil, fmt.Errorf("relationship type required")
		}
		props := edge["properties"].(map[string]any)
		for _, key := range []string{"id", "dataset_id"} {
			if value := props[key]; value != nil {
				if _, ok := value.(string); !ok {
					return nil, fmt.Errorf("%s must be a string", key)
				}
			}
		}
		from, hasFrom, err := episodeSecond(props["valid_from"])
		if err != nil {
			return nil, err
		}
		until, closed, err := episodeSecond(props["valid_until"])
		if err != nil {
			return nil, err
		}
		if closed && !hasFrom {
			from = 0
			hasFrom = true
		}
		if closed && until < from {
			return nil, fmt.Errorf("reversed validity bounds")
		}
		delete(props, "valid_from")
		delete(props, "valid_until")
		if hasFrom {
			props["valid_from"] = from
		}
		if closed {
			props["valid_until"] = until
		}
		id, _ := props["id"].(string)
		if id == "" {
			id = fmt.Sprintf("%s_%s_%s", edge["from_node"], edge["relationship_name"], edge["to_node"])
		}
		edge["candidate_id"], edge["has_from"], edge["closed"] = id, hasFrom, closed
	}
	return batch, nil
}

func episodeRows(ctx context.Context, tx neo4j.ManagedTransaction, query string, params map[string]any) ([]map[string]any, error) {
	res, err := tx.Run(ctx, query, params)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	for res.Next(ctx) {
		row := map[string]any{}
		for _, key := range res.Record().Keys {
			row[key], _ = res.Record().Get(key)
		}
		rows = append(rows, row)
	}
	return rows, res.Err()
}

func lockEpisodeIdentity(ctx context.Context, tx neo4j.ManagedTransaction, id string) error {
	rows, err := episodeRows(ctx, tx, "MERGE (n:__EpisodeIdentity__ {id:$id}) WITH n CALL apoc.lock.nodes([n]) RETURN count(n) AS count", map[string]any{"id": id})
	if err != nil {
		return err
	}
	if len(rows) != 1 || rows[0]["count"] != int64(1) {
		return fmt.Errorf("identity reservation failed")
	}
	return nil
}

func lockEpisodeBatch(ctx context.Context, tx neo4j.ManagedTransaction, nodes []NodeRecord, edges []EdgeRecord, batch []map[string]any) error {
	candidates := map[string]bool{}
	for _, edge := range batch {
		candidates[edge["candidate_id"].(string)] = true
		if !edge["closed"].(bool) {
			props := edge["properties"].(map[string]any)
			dataset, _ := props["dataset_id"].(string)
			relation := edge["relationship_name"].(string)
			rows, err := episodeRows(ctx, tx, `MATCH (a:__Node__ {id:$source})-[r]->(b:__Node__ {id:$target})
			 WHERE coalesce(r.dataset_id,'')=$dataset AND r.valid_until IS NULL
			 AND (($exclusive AND toLower(type(r))=toLower($type)) OR (NOT $exclusive AND type(r)=$type))
			 RETURN r.id AS id`, map[string]any{"source": edge["from_node"], "target": edge["to_node"], "dataset": dataset, "type": relation, "exclusive": graph.IsExclusiveRelationship(relation)})
			if err != nil {
				return err
			}
			for _, row := range rows {
				if id, ok := row["id"].(string); ok && id != "" {
					candidates[id] = true
				}
			}
		}
	}
	orderedIDs := make([]string, 0, len(candidates))
	for id := range candidates {
		orderedIDs = append(orderedIDs, id)
	}
	sort.Strings(orderedIDs)
	for _, id := range orderedIDs {
		if err := lockEpisodeIdentity(ctx, tx, id); err != nil {
			return err
		}
	}
	create := map[string]bool{}
	endpoints := map[string]bool{}
	for _, node := range nodes {
		create[node.ID] = true
		endpoints[node.ID] = true
	}
	for _, edge := range edges {
		endpoints[edge.SourceID] = true
		endpoints[edge.TargetID] = true
	}
	ordered := make([]string, 0, len(endpoints))
	for id := range endpoints {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		match := "MATCH"
		if create[id] {
			match = "MERGE"
		}
		rows, err := episodeRows(ctx, tx, match+" (n:__Node__ {id:$id}) WITH n CALL apoc.lock.nodes([n]) RETURN count(n) AS count", map[string]any{"id": id})
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0]["count"] != int64(1) {
			return fmt.Errorf("missing endpoint %s", id)
		}
	}
	return nil
}

func episodeEvidence(props map[string]any) string {
	ordinary := map[string]any{}
	for key, value := range props {
		if key != "id" && key != "valid_from" && key != "valid_until" && key != "superseded_by" && key != "source_node_id" && key != "target_node_id" && key != "dataset_id" {
			ordinary[key] = value
		}
	}
	encoded, _ := json.Marshal(ordinary)
	return string(encoded)
}

func episodeByID(ctx context.Context, tx neo4j.ManagedTransaction, id string) ([]map[string]any, error) {
	return episodeRows(ctx, tx, "MATCH (a:__Node__)-[r]->(b:__Node__) WHERE r.id=$id RETURN a.id AS source,b.id AS target,type(r) AS type,elementId(r) AS element,properties(r) AS props", map[string]any{"id": id})
}

func runEdgeMerge(ctx context.Context, tx neo4j.ManagedTransaction, batch []map[string]any) (int, error) {
	now := time.Now().Unix()
	for _, edge := range batch {
		original := edge["properties"].(map[string]any)
		props := map[string]any{}
		for key, value := range original {
			props[key] = value
		}
		dataset, _ := props["dataset_id"].(string)
		relation := edge["relationship_name"].(string)
		exclusive := graph.IsExclusiveRelationship(relation)
		closed := edge["closed"].(bool)
		params := map[string]any{"source": edge["from_node"], "target": edge["to_node"], "dataset": dataset, "type": relation, "exclusive": exclusive}
		id := edge["candidate_id"].(string)
		if closed {
			rows, err := episodeByID(ctx, tx, id)
			if err != nil {
				return 0, err
			}
			if len(rows) > 0 {
				old := rows[0]["props"].(map[string]any)
				oldDataset, _ := old["dataset_id"].(string)
				oldType := rows[0]["type"].(string)
				sameType := oldType == relation || exclusive && strings.EqualFold(oldType, relation)
				from, _, err := episodeSecond(old["valid_from"])
				if err != nil {
					return 0, err
				}
				until, wasClosed, err := episodeSecond(old["valid_until"])
				if err != nil {
					return 0, err
				}
				if len(rows) != 1 || rows[0]["source"] != edge["from_node"] || rows[0]["target"] != edge["to_node"] || oldDataset != dataset || !sameType || !wasClosed || from != props["valid_from"] || until != props["valid_until"] || episodeEvidence(old) == "" || episodeEvidence(props) == "" || episodeEvidence(old) != episodeEvidence(props) {
					return 0, fmt.Errorf("closed episode ID conflict")
				}
				if successor, ok := props["superseded_by"]; ok && successor != old["superseded_by"] {
					return 0, fmt.Errorf("closed successor conflict")
				}
				continue
			}
		}
		var active []map[string]any
		if !closed {
			var err error
			active, err = episodeRows(ctx, tx, `MATCH (a:__Node__ {id:$source})-[r]->(b:__Node__ {id:$target})
			 WHERE coalesce(r.dataset_id,'')=$dataset AND r.valid_until IS NULL
			 AND (($exclusive AND toLower(type(r))=toLower($type)) OR (NOT $exclusive AND type(r)=$type))
			 RETURN elementId(r) AS element,properties(r) AS props,type(r) AS type ORDER BY elementId(r)`, params)
			if err != nil {
				return 0, err
			}
			if len(active) > 1 {
				return 0, fmt.Errorf("duplicate active episodes")
			}
		}
		from := now
		if edge["has_from"].(bool) {
			from = props["valid_from"].(int64)
		}
		var element string
		if len(active) == 1 {
			old := active[0]["props"].(map[string]any)
			storedFrom, hasStart, err := episodeSecond(old["valid_from"])
			if err != nil {
				return 0, err
			}
			if !hasStart {
				storedFrom = 0
			}
			if edge["has_from"].(bool) && from != storedFrom {
				return 0, fmt.Errorf("active episode start conflict")
			}
			from = storedFrom
			element = active[0]["element"].(string)
			if storedID, ok := old["id"].(string); ok && storedID != "" {
				id = storedID
			}
		}
		for {
			rows, err := episodeByID(ctx, tx, id)
			if err != nil {
				return 0, err
			}
			if len(rows) == 0 || len(rows) == 1 && rows[0]["element"] == element {
				break
			}
			if element != "" {
				return 0, fmt.Errorf("active episode ID conflict")
			}
			id = edge["candidate_id"].(string) + "_" + uuid.NewString()
			if err := lockEpisodeIdentity(ctx, tx, id); err != nil {
				return 0, err
			}
		}
		if exclusive && !closed {
			params["element"] = element
			rows, err := episodeRows(ctx, tx, `MATCH (a:__Node__ {id:$source})-[r]->()
			 WHERE coalesce(r.dataset_id,'')=$dataset AND r.valid_until IS NULL
			 AND toLower(type(r))=toLower($type) AND elementId(r)<>$element RETURN properties(r) AS props`, params)
			if err != nil {
				return 0, err
			}
			for _, row := range rows {
				oldFrom, _, err := episodeSecond(row["props"].(map[string]any)["valid_from"])
				if err != nil {
					return 0, err
				}
				if oldFrom > from {
					return 0, fmt.Errorf("backdated exclusive transition")
				}
			}
		}
		props["id"], props["valid_from"] = id, from
		if !closed {
			delete(props, "valid_until")
			delete(props, "superseded_by")
		}
		params["props"], params["element"] = props, element
		var query string
		if element != "" {
			query = `MATCH ()-[r]->() WHERE elementId(r)=$element SET r += $props, r.valid_until=NULL, r.superseded_by=NULL
			 WITH r CALL apoc.refactor.setType(r,$type) YIELD output RETURN elementId(output) AS element`
		} else {
			query = `MATCH (a:__Node__ {id:$source}),(b:__Node__ {id:$target})
			 CALL apoc.create.relationship(a,$type,$props,b) YIELD rel RETURN elementId(rel) AS element`
		}
		rows, err := episodeRows(ctx, tx, query, params)
		if err != nil {
			return 0, err
		}
		if len(rows) != 1 {
			return 0, fmt.Errorf("episode write count mismatch")
		}
		if exclusive && !closed {
			params["element"], params["from"], params["id"] = rows[0]["element"], from, id
			if _, err := episodeRows(ctx, tx, `MATCH (a:__Node__ {id:$source})-[r]->()
			 WHERE coalesce(r.dataset_id,'')=$dataset AND r.valid_until IS NULL
			 AND toLower(type(r))=toLower($type) AND elementId(r)<>$element
			 SET r.valid_until=$from,r.superseded_by=$id RETURN count(r) AS count`, params); err != nil {
				return 0, err
			}
		}
	}
	return len(batch), nil
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
	w.ensureValidFromBackfill(ctx)
	return RunReadTx(ctx, w, func(ctx context.Context, tx neo4j.ManagedTransaction) (GraphReadResult, error) {
		var result GraphReadResult

		res, err := tx.Run(ctx,
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

		res, err = tx.Run(ctx,
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
	})
}

// ReadIDFiltered returns nodes/edges touching the given IDs.
func (w *Writer) ReadIDFiltered(ctx context.Context, ids []string) (GraphReadResult, error) {
	w.ensureValidFromBackfill(ctx)
	query := fmt.Sprintf(`
		MATCH (a:`+"`%s`"+`)-[r]-(b:`+"`%s`"+`)
		WHERE a.id IN $ids OR b.id IN $ids
		WITH DISTINCT r, startNode(r) AS a, endNode(r) AS b
		RETURN properties(a) AS a_props, properties(b) AS b_props,
		       TYPE(r) AS typ, properties(r) AS r_props
	`, baseLabel, baseLabel)

	return RunReadTx(ctx, w, func(ctx context.Context, tx neo4j.ManagedTransaction) (GraphReadResult, error) {
		var result GraphReadResult
		res, err := tx.Run(ctx, query, map[string]any{"ids": ids})
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
	})
}

// ReadNeighbours returns direct neighbors of a node.
func (w *Writer) ReadNeighbours(ctx context.Context, nodeID string) (GraphReadResult, error) {
	return w.ReadIDFiltered(ctx, []string{nodeID})
}

// ReadSubgraph returns nodes matching label+names with their neighbors.
// label is interpolated into Cypher (Neo4j cannot parameterize labels), so it
// must be a syntactically valid identifier — anything else is rejected to
// prevent Cypher injection.
func (w *Writer) ReadSubgraph(ctx context.Context, label string, names []string) (result GraphReadResult, err error) {
	defer metrics.ObserveExternalCall("neo4j", "read", time.Now(), &err)
	if _, err := safeLabel(label); err != nil {
		return GraphReadResult{}, err
	}
	w.ensureValidFromBackfill(ctx)
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

	return RunReadTx(ctx, w, func(ctx context.Context, tx neo4j.ManagedTransaction) (GraphReadResult, error) {
		var result GraphReadResult
		res, err := tx.Run(ctx, query, map[string]any{"names": names})
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
	})
}

// Query executes an arbitrary read Cypher query and returns rows as []map[string]any.
// All current callers (graph_search, mcp_doctor, api_search) execute MATCH/SHOW
// queries — routed through ExecuteRead so cluster deployments can use replicas.
// Use ExecuteWrite directly via session if a write-flavoured query is needed.
func (w *Writer) Query(ctx context.Context, cypher string, params map[string]any) (rows []map[string]any, err error) {
	defer metrics.ObserveExternalCall("neo4j", "query", time.Now(), &err)
	return RunReadTx(ctx, w, func(ctx context.Context, tx neo4j.ManagedTransaction) ([]map[string]any, error) {
		res, err := tx.Run(ctx, cypher, params)
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
	})
}

func toStringMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}
