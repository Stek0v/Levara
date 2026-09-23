package store

import (
	"encoding/json"
)

// Rebuild classification for one collection record against the current
// document set and its published generations.
const (
	RebuildKeep       = "keep"
	RebuildOrphan     = "orphan"     // document no longer in the dataset
	RebuildSuperseded = "superseded" // older cognify generation of a live document
	RebuildNoDocMeta  = "nodocmeta"  // metadata carries no document_id — kept conservatively
)

// rebuildRecordMeta is the subset of chunk metadata the classifier needs.
type rebuildRecordMeta struct {
	DocumentID string `json:"document_id"`
	Generation string `json:"generation"`
}

// ClassifyRebuildRecords splits collection records into the IDs to keep and
// the IDs to remove (with reason). currentDocs is the set of live dataset
// data_ids; currentGens maps data_id → its latest published generation
// (empty string = unknown, generation check skipped).
func ClassifyRebuildRecords(records []SnapshotRecord, currentDocs map[string]struct{}, currentGens map[string]string) (keep []string, remove map[string]string) {
	keep = make([]string, 0, len(records))
	remove = make(map[string]string)
	for _, r := range records {
		var m rebuildRecordMeta
		_ = json.Unmarshal(r.Data, &m)
		if m.DocumentID == "" {
			keep = append(keep, r.ID)
			continue
		}
		if _, ok := currentDocs[m.DocumentID]; !ok {
			remove[r.ID] = RebuildOrphan
			continue
		}
		if cur, ok := currentGens[m.DocumentID]; ok && m.Generation != "" && cur != "" && m.Generation != cur {
			remove[r.ID] = RebuildSuperseded
			continue
		}
		keep = append(keep, r.ID)
	}
	return keep, remove
}
