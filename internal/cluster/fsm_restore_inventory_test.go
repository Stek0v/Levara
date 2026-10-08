package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stek0v/levara/internal/store"
)

func TestSnapshotSourceReadFailureAndEmptyMetadata(t *testing.T) {
	for _, broken := range []bool{true, false} {
		name := "empty_metadata"
		if broken {
			name = "truncated_nonempty_metadata"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.bin")
			source, err := store.NewLevara(2, path)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			metadata := []byte{}
			if broken {
				metadata = []byte(`{"recoverable":true}`)
			}
			if err := source.Insert("source", []float32{1, 0}, metadata); err != nil {
				t.Fatal(err)
			}
			walBefore, err := os.ReadFile(path + ".wal")
			if err != nil {
				t.Fatal(err)
			}
			if broken {
				if err := os.Truncate(path, 0); err != nil {
					t.Fatal(err)
				}
			}
			snapshot, err := NewFSM(source).Snapshot()
			if broken {
				if err == nil {
					snapshot.Release()
					t.Error("unreadable metadata published as successful Raft snapshot")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				sink := &inMemorySink{}
				if err := snapshot.Persist(sink); err != nil {
					t.Fatal(err)
				}
				snapshot.Release()
				var inventory []store.SnapshotRecord
				if err := json.Unmarshal(sink.buf.Bytes(), &inventory); err != nil || len(inventory) != 1 {
					t.Fatal("legitimate empty metadata snapshot rejected")
				}
			}
			rs := NewReplicationServer("source", nil, source)
			rr := httptest.NewRecorder()
			rs.HandleSnapshot(rr, httptest.NewRequest(http.MethodGet, "/cluster/snapshot", nil))
			wantStatus := http.StatusOK
			if broken {
				wantStatus = http.StatusInternalServerError
			}
			if rr.Code != wantStatus {
				t.Errorf("snapshot endpoint status=%d want%d", rr.Code, wantStatus)
			}
			server := httptest.NewServer(http.HandlerFunc(rs.HandleStreamWAL))
			target := newReplicaStore(t, 2)
			defer target.Close()
			if err := target.Insert("keep", []float32{0, 1}, json.RawMessage(`{"old":true}`)); err != nil {
				t.Fatal(err)
			}
			client := NewReplicaClient(strings.TrimPrefix(server.URL, "http://"), "target", target, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err = client.Start(ctx)
			client.Stop()
			cancel()
			server.Close()
			if broken {
				if err == nil {
					t.Error("corrupt source admitted HTTP replacement snapshot")
				}
				_, got, ok := target.Get("keep")
				if !ok || string(got) != `{"old":true}` || target.Count() != 1 {
					t.Error("failed source snapshot changed prior replica state")
				}
				rs.mu.RLock()
				count := len(rs.listeners)
				rs.mu.RUnlock()
				if count != 0 {
					t.Error("failed snapshot capture leaked listener")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, _, ok := target.Get("source"); !ok || target.Count() != 1 {
					t.Fatal("empty metadata snapshot not applied")
				}
			}
			if err := source.Checkpoint(); (err != nil) != broken {
				t.Error("checkpoint did not distinguish failed read from legitimate empty metadata")
			}
			walAfter, err := os.ReadFile(path + ".wal")
			if err != nil || !bytes.Equal(walBefore, walAfter) {
				t.Fatal("snapshot capture changed source WAL")
			}
			if err := source.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.NewLevara(2, path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			_, restored, ok := reopened.Get("source")
			if !ok || !bytes.Equal(restored, metadata) {
				t.Fatal("source WAL-recoverable metadata lost")
			}
		})
	}
}

func TestFSMRestoreInventoryNativeReopen(t *testing.T) {
	source, err := store.NewLevara(2, filepath.Join(t.TempDir(), "source.bin"))
	if err != nil {
		t.Fatal(err)
	}
	sourceFSM := NewFSM(source)
	mkApply(t, sourceFSM, Command{Op: "insert", Id: "replacement", Vector: []float32{0, 1}, Data: json.RawMessage(`{"new":true}`)})
	snapshot, err := sourceFSM.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	sink := &inMemorySink{}
	if err := snapshot.Persist(sink); err != nil {
		t.Fatal(err)
	}
	snapshot.Release()
	image := append([]byte(nil), sink.buf.Bytes()...)
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		wire  []byte
		valid bool
		empty bool
	}{
		{"null", []byte("null"), false, false},
		{"truncated", image[:len(image)-1], false, false},
		{"wrong_dimension", []byte(`[{"id":"bad","vector":[1],"data":{}}]`), false, false},
		{"explicit_empty", []byte("[]"), true, true},
		{"native_snapshot", image, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "target.bin")
			db, err := store.NewLevara(2, path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			fsm := NewFSM(db)
			mkApply(t, fsm, Command{Op: "insert", Id: "keep", Vector: []float32{1, 0}, Data: json.RawMessage(`{"old":true}`)})
			want := db.AllRecords()
			err = fsm.Restore(io.NopCloser(bytes.NewReader(test.wire)))
			if !test.valid && err == nil {
				t.Error("invalid snapshot accepted")
			}
			if test.valid && err != nil {
				t.Fatal(err)
			}
			if test.valid {
				if test.empty {
					want = []store.SnapshotRecord{}
				} else {
					if err := json.Unmarshal(image, &want); err != nil {
						t.Fatal(err)
					}
				}
			}
			if got := db.AllRecords(); !reflect.DeepEqual(got, want) {
				t.Errorf("restore state=%v want=%v", got, want)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.NewLevara(2, path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if got := reopened.AllRecords(); !reflect.DeepEqual(got, want) {
				t.Errorf("reopened native WAL state=%v want=%v", got, want)
			}
		})
	}
}
