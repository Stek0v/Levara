package graph

import (
	"strings"

	"github.com/google/uuid"
)

// NamespaceOID is uuid.NAMESPACE_OID — matches Python's uuid.NAMESPACE_OID.
var NamespaceOID = uuid.MustParse("6ba7b812-9dad-11d1-80b4-00c04fd430c8")

// DeduplicateResult holds the deduplicated nodes, edges, and generated triplets.
type DeduplicateResult struct {
	Nodes    []DedupNode
	Edges    []DedupEdge
	Triplets []Triplet
}

// DedupNode is a node for deduplication (preserves all original fields).
type DedupNode struct {
	ID            string
	Name          string
	Description   string
	Type          string
	Text          string
	Properties    map[string]string // additional key-value pairs
	// DataPoint provenance (GAP-9)
	Confidence    float32 // 0-1, LLM extraction confidence
	SourceChunkID string  // chunk ID this entity was extracted from
	SourceDocID   string  // document ID
	ExtractedAt   string  // RFC3339 timestamp
}

// DedupEdge is an edge for deduplication.
type DedupEdge struct {
	SourceID         string
	TargetID         string
	RelationshipName string
	EdgeText         string
	Properties       map[string]string
}

// Triplet is the result of triplet generation from deduplicated graph.
type Triplet struct {
	ID         string // UUID5(source_id + relationship + target_id)
	FromNodeID string
	ToNodeID   string
	Text       string // "source_text -› relationship -› target_text"
}

// Deduplicate removes duplicate nodes (by ID) and edges (by source+relationship+target),
// then generates triplets. Mirrors Levara's deduplicate_nodes_and_edges + _create_triplets_from_graph.
func Deduplicate(nodes []DedupNode, edges []DedupEdge) DeduplicateResult {
	// --- Deduplicate nodes (first occurrence wins) ---
	seen := make(map[string]bool, len(nodes))
	uniqueNodes := make([]DedupNode, 0, len(nodes))
	for _, n := range nodes {
		if !seen[n.ID] {
			seen[n.ID] = true
			uniqueNodes = append(uniqueNodes, n)
		}
	}

	// --- Deduplicate edges (key = source_id + relationship_name + target_id) ---
	edgeSeen := make(map[string]bool, len(edges))
	uniqueEdges := make([]DedupEdge, 0, len(edges))
	for _, e := range edges {
		key := e.SourceID + e.RelationshipName + e.TargetID
		if !edgeSeen[key] {
			edgeSeen[key] = true
			uniqueEdges = append(uniqueEdges, e)
		}
	}

	// --- Build node map for triplet generation ---
	nodeMap := make(map[string]*DedupNode, len(uniqueNodes))
	for i := range uniqueNodes {
		nodeMap[uniqueNodes[i].ID] = &uniqueNodes[i]
	}

	// --- Generate triplets ---
	tripletSeen := make(map[string]bool, len(uniqueEdges))
	triplets := make([]Triplet, 0, len(uniqueEdges))

	for _, e := range uniqueEdges {
		src := nodeMap[e.SourceID]
		tgt := nodeMap[e.TargetID]
		if src == nil || tgt == nil || e.RelationshipName == "" {
			continue
		}

		// Generate deterministic triplet ID (matches Python uuid5)
		idInput := e.SourceID + e.RelationshipName + e.TargetID
		tripletID := GenerateNodeID(idInput)

		if tripletSeen[tripletID] {
			continue
		}
		tripletSeen[tripletID] = true

		// Build embeddable text: "source -› relationship -› target"
		srcText := extractText(src)
		tgtText := extractText(tgt)
		relText := e.EdgeText
		if relText == "" {
			relText = e.RelationshipName
		}

		text := strings.TrimSpace(srcText + " -› " + relText + " -› " + tgtText)

		triplets = append(triplets, Triplet{
			ID:         tripletID,
			FromNodeID: e.SourceID,
			ToNodeID:   e.TargetID,
			Text:       text,
		})
	}

	return DeduplicateResult{
		Nodes:    uniqueNodes,
		Edges:    uniqueEdges,
		Triplets: triplets,
	}
}

// GenerateNodeID produces a deterministic UUID5 matching Python's generate_node_id().
// uuid5(NAMESPACE_OID, input.lower().replace(" ","_").replace("'",""))
func GenerateNodeID(input string) string {
	normalized := strings.ToLower(input)
	normalized = strings.ReplaceAll(normalized, " ", "_")
	normalized = strings.ReplaceAll(normalized, "'", "")
	return uuid.NewSHA1(NamespaceOID, []byte(normalized)).String()
}

// extractText gets embeddable text from a node (name, description, text — first non-empty).
func extractText(n *DedupNode) string {
	if n.Text != "" {
		return n.Text
	}
	if n.Name != "" {
		return n.Name
	}
	if n.Description != "" {
		return n.Description
	}
	return ""
}
