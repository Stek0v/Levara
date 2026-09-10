package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	accesspkg "github.com/stek0v/levara/pkg/access"
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
		f, err := openRawLocal(ctx, cfg, localPath)
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
	if r.ContentHash != "" {
		return "ingest/" + r.ID + "/" + r.ContentHash
	}
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.HasPrefix(location, storageURIPrefix) {
		if cfg.FileStorage == nil {
			return nil, errors.New("file storage backend is not configured")
		}
		rc, err := cfg.FileStorage.Load(ctx, strings.TrimPrefix(location, storageURIPrefix))
		if err != nil {
			if rc != nil {
				err = errors.Join(err, rc.Close())
			}
			return nil, err
		}
		if rc == nil {
			return nil, errors.New("file storage returned no reader")
		}
		return readRawAndClose(ctx, rc)
	}
	file, err := openRawLocal(ctx, cfg, strings.TrimPrefix(location, "file://"))
	if err != nil {
		return nil, err
	}
	return readRawAndClose(ctx, file)
}

func readRawAndClose(ctx context.Context, reader io.ReadCloser) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, reader.Close())
	}
	data, readErr := io.ReadAll(reader)
	if err := errors.Join(readErr, reader.Close(), ctx.Err()); err != nil {
		return nil, err
	}
	return data, nil
}

// openRawLocal opens relative to a native directory handle, so containment
// survives symlink swaps between checking a row's location and opening it.
// Configured ancestors are canonicalized for /var -> /private/var and equivalent
// deployment aliases. A symlink within the root may never escape that root.
func openRawLocal(ctx context.Context, cfg APIConfig, path string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rootPath := cfg.StoragePath
	if strings.TrimSpace(rootPath) == "" {
		// Legacy helpers/no-SQL development may read trusted local artifacts without
		// configured roots. Authenticated requests must never inherit that fallback.
		e, _ := ctx.Value(searchEgressKey{}).(searchEgress)
		actor, _ := ctx.Value(searchActorKey{}).(accesspkg.Actor)
		if cfg.RequireAuth || cfg.DB != nil || e.kind != "" || e.actor.UserID != "" || actor.UserID != "" {
			return nil, errors.New("authenticated local reads require a storage root")
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		rootPath = filepath.VolumeName(absolute) + string(filepath.Separator)
	}
	rootAbs, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, err
	}
	rootCanonical, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	relative, inside := rawPathRelative(rootAbs, absolute)
	if !inside {
		relative, inside = rawPathRelative(rootCanonical, absolute)
	}
	if !inside {
		// A legacy location can use a different alias of the same existing parent.
		parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
		if err != nil {
			return nil, err
		}
		relative, inside = rawPathRelative(rootCanonical, filepath.Join(parent, filepath.Base(absolute)))
	}
	if !inside {
		return nil, errors.New("raw location is outside the storage root")
	}
	root, err := os.OpenRoot(rootCanonical)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Stat(relative)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("raw location must be a regular file")
	}
	// O_NONBLOCK avoids a FIFO substitution hanging Open before descriptor-level
	// regular-file validation. It has no effect on ordinary regular-file reads.
	file, err := root.OpenFile(relative, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("raw location must be a regular file")
	}
	if err := ctx.Err(); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func rawPathRelative(root, path string) (string, bool) {
	relative, err := filepath.Rel(root, path)
	return relative, err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
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
