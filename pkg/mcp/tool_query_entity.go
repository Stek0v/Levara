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

	allowed, err := graphDatasetScope(ctx, deps)
	if err != nil {
		return toolError(err.Error())
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
	nodeIDs := resolveEntityNodes(ctx, db, deps.Q, name, datasetID, allowed)
	if len(nodeIDs) == 0 {
		return ToolResult{Content: []Content{{
			Type: "text",
			Text: fmt.Sprintf("No entity found with name '%s'", name),
		}}}
	}

	edges, err := queryEntityEdges(ctx, db, deps.Q, nodeIDs, asOf, datasetID, limit, allowed)
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

// graphDatasetPredicate allocates fresh parameter positions on every use so
// SQLite's positional rewrite and PostgreSQL use the same argument sequence.
func graphDatasetPredicate(column string, allowed []string, args *[]any) string {
	if allowed == nil {
		return ""
	}
	if len(allowed) == 0 {
		return " AND 1=0"
	}
	placeholders := make([]string, 0, len(allowed))
	for _, id := range allowed {
		*args = append(*args, id)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(*args)))
	}
	return " AND " + column + " IN (" + strings.Join(placeholders, ",") + ")"
}

// resolveEntityNodes filters both explicit dataset scope and caller access
// before applying the node limit. SQL failure preserves the not-found result.
func resolveEntityNodes(ctx context.Context, db *sql.DB, rewrite func(string) string, name, datasetID string, allowed []string) []string {
	if allowed != nil && len(allowed) == 0 {
		return nil
	}
	query := "SELECT id FROM graph_nodes WHERE name = $1"
	args := []any{name}
	if datasetID != "" {
		query += " AND dataset_id = $2"
		args = append(args, datasetID)
	}
	query += graphDatasetPredicate("dataset_id", allowed, &args)
	query += fmt.Sprintf(" LIMIT %d", queryEntityNodeResolveLimit)
	rows, err := db.QueryContext(ctx, rewrite(query), args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// queryEntityEdges fetches edges touching any of nodeIDs (as source
// or target), applying the active-now or as_of-based validity filter.
// Returns SQL errors to the caller since a malformed query here is a
// real fault, not a not-found.
func queryEntityEdges(ctx context.Context, db *sql.DB, rewrite func(string) string, nodeIDs []string, asOf, datasetID string, limit int, allowed []string) ([]map[string]any, error) {
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
		validityClause = " AND (valid_until IS NULL OR valid_until > CURRENT_TIMESTAMP)"
	} else {
		validityClause = fmt.Sprintf(
			" AND (valid_from IS NULL OR valid_from <= $%d) AND (valid_until IS NULL OR valid_until > $%d)",
			pos, pos+1,
		)
		qargs = append(qargs, asOf, asOf)
		pos += 2
	}

	// Explicit scope narrows the caller-authorized graph.
	var datasetClause string
	if datasetID != "" {
		datasetClause = fmt.Sprintf(" AND dataset_id = $%d", pos)
		qargs = append(qargs, datasetID)
		pos++
	}

	accessClause := graphDatasetPredicate("graph_edges.dataset_id", allowed, &qargs)
	if allowed != nil {
		// An allowed edge alone does not authorize disclosing a foreign or
		// missing endpoint. Check both nodes independently of edge provenance.
		accessClause += " AND EXISTS (SELECT 1 FROM graph_nodes src WHERE src.id = graph_edges.source_id" + graphDatasetPredicate("src.dataset_id", allowed, &qargs) + ")"
		accessClause += " AND EXISTS (SELECT 1 FROM graph_nodes dst WHERE dst.id = graph_edges.target_id" + graphDatasetPredicate("dst.dataset_id", allowed, &qargs) + ")"
	}
	pos = len(qargs) + 1

	// Scan timestamp columns as NullString so the driver returns "" for NULL
	// without forcing a Postgres-specific COALESCE(..::text, ''). SQLite (used
	// by tests) and Postgres both accept this — earlier `COALESCE(col, '')`
	// failed under PG because '' isn't a valid timestamptz.
	sqlStr := fmt.Sprintf(`
		SELECT id, source_id, target_id, relationship_name, properties,
			valid_from, valid_until, superseded_by, confidence
		FROM graph_edges
		WHERE (source_id IN (%s) OR target_id IN (%s))%s%s%s
		ORDER BY updated_at DESC LIMIT $%d
	`, strings.Join(srcPlaceholders, ","), strings.Join(tgtPlaceholders, ","), validityClause, datasetClause, accessClause, pos)
	qargs = append(qargs, limit)

	rows, err := db.QueryContext(ctx, rewrite(sqlStr), qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var edges []map[string]any
	for rows.Next() {
		var id, src, tgt, rel, props, sb string
		var vf, vu sql.NullString
		var conf float64
		if err := rows.Scan(&id, &src, &tgt, &rel, &props, &vf, &vu, &sb, &conf); err != nil {
			continue
		}
		edges = append(edges, map[string]any{
			"id":            id,
			"source_id":     src,
			"target_id":     tgt,
			"relationship":  rel,
			"properties":    json.RawMessage(props),
			"valid_from":    vf.String,
			"valid_until":   vu.String,
			"superseded_by": sb,
			"confidence":    conf,
		})
	}
	return edges, nil
}
