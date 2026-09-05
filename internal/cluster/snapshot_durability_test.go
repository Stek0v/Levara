package cluster

import (
	"bytes"
	"io"
	"path/filepath"
	"testing"

	"github.com/stek0v/levara/internal/store"
)

func TestSnapshotRestoreReplacesWALAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta.bin")
	db, err := store.NewLevara(2, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if db != nil {
			db.Close()
		}
	}()
	if err := db.Insert("old", []float32{1, 0}, nil); err != nil {
		t.Fatal(err)
	}
	if err := NewFSM(db).Restore(io.NopCloser(bytes.NewBufferString(`[{"ID":"new","Vector":[0,1],"Data":null}]`))); err != nil {
		t.Fatal(err)
	}
	if _, _, exists := db.Get("old"); exists {
		t.Fatal("old record survived in memory")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.NewLevara(2, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, exists := db.Get("old"); exists {
		t.Error("old record resurrected from WAL after snapshot restore")
	}
	if _, _, exists := db.Get("new"); !exists {
		t.Error("snapshot record missing after restart")
	}
}

func TestSnapshotRestoreFailurePreservesPreviousState(t *testing.T) {
	fsm, db, cleanup := newFSMWithDB(t, 2)
	defer cleanup()
	if err := db.Insert("old", []float32{1, 0}, nil); err != nil {
		t.Fatal(err)
	}
	err := fsm.Restore(io.NopCloser(bytes.NewBufferString(`[{"ID":"new","Vector":[0,1],"Data":null},{"ID":"invalid","Vector":[1],"Data":null}]`)))
	if err == nil {
		t.Fatal("invalid snapshot accepted")
	}
	if _, _, exists := db.Get("old"); !exists {
		t.Error("failed restore destroyed prior state")
	}
	if _, _, exists := db.Get("new"); exists {
		t.Error("failed restore exposed partial snapshot")
	}
}
