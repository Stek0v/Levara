package bm25

import (
	"bufio"
	"testing"
	"time"
)

func TestSnapshotPreservesConcurrentMutation(t *testing.T) {
	for _, operation := range []string{"put", "delete", "clear"} {
		t.Run(operation, func(t *testing.T) {
			store := NewSnapshotStore(t.TempDir())
			idx := NewIndex()
			idx.Add("old", "old keyword", "{}")
			store.Attach("c", idx)
			path := store.pathFor("c")
			if err := SaveSnapshot(path, idx); err != nil {
				t.Fatal(err)
			}
			captured, resume := make(chan struct{}), make(chan struct{})
			snapshotDone := make(chan error, 1)
			go func() {
				snapshotDone <- saveSnapshot(path, idx, func(w *bufio.Writer, docs []Document) error {
					close(captured)
					<-resume
					return writeSnapshotDocuments(w, docs)
				})
			}()
			<-captured
			mutationDone := make(chan struct{})
			go func() {
				switch operation {
				case "put":
					idx.Add("new", "fresh keyword", "{}")
				case "delete":
					idx.Remove("old")
				case "clear":
					idx.Clear()
				}
				close(mutationDone)
			}()
			// Allow both legal orders: persist before replacement, or queue behind the snapshot.
			select {
			case <-mutationDone:
				t.Log("mutation completed while snapshot replacement was paused")
			case <-time.After(100 * time.Millisecond):
			}
			close(resume)
			if err := <-snapshotDone; err != nil {
				t.Fatal(err)
			}
			<-mutationDone
			loaded, err := LoadSnapshot(path)
			if err != nil {
				t.Fatal(err)
			}
			if operation == "put" {
				if len(loaded.Search("fresh", 10)) != 1 {
					t.Fatal("completed addition lost after snapshot replacement")
				}
			} else if len(loaded.Search("old", 10)) != 0 {
				t.Fatal("deleted document resurrected after snapshot replacement")
			}
		})
	}
}
