package store

import (
	"fmt"
	"path/filepath"
	"testing"
)

func newTestCluster(t testing.TB, count int) *Cluster {
	t.Helper()
	shards := make([]ShardHandler, count)
	for i := range shards {
		db, err := NewLevara(2, filepath.Join(t.TempDir(), "meta.bin"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		shards[i] = db
	}
	return NewCluster(shards)
}

func TestClusterBatchDeleteRoutingAndRecovery(t *testing.T) {
	c := newTestCluster(t, 4)
	ids := []string{"", "юникод"}
	for i := 0; i < 32; i++ {
		ids = append(ids, fmt.Sprint(i))
	}
	for _, id := range append(append([]string{}, ids...), "keep") {
		if err := c.Insert(id, []float32{1, 0}, nil); err != nil {
			t.Fatal(err)
		}
	}
	before := make([]uint64, c.NumShards())
	for i, shard := range c.shards {
		before[i] = shard.(*Levara).wal.SyncCount()
	}
	if errs := c.BatchDelete(append(append([]string{}, ids...), "missing", ids[0])); len(errs) != 2 {
		t.Fatalf("missing and duplicate errors = %v", errs)
	}
	for i, shard := range c.shards {
		db := shard.(*Levara)
		if got := db.wal.SyncCount() - before[i]; got != 1 {
			t.Errorf("shard %d syncs = %d, want 1", i, got)
		}
		before[i] = db.wal.SyncCount()
	}
	if errs := c.BatchDelete(ids); len(errs) != len(ids) {
		t.Fatalf("all missing errors = %v", errs)
	}
	if errs := c.BatchDelete(nil); len(errs) != 0 {
		t.Fatal(errs)
	}
	for i, shard := range c.shards {
		db := shard.(*Levara)
		if db.wal.SyncCount() != before[i] {
			t.Errorf("empty/failed batch flushed shard %d", i)
		}
		path := db.disk.file.Name()
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := NewLevara(2, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { reopened.Close() })
		for _, id := range ids {
			if _, _, ok := reopened.Get(id); ok {
				t.Errorf("deleted ID %q recovered on shard %d", id, i)
			}
		}
		if c.getShard("keep") == shard {
			if _, _, ok := reopened.Get("keep"); !ok {
				t.Error("survivor lost")
			}
		} else if reopened.Count() != 0 {
			t.Errorf("unexpected records on shard %d", i)
		}
	}
}

func BenchmarkClusterBatchDelete(b *testing.B) {
	b.StopTimer()
	c := newTestCluster(b, 4)
	items := make([]BatchItem, 32)
	ids := make([]string, len(items))
	for i := range items {
		ids[i] = fmt.Sprint(i)
		items[i] = BatchItem{ID: ids[i], Vector: []float32{1, 0}}
	}
	var syncs uint64
	for n := 0; n < b.N; n++ {
		if errs := c.BatchInsert(items); len(errs) != 0 {
			b.Fatal(errs)
		}
		var before uint64
		for _, shard := range c.shards {
			before += shard.(*Levara).wal.SyncCount()
		}
		b.StartTimer()
		if errs := c.BatchDelete(ids); len(errs) != 0 {
			b.Fatal(errs)
		}
		b.StopTimer()
		for _, shard := range c.shards {
			syncs += shard.(*Levara).wal.SyncCount()
		}
		syncs -= before
	}
	b.ReportMetric(float64(syncs)/float64(b.N), "syncs/batch")
}
