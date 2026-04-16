package mcp

import "testing"

func TestValidHalls_StableOrder(t *testing.T) {
	got := ValidHalls()
	want := []string{"fact", "event", "decision", "preference", "advice", "discovery"}
	if len(got) != len(want) {
		t.Fatalf("got %d halls, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("hall[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestIsValidHall(t *testing.T) {
	for _, h := range []string{"fact", "event", "decision", "preference", "advice", "discovery"} {
		if !IsValidHall(h) {
			t.Errorf("IsValidHall(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"", "facts", "FACT", "unknown", "memory"} {
		if IsValidHall(h) {
			t.Errorf("IsValidHall(%q) = true, want false", h)
		}
	}
}

func TestChunkMetaMatches_NoFilter(t *testing.T) {
	if !ChunkMetaMatches(nil, "", nil) {
		t.Error("no filter should always match")
	}
	if !ChunkMetaMatches([]byte(`{"room":"x"}`), "", nil) {
		t.Error("no filter should match arbitrary metadata")
	}
}

func TestChunkMetaMatches_RoomFilter(t *testing.T) {
	meta := []byte(`{"room":"auth","tags":["security"]}`)
	if !ChunkMetaMatches(meta, "auth", nil) {
		t.Error("room match should pass")
	}
	if ChunkMetaMatches(meta, "deploy", nil) {
		t.Error("room mismatch should fail")
	}
}

func TestChunkMetaMatches_TagFilterOrSemantics(t *testing.T) {
	meta := []byte(`{"room":"auth","tags":["security","oauth"]}`)
	// Any-match: presence of any wanted tag is enough.
	if !ChunkMetaMatches(meta, "", []string{"oauth"}) {
		t.Error("single tag match should pass")
	}
	if !ChunkMetaMatches(meta, "", []string{"missing", "oauth"}) {
		t.Error("multi-tag OR should pass when one matches")
	}
	if ChunkMetaMatches(meta, "", []string{"missing", "absent"}) {
		t.Error("no overlapping tag should fail")
	}
}

func TestChunkMetaMatches_BothFilters(t *testing.T) {
	meta := []byte(`{"room":"auth","tags":["security"]}`)
	if !ChunkMetaMatches(meta, "auth", []string{"security"}) {
		t.Error("both matching should pass")
	}
	if ChunkMetaMatches(meta, "auth", []string{"oauth"}) {
		t.Error("room match + tag miss should fail")
	}
	if ChunkMetaMatches(meta, "deploy", []string{"security"}) {
		t.Error("room miss + tag match should fail")
	}
}

func TestChunkMetaMatches_MalformedJSON_DropsWhenFiltered(t *testing.T) {
	// Older chunks without metadata: dropped only when a filter is requested.
	if !ChunkMetaMatches([]byte("not json"), "", nil) {
		t.Error("no filter: should match even malformed metadata")
	}
	if ChunkMetaMatches([]byte("not json"), "auth", nil) {
		t.Error("with filter: should drop malformed metadata")
	}
}
