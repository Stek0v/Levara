package mcp

// Knowledge-graph entity lookup: query_entity.
// Extracted from deps.go during F-4 wave 3j-split.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/sqlcompat"
)

const (
	// queryEntityNodeResolveLimit caps how many nodes we resolve for a
	// given entity name. Rarely more than one, but duplicates can exist
	// if the same name was imported from distinct sources.
	queryEntityNodeResolveLimit = 10
	// queryEntityEdgeLimit caps the number of edges returned per call.
	// Default value used when no "limit" arg is supplied.
	queryEntityEdgeLimit = 50
)

// ToolQueryEntity returns all graph edges touching the named entity,
// filtered by validity.
//
// Validity modes:
//   - No "as_of": only currently-active edges (valid_until is NULL or
//     in the future relative to CURRENT_TIMESTAMP).
//   - "as_of" supplied: temporal snapshot — edges whose validity window
//     (valid_from, valid_until) includes the given timestamp.
//
// Returns a not-found message (IsError=false) when the entity name
// resolves to zero nodes — an unknown name is a reasonable query, not
// a client error.
func ToolQueryEntity(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	db := deps.DB()
	if db == nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: database not configured"}},
			IsError: true,
		}
	}
	name, _ := args["name"].(string)
	if name == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'name' required"}},
			IsError: true,
		}
	}
	asOf, _ := args["as_of"].(string)
	datasetID, _ := args["dataset_id"].(string)
	limit := queryEntityEdgeLimit
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}
	if limit > 200 {
		limit = 200
	}

	allowed, err := graphDatasetScope(ctx, deps)
	if err != nil {
		return toolError(err.Error())
	}
	if scope, ok := deps.(GraphAssertionAuthorizer); ok {
		allowed, err = scope.GraphDatasetIDs(ctx)
		if err != nil {
			return toolError("graph access denied")
		}
	}
	if allowed != nil && datasetID != "" {
		permitted := false
		for _, id := range allowed {
			if id == datasetID {
				permitted = true
				break
			}
		}
		if !permitted {
			return toolError("dataset access denied")
		}
	}
	if datasetID != "" {
		allowed = []string{datasetID}
	}
	nodeIDs, err := resolveEntityNodes(ctx, db, deps.Q, name, datasetID, allowed, deps)
	if err != nil {
		return toolError(err.Error())
	}
	if len(nodeIDs) == 0 {
		return jsonResult(map[string]any{
			"entity": name, "as_of": asOf, "dataset_id": datasetID,
			"node_ids": []any{}, "edges": []any{},
		})
	}

	edges, err := queryEntityEdges(ctx, db, deps.Q, nodeIDs, asOf, datasetID, limit, allowed, deps)
	if err != nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: " + err.Error()}},
			IsError: true,
		}
	}

	resp := map[string]any{
		"entity":     name,
		"as_of":      asOf,
		"dataset_id": datasetID,
		"node_ids":   nodeIDs,
		"edges":      edges,
	}
	return jsonResult(resp)
}

// graphDatasetScope accepts unrestricted reads only in standalone mode or for
// an active instance administrator. Dataset policy failures remain fail-closed.
func graphDatasetScope(ctx context.Context, deps Deps) ([]string, error) {
	actor := dataActor(ctx)
	policy := access.SQLPolicy{DB: deps.DB(), Q: deps.Q}
	if actor.UserID != "" {
		active, err := policy.IsActive(ctx, actor.UserID)
		if err != nil || !active {
			return nil, fmt.Errorf("graph access denied")
		}
	}
	allowed := deps.AllowedDatasetIDs(ctx)
	if actor.UserID != "" && allowed == nil {
		super, err := policy.IsSuperuser(ctx, actor.UserID)
		if err != nil || !super {
			return nil, fmt.Errorf("graph access denied")
		}
	}
	return allowed, nil
}

// graphDatasetPredicate passes the allowlist as one JSON value. Expanding one
// placeholder per dataset exceeded SQLite and PostgreSQL parameter limits, and
// the edge query repeated that expansion for the edge and both endpoints.
func graphDatasetPredicate(column string, allowed []string, args *[]any) string {
	if allowed == nil {
		return ""
	}
	if len(allowed) == 0 {
		return " AND 1=0"
	}
	encoded, err := json.Marshal(allowed)
	if err != nil {
		// []string cannot fail JSON encoding. Keep this branch fail-closed if
		// the type changes later.
		return " AND 1=0"
	}
	*args = append(*args, string(encoded))
	placeholder := fmt.Sprintf("$%d", len(*args))
	if sqlcompat.CurrentProvider() == sqlcompat.SQLite {
		return " AND " + column + " IN (SELECT value FROM json_each(" + placeholder + "))"
	}
	return " AND " + column + " IN (SELECT value FROM jsonb_array_elements_text(CAST(" + placeholder + " AS jsonb)))"
}

// resolveEntityNodes filters both explicit dataset scope and caller access
// before applying the node limit. SQL failures remain distinguishable from an
// unknown entity name.
func resolveEntityNodes(ctx context.Context, db *sql.DB, rewrite func(string) string, name, datasetID string, allowed []string, deps Deps) ([]string, error) {
	if allowed != nil && len(allowed) == 0 {
		return nil, nil
	}
	var out []string
	afterID := ""
	for len(out) < queryEntityNodeResolveLimit {
		query := "SELECT id,dataset_id,COALESCE(properties,'{}') FROM graph_nodes WHERE name = $1 AND id > $2"
		args := []any{name, afterID}
		if datasetID != "" {
			query += " AND dataset_id = $3"
			args = append(args, datasetID)
		}
		query += graphDatasetPredicate("dataset_id", allowed, &args)
		query += " ORDER BY id LIMIT 128"
		rows, err := db.QueryContext(ctx, rewrite(query), args...)
		if err != nil {
			return nil, err
		}
		type candidate struct {
			id, dataset string
			properties  []byte
		}
		var candidates []candidate
		fetched := 0
		for rows.Next() {
			fetched++
			var c candidate
			if err := rows.Scan(&c.id, &c.dataset, &c.properties); err != nil {
				rows.Close()
				return nil, err
			}
			afterID = c.id
			candidates = append(candidates, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		for _, c := range candidates {
			if graphAssertionsAllowed(ctx, deps, []GraphAssertion{{DatasetID: c.dataset, Properties: c.properties}}) {
				out = append(out, c.id)
				if len(out) == queryEntityNodeResolveLimit {
					break
				}
			}
		}
		if fetched < 128 {
			break
		}
	}
	return out, nil
}

func graphAssertionsAllowed(ctx context.Context, deps Deps, assertions []GraphAssertion) bool {
	authorizer, ok := deps.(GraphAssertionAuthorizer)
	return !ok || authorizer.GraphAssertionsAllowed(ctx, assertions)
}

// queryEntityEdges fetches edges touching any of nodeIDs (as source
// or target), applying the active-now or as_of-based validity filter.
// Returns SQL errors to the caller since a malformed query here is a
// real fault, not a not-found.
func queryEntityEdges(ctx context.Context, db *sql.DB, rewrite func(string) string, nodeIDs []string, asOf, datasetID string, limit int, allowed []string, deps Deps) ([]map[string]any, error) {
	srcPlaceholders := make([]string, 0, len(nodeIDs))
	tgtPlaceholders := make([]string, 0, len(nodeIDs))
	qargs := make([]any, 0, len(nodeIDs)*2+2)
	pos := 1
	for _, id := range nodeIDs {
		srcPlaceholders = append(srcPlaceholders, fmt.Sprintf("$%d", pos))
		qargs = append(qargs, id)
		pos++
	}
	for _, id := range nodeIDs {
		tgtPlaceholders = append(tgtPlaceholders, fmt.Sprintf("$%d", pos))
		qargs = append(qargs, id)
		pos++
	}

	var validityClause string
	if asOf == "" {
		validityClause = " AND (graph_edges.valid_until IS NULL OR graph_edges.valid_until > CURRENT_TIMESTAMP)"
	} else {
		validityClause = fmt.Sprintf(
			" AND (graph_edges.valid_from IS NULL OR graph_edges.valid_from <= $%d) AND (graph_edges.valid_until IS NULL OR graph_edges.valid_until > $%d)",
			pos, pos+1,
		)
		qargs = append(qargs, asOf, asOf)
		pos += 2
	}

	// Explicit scope narrows the caller-authorized graph.
	var datasetClause string
	if datasetID != "" {
		datasetClause = fmt.Sprintf(" AND graph_edges.dataset_id = $%d", pos)
		qargs = append(qargs, datasetID)
	}

	accessClause := graphDatasetPredicate("graph_edges.dataset_id", allowed, &qargs)
	if allowed != nil {
		// An allowed edge alone does not authorize disclosing a foreign or
		// missing endpoint. Check both nodes independently of edge provenance.
		accessClause += graphDatasetPredicate("src.dataset_id", allowed, &qargs)
		accessClause += graphDatasetPredicate("dst.dataset_id", allowed, &qargs)
	}

	type candidate struct {
		id, src, tgt, rel, props, sb           string
		srcDataset, srcProperties, edgeDataset string
		dstDataset, dstProperties              string
		vf, vu                                 sql.NullString
		conf                                   float64
	}
	var edges []map[string]any
	authorizations := map[string]bool{}
	for offset := 0; len(edges) < limit; offset += 128 {
		pos = len(qargs) + 1
		sqlStr := fmt.Sprintf(`
			SELECT graph_edges.id, graph_edges.source_id, graph_edges.target_id,
				graph_edges.relationship_name, graph_edges.properties,
				graph_edges.valid_from, graph_edges.valid_until,
				graph_edges.superseded_by, graph_edges.confidence,
				COALESCE(src.dataset_id,''), COALESCE(src.properties,'{}'),
				COALESCE(graph_edges.dataset_id,''),
				COALESCE(dst.dataset_id,''), COALESCE(dst.properties,'{}')
			FROM graph_edges
			JOIN graph_nodes src ON src.id=graph_edges.source_id
			JOIN graph_nodes dst ON dst.id=graph_edges.target_id
			WHERE (graph_edges.source_id IN (%s) OR graph_edges.target_id IN (%s))%s%s%s
			ORDER BY graph_edges.updated_at DESC,graph_edges.id LIMIT $%d OFFSET $%d
		`, strings.Join(srcPlaceholders, ","), strings.Join(tgtPlaceholders, ","), validityClause, datasetClause, accessClause, pos, pos+1)
		args := append(append([]any(nil), qargs...), 128, offset)
		rows, err := db.QueryContext(ctx, rewrite(sqlStr), args...)
		if err != nil {
			return nil, err
		}
		var candidates []candidate
		fetched := 0
		for rows.Next() {
			fetched++
			var c candidate
			if err := rows.Scan(&c.id, &c.src, &c.tgt, &c.rel, &c.props, &c.vf, &c.vu, &c.sb, &c.conf,
				&c.srcDataset, &c.srcProperties, &c.edgeDataset, &c.dstDataset, &c.dstProperties); err != nil {
				rows.Close()
				return nil, err
			}
			candidates = append(candidates, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		for _, c := range candidates {
			if c.edgeDataset == "" && c.srcDataset == c.dstDataset {
				c.edgeDataset = c.srcDataset
			}
			assertions := []GraphAssertion{
				{DatasetID: c.srcDataset, Properties: []byte(c.srcProperties)},
				{DatasetID: c.edgeDataset, Properties: []byte(c.props)},
				{DatasetID: c.dstDataset, Properties: []byte(c.dstProperties)},
			}
			key, _ := json.Marshal(assertions)
			allowed, checked := authorizations[string(key)]
			if !checked {
				allowed = graphAssertionsAllowed(ctx, deps, assertions)
				authorizations[string(key)] = allowed
			}
			if !allowed {
				continue
			}
			edges = append(edges, map[string]any{
				"id":            c.id,
				"source_id":     c.src,
				"target_id":     c.tgt,
				"relationship":  c.rel,
				"properties":    json.RawMessage(c.props),
				"valid_from":    c.vf.String,
				"valid_until":   c.vu.String,
				"superseded_by": c.sb,
				"confidence":    c.conf,
			})
			if len(edges) == limit {
				break
			}
		}
		if fetched < 128 {
			break
		}
	}
	return edges, nil
}
