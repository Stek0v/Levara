package http

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestExpandRecordsToUnits_ShortPassthrough(t *testing.T) {
	recs := []syncCollectionRecord{
		{ID: "rec-1", Text: "short text", Metadata: json.RawMessage(`{"text":"short text","k":1}`)},
	}
	units, skipped := expandRecordsToUnits(recs, 100, 20)
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if len(units) != 1 {
		t.Fatalf("len(units) = %d, want 1", len(units))
	}
	if units[0].id != "rec-1" {
		t.Errorf("id = %q, want unchanged %q", units[0].id, "rec-1")
	}
	if units[0].text != "short text" {
		t.Errorf("text = %q, want unchanged", units[0].text)
	}
	if string(units[0].meta) != `{"text":"short text","k":1}` {
		t.Errorf("meta = %s, want unchanged", units[0].meta)
	}
}

func TestExpandRecordsToUnits_LongSplits(t *testing.T) {
	maxRunes := 100
	long := strings.Repeat("word ", 200) // 1000 runes, well over the limit
	recs := []syncCollectionRecord{
		{ID: "doc-A", Text: long, Metadata: json.RawMessage(`{"text":"FULL DOCUMENT"}`)},
	}
	units, skipped := expandRecordsToUnits(recs, maxRunes, maxRunes/5)
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if len(units) < 2 {
		t.Fatalf("len(units) = %d, want >= 2 chunks", len(units))
	}
	seen := map[string]bool{}
	for i, u := range units {
		if n := utf8.RuneCountInString(u.text); n > maxRunes {
			t.Errorf("unit %d text has %d runes, want <= %d", i, n, maxRunes)
		}
		if u.id == "doc-A" {
			t.Errorf("unit %d kept the raw record ID; chunk IDs must be derived", i)
		}
		if seen[u.id] {
			t.Errorf("duplicate chunk ID %q", u.id)
		}
		seen[u.id] = true

		var m map[string]any
		if err := json.Unmarshal(u.meta, &m); err != nil {
			t.Fatalf("unit %d meta not an object: %v", i, err)
		}
		if m["_source_id"] != "doc-A" {
			t.Errorf("unit %d _source_id = %v, want doc-A", i, m["_source_id"])
		}
		if _, ok := m["_chunk_index"]; !ok {
			t.Errorf("unit %d missing _chunk_index", i)
		}
		if got := m["_chunk_total"]; got != float64(len(units)) {
			t.Errorf("unit %d _chunk_total = %v, want %d", i, got, len(units))
		}
		if m["text"] == "FULL DOCUMENT" {
			t.Errorf("unit %d still carries the full document text in metadata", i)
		}
	}
}

func TestExpandRecordsToUnits_SkipsOversizedWhitespace(t *testing.T) {
	recs := []syncCollectionRecord{
		{ID: "blank", Text: strings.Repeat(" ", 500), Metadata: json.RawMessage(`{}`)},
	}
	units, skipped := expandRecordsToUnits(recs, 100, 20)
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
	if len(units) != 0 {
		t.Errorf("len(units) = %d, want 0", len(units))
	}
}

func TestChunkMeta_ReplacesMatchedTextField(t *testing.T) {
	orig := json.RawMessage(`{"text":"whole doc","author":"x"}`)
	out := chunkMeta(orig, "src-1", "chunk piece", 2, 5)

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("not an object: %v", err)
	}
	if m["text"] != "chunk piece" {
		t.Errorf("text = %v, want chunk piece", m["text"])
	}
	if m["author"] != "x" {
		t.Errorf("author = %v, want preserved", m["author"])
	}
	if m["_source_id"] != "src-1" {
		t.Errorf("_source_id = %v, want src-1", m["_source_id"])
	}
	if m["_chunk_index"] != float64(2) {
		t.Errorf("_chunk_index = %v, want 2", m["_chunk_index"])
	}
	if m["_chunk_total"] != float64(5) {
		t.Errorf("_chunk_total = %v, want 5", m["_chunk_total"])
	}
}

func TestChunkMeta_ContentFieldPreferredWhenNoText(t *testing.T) {
	orig := json.RawMessage(`{"content":"whole doc"}`)
	out := chunkMeta(orig, "src-2", "piece", 0, 1)

	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["content"] != "piece" {
		t.Errorf("content = %v, want piece (matched field replaced)", m["content"])
	}
	if _, ok := m["text"]; ok {
		t.Errorf("unexpected text key added when content was the matched field")
	}
}

func TestChunkMeta_NonObjectWraps(t *testing.T) {
	orig := json.RawMessage(`"just a json string"`)
	out := chunkMeta(orig, "src-3", "piece", 1, 3)

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("non-object metadata was not wrapped into an object: %v", err)
	}
	if m["text"] != "piece" {
		t.Errorf("text = %v, want piece", m["text"])
	}
	if m["_source_id"] != "src-3" {
		t.Errorf("_source_id = %v, want src-3", m["_source_id"])
	}
}
