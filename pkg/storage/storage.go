// Package storage provides a unified file storage interface with local filesystem
// and S3-compatible backends.
//
// Usage:
//
//	// Local (default):
//	fs := storage.NewLocalStorage("/data/uploads")
//	fs.Save(ctx, "docs/file.pdf", reader)
//
//	// S3:
//	s3 := storage.NewS3Storage("my-bucket", "us-east-1", "", accessKey, secretKey)
//	s3.Save(ctx, "docs/file.pdf", reader)
//
// Set STORAGE_BACKEND=s3 with S3_BUCKET, S3_REGION, S3_ENDPOINT, AWS_ACCESS_KEY_ID,
// AWS_SECRET_ACCESS_KEY env vars to use S3.
package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
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

// Presigner is an optional extension for storage backends that can issue
// time-limited direct download URLs (for example S3-compatible backends).
type Presigner interface {
	PresignGet(ctx context.Context, path string, ttl time.Duration) (string, error)
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

// fullPath resolves a relative storage key under the storage root,
// enforcing containment: keys containing .. (or absolute paths) must never
// escape the base directory (finding H10, 2026-09-03 review).
func (s *LocalStorage) fullPath(key string) (string, error) {
	if filepath.IsAbs(key) {
		return "", fmt.Errorf("storage: absolute key %q not allowed", key)
	}
	cleaned := filepath.Clean(key)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("storage: key %q escapes storage root", key)
	}
	fp := filepath.Join(s.basePath, cleaned)
	// Defense in depth: verify containment even for tricky inputs (e.g.
	// symlinks created inside the root, or separator edge cases).
	rootAbs, err := filepath.Abs(s.basePath)
	if err != nil {
		return "", err
	}
	fpAbs, err := filepath.Abs(fp)
	if err != nil {
		return "", err
	}
	if fpAbs != rootAbs && !strings.HasPrefix(fpAbs, rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("storage: key %q resolves outside storage root", key)
	}
	return fp, nil
}

// Save writes data to the given path, creating parent directories as needed.
func (s *LocalStorage) Save(_ context.Context, path string, data io.Reader) error {
	fp, err := s.fullPath(path)
	if err != nil {
		return err
	}
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
	fp, err := s.fullPath(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(fp)
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}
	return f, nil
}

// Delete removes the file at path.
func (s *LocalStorage) Delete(_ context.Context, path string) error {
	fp, err := s.fullPath(path)
	if err != nil {
		return err
	}
	err = os.Remove(fp)
	if os.IsNotExist(err) {
		return nil // idempotent
	}
	return err
}

// List returns all relative paths under the given prefix.
func (s *LocalStorage) List(_ context.Context, prefix string) ([]string, error) {
	root, err := s.fullPath(prefix)
	if err != nil {
		return nil, err
	}
	var paths []string

	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
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
	fp, err := s.fullPath(path)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(fp)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// NewFromEnv creates a Storage backend based on environment variables.
//
//	STORAGE_BACKEND: "local" (default) or "s3"
//	S3_BUCKET, S3_REGION, S3_ENDPOINT: S3 configuration
//	AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY: S3 credentials
// ---------------------------------------------------------------------------

// NewFromEnv creates a Storage backend from environment variables.
// An empty backend selects local storage; unknown providers are errors.
func NewFromEnv(defaultLocalPath string) (Storage, error) {
	backend := strings.ToLower(os.Getenv("STORAGE_BACKEND"))
	switch backend {
	case "s3":
		bucket := os.Getenv("S3_BUCKET")
		region := os.Getenv("S3_REGION")
		endpoint := os.Getenv("S3_ENDPOINT")
		stateKey, err := decodeMultipartStateKey(os.Getenv("S3_MULTIPART_STATE_KEY"))
		if err != nil {
			return nil, err
		}
		return NewS3StorageWithConfig(context.Background(), S3Config{Bucket: bucket, Region: region, Endpoint: endpoint, MultipartStateKey: stateKey})
	case "", "local":
		return NewLocalStorage(defaultLocalPath), nil
	default:
		return nil, fmt.Errorf("storage: unknown backend %q", backend)
	}
}
