// Package graph provides in-memory graph structures and triplet scoring
// for fast search-time ranking of knowledge graph edges.
//
// The algorithm mirrors Levara's brute_force_triplet_search:
//  1. Build in-memory graph from nodes + edges
//  2. Map vector distances from DB search results onto graph elements
//  3. Score each edge as sum(node1_dist + edge_dist + node2_dist)
//  4. Return top-k edges by lowest score (heap-based selection)
package graph

import (
	"container/heap"
	"fmt"
	"strings"
)

// DefaultDistancePenalty is applied to nodes/edges not found in vector search.
const DefaultDistancePenalty = 3.5

// Node represents a graph node with vector distance tracking.
type Node struct {
	ID          string
	Name        string
	Description string
	Type        string
	Text        string
	Distance    float32 // from vector search (penalty if not found)
}

// Edge represents a directed graph edge between two nodes.
type Edge struct {
	Node1ID          string
	Node2ID          string
	RelationshipType string
	EdgeText         string
	EdgeTypeID       string  // grouping key for edge distances
	Distance         float32 // from vector search (penalty if not found)
}

// ScoredEdge is an edge with its computed triplet score.
type ScoredEdge struct {
	Node1    *Node
	Node2    *Node
	Edge     *Edge
	Score    float32 // node1.Distance + edge.Distance + node2.Distance
}

// DistanceEntry maps an ID to a vector distance.
type DistanceEntry struct {
	ID       string
	Distance float32
}

// Graph is an in-memory graph for triplet search scoring.
type Graph struct {
	Nodes           map[string]*Node
	Edges           []*Edge
	DistancePenalty float32
}

// NewGraph creates a graph with the given penalty for unseen elements.
func NewGraph(penalty float32) *Graph {
	if penalty <= 0 {
		penalty = DefaultDistancePenalty
	}
	return &Graph{
		Nodes:           make(map[string]*Node),
		DistancePenalty: penalty,
	}
}

// AddNode adds a node to the graph with default distance penalty.
func (g *Graph) AddNode(id, name, description, typ, text string) {
	g.Nodes[id] = &Node{
		ID:          id,
		Name:        name,
		Description: description,
		Type:        typ,
		Text:        text,
		Distance:    g.DistancePenalty,
	}
}

// AddEdge adds a directed edge between two nodes.
func (g *Graph) AddEdge(node1ID, node2ID, relType, edgeText, edgeTypeID string) {
	g.Edges = append(g.Edges, &Edge{
		Node1ID:          node1ID,
		Node2ID:          node2ID,
		RelationshipType: relType,
		EdgeText:         edgeText,
		EdgeTypeID:       edgeTypeID,
		Distance:         g.DistancePenalty,
	})
}

// MapNodeDistances assigns vector distances to nodes by ID.
// Multiple collections can contribute distances; lowest wins.
func (g *Graph) MapNodeDistances(entries []DistanceEntry) {
	for _, e := range entries {
		if node, ok := g.Nodes[e.ID]; ok {
			if e.Distance < node.Distance {
				node.Distance = e.Distance
			}
		}
	}
}

// MapEdgeDistances assigns vector distances to edges by EdgeTypeID.
// All edges sharing the same EdgeTypeID get the same distance.
func (g *Graph) MapEdgeDistances(entries []DistanceEntry) {
	// Build lookup: edge_type_id → distance
	distMap := make(map[string]float32, len(entries))
	for _, e := range entries {
		distMap[e.ID] = e.Distance
	}

	for _, edge := range g.Edges {
		if d, ok := distMap[edge.EdgeTypeID]; ok {
			if d < edge.Distance {
				edge.Distance = d
			}
		}
	}
}

// SearchTopK returns the top-k triplets scored by sum of distances.
// Lower score = more relevant. Uses heap for O(n + k*log(n)).
func (g *Graph) SearchTopK(k int) []ScoredEdge {
	if k <= 0 {
		k = 5
	}

	// Score all edges
	scored := make(scoredHeap, 0, len(g.Edges))
	for _, edge := range g.Edges {
		node1, ok1 := g.Nodes[edge.Node1ID]
		node2, ok2 := g.Nodes[edge.Node2ID]
		if !ok1 || !ok2 {
			continue // skip edges with missing nodes
		}

		score := node1.Distance + edge.Distance + node2.Distance
		scored = append(scored, ScoredEdge{
			Node1: node1,
			Node2: node2,
			Edge:  edge,
			Score: score,
		})
	}

	// Heap-based top-k selection (min-heap by score)
	heap.Init(&scored)

	result := make([]ScoredEdge, 0, min(k, len(scored)))
	for i := 0; i < k && len(scored) > 0; i++ {
		result = append(result, heap.Pop(&scored).(ScoredEdge))
	}

	return result
}

// FormatTriplets produces human-readable text for LLM context.
func FormatTriplets(triplets []ScoredEdge) string {
	if len(triplets) == 0 {
		return ""
	}

	var b strings.Builder
	for _, t := range triplets {
		// Node1
		b.WriteString("Node1: {")
		writeNodeAttrs(&b, t.Node1)
		b.WriteString("}\n")

		// Edge
		b.WriteString("Edge: {")
		writeEdgeAttrs(&b, t.Edge)
		b.WriteString("}\n")

		// Node2
		b.WriteString("Node2: {")
		writeNodeAttrs(&b, t.Node2)
		b.WriteString("}\n\n")
	}

	return b.String()
}

func writeNodeAttrs(b *strings.Builder, n *Node) {
	parts := make([]string, 0, 5)
	parts = append(parts, fmt.Sprintf("'id': '%s'", n.ID))
	if n.Name != "" {
		parts = append(parts, fmt.Sprintf("'name': '%s'", n.Name))
	}
	if n.Description != "" {
		parts = append(parts, fmt.Sprintf("'description': '%s'", n.Description))
	}
	if n.Type != "" {
		parts = append(parts, fmt.Sprintf("'type': '%s'", n.Type))
	}
	if n.Text != "" {
		parts = append(parts, fmt.Sprintf("'text': '%s'", n.Text))
	}
	b.WriteString(strings.Join(parts, ", "))
}

func writeEdgeAttrs(b *strings.Builder, e *Edge) {
	parts := make([]string, 0, 2)
	if e.RelationshipType != "" {
		parts = append(parts, fmt.Sprintf("'relationship_type': '%s'", e.RelationshipType))
	}
	if e.EdgeText != "" {
		parts = append(parts, fmt.Sprintf("'edge_text': '%s'", e.EdgeText))
	}
	b.WriteString(strings.Join(parts, ", "))
}

// --- min-heap for scored edges (lowest score first) ---

type scoredHeap []ScoredEdge

func (h scoredHeap) Len() int            { return len(h) }
func (h scoredHeap) Less(i, j int) bool   { return h[i].Score < h[j].Score }
func (h scoredHeap) Swap(i, j int)        { h[i], h[j] = h[j], h[i] }
func (h *scoredHeap) Push(x interface{})  { *h = append(*h, x.(ScoredEdge)) }
func (h *scoredHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}
