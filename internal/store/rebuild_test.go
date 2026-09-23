package store

import (
	"encoding/json"
	"testing"
)

func rec(id, doc, gen string) SnapshotRecord {
	meta, _ := json.Marshal(map[string]string{"document_id": doc, "generation": gen})
	return SnapshotRecord{ID: id, Vector: make([]float32, 4), Data: meta}
}

func TestClassifyRebuildRecords(t *testing.T) {
	records := []SnapshotRecord{
		rec("k1", "doc-a", "g1"),    // current generation → keep
		rec("s1", "doc-a", "g0"),    // older generation → superseded
		rec("o1", "doc-gone", "g1"), // deleted document → orphan
		rec("k2", "doc-b", ""),      // no generation info → keep
		{ID: "k3", Vector: nil, Data: json.RawMessage(`{"text":"x"}`)}, // no document_id → keep
		rec("s2", "doc-c", "old"),                                      // doc-c current gen differs → superseded
	}
	docs := map[string]struct{}{"doc-a": {}, "doc-b": {}, "doc-c": {}}
	gens := map[string]string{"doc-a": "g1", "doc-c": "new", "doc-b": ""}

	keep, remove := ClassifyRebuildRecords(records, docs, gens)
	if len(keep) != 3 {
		t.Fatalf("keep = %v, want k1,k2,k3", keep)
	}
	if remove["s1"] != RebuildSuperseded || remove["s2"] != RebuildSuperseded || remove["o1"] != RebuildOrphan {
		t.Fatalf("remove = %v", remove)
	}
}
