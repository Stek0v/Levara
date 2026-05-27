package http

// Document chunking for the collection re-embed path. Collection records can
// hold whole documents whose text exceeds the embed model's context window,
// which makes a single-vector re-embed fail with "input length exceeds the
// context length". Such records are split into overlapping chunks here, each
// embedded and stored as its own vector; records that already fit pass through
// unchanged.

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/stek0v/levara/pkg/chunker"
)

// reembedMaxRunes is the per-record text budget for a single embed call during
// collection import. A BPE token always covers at least one byte, so a record
// of at most 4000 runes (<= ~8000 bytes even for 2-byte scripts) stays under
// nomic-embed's 8192-token context regardless of language. Records above this
// are split into overlapping chunks. It mirrors the repo's sliding-chunk
// convention (overlap = window/5).
const reembedMaxRunes = 4000

// embedUnit is one text to embed and store: either a whole record (original ID
// and metadata preserved) or a chunk of an oversized record (derived ID and
// chunk-scoped metadata).
type embedUnit struct {
	id   string
	text string
	meta json.RawMessage
}

// expandRecordsToUnits turns import records into embeddable units. Records whose
// text fits maxRunes pass through unchanged. Oversized records are split with
// the shared sliding chunker (snap-to-sentence on) into units with deterministic
// chunk IDs and chunk-scoped metadata. Returns the units and the count of
// records dropped because they held no embeddable text.
func expandRecordsToUnits(records []syncCollectionRecord, maxRunes, overlap int) ([]embedUnit, int) {
	units := make([]embedUnit, 0, len(records))
	skipped := 0
	for _, r := range records {
		if utf8.RuneCountInString(r.Text) <= maxRunes {
			units = append(units, embedUnit{id: r.ID, text: r.Text, meta: r.Metadata})
			continue
		}
		chunks := chunker.ChunkBySliding(r.Text, maxRunes, overlap, r.ID)
		if len(chunks) == 0 {
			skipped++
			continue
		}
		for _, ch := range chunks {
			units = append(units, embedUnit{
				id:   ch.ID,
				text: ch.Text,
				meta: chunkMeta(r.Metadata, r.ID, ch.Text, ch.ChunkIndex, len(chunks)),
			})
		}
	}
	return units, skipped
}

// chunkMeta builds metadata for a chunk. When the source metadata is a JSON
// object, the field that textFromMetadata would have read is replaced with the
// chunk text (so the stored payload matches the chunk vector and the full
// document is not duplicated once per chunk) and chunk lineage is added
// (_source_id, _chunk_index, _chunk_total). Non-object metadata is wrapped in a
// minimal object.
func chunkMeta(orig json.RawMessage, sourceID, chunkText string, idx, total int) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(orig) == 0 || json.Unmarshal(orig, &m) != nil {
		m = map[string]json.RawMessage{}
	}
	set := func(k string, v any) {
		if b, err := json.Marshal(v); err == nil {
			m[k] = b
		}
	}
	// Replace the same field textFromMetadata reads, mirroring its priority.
	replaced := false
	for _, k := range []string{"text", "name", "description", "content", "value", "key"} {
		if _, ok := m[k]; ok {
			set(k, chunkText)
			replaced = true
			break
		}
	}
	if !replaced {
		set("text", chunkText)
	}
	set("_source_id", sourceID)
	set("_chunk_index", idx)
	set("_chunk_total", total)
	if out, err := json.Marshal(m); err == nil {
		return out
	}
	return orig
}
