package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type FileLocation struct {
	Offset int64
	Length int32
}

type DiskStore struct {
	mu   sync.RWMutex
	file *os.File
	pos  int64 // current write position
}

func NewDiskStore(path string) (*DiskStore, error) {

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directories for disk store: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("Failed to open disk store: %w", err)
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}

	return &DiskStore{
		file: f,
		pos:  info.Size(),
	}, nil
}

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

func (ds *DiskStore) Close() error {
	return ds.file.Close()
}
