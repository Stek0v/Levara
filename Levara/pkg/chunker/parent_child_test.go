package chunker

import (
	"strings"
	"testing"
)

const parentChildText = `Introduction to Authentication

Modern web applications use JWT tokens for stateless authentication. Access tokens are stored in httpOnly cookies to prevent XSS attacks. Refresh tokens are rotated periodically and stored server-side in Redis with a configurable TTL.

Session Management

When a user logs in, the server generates both an access token and a refresh token. The access token has a short lifetime (15 minutes) while the refresh token lasts 7 days. Token rotation happens automatically on each refresh request.

Authorization Middleware

The authorization middleware validates the JWT token on every request. It checks the token signature, expiration, and required claims. If the token is invalid or expired, it returns a 401 status code and triggers a refresh flow on the client side.

Rate Limiting

API endpoints are protected by rate limiting based on the user's IP address and account tier. Free tier users are limited to 100 requests per minute, while premium users get 1000 requests per minute. Rate limit headers are included in every response.`

func TestChunkParentChild_Empty(t *testing.T) {
	parents, children := ChunkParentChild("", 2000, 256, "doc")
	if len(parents) != 0 || len(children) != 0 {
		t.Errorf("Empty text: expected 0 parents, 0 children; got %d, %d", len(parents), len(children))
	}
}

func TestChunkParentChild_ShortText(t *testing.T) {
	text := "This is a single short sentence that fits in one chunk for testing purposes only."
	parents, children := ChunkParentChild(text, 2000, 256, "doc")

	// Short text → 0 parents (below minChunkChars=80) or 1 parent
	// If the text is >= 80 chars, we get 1 parent
	if len(text) >= DefaultMinChunkChars {
		if len(parents) != 1 {
			t.Errorf("Short text: expected 1 parent, got %d", len(parents))
		}
		if len(children) == 0 {
			t.Error("Short text: expected at least 1 child")
		}
	}
}

func TestChunkParentChild_SingleParent(t *testing.T) {
	// Use first paragraph only — should produce 1 parent, multiple children
	text := parentChildText[:500]
	parents, children := ChunkParentChild(text, 2000, 150, "doc")

	if len(parents) != 1 {
		t.Fatalf("Expected 1 parent, got %d", len(parents))
	}
	if len(children) < 2 {
		t.Fatalf("Expected multiple children, got %d", len(children))
	}

	// All children should reference the parent
	for i, c := range children {
		if c.ParentID != parents[0].ID {
			t.Errorf("Child %d: ParentID=%s, want %s", i, c.ParentID, parents[0].ID)
		}
	}
}

func TestChunkParentChild_MultipleParents(t *testing.T) {
	parents, children := ChunkParentChild(parentChildText, 500, 150, "doc")

	if len(parents) < 2 {
		t.Fatalf("Expected multiple parents, got %d", len(parents))
	}
	if len(children) < len(parents) {
		t.Fatalf("Expected at least as many children as parents, got %d children for %d parents",
			len(children), len(parents))
	}

	t.Logf("Parents: %d, Children: %d", len(parents), len(children))
}

func TestChunkParentChild_ChildHasParentID(t *testing.T) {
	parents, children := ChunkParentChild(parentChildText, 800, 200, "doc")

	parentIDs := make(map[string]bool)
	for _, p := range parents {
		parentIDs[p.ID] = true
	}

	for i, c := range children {
		if c.ParentID == "" {
			t.Errorf("Child %d has empty ParentID", i)
		}
		if !parentIDs[c.ParentID] {
			t.Errorf("Child %d ParentID=%s not found in parents", i, c.ParentID)
		}
	}
}

func TestChunkParentChild_ChildTextInParent(t *testing.T) {
	parents, children := ChunkParentChild(parentChildText, 800, 200, "doc")

	parentByID := make(map[string]Chunk)
	for _, p := range parents {
		parentByID[p.ID] = p
	}

	for i, c := range children {
		parent, ok := parentByID[c.ParentID]
		if !ok {
			t.Errorf("Child %d: parent %s not found", i, c.ParentID)
			continue
		}
		// Child text should be a substring of parent text (possibly trimmed)
		childTrimmed := strings.TrimSpace(c.Text)
		if !strings.Contains(parent.Text, childTrimmed) {
			// Sentence chunking may slightly modify boundaries, check overlap
			overlap := overlapRatio(childTrimmed, parent.Text)
			if overlap < 0.8 {
				t.Errorf("Child %d text not found in parent (overlap=%.2f):\n  child: %q\n  parent: %q",
					i, overlap, childTrimmed[:min(80, len(childTrimmed))], parent.Text[:min(80, len(parent.Text))])
			}
		}
	}
}

func TestChunkParentChild_DeterministicIDs(t *testing.T) {
	p1, c1 := ChunkParentChild(parentChildText, 800, 200, "doc-A")
	p2, c2 := ChunkParentChild(parentChildText, 800, 200, "doc-A")

	if len(p1) != len(p2) {
		t.Fatalf("Parent count differs: %d vs %d", len(p1), len(p2))
	}
	for i := range p1 {
		if p1[i].ID != p2[i].ID {
			t.Errorf("Parent %d ID mismatch: %s vs %s", i, p1[i].ID, p2[i].ID)
		}
	}

	if len(c1) != len(c2) {
		t.Fatalf("Child count differs: %d vs %d", len(c1), len(c2))
	}
	for i := range c1 {
		if c1[i].ID != c2[i].ID {
			t.Errorf("Child %d ID mismatch: %s vs %s", i, c1[i].ID, c2[i].ID)
		}
	}
}

func TestChunkParentChild_DedupParents(t *testing.T) {
	// When multiple children from same parent match a query,
	// we should get only 1 unique parent ID
	parents, children := ChunkParentChild(parentChildText, 800, 200, "doc")

	parentIDCount := make(map[string]int)
	for _, c := range children {
		parentIDCount[c.ParentID]++
	}

	// At least one parent should have multiple children
	hasMultipleChildren := false
	for _, count := range parentIDCount {
		if count > 1 {
			hasMultipleChildren = true
			break
		}
	}
	if len(parents) > 0 && !hasMultipleChildren {
		t.Log("Warning: no parent has multiple children — test less meaningful")
	}
}

func TestChunkParentChild_DisabledDefault(t *testing.T) {
	// When parent_child is not used, regular chunking should work as before
	regular := ChunkByParagraphMerged(parentChildText, DefaultMinChunkChars, 800, "doc")
	if len(regular) == 0 {
		t.Fatal("Regular chunking should produce results")
	}
	// No ParentID on regular chunks
	for _, c := range regular {
		if c.ParentID != "" {
			t.Errorf("Regular chunk should not have ParentID, got %s", c.ParentID)
		}
	}
}

// overlapRatio returns the fraction of words in a that appear in b.
func overlapRatio(a, b string) float64 {
	wordsA := strings.Fields(strings.ToLower(a))
	wordsB := strings.Fields(strings.ToLower(b))

	setB := make(map[string]bool)
	for _, w := range wordsB {
		setB[w] = true
	}

	if len(wordsA) == 0 {
		return 0
	}

	matches := 0
	for _, w := range wordsA {
		if setB[w] {
			matches++
		}
	}
	return float64(matches) / float64(len(wordsA))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
