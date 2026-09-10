package bm25

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestLoadSnapshotStrictOperationsAndCorruption(t *testing.T) {
	good := "{\"op\":\"put\",\"i\":\"a\",\"t\":\"old\"}\n{\"op\":\"delete\",\"i\":\"a\"}\n{\"op\":\"put\",\"i\":\"b\",\"t\":\"recovery searchable\"}\n"
	idx, err := LoadSnapshotStrict(context.Background(), strings.NewReader(good), int64(len(good)), 10)
	if err != nil {
		t.Fatal(err)
	}
	docs := idx.Documents()
	if len(docs) != 1 || docs[0].ID != "b" || len(idx.Search("searchable", 1)) != 1 {
		t.Fatalf("native logical content missing: %+v", docs)
	}
	for _, bad := range []string{good + "{", `{"i":""}`, `{"op":"unknown","i":"x"}`, good + "\n"} {
		if _, err := LoadSnapshotStrict(context.Background(), strings.NewReader(bad), int64(len(bad)), 10); err == nil {
			t.Fatalf("bad record accepted: %q", bad)
		}
	}
	if _, err = LoadSnapshotStrict(context.Background(), strings.NewReader(good), int64(len(good)-1), 10); err == nil {
		t.Fatal("byte budget ignored")
	}
	if _, err = LoadSnapshotStrict(context.Background(), strings.NewReader(good), int64(len(good)), 1); err == nil {
		t.Fatal("operation budget ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = LoadSnapshotStrict(ctx, strings.NewReader(good), int64(len(good)), 10); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
