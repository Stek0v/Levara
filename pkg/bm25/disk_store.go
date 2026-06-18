package bm25

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const snapshotExt = ".jsonl"

// SnapshotStore persists one BM25 snapshot per collection.
type SnapshotStore struct {
	dir string
}

// NewSnapshotStore creates a disk-backed BM25 snapshot store.
func NewSnapshotStore(dir string) *SnapshotStore {
	return &SnapshotStore{dir: dir}
}

// LoadAll loads every persisted BM25 collection snapshot from disk.
func (s *SnapshotStore) LoadAll() (map[string]*Index, error) {
	indexes := make(map[string]*Index)
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return indexes, nil
		}
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), snapshotExt) {
			continue
		}
		collection, ok := decodeCollectionName(strings.TrimSuffix(entry.Name(), snapshotExt))
		if !ok {
			continue
		}
		idx, err := LoadSnapshot(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			log.Printf("[bm25] load snapshot %q: %v", entry.Name(), err)
			continue
		}
		s.Attach(collection, idx)
		indexes[collection] = idx
	}
	return indexes, nil
}

// SaveAll atomically saves all indexes in the supplied map.
func (s *SnapshotStore) SaveAll(indexes map[string]*Index) error {
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return err
	}
	for collection, idx := range indexes {
		if collection == "" || idx == nil {
			continue
		}
		if err := SaveSnapshot(s.pathFor(collection), idx); err != nil {
			return fmt.Errorf("save %s: %w", collection, err)
		}
	}
	return nil
}

// Attach enables immediate append-log persistence for subsequent mutations.
func (s *SnapshotStore) Attach(collection string, idx *Index) {
	if s == nil || collection == "" || idx == nil {
		return
	}
	idx.SetOnChange(func(change Change) {
		if err := s.AppendChange(collection, change); err != nil {
			log.Printf("[bm25] append %s/%s failed: %v", collection, change.Op, err)
		}
	})
}

// Remove deletes the persisted sidecar for a collection.
func (s *SnapshotStore) Remove(collection string) error {
	if s == nil || collection == "" {
		return nil
	}
	err := os.Remove(s.pathFor(collection))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	err = os.Remove(s.pathFor(collection) + ".tmp")
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// AppendChange appends one index mutation to a collection log immediately.
func (s *SnapshotStore) AppendChange(collection string, change Change) error {
	if s == nil || collection == "" {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.pathFor(collection), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	entry := diskDoc(change)
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if _, err := f.Write([]byte("\n")); err != nil {
		return err
	}
	return f.Sync()
}

// StartAutosave periodically snapshots BM25 indexes until ctx is cancelled.
func (s *SnapshotStore) StartAutosave(ctx context.Context, indexes map[string]*Index, interval time.Duration) func() {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := s.SaveAll(indexes); err != nil {
					log.Printf("[bm25] autosave failed: %v", err)
				}
			case <-ctx.Done():
				if err := s.SaveAll(indexes); err != nil {
					log.Printf("[bm25] final save failed: %v", err)
				}
				return
			}
		}
	}()
	return cancel
}

// SaveSnapshot writes a complete BM25 index snapshot with atomic rename.
func SaveSnapshot(path string, idx *Index) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, doc := range idx.Documents() {
		entry := diskDoc{Op: "put", ID: doc.ID, Text: doc.Text, Metadata: doc.Metadata}
		data, err := json.Marshal(entry)
		if err != nil {
			_ = f.Close()
			return err
		}
		if _, err := w.Write(data); err != nil {
			_ = f.Close()
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadSnapshot loads a complete BM25 index snapshot from disk.
func LoadSnapshot(path string) (*Index, error) {
	idx := NewIndex()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return idx, nil
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		var doc diskDoc
		if err := json.Unmarshal(scanner.Bytes(), &doc); err != nil {
			continue
		}
		switch doc.Op {
		case "", "put":
			idx.Add(doc.ID, doc.Text, doc.Metadata)
		case "delete":
			idx.Remove(doc.ID)
		case "clear":
			idx.Clear()
		}
	}
	return idx, scanner.Err()
}

func (s *SnapshotStore) pathFor(collection string) string {
	return filepath.Join(s.dir, encodeCollectionName(collection)+snapshotExt)
}

func encodeCollectionName(collection string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(collection))
}

func decodeCollectionName(encoded string) (string, bool) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", false
	}
	return string(data), true
}
