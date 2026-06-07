package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// FileLocation identifies a byte range within the DiskStore's metadata file.
// It is persisted in the WAL so that metadata can be relocated after recovery.
type FileLocation struct {
	Offset int64
	Length int32
}

// DiskStore is an append-only file store for JSON record metadata (meta.bin).
//
// Each [DiskStore.Write] appends data to the file and returns a [FileLocation]
// that can be used later with [DiskStore.Read] for random-access retrieval.
// The file is never compacted in-place; during crash recovery the WAL rebuilds
// it from scratch via [DiskStore.Truncate].
type DiskStore struct {
	mu   sync.RWMutex
	file *os.File
	pos  int64 // current write position
}

// NewDiskStore opens (or creates) the metadata file at path and returns a DiskStore.
// Parent directories are created with mode 0755 if they do not exist.
func NewDiskStore(path string) (*DiskStore, error) {

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directories for disk store: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open disk store: %w", err)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}

	return &DiskStore{
		file: f,
		pos:  info.Size(),
	}, nil
}

// Write appends data to the metadata file and returns its [FileLocation].
// The write is not fsynced; durability is ensured by the WAL.
func (ds *DiskStore) Write(data []byte) (FileLocation, error) {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	loc := FileLocation{
		Offset: ds.pos,
		Length: int32(len(data)),
	}

	n, err := ds.file.Write(data)
	if err != nil {
		return FileLocation{}, err
	}
	ds.pos += int64(n)
	return loc, nil
}

// Read returns the bytes stored at loc using a random-access pread. It does not
// require exclusive access and may be called concurrently with writes.
func (ds *DiskStore) Read(loc FileLocation) ([]byte, error) {
	ds.mu.RLock()
	defer ds.mu.RUnlock()

	if loc.Length <= 0 || loc.Offset < 0 {
		return nil, fmt.Errorf("invalid file location: offset=%d, length=%d", loc.Offset, loc.Length)
	}

	buffer := make([]byte, loc.Length)

	_, err := ds.file.ReadAt(buffer, loc.Offset)
	if err != nil {
		return nil, err
	}
	return buffer, nil
}

// Truncate resets the disk store file to zero length.
// Used before WAL recovery to rebuild metadata from scratch.
func (ds *DiskStore) Truncate() error {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	if err := ds.file.Truncate(0); err != nil {
		return fmt.Errorf("truncate: %w", err)
	}
	if _, err := ds.file.Seek(0, 0); err != nil {
		return fmt.Errorf("seek after truncate: %w", err)
	}
	ds.pos = 0
	return nil
}

// Close closes the underlying file descriptor.
func (ds *DiskStore) Close() error {
	return ds.file.Close()
}
