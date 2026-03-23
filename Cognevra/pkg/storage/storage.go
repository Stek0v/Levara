// Package storage provides a unified file storage interface with local filesystem
// and S3-compatible backends.
//
// Usage:
//
//	// Local (default):
//	fs := storage.NewLocalStorage("/data/uploads")
//	fs.Save(ctx, "docs/file.pdf", reader)
//
//	// S3 (future):
//	s3, _ := storage.NewS3Storage("my-bucket", "us-east-1", "")
//	s3.Save(ctx, "docs/file.pdf", reader)
//
// Set STORAGE_BACKEND=s3 with S3_BUCKET, S3_REGION, S3_ENDPOINT env vars to use S3.
package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Storage is the file storage interface.
// All paths are relative keys (e.g. "datasets/abc/file.txt").
type Storage interface {
	Save(ctx context.Context, path string, data io.Reader) error
	Load(ctx context.Context, path string) (io.ReadCloser, error)
	Delete(ctx context.Context, path string) error
	List(ctx context.Context, prefix string) ([]string, error)
	Exists(ctx context.Context, path string) (bool, error)
}

// ---------------------------------------------------------------------------
// LocalStorage — filesystem backend
// ---------------------------------------------------------------------------

// LocalStorage stores files on the local filesystem under basePath.
type LocalStorage struct {
	basePath string
}

// NewLocalStorage creates a local filesystem storage rooted at basePath.
// The directory is created if it does not exist.
func NewLocalStorage(basePath string) *LocalStorage {
	os.MkdirAll(basePath, 0755)
	return &LocalStorage{basePath: basePath}
}

func (s *LocalStorage) fullPath(key string) string {
	return filepath.Join(s.basePath, filepath.Clean(key))
}

// Save writes data to the given path, creating parent directories as needed.
func (s *LocalStorage) Save(_ context.Context, path string, data io.Reader) error {
	fp := s.fullPath(path)
	if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
		return fmt.Errorf("storage: mkdir %s: %w", filepath.Dir(fp), err)
	}

	f, err := os.Create(fp)
	if err != nil {
		return fmt.Errorf("storage: create %s: %w", fp, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, data); err != nil {
		return fmt.Errorf("storage: write %s: %w", fp, err)
	}
	return f.Sync()
}

// Load opens the file at path for reading.
func (s *LocalStorage) Load(_ context.Context, path string) (io.ReadCloser, error) {
	f, err := os.Open(s.fullPath(path))
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}
	return f, nil
}

// Delete removes the file at path.
func (s *LocalStorage) Delete(_ context.Context, path string) error {
	err := os.Remove(s.fullPath(path))
	if os.IsNotExist(err) {
		return nil // idempotent
	}
	return err
}

// List returns all relative paths under the given prefix.
func (s *LocalStorage) List(_ context.Context, prefix string) ([]string, error) {
	root := s.fullPath(prefix)
	var paths []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.IsDir() {
			rel, _ := filepath.Rel(s.basePath, path)
			paths = append(paths, rel)
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	return paths, err
}

// Exists checks whether a file exists at path.
func (s *LocalStorage) Exists(_ context.Context, path string) (bool, error) {
	_, err := os.Stat(s.fullPath(path))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// ---------------------------------------------------------------------------
// S3Storage — AWS S3 / MinIO / DigitalOcean Spaces (stub)
// ---------------------------------------------------------------------------

// S3Storage uses an S3-compatible object store.
// Currently a stub that returns errors indicating S3 is not yet implemented.
// The Storage interface is the important contract for future backends.
type S3Storage struct {
	bucket   string
	region   string
	endpoint string // custom endpoint for MinIO / DO Spaces
}

// NewS3Storage creates an S3 storage backend (stub).
// When fully implemented, this will use AWS Signature V4 with a minimal HTTP client
// (no full AWS SDK dependency).
func NewS3Storage(bucket, region, endpoint string) (*S3Storage, error) {
	if bucket == "" {
		return nil, fmt.Errorf("storage: S3 bucket name required")
	}
	return &S3Storage{
		bucket:   bucket,
		region:   region,
		endpoint: endpoint,
	}, nil
}

func (s *S3Storage) stub() error {
	return fmt.Errorf("storage: S3 backend not yet implemented (bucket=%s, region=%s)", s.bucket, s.region)
}

func (s *S3Storage) Save(_ context.Context, _ string, _ io.Reader) error     { return s.stub() }
func (s *S3Storage) Load(_ context.Context, _ string) (io.ReadCloser, error) { return nil, s.stub() }
func (s *S3Storage) Delete(_ context.Context, _ string) error                { return s.stub() }
func (s *S3Storage) List(_ context.Context, _ string) ([]string, error)      { return nil, s.stub() }
func (s *S3Storage) Exists(_ context.Context, _ string) (bool, error)        { return false, s.stub() }

// ---------------------------------------------------------------------------
// NewFromEnv creates a Storage backend based on environment variables.
//
//	STORAGE_BACKEND: "local" (default) or "s3"
//	S3_BUCKET, S3_REGION, S3_ENDPOINT: S3 configuration
// ---------------------------------------------------------------------------

// NewFromEnv creates a Storage backend from environment variables.
// Falls back to LocalStorage with the given default path.
func NewFromEnv(defaultLocalPath string) (Storage, error) {
	backend := strings.ToLower(os.Getenv("STORAGE_BACKEND"))
	switch backend {
	case "s3":
		bucket := os.Getenv("S3_BUCKET")
		region := os.Getenv("S3_REGION")
		endpoint := os.Getenv("S3_ENDPOINT")
		return NewS3Storage(bucket, region, endpoint)
	default:
		return NewLocalStorage(defaultLocalPath), nil
	}
}
