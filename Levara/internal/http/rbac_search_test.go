// rbac_search_test.go — Wave D: end-to-end RBAC for search.
//
// The production code path is:
//
//   searchHandler → c.Locals("user_id") → GetAllowedDatasetIDs(db, ctx, uid)
//     → req.AllowedDatasetIDs → filterByAllowedDatasets(results, allowed)
//
// We wire a middleware that sets Locals("user_id") to a test user, seed
// users/datasets/dataset_shares, and insert vectors tagged with dataset_id
// in their metadata. The tests verify:
//
//   * User B cannot see user A's data (isolation).
//   * Once A shares with B, B can see it (share grant).
//   * Without user_id (anonymous) no filter applies (dev-mode compat).
//   * Superusers bypass the filter.
//
// This complements the Wave A RBAC test, which only checked the "no
// filter" branch — here we prove the filter itself works end-to-end.
package http

import (
	"testing"
)

func TestRBAC_UserBCannotSeeUserAData(t *testing.T) {
	env := newSearchTestEnv(t)
	env.startWithUser("user-b")

	env.insertUser("user-a", "a@example.com", false)
	env.insertUser("user-b", "b@example.com", false)
	env.insertDataset("ds-a", "user-a")
	env.insertDataset("ds-b", "user-b")

	vec := []float32{1, 0, 0, 0}
	// Data in user A's dataset (not shared).
	env.insertVector("entities", "a1", vec, map[string]any{
		"name": "Alice", "dataset_id": "ds-a",
	})
	// Data in user B's own dataset.
	env.insertVector("entities", "b1", vec, map[string]any{
		"name": "Bob", "dataset_id": "ds-b",
	})

	_, body := env.postSearch(map[string]any{
		"query_text": "q",
		"query_type": "GRAPH_COMPLETION",
		"collection": "entities",
	})
	chunks, _ := body["chunks"].([]any)
	if len(chunks) != 1 {
		t.Fatalf("chunks len=%d, want 1 (user-b only sees ds-b)", len(chunks))
	}
	chunk, _ := chunks[0].(map[string]any)
	if id, _ := chunk["id"].(string); id != "b1" {
		t.Errorf("visible chunk id=%q, want b1", id)
	}
}

// After owner A shares ds-a with user B, user B's search must include
// ds-a's chunks. Verifies dataset_shares is read into AllowedDatasetIDs.
func TestRBAC_SharedDatasetVisibleToGrantee(t *testing.T) {
	env := newSearchTestEnv(t)
	env.startWithUser("user-b")

	env.insertUser("user-a", "a@example.com", false)
	env.insertUser("user-b", "b@example.com", false)
	env.insertDataset("ds-a", "user-a")
	env.shareDataset("ds-a", "user-b", "viewer")

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "a1", vec, map[string]any{
		"name": "Alice", "dataset_id": "ds-a",
	})

	_, body := env.postSearch(map[string]any{
		"query_text": "q",
		"query_type": "GRAPH_COMPLETION",
		"collection": "entities",
	})
	chunks, _ := body["chunks"].([]any)
	if len(chunks) != 1 {
		t.Fatalf("chunks len=%d, want 1 (ds-a now shared to user-b)", len(chunks))
	}
}

// Anonymous request (no Locals("user_id")) → GetAllowedDatasetIDs returns
// nil → filterByAllowedDatasets is a no-op. Both chunks come back.
func TestRBAC_AnonymousBypass(t *testing.T) {
	env := newSearchTestEnv(t)
	env.start() // no user middleware

	env.insertUser("user-a", "a@example.com", false)
	env.insertDataset("ds-a", "user-a")

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "a1", vec, map[string]any{
		"name": "Alice", "dataset_id": "ds-a",
	})
	env.insertVector("entities", "orphan", vec, map[string]any{
		"name": "Orphan", // no dataset_id
	})

	_, body := env.postSearch(map[string]any{
		"query_text": "q",
		"query_type": "GRAPH_COMPLETION",
		"collection": "entities",
	})
	chunks, _ := body["chunks"].([]any)
	if len(chunks) != 2 {
		t.Errorf("chunks len=%d, want 2 (no filter without user_id)", len(chunks))
	}
}

// Superuser bypass: is_superuser=true → GetAllowedDatasetIDs returns nil
// → no filter, even with user_id set. Validates the bypass clause in
// rbac.go:229-233.
func TestRBAC_SuperuserBypass(t *testing.T) {
	env := newSearchTestEnv(t)
	env.startWithUser("admin")

	env.insertUser("admin", "admin@example.com", true)
	env.insertUser("user-a", "a@example.com", false)
	env.insertDataset("ds-a", "user-a")

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "a1", vec, map[string]any{
		"name": "Alice", "dataset_id": "ds-a",
	})

	_, body := env.postSearch(map[string]any{
		"query_text": "q",
		"query_type": "GRAPH_COMPLETION",
		"collection": "entities",
	})
	chunks, _ := body["chunks"].([]any)
	if len(chunks) != 1 {
		t.Errorf("chunks len=%d, want 1 (superuser sees everything)", len(chunks))
	}
}

// Chunks with no dataset_id on their metadata must pass the filter even
// when the user has a narrow allow-list. This matches production
// filterByAllowedDatasets behaviour (empty dsID → always allowed) and
// protects legacy data without dataset tags.
func TestRBAC_OrphanChunksAllowed(t *testing.T) {
	env := newSearchTestEnv(t)
	env.startWithUser("user-b")

	env.insertUser("user-a", "a@example.com", false)
	env.insertUser("user-b", "b@example.com", false)
	env.insertDataset("ds-a", "user-a")
	env.insertDataset("ds-b", "user-b")

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "a1", vec, map[string]any{
		"name": "Alice", "dataset_id": "ds-a",
	})
	env.insertVector("entities", "orphan", vec, map[string]any{
		"name": "Orphan", // no dataset_id → always visible
	})

	_, body := env.postSearch(map[string]any{
		"query_text": "q",
		"query_type": "GRAPH_COMPLETION",
		"collection": "entities",
	})
	chunks, _ := body["chunks"].([]any)
	if len(chunks) != 1 {
		t.Fatalf("chunks len=%d, want 1 (orphan only; ds-a filtered out)", len(chunks))
	}
	chunk, _ := chunks[0].(map[string]any)
	if id, _ := chunk["id"].(string); id != "orphan" {
		t.Errorf("visible chunk id=%q, want orphan", id)
	}
}

// Same isolation check, but for the triplet path (different handler,
// same filter code). Regression guard: a future refactor must not drop
// the filter call from any handler that surfaces chunks.
func TestRBAC_TripletHandlerHonoursFilter(t *testing.T) {
	env := newSearchTestEnv(t)
	env.startWithUser("user-b")

	env.insertUser("user-a", "a@example.com", false)
	env.insertUser("user-b", "b@example.com", false)
	env.insertDataset("ds-a", "user-a")
	env.insertDataset("ds-b", "user-b")

	vec := []float32{1, 0, 0, 0}
	env.insertVector("triplets_main", "t-a", vec, map[string]any{
		"source": "Alice", "rel": "KNOWS", "target": "Bob", "dataset_id": "ds-a",
	})
	env.insertVector("triplets_main", "t-b", vec, map[string]any{
		"source": "Charlie", "rel": "WORKS_AT", "target": "Acme", "dataset_id": "ds-b",
	})

	_, body := env.postSearch(map[string]any{
		"query_text": "q",
		"query_type": "TRIPLET_COMPLETION",
		"collection": "triplets_main",
	})
	triplets, _ := body["triplets"].([]any)
	if len(triplets) != 1 {
		t.Fatalf("triplets len=%d, want 1 (user-b only sees ds-b)", len(triplets))
	}
	tri, _ := triplets[0].(map[string]any)
	if id, _ := tri["id"].(string); id != "t-b" {
		t.Errorf("visible triplet id=%q, want t-b", id)
	}
}
