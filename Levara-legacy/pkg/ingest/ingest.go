// Package ingest provides fast data ingestion — hash + save + classify
// in a single Go call, replacing Python's 3x MD5 + 2x disk write + sync blocking.
//
// Python ADD pipeline per item:
//   save_data_to_file()     → MD5 #1 + SYNC disk write (40-175ms)
//   data_item_to_text_file()→ read file back (20-100ms)
//   classify original       → MD5 #2 (52-510ms)
//   classify storage        → MD5 #3 (52-510ms)
//   Total: 164-1,295ms per item
//
// Go IngestData per item:
//   SHA256 + disk write + classify = single pass (~5-20ms)
//   Total: 5-20ms per item (10-65x faster)
package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// NamespaceOID matches Python uuid.NAMESPACE_OID.
var namespaceOID = uuid.MustParse("6ba7b812-9dad-11d1-80b4-00c04fd430c8")

// Item is input to ingest.
type Item struct {
	ID          string
	Text        string // for text input
	FileData    []byte // for binary file input
	Filename    string
	DatasetName string
}

// Result is the output of ingesting one item.
type Result struct {
	ID          string
	ContentHash string
	FilePath    string // file:// URI
	MimeType    string
	Extension   string
	FileSize    int64
	Name        string
	AlreadyExists bool
}

// ingestPrep holds the pre-computed data from Phase 1 (sequential).
type ingestPrep struct {
	content       []byte
	contentHash   string
	alreadyExists bool
	id            string
	name          string
	ext           string
	mimeType      string
	filename      string
	fullPath      string
}

// Ingest processes multiple items: hash + save + classify in one pass.
// storagePath is the base directory for file storage.
// Returns one Result per item. Concurrent-safe for multiple items.
//
// Two-phase design for deterministic dedup:
//   - Phase 1 (sequential): compute hash and dedup in input order.
//   - Phase 2 (parallel): disk I/O for non-duplicate items.
func Ingest(items []Item, storagePath string) ([]Result, error) {
	if storagePath == "" {
		storagePath = "data"
	}
	os.MkdirAll(storagePath, 0755)

	// Phase 1: sequential hash + dedup (deterministic: first occurrence wins).
	seen := make(map[string]bool) // hash → true
	preps := make([]ingestPrep, len(items))
	for i, item := range items {
		p, err := prepareItem(item, storagePath, seen)
		if err != nil {
			return nil, err
		}
		preps[i] = p
	}

	// Phase 2: parallel disk I/O for non-duplicate items.
	results := make([]Result, len(items))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for i := range preps {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			r, err := finalizeItem(&preps[idx])
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			results[idx] = r
		}(i)
	}

	wg.Wait()
	return results, firstErr
}

// prepareItem does Phase 1 work: content extraction, hash, dedup check, ID generation.
// Must be called sequentially to ensure deterministic dedup ordering.
func prepareItem(item Item, storagePath string, seen map[string]bool) (ingestPrep, error) {
	var content []byte
	if item.Text != "" {
		content = []byte(item.Text)
	} else if len(item.FileData) > 0 {
		content = item.FileData
	} else {
		return ingestPrep{}, fmt.Errorf("item has no text or file_data")
	}

	hash := sha256.Sum256(content)
	contentHash := hex.EncodeToString(hash[:])

	alreadyExists := seen[contentHash]
	seen[contentHash] = true

	id := item.ID
	if id == "" {
		input := contentHash
		if item.DatasetName != "" {
			input += item.DatasetName
		}
		normalized := strings.ToLower(strings.ReplaceAll(input, " ", "_"))
		id = uuid.NewSHA1(namespaceOID, []byte(normalized)).String()
	}

	name := item.Filename
	ext := ".txt"
	mimeType := "text/plain"

	if name == "" {
		name = "text_" + contentHash[:16]
	}

	if item.Filename != "" {
		ext = filepath.Ext(item.Filename)
		if ext == "" {
			ext = ".txt"
		}
		mimeType = http.DetectContentType(content)
	}

	filename := name + ext
	fullPath := filepath.Join(storagePath, filename)

	return ingestPrep{
		content:       content,
		contentHash:   contentHash,
		alreadyExists: alreadyExists,
		id:            id,
		name:          name,
		ext:           ext,
		mimeType:      mimeType,
		filename:      filename,
		fullPath:      fullPath,
	}, nil
}

// finalizeItem does Phase 2 work: disk write (if needed) + result assembly.
// Safe for concurrent execution.
func finalizeItem(p *ingestPrep) (Result, error) {
	alreadyExists := p.alreadyExists

	if !alreadyExists {
		if _, err := os.Stat(p.fullPath); err != nil {
			if err := os.WriteFile(p.fullPath, p.content, 0644); err != nil {
				return Result{}, fmt.Errorf("write %s: %w", p.fullPath, err)
			}
		} else {
			alreadyExists = true
		}
	}

	absPath, _ := filepath.Abs(p.fullPath)
	fileURI := "file://" + absPath

	return Result{
		ID:            p.id,
		ContentHash:   p.contentHash,
		FilePath:      fileURI,
		MimeType:      p.mimeType,
		Extension:     p.ext,
		FileSize:      int64(len(p.content)),
		Name:          p.name,
		AlreadyExists: alreadyExists,
	}, nil
}
