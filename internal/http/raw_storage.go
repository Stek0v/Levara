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
func loadRawDataByLocation(ctx context.Context, cfg APIConfig, location string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	switch {
	case strings.HasPrefix(location, "file://"):
		path := strings.TrimPrefix(location, "file://")
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
		// Backward compatibility for plain local paths.
		return os.ReadFile(location)
	}
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
