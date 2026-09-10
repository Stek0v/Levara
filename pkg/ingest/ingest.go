// Package ingest provides fast data ingestion — hash + save + classify
// in a single Go call, replacing Python's 3x MD5 + 2x disk write + sync blocking.
//
// Python ADD pipeline per item:
//
//	save_data_to_file()     → MD5 #1 + SYNC disk write (40-175ms)
//	data_item_to_text_file()→ read file back (20-100ms)
//	classify original       → MD5 #2 (52-510ms)
//	classify storage        → MD5 #3 (52-510ms)
//	Total: 164-1,295ms per item
//
// Go IngestData per item:
//
//	SHA256 + disk write + classify = single pass (~5-20ms)
//	Total: 5-20ms per item (10-65x faster)
package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// sanitizeFilename strips any directory component and rejects traversal
// patterns from a user-supplied filename. Returns "" if the input cannot be
// reduced to a safe basename — callers fall back to a hash-derived name.
func sanitizeFilename(s string) string {
	if s == "" || strings.ContainsRune(s, 0) {
		return ""
	}
	// Normalize OS separators so "..\\foo" on any platform is caught.
	s = strings.ReplaceAll(s, "\\", "/")
	base := filepath.Base(s)
	// filepath.Base("..") == "..", filepath.Base("/") == "/", filepath.Base(".") == "."
	if base == "." || base == ".." || base == "/" || base == "" {
		return ""
	}
	if strings.ContainsAny(base, "/\\") {
		return ""
	}
	return base
}

// isInside reports whether child resolves to a path inside root.
func isInside(root, child string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	childAbs, err := filepath.Abs(child)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, childAbs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Item is input to ingest.
type Item struct {
	ID                 string
	Text               string // for text input
	FileData           []byte // for binary file input
	StructuredArtifact []byte // optional structured extraction bound to this source version
	Filename           string
	DatasetName        string
	OwnerID            string   // user who uploaded (for dedup scoping)
	Tags               []string // semantic tags for grouping (node_set equivalent)
	Room               string   // sub-topic within collection (auth, deploy, ocr-bench)
}

// Result is the output of ingesting one item.
type Result struct {
	OriginalFilePath          string // optional source bytes, separate from extracted text
	OriginalContentHash       string
	OriginalFileSize          int64
	ID                        string
	ContentHash               string
	FilePath                  string // file:// URI
	MimeType                  string
	Extension                 string
	FileSize                  int64
	Name                      string
	Tags                      string // JSON array string, e.g. '["backend","api"]'
	Room                      string // sub-topic, propagated from Item
	AlreadyExists             bool
	SourceRevision            int64 // populated by explicit source replacement
	StructuredArtifactID      string
	StructuredArtifactPath    string
	StructuredArtifactHash    string
	StructuredArtifactSize    int64
	structuredArtifactChanged bool
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
	fullPath      string
	tagsJSON      string
	room          string
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
	storagePath = filepath.Join(storagePath, "blobs")
	if err := os.MkdirAll(storagePath, 0700); err != nil {
		return nil, fmt.Errorf("create blob storage: %w", err)
	}

	// Phase 1: sequential hash + dedup (deterministic: first occurrence wins).
	seen := make(map[string]bool) // owner/content storage key → true
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

	// A display filename must never alias another owner's document or version.
	storageHash := sha256.Sum256([]byte(item.OwnerID + "\x00" + contentHash))
	storageKey := hex.EncodeToString(storageHash[:])
	alreadyExists := seen[storageKey]
	seen[storageKey] = true

	id := item.ID
	if id == "" {
		input := contentHash
		if item.OwnerID != "" {
			input += item.OwnerID
		}
		normalized := strings.ToLower(strings.ReplaceAll(input, " ", "_"))
		id = uuid.NewSHA1(namespaceOID, []byte(normalized)).String()
	}

	// Sanitize untrusted filename: strip directories, reject traversal/null bytes.
	// User-supplied filename reaches disk via filepath.Join below; without this
	// step a value like "../../etc/passwd" or "/etc/passwd" would escape storagePath.
	safeName := sanitizeFilename(item.Filename)

	name := safeName
	ext := ".txt"
	mimeType := "text/plain"

	if name == "" {
		name = "text_" + contentHash[:16]
	}

	if safeName != "" {
		ext = filepath.Ext(safeName)
		if ext == "" {
			ext = ".txt"
		}
		mimeType = http.DetectContentType(content)
	}

	fullPath := filepath.Join(storagePath, storageKey)
	// Defense-in-depth: ensure the resolved path stays inside storagePath even
	// if a future change weakens sanitizeFilename.
	if !isInside(storagePath, fullPath) {
		return ingestPrep{}, fmt.Errorf("filename escapes storage root: %q", item.Filename)
	}

	tagsJSON := "[]"
	if len(item.Tags) > 0 {
		encoded, _ := json.Marshal(item.Tags) // []string is always JSON-encodable.
		tagsJSON = string(encoded)
	}

	return ingestPrep{
		content:       content,
		contentHash:   contentHash,
		alreadyExists: alreadyExists,
		id:            id,
		name:          name,
		ext:           ext,
		mimeType:      mimeType,
		fullPath:      fullPath,
		tagsJSON:      tagsJSON,
		room:          item.Room,
	}, nil
}

// finalizeItem does Phase 2 work: disk write (if needed) + result assembly.
// Safe for concurrent execution.
func finalizeItem(p *ingestPrep) (Result, error) {
	alreadyExists := p.alreadyExists

	if !alreadyExists {
		if _, err := os.Stat(p.fullPath); err == nil {
			alreadyExists = true
		} else if !os.IsNotExist(err) {
			return Result{}, fmt.Errorf("stat blob: %w", err)
		} else {
			f, err := os.CreateTemp(filepath.Dir(p.fullPath), ".ingest-*")
			if err != nil {
				return Result{}, err
			}
			defer os.Remove(f.Name())
			_, writeErr := f.Write(p.content)
			if writeErr == nil {
				writeErr = f.Sync()
			}
			closeErr := f.Close()
			if writeErr != nil {
				return Result{}, fmt.Errorf("write blob: %w", writeErr)
			}
			if closeErr != nil {
				return Result{}, closeErr
			}
			// Publish complete bytes atomically; concurrent copies have identical content.
			if err := os.Rename(f.Name(), p.fullPath); err != nil {
				return Result{}, fmt.Errorf("publish blob: %w", err)
			}
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
		Tags:          p.tagsJSON,
		Room:          p.room,
		AlreadyExists: alreadyExists,
	}, nil
}
