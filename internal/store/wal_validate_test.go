package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWalkWALStrictNativeRoundTripAndCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.wal")
	wal, err := OpenWal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = wal.WriteEntry(OpInsert, "a", []float32{1, 0}, []byte(`{"a":1}`), FileLocation{Length: 7}); err != nil {
		t.Fatal(err)
	}
	if err = wal.WriteEntry(OpDelete, "a", nil, nil, FileLocation{}); err != nil {
		t.Fatal(err)
	}
	if err = wal.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ops []byte
	visit := func(op byte, r SnapshotRecord) error { ops = append(ops, op); return nil }
	if err = WalkWALStrict(context.Background(), bytes.NewReader(body), 2, int64(len(body)), visit); err != nil || len(ops) != 2 {
		t.Fatalf("native WAL rejected: %v %+v", err, ops)
	}
	for _, tc := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"short header", func(b []byte) []byte { return append(b, 1) }}, {"short frame", func(b []byte) []byte { return b[:len(b)-1] }}, {"unknown operation", func(b []byte) []byte { b[4] = 99; return b }}, {"oversized frame", func(b []byte) []byte { binary.LittleEndian.PutUint32(b, 0xffffffff); return b }}, {"trailing frame data", func(b []byte) []byte { binary.LittleEndian.PutUint32(b, binary.LittleEndian.Uint32(b)+1); return b }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := tc.mutate(append([]byte(nil), body...))
			if err := WalkWALStrict(context.Background(), bytes.NewReader(bad), 2, int64(len(bad)), visit); err == nil {
				t.Fatal("corruption accepted")
			}
		})
	}
	if err = WalkWALStrict(context.Background(), bytes.NewReader(body), 3, int64(len(body)), visit); err == nil {
		t.Fatal("wrong dimension accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = WalkWALStrict(ctx, bytes.NewReader(body), 2, int64(len(body)), visit); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	sentinel := errors.New("callback failed")
	if err = WalkWALStrict(context.Background(), bytes.NewReader(body), 2, int64(len(body)), func(byte, SnapshotRecord) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}
