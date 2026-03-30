// postgres.go — PostgreSQL graph store with recursive CTE for multi-hop traversal.
package graphstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// PostgresGraphStore implements GraphStore using SQL JOINs and recursive CTEs.
type PostgresGraphStore struct {
	db *sql.DB
}

// NewPostgresGraphStore creates a PostgreSQL-backed graph store.
func NewPostgresGraphStore(db *sql.DB) *PostgresGraphStore {
	return &PostgresGraphStore{db: db}
}

func (p *PostgresGraphStore) Close() error { return nil }

func (p *PostgresGraphStore) Query1Hop(ctx context.Context, entityNames []string) ([]GraphContext, error) {
	return p.QueryNHop(ctx, entityNames, 1)
}

func (p *PostgresGraphStore) Query2Hop(ctx context.Context, entityNames []string) ([]GraphContext, error) {
	return p.QueryNHop(ctx, entityNames, 2)
}

func (p *PostgresGraphStore) QueryNHop(ctx context.Context, entityNames []string, hops int) ([]GraphContext, error) {
	if len(entityNames) == 0 || p.db == nil {
		return nil, nil
	}

	// Build IN clause
	placeholders := make([]string, len(entityNames))
	args := make([]any, len(entityNames))
	for i, name := range entityNames {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = name
	}
	inClause := strings.Join(placeholders, ",")
	maxDepthArg := fmt.Sprintf("$%d", len(entityNames)+1)
	args = append(args, hops)

	// Recursive CTE for N-hop traversal
	query := fmt.Sprintf(`
		WITH RECURSIVE traversal AS (
			-- Seed: direct neighbors of named entities
			SELECT
				gn.name AS source_name, gn.type AS source_type,
				ge.relationship_name,
				gn2.name AS target_name, gn2.type AS target_type,
				1 AS depth
			FROM graph_nodes gn
			JOIN graph_edges ge ON ge.source_id = gn.id
			JOIN graph_nodes gn2 ON gn2.id = ge.target_id
			WHERE gn.name IN (%s)

			UNION ALL

			-- Recursive step: neighbors of neighbors
			SELECT
				gn.name, gn.type,
				ge.relationship_name,
				gn2.name, gn2.type,
				t.depth + 1
			FROM traversal t
			JOIN graph_nodes gn ON gn.name = t.target_name
			JOIN graph_edges ge ON ge.source_id = gn.id
			JOIN graph_nodes gn2 ON gn2.id = ge.target_id
			WHERE t.depth < %s
		)
		SELECT DISTINCT source_name, source_type, relationship_name, target_name, target_type
		FROM traversal
		ORDER BY source_name
		LIMIT 100
	`, inClause, maxDepthArg)

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		// Fallback: simple JOIN (no recursion, for SQLite without CTE support)
		return p.querySimpleJoin(ctx, entityNames)
	}
	defer rows.Close()

	var results []GraphContext
	for rows.Next() {
		var gc GraphContext
		rows.Scan(&gc.SourceName, &gc.SourceType, &gc.Relationship, &gc.TargetName, &gc.TargetType)
		results = append(results, gc)
	}
	return results, nil
}

// querySimpleJoin — fallback for databases without recursive CTE support.
func (p *PostgresGraphStore) querySimpleJoin(ctx context.Context, entityNames []string) ([]GraphContext, error) {
	if len(entityNames) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(entityNames))
	args := make([]any, len(entityNames))
	for i, name := range entityNames {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = name
	}
	inClause := strings.Join(placeholders, ",")

	query := fmt.Sprintf(`
		SELECT gn.name, gn.type, ge.relationship_name, gn2.name, gn2.type
		FROM graph_nodes gn
		JOIN graph_edges ge ON ge.source_id = gn.id
		JOIN graph_nodes gn2 ON gn2.id = ge.target_id
		WHERE gn.name IN (%s)
		LIMIT 50
	`, inClause)

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []GraphContext
	for rows.Next() {
		var gc GraphContext
		rows.Scan(&gc.SourceName, &gc.SourceType, &gc.Relationship, &gc.TargetName, &gc.TargetType)
		results = append(results, gc)
	}
	return results, nil
}
