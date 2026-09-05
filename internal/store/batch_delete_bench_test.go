package store

import (
	"fmt"
	"path/filepath"
	"testing"
)

func BenchmarkBatchDelete(b *testing.B) {
	b.StopTimer()
	db, err := NewLevara(2, filepath.Join(b.TempDir(), "meta.bin"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	ids := make([]string, 8)
	for i := range ids {
		ids[i] = fmt.Sprint(i)
	}
	var syncs uint64
	for n := 0; n < b.N; n++ {
		for _, id := range ids {
			if err := db.Insert(id, []float32{1, 0}, nil); err != nil {
				b.Fatal(err)
			}
		}
		before := db.wal.SyncCount()
		b.StartTimer()
		if errs := db.BatchDelete(ids); len(errs) != 0 {
			b.Fatal(errs)
		}
		b.StopTimer()
		syncs += db.wal.SyncCount() - before
	}
	b.ReportMetric(float64(syncs)/float64(b.N), "syncs/batch")
}

func TestBatchDeleteOneFlushAndRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta.bin")
	db, err := NewLevara(2, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "keep"} {
		if err := db.Insert(id, []float32{1, 0}, nil); err != nil {
			t.Fatal(err)
		}
	}
	before := db.wal.SyncCount()
	if errs := db.BatchDelete([]string{"a", "missing", "a", "b"}); len(errs) != 2 {
		t.Fatalf("expected missing and duplicate errors: %v", errs)
	}
	if syncs := db.wal.SyncCount() - before; syncs != 1 {
		t.Fatalf("batch syncs=%d want 1", syncs)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewLevara(2, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Count() != 1 {
		t.Fatalf("recovered %d records", reopened.Count())
	}
	if _, _, ok := reopened.Get("keep"); !ok {
		t.Fatal("surviving record lost")
	}
}
