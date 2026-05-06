package llmcache

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"sync"
)

// diskEntry is the JSON format for each cached line on disk.
type diskEntry struct {
	Key      string `json:"k"`
	Response string `json:"r"`
	Model    string `json:"m"`
}

// PersistentCache wraps Cache with disk persistence (append-only JSONL file).
// Loads existing entries on creation, appends new entries on Put.
type PersistentCache struct {
	*Cache
	path string
	file *os.File
	mu   sync.Mutex
}

// NewPersistent creates a cache that persists to disk.
// Loads existing entries from file if it exists.
func NewPersistent(maxSize int, path string) (*PersistentCache, error) {
	c := New(maxSize, 0)
	pc := &PersistentCache{Cache: c, path: path}

	// Load existing entries
	if err := pc.load(); err != nil {
		log.Printf("[llmcache] load %s: %v (starting fresh)", path, err)
	}

	// Open for append
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	pc.file = f

	return pc, nil
}

// Put stores a response in memory and appends to disk.
func (pc *PersistentCache) Put(key, response, model string) {
	pc.Cache.Put(key, response, model)

	pc.mu.Lock()
	defer pc.mu.Unlock()

	if pc.file != nil {
		entry := diskEntry{Key: key, Response: response, Model: model}
		data, _ := json.Marshal(entry)
		pc.file.Write(data)
		pc.file.Write([]byte("\n"))
	}
}

// Close flushes and closes the persistence file.
func (pc *PersistentCache) Close() error {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if pc.file != nil {
		return pc.file.Close()
	}
	return nil
}

func (pc *PersistentCache) load() error {
	f, err := os.Open(pc.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no file yet
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // 10MB max line

	loaded := 0
	for scanner.Scan() {
		var entry diskEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue // skip corrupt lines
		}
		pc.Cache.Put(entry.Key, entry.Response, entry.Model)
		loaded++
	}

	if loaded > 0 {
		log.Printf("[llmcache] loaded %d entries from %s", loaded, pc.path)
	}
	return scanner.Err()
}
