// path.go — Stage-4 path/traversal queries: shortest-path between two nodes
// returned as a flat edge list, with as_of temporal filtering and cursor-based
// pagination over multiple shortest paths.
package graphdb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/stek0v/levara/internal/metrics"
)

// PathEdge is one edge in a returned path.
//
// ValidFrom / ValidUntil are unix-seconds; a missing valid_from is normalized
// to 0 (Stage-5 lazy migration semantics) and missing valid_until means open-
// ended (still valid). Properties carries the raw edge payload for callers
// that need more than the temporal envelope.
type PathEdge struct {
	SourceID   string         `json:"source_id"`
	TargetID   string         `json:"target_id"`
	Type       string         `json:"type"`
	ValidFrom  int64          `json:"valid_from"`
	ValidUntil *int64         `json:"valid_until"`
	Properties map[string]any `json:"properties"`
}

// PathQuery parameters for PathBetween.
type PathQuery struct {
	From    string
	To      string
	MaxHops int
	// AsOf filters edges so only those with valid_from <= AsOf and
	// (valid_until IS NULL OR valid_until >= AsOf) are returned. Zero means
	// "no temporal filter" (all edges visible).
	AsOf int64
	// Limit caps the number of edges returned in this page (not paths).
	Limit int
	// Cursor is an opaque continuation token from a previous PathBetween;
	// empty means "first page". Decoded as base64(JSON{offset:N}).
	Cursor string
}

// PathResult is the response from PathBetween.
type PathResult struct {
	Edges      []PathEdge `json:"edges"`
	NextCursor string     `json:"next_cursor,omitempty"`
	AsOf       int64      `json:"as_of"`
}

type pathCursor struct {
	Offset int `json:"o"`
}

func encodeCursor(c pathCursor) string {
	b, _ := json.Marshal(c)
	return base64.URLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (pathCursor, error) {
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

// PathBetween returns shortest-path edges between From and To, optionally
// filtered to a point in time (AsOf) and paginated via Cursor.
//
// Path semantics: returns the union of all shortest paths up to MaxHops in a
// single flat edge list. Callers can reconstruct paths client-side from
// (source_id, target_id) pairs since each edge keeps its endpoints. The
// flatten-then-paginate model is simpler than nested-tree pagination and is
// what the current MCP/UI consumers want.
//
// MaxHops <= 0 defaults to 4. Limit <= 0 defaults to 100; Limit > 1000 is
// clamped to 1000 to bound query cost.
func (w *Writer) PathBetween(ctx context.Context, q PathQuery) (_ PathResult, err error) {
	defer metrics.ObserveExternalCall("neo4j", "read", time.Now(), &err)

	if q.From == "" || q.To == "" {
		return PathResult{}, fmt.Errorf("from and to required")
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

	cur, err := decodeCursor(q.Cursor)
	if err != nil {
		return PathResult{}, err
	}

	// Triggering lazy backfill keeps post-Stage-5 readers seeing valid_from=0
	// on legacy edges so the temporal filter below behaves predictably even
	// before any cognify run has touched these relationships.
	w.ensureValidFromBackfill(ctx)

	// allShortestPaths bounds the result set to k-shortest geodesics; we then
	// flatten edges across all returned paths and paginate via SKIP/LIMIT.
	// Unbounded * + filter would explode for highly-connected graphs.
	cypher := strings.TrimSpace(fmt.Sprintf(`
		MATCH p=allShortestPaths((a:`+"`%s`"+` {id: $from})-[*..%d]-(b:`+"`%s`"+` {id: $to}))
		UNWIND relationships(p) AS r
		WITH DISTINCT r
		WHERE ($asOf = 0)
		   OR (coalesce(r.valid_from, 0) <= $asOf
		       AND (r.valid_until IS NULL OR r.valid_until >= $asOf))
		RETURN startNode(r).id AS src,
		       endNode(r).id   AS tgt,
		       TYPE(r)         AS typ,
		       coalesce(r.valid_from, 0) AS vf,
		       r.valid_until   AS vu,
		       properties(r)   AS props
		ORDER BY src, tgt, typ
		SKIP $skip LIMIT $limit
	`, baseLabel, maxHops, baseLabel))

	params := map[string]any{
		"from":  q.From,
		"to":    q.To,
		"asOf":  q.AsOf,
		"skip":  int64(cur.Offset),
		"limit": int64(limit + 1), // overfetch by 1 to detect "more available"
	}

	rows, err := RunReadTx(ctx, w, func(ctx context.Context, tx neo4j.ManagedTransaction) ([]PathEdge, error) {
		res, runErr := tx.Run(ctx, cypher, params)
		if runErr != nil {
			return nil, fmt.Errorf("path query: %w", runErr)
		}
		var edges []PathEdge
		for res.Next(ctx) {
			rec := res.Record()
			src, _ := rec.Get("src")
			tgt, _ := rec.Get("tgt")
			typ, _ := rec.Get("typ")
			vfRaw, _ := rec.Get("vf")
			vuRaw, _ := rec.Get("vu")
			propsRaw, _ := rec.Get("props")

			edges = append(edges, PathEdge{
				SourceID:   fmt.Sprint(src),
				TargetID:   fmt.Sprint(tgt),
				Type:       fmt.Sprint(typ),
				ValidFrom:  toInt64(vfRaw),
				ValidUntil: toInt64Ptr(vuRaw),
				Properties: toStringMap(propsRaw),
			})
		}
		if err := res.Err(); err != nil {
			return edges, fmt.Errorf("path iterate: %w", err)
		}
		return edges, nil
	})
	if err != nil {
		return PathResult{}, err
	}

	out := PathResult{AsOf: q.AsOf}
	if len(rows) > limit {
		out.Edges = rows[:limit]
		out.NextCursor = encodeCursor(pathCursor{Offset: cur.Offset + limit})
	} else {
		out.Edges = rows
	}
	return out, nil
}

func toInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	}
	return 0
}

func toInt64Ptr(v any) *int64 {
	if v == nil {
		return nil
	}
	n := toInt64(v)
	return &n
}
