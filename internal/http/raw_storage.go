package http

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stek0v/levara/pkg/ingest"
	"github.com/stek0v/levara/pkg/storage"
)

const storageURIPrefix = "storage://"

// mirrorResultsToFileStorage uploads locally-ingested files to cfg.FileStorage
// and rewrites FilePath to storage://<key> for non-local backends.
func mirrorResultsToFileStorage(ctx context.Context, cfg APIConfig, results []ingest.Result) ([]ingest.Result, error) {
	if len(results) == 0 || cfg.FileStorage == nil {
		return results, nil
	}
	if _, isLocal := cfg.FileStorage.(*storage.LocalStorage); isLocal {
		// Keep existing file:// semantics for local backend.
		return results, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	rewritten := make([]ingest.Result, len(results))
	copy(rewritten, results)

	for i := range rewritten {
		loc := rewritten[i].FilePath
		if !strings.HasPrefix(loc, "file://") {
			continue
		}
		localPath := strings.TrimPrefix(loc, "file://")
		f, err := os.Open(localPath)
		if err != nil {
			return nil, fmt.Errorf("open local ingest artifact %q: %w", localPath, err)
		}
		key := storageKeyForResult(rewritten[i])
		saveErr := cfg.FileStorage.Save(ctx, key, f)
		closeErr := f.Close()
		if saveErr != nil {
			return nil, fmt.Errorf("store artifact %q: %w", key, saveErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close local ingest artifact %q: %w", localPath, closeErr)
		}
		rewritten[i].FilePath = storageURIPrefix + key
	}

	return rewritten, nil
}

func storageKeyForResult(r ingest.Result) string {
	return storageKeyForData(r.ID, r.Extension, "")
}

// loadRawDataByLocation resolves file:// and storage:// locations.
// Local filesystem locations (file:// and legacy plain paths) are contained
// to the configured storage root: a tampered raw_data_location row must not
// read arbitrary server files (finding M27, 2026-09-03 review).
func loadRawDataByLocation(ctx context.Context, cfg APIConfig, location string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	switch {
	case strings.HasPrefix(location, "file://"):
		path := strings.TrimPrefix(location, "file://")
		if !pathInsideStorageRoot(cfg.StoragePath, path) {
			return nil, fmt.Errorf("raw location %q is outside the storage root", location)
		}
		return os.ReadFile(path)
	case strings.HasPrefix(location, storageURIPrefix):
		if cfg.FileStorage == nil {
			return nil, fmt.Errorf("file storage backend is not configured")
		}
		key := strings.TrimPrefix(location, storageURIPrefix)
		rc, err := cfg.FileStorage.Load(ctx, key)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	default:
		// Backward compatibility for plain local paths — same containment.
		if !pathInsideStorageRoot(cfg.StoragePath, location) {
			return nil, fmt.Errorf("raw location %q is outside the storage root", location)
		}
		return os.ReadFile(location)
	}
}

// pathInsideStorageRoot reports whether path resolves inside root without
// traversal. Empty root disables the check (unconfigured deployments).
func pathInsideStorageRoot(root, path string) bool {
	if strings.TrimSpace(root) == "" {
		return true
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	if pathAbs == rootAbs {
		return true
	}
	return strings.HasPrefix(pathAbs, rootAbs+string(filepath.Separator))
}

func storageKeyForData(id, extension, fallbackPath string) string {
	ext := strings.TrimSpace(extension)
	if ext == "" && fallbackPath != "" {
		ext = filepath.Ext(fallbackPath)
	}
	if ext == "" {
		ext = ".txt"
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return "ingest/" + id + ext
}

func presignRawLocation(ctx context.Context, cfg APIConfig, location string, ttl time.Duration) (string, bool, error) {
	if !strings.HasPrefix(location, storageURIPrefix) || cfg.FileStorage == nil {
		return "", false, nil
	}
	type presigner interface {
		PresignGet(context.Context, string, time.Duration) (string, error)
	}
	s, ok := cfg.FileStorage.(presigner)
	if !ok {
		return "", false, nil
	}
	key := strings.TrimPrefix(location, storageURIPrefix)
	u, err := s.PresignGet(ctx, key, ttl)
	if err != nil {
		return "", true, err
	}
	return u, true, nil
}
