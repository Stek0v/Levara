package bm25

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"sync"
)

// diskDoc is the JSON format for each indexed document on disk.
type diskDoc struct {
	ID       string `json:"i"`
	Text     string `json:"t"`
	Metadata string `json:"m"`
}

// PersistentIndex wraps Index with disk persistence (append-only JSONL file).
type PersistentIndex struct {
	*Index
	path string
	file *os.File
	mu   sync.Mutex
}

// NewPersistent creates a BM25 index that persists to disk.
func NewPersistent(path string) (*PersistentIndex, error) {
	idx := NewIndex()
	pi := &PersistentIndex{Index: idx, path: path}

	if err := pi.load(); err != nil {
		log.Printf("[bm25] load %s: %v (starting fresh)", path, err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	pi.file = f

	return pi, nil
}

// Add indexes a document and appends to disk.
func (pi *PersistentIndex) Add(id, text, metadata string) {
	pi.Index.Add(id, text, metadata)

	pi.mu.Lock()
	defer pi.mu.Unlock()

	if pi.file != nil {
		entry := diskDoc{ID: id, Text: text, Metadata: metadata}
		data, _ := json.Marshal(entry)
		pi.file.Write(data)
		pi.file.Write([]byte("\n"))
	}
}

// Close flushes and closes the file.
func (pi *PersistentIndex) Close() error {
	pi.mu.Lock()
	defer pi.mu.Unlock()
	if pi.file != nil {
		return pi.file.Close()
	}
	return nil
}

func (pi *PersistentIndex) load() error {
	f, err := os.Open(pi.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	loaded := 0
	for scanner.Scan() {
		var doc diskDoc
		if err := json.Unmarshal(scanner.Bytes(), &doc); err != nil {
			continue
		}
		pi.Index.Add(doc.ID, doc.Text, doc.Metadata)
		loaded++
	}

	if loaded > 0 {
		log.Printf("[bm25] loaded %d docs from %s", loaded, pi.path)
	}
	return scanner.Err()
}
