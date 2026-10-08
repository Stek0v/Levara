package community

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommunityPublicationProofLimits(t *testing.T) {
	source := Source{DatasetID: "dataset", DocumentID: "document", SourceRevision: 1, RawContentHash: strings.Repeat("a", 64), InputSHA256: strings.Repeat("b", 64)}
	encode := func(count int) string {
		sources := make([]Source, count)
		for i := range sources {
			sources[i] = source
		}
		raw, err := json.Marshal(sources)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if sources, err := ParseSources(encode(256)); err != nil || len(sources) != 256 {
		t.Fatalf("bounded proof rejected: count=%d err=%v", len(sources), err)
	}
	for name, raw := range map[string]string{
		"too_many_sources": encode(257),
		"too_many_bytes":   encode(1) + strings.Repeat(" ", 128<<10),
		"trailing_json":    encode(1) + "[]",
		"mixed_snapshot":   strings.Replace(encode(2), strings.Repeat("b", 64), strings.Repeat("c", 64), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSources(raw); err == nil {
				t.Fatal("invalid publication proof accepted")
			}
		})
	}
}
