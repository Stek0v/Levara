// Package graphstore provides a pluggable interface for graph storage backends.
// Supports: Neo4j (external), SQL graph tables (SQLite/PostgreSQL).
//
// ADR-001 activation is in progress: SQL graph reads/path traversal are wired
// into selected HTTP handlers, while some legacy paths still import graphdb
// directly. See docs/adr/001-graph-layering.md for the full migration plan.
package graphstore

import (
	"context"

	"github.com/stek0v/levara/pkg/graphdb"
)

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

// BatchWriteResult holds the outcome of a graph write operation.
type BatchWriteResult struct {
	NodesWritten int
	EdgesWritten int
	Errors       []string
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

	// ReadFullGraph returns graph data in the DTO used by existing graph APIs.
	ReadFullGraph(ctx context.Context) (graphdb.GraphReadResult, error)

	// PathBetween returns shortest-path edges between two node IDs.
	PathBetween(ctx context.Context, q graphdb.PathQuery) (graphdb.PathResult, error)

	// WriteGraph upserts nodes and edges into the graph store. datasetID scopes
	// rows for SQL stores and is also projected into properties for adapters that
	// carry tenant metadata in the payload.
	WriteGraph(ctx context.Context, datasetID string, nodes []NodeRecord, edges []EdgeRecord) BatchWriteResult

	// Close releases resources.
	Close() error
}
