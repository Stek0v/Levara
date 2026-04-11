package chunker

import (
	"fmt"

	"github.com/google/uuid"
)

// ChunkParentChild creates a two-level chunking hierarchy.
// Parents: large merged paragraphs (parentMaxChars).
// Children: small sentence-based chunks (childMaxChars) within each parent.
// Each child has ParentID set to the parent's ID.
//
// Returns (parents, children). Both slices share the same documentID namespace.
func ChunkParentChild(text string, parentMaxChars, childMaxChars int, documentID string) (parents []Chunk, children []Chunk) {
	if parentMaxChars <= 0 {
		parentMaxChars = 2000
	}
	if childMaxChars <= 0 {
		childMaxChars = 256
	}

	// Step 1: Create parent chunks (large, paragraph-merged)
	parents = ChunkByParagraphMerged(text, DefaultMinChunkChars, parentMaxChars, documentID)

	// Step 2: For each parent, create children (small, sentence-based)
	for _, parent := range parents {
		childDocID := parent.ID // children are scoped within parent
		parentChildren := ChunkBySentence(parent.Text, 30, childMaxChars, childDocID)

		// If sentence chunking produces 0 children (text has no sentence ends),
		// create a single child with the full parent text
		if len(parentChildren) == 0 && len(parent.Text) > 0 {
			parentChildren = []Chunk{{
				ID:         childChunkID(parent.ID, 0),
				Text:       parent.Text,
				ChunkIndex: 0,
				CutType:    "sentence",
			}}
		}

		for i := range parentChildren {
			parentChildren[i].ParentID = parent.ID
			// Override ID with parent-scoped deterministic ID
			parentChildren[i].ID = childChunkID(parent.ID, i)
			parentChildren[i].ChunkIndex = len(children) + i
		}

		children = append(children, parentChildren...)
	}

	return parents, children
}

// childChunkID generates a deterministic UUID5 for a child chunk.
// Format: UUID5(NAMESPACE_OID, "{parentID}-child-{index}")
func childChunkID(parentID string, index int) string {
	name := fmt.Sprintf("%s-child-%d", parentID, index)
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()
}
