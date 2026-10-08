package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func startupTestFrame(t *testing.T, op byte, id string, vector []float32, metadata []byte, loc FileLocation) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "frame.wal")
	wal, err := OpenWal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := wal.WriteEntry(op, id, vector, metadata, loc); err != nil {
		t.Fatal(err)
	}
	if err := wal.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestNativeStartupTailBoundaryAndContinuation(t *testing.T) {
	normal := startupTestFrame(t, OpInsert, "uncertain", []float32{1, 0}, []byte(`{"tail":true}`), FileLocation{})
	for name, tail := range map[string][]byte{
		"clean": nil, "partial_header": normal[:2], "partial_id": normal[:11], "partial_vector": normal[:21], "partial_location": normal[:len(normal)-7],
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "meta.bin")
			db, err := NewLevara(2, path)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Insert("keep", []float32{1, 0}, json.RawMessage(`{"old":true}`)); err != nil {
				t.Fatal(err)
			}
			if err := db.Insert("replace", []float32{0, 1}, nil); err != nil {
				t.Fatal(err)
			}
			if err := db.Delete("replace"); err != nil {
				t.Fatal(err)
			}
			if err := db.Insert("replace", []float32{-1, 0}, json.RawMessage(`{"new":true}`)); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			prefix, err := os.ReadFile(path + ".wal")
			if err != nil {
				t.Fatal(err)
			}
			damaged := append(append([]byte(nil), prefix...), tail...)
			if err := os.WriteFile(path+".wal", damaged, 0600); err != nil {
				t.Fatal(err)
			}
			if len(tail) > 0 && WalkWALStrict(context.Background(), bytes.NewReader(damaged), 2, int64(len(damaged)), func(byte, SnapshotRecord) error { return nil }) == nil {
				t.Fatal("strict verifier accepted partial tail")
			}
			reopened, err := NewLevara(2, path)
			if err != nil {
				t.Fatal(err)
			}
			repaired, err := os.ReadFile(path + ".wal")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(repaired, prefix) {
				t.Errorf("startup did not preserve exact complete prefix: got=%d want=%d", len(repaired), len(prefix))
			}
			if err := reopened.Insert("after", []float32{0, 1}, json.RawMessage(`{"ack":true}`)); err != nil {
				t.Fatal(err)
			}
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
			again, err := NewLevara(2, path)
			if err != nil {
				t.Fatal(err)
			}
			defer again.Close()
			for id, want := range map[string]string{"keep": `{"old":true}`, "replace": `{"new":true}`, "after": `{"ack":true}`} {
				_, data, ok := again.Get(id)
				if !ok || string(data) != want {
					t.Errorf("acknowledged %s lost after continuation: %s", id, data)
				}
			}
			if again.Count() != 3 {
				t.Errorf("unexpected recovered count %d", again.Count())
			}
		})
	}
}

func TestNativeStartupCorruptionPreservesFiles(t *testing.T) {
	normal := startupTestFrame(t, OpInsert, "id", []float32{1, 0}, []byte("{}"), FileLocation{})
	unknown := append([]byte(nil), normal...)
	unknown[4] = 99
	oversized := append([]byte(nil), normal[:4]...)
	binary.LittleEndian.PutUint32(oversized, math.MaxUint32)
	wrongSize := append([]byte(nil), normal...)
	binary.LittleEndian.PutUint32(wrongSize, binary.LittleEndian.Uint32(normal)+1)
	tooSmall := append([]byte(nil), normal...)
	binary.LittleEndian.PutUint32(tooSmall, 24)
	nonFinite := append([]byte(nil), normal...)
	binary.LittleEndian.PutUint32(nonFinite[15:], math.Float32bits(float32(math.NaN())))
	negativeLocation := append([]byte(nil), normal...)
	binary.LittleEndian.PutUint64(negativeLocation[len(normal)-12:], math.MaxUint64)
	partialID := append([]byte(nil), normal[:8]...)
	copy(partialID[5:], []byte{255, 255, 255})
	partialVector := append([]byte(nil), normal[:12]...)
	partialVector[11] = 3 // no completion can be the required eight bytes
	partialMetadata := append([]byte(nil), normal[:24]...)
	partialMetadata[23] = 3 // declaration requires exactly two metadata bytes
	deleteSize := startupTestFrame(t, OpDelete, "id", nil, nil, FileLocation{})[:9]
	binary.LittleEndian.PutUint32(deleteSize, 28)
	insertCapacity := append([]byte(nil), normal[:9]...)
	binary.LittleEndian.PutUint32(insertCapacity, 25+2*(1<<20)+8)
	for name, tail := range map[string][]byte{
		"unknown_op": unknown, "unknown_op_partial": unknown[:12], "oversized_declaration": oversized, "oversized_partial_header": {255, 255, 255},
		"wrong_frame_size": wrongSize, "short_declaration": tooSmall,
		"impossible_minimum_header": {25, 0, 0, 0}, "impossible_minimum_partial_header": {25, 0, 0},
		"wrong_dimension": startupTestFrame(t, OpInsert, "id", []float32{1}, nil, FileLocation{}),
		"non_finite":      nonFinite, "negative_location": negativeLocation,
		"partial_id_over_limit": partialID, "partial_vector_mismatch": partialVector, "partial_metadata_mismatch": partialMetadata,
		"delete_impossible_size_before_id": deleteSize, "insert_impossible_capacity_before_id": insertCapacity,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "meta.bin")
			metadata := []byte("old metadata must remain untouched")
			if err := os.WriteFile(path, metadata, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path+".wal", tail, 0600); err != nil {
				t.Fatal(err)
			}
			db, err := NewLevara(2, path)
			if err == nil {
				db.Close()
				t.Error("corrupt startup returned usable database")
			}
			got, readErr := os.ReadFile(path + ".wal")
			if readErr != nil || !bytes.Equal(got, tail) {
				t.Error("corrupt WAL changed")
			}
			got, readErr = os.ReadFile(path)
			if readErr != nil || !bytes.Equal(got, metadata) {
				t.Error("metadata changed before corruption rejection")
			}
		})
	}
}

func TestNativeWriteAdmissionPreservesDurableState(t *testing.T) {
	for name, rec := range map[string]SnapshotRecord{
		"empty_id":           {ID: "", Vector: []float32{1, 0}},
		"oversized_id":       {ID: strings.Repeat("i", (1<<20)+1), Vector: []float32{1, 0}},
		"oversized_metadata": {ID: "bad", Vector: []float32{1, 0}, Data: bytes.Repeat([]byte(" "), (1<<20)+1)},
		"wrong_dimension":    {ID: "bad", Vector: []float32{1}},
		"non_finite":         {ID: "bad", Vector: []float32{float32(math.Inf(1)), 0}},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "meta.bin")
			db, err := NewLevara(2, path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.Insert("keep", []float32{1, 0}, json.RawMessage("{}")); err != nil {
				t.Fatal(err)
			}
			beforeWAL, err := os.ReadFile(path + ".wal")
			if err != nil {
				t.Fatal(err)
			}
			beforeMetadata, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			check := func() {
				t.Helper()
				walBytes, err := os.ReadFile(path + ".wal")
				if err != nil || !bytes.Equal(walBytes, beforeWAL) {
					t.Fatal("rejected input changed WAL")
				}
				metaBytes, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(metaBytes, beforeMetadata) {
					t.Fatal("rejected input changed metadata")
				}
				if db.Count() != 1 {
					t.Fatal("rejected input changed inventory")
				}
			}
			if err := db.Insert(rec.ID, rec.Vector, []byte(rec.Data)); err == nil {
				t.Fatal("invalid insert accepted")
			}
			check()
			// Raw metadata is already serialized; BatchInsert marshals ordinary values.
			data := any(json.RawMessage(rec.Data))
			if name == "oversized_metadata" {
				data = strings.Repeat("m", 1<<20)
			}
			if errs := db.BatchInsert([]BatchItem{{ID: rec.ID, Vector: rec.Vector, Data: data}}); len(errs) == 0 {
				t.Fatal("invalid batch insert accepted")
			}
			check()
			if err := db.RestoreSnapshot([]SnapshotRecord{{ID: "valid-first", Vector: []float32{0, 1}, Data: json.RawMessage("{}")}, rec}); err == nil {
				t.Fatal("invalid snapshot accepted")
			}
			check()
			wal, err := OpenWal(filepath.Join(t.TempDir(), "writer.wal"))
			if err != nil {
				t.Fatal(err)
			}
			if err := wal.WriteEntry(OpInsert, rec.ID, rec.Vector, rec.Data, FileLocation{}); name != "wrong_dimension" && err == nil {
				t.Fatal("invalid WAL write accepted")
			}
			if err := wal.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeWriteAdmissionExactLimitsRecover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta.bin")
	id := strings.Repeat("i", 1<<20)
	data := json.RawMessage("\"" + strings.Repeat("m", (1<<20)-2) + "\"")
	db, err := NewLevara(2, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Insert(id, []float32{1, 0}, data); err != nil {
		t.Fatal(err)
	}
	if errs := db.BatchInsert([]BatchItem{{ID: id, Vector: []float32{0, 1}, Data: data}}); len(errs) != 0 {
		t.Fatal(errs)
	}
	if err := db.RestoreSnapshot([]SnapshotRecord{{ID: id, Vector: []float32{0, 1}, Data: data}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewLevara(2, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	vector, metadata, ok := reopened.Get(id)
	if !ok || len(vector) != 2 || vector[1] != 1 || !bytes.Equal(metadata, data) {
		t.Fatal("exact limit acknowledged record lost after native reopen")
	}
}

func TestPublicWALRecoveryDoesNotRepairTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.wal")
	complete := startupTestFrame(t, OpInsert, "id", []float32{1, 0}, nil, FileLocation{})
	damaged := append(append([]byte(nil), complete...), complete[:2]...)
	if err := os.WriteFile(path, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	wal, err := OpenWal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := wal.RecoverEx(func(byte, string, []float32, []byte, FileLocation) {}); err != nil {
		t.Fatal(err)
	}
	if err := wal.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, damaged) {
		t.Fatal("public replay mutated tail")
	}
}
