// Package graphstore provides a pluggable interface for graph storage backends.
// Supports: Neo4j (external), PostgreSQL (recursive CTE for multi-hop).
package graphstore

import "context"

// NodeRecord represents a graph node for read/write.
type NodeRecord struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Type        string         `json:"type"`
	Description string         `json:"description"`
	Properties  map[string]any `json:"properties,omitempty"`
}

// EdgeRecord represents a graph edge for read/write.
type EdgeRecord struct {
	ID               string         `json:"id"`
	SourceID         string         `json:"source_id"`
	TargetID         string         `json:"target_id"`
	RelationshipName string         `json:"relationship_name"`
	Properties       map[string]any `json:"properties,omitempty"`
}

// GraphContext is a single relationship returned from traversal queries.
type GraphContext struct {
	SourceName   string `json:"source"`
	Relationship string `json:"relationship"`
	TargetName   string `json:"target"`
	SourceType   string `json:"source_type,omitempty"`
	TargetType   string `json:"target_type,omitempty"`
}

// GraphStore is the interface for graph storage backends.
type GraphStore interface {
	// Query1Hop returns direct neighbors of named entities.
	Query1Hop(ctx context.Context, entityNames []string) ([]GraphContext, error)

	// Query2Hop returns neighbors + neighbors-of-neighbors.
	Query2Hop(ctx context.Context, entityNames []string) ([]GraphContext, error)

	// QueryNHop returns N-hop traversal results.
	QueryNHop(ctx context.Context, entityNames []string, hops int) ([]GraphContext, error)

	// Close releases resources.
	Close() error
}
