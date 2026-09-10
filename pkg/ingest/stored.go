package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stek0v/levara/pkg/storage"
)

// IngestStored sends source bytes directly to the configured object/encryption
// backend. Remote or encrypted storage never stages plaintext in local blobs.
// Local storage retains the existing file:// locations for compatibility.
func IngestStored(ctx context.Context, items []Item, storagePath string, backend storage.Storage) ([]Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if backend == nil {
		return Ingest(items, storagePath)
	}
	if _, local := backend.(*storage.LocalStorage); local {
		return Ingest(items, storagePath)
	}
	results := make([]Result, 0, len(items))
	seen := make(map[string]bool)
	preps := make([]ingestPrep, 0, len(items))
	for _, item := range items {
		prep, err := prepareItem(item, filepath.Join(storagePath, "blobs"), seen)
		if err != nil {
			return nil, err
		}
		// Use the ID only as one canonical object component, never a supplied path.
		if prep.id == "" || filepath.Base(prep.id) != prep.id || prep.id == "." || prep.id == ".." || strings.ContainsAny(prep.id, "/\\\x00") {
			return nil, fmt.Errorf("invalid document ID")
		}
		preps = append(preps, prep)
	}
	// Validate the complete input before the first external effect.
	// ponytail: sequential uploads keep bytes and in-flight operations bounded;
	// add a measured small worker pool only if backend latency dominates.
	for _, prep := range preps {
		key := "ingest/" + prep.id + "/" + prep.contentHash
		if err := backend.Save(ctx, key, bytes.NewReader(prep.content)); err != nil {
			return nil, fmt.Errorf("save ingest object: %w", err)
		}
		results = append(results, Result{ID: prep.id, ContentHash: prep.contentHash, FilePath: "storage://" + key, MimeType: prep.mimeType, Extension: prep.ext, FileSize: int64(len(prep.content)), Name: prep.name, Tags: prep.tagsJSON, Room: prep.room, AlreadyExists: prep.alreadyExists})
	}
	return results, nil
}

// ingestDestination deliberately treats only the plain LocalStorage as local.
// Encryption wrappers must go through Storage.Save, never a plaintext stage.
type ingestDestination struct {
	root, identity string
	backend        storage.Storage
}

func (w *MetadataWriter) ingestDestination(storagePath string, backend storage.Storage) (ingestDestination, error) {
	_, local := backend.(*storage.LocalStorage)
	if backend == nil || local {
		if storagePath == "" {
			storagePath = "data"
		}
		root, err := filepath.Abs(storagePath)
		if err != nil {
			return ingestDestination{}, err
		}
		// Configuration may use system aliases such as macOS /var. Resolve
		// ancestors once into the destination identity; an explicit symlink
		// root and symlinks inside the private attempt tree are rejected.
		if info, err := os.Lstat(root); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
			return ingestDestination{}, errors.New("local ingest root must be a directory, not a symlink")
		}
		existing := root
		var suffix []string
		for {
			_, err := os.Lstat(existing)
			if err == nil {
				break
			}
			if !os.IsNotExist(err) {
				return ingestDestination{}, err
			}
			suffix = append(suffix, filepath.Base(existing))
			existing = filepath.Dir(existing)
		}
		root, err = filepath.EvalSymlinks(existing)
		if err != nil {
			return ingestDestination{}, err
		}
		for i := len(suffix) - 1; i >= 0; i-- {
			root = filepath.Join(root, suffix[i])
		}

		return ingestDestination{root: root, identity: "local:" + root}, nil
	}
	if strings.TrimSpace(w.storageIdentity) == "" {
		return ingestDestination{}, errors.New("non-local ingest requires a stable storage destination identity")
	}
	return ingestDestination{backend: backend, identity: "backend:" + w.storageIdentity}, nil
}
func (d ingestDestination) location(key string) string {
	if d.backend != nil {
		return "storage://" + key
	}
	return "file://" + filepath.Join(d.root, filepath.FromSlash(key))
}
func (d ingestDestination) key(location string) (string, error) {
	prefix := "storage://"
	if d.backend == nil {
		prefix = "file://" + d.root + string(filepath.Separator)
	}
	if !strings.HasPrefix(location, prefix) {
		return "", errors.New("invalid ingest location")
	}
	key := strings.TrimPrefix(location, prefix)
	if key == "" || strings.Contains(key, "\\") || filepath.Clean(key) != key || filepath.IsAbs(key) || strings.Contains(key, "\x00") {
		return "", errors.New("invalid ingest key")
	}
	return key, nil
}
func (d ingestDestination) owns(attempt, location string) bool {
	key, err := d.key(location)
	if err != nil {
		return false
	}
	prefix := ingestAttemptPrefix + attempt + "/"
	if !strings.HasPrefix(key, prefix) {
		return false
	}
	leaf := strings.TrimPrefix(key, prefix)
	number, suffix, ok := strings.Cut(leaf, "-")
	if !ok || (suffix != "text" && suffix != "original" && suffix != "structured") || number == "" {
		return false
	}
	for _, r := range number {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (d ingestDestination) ownsStructured(location string) bool {
	key, err := d.key(location)
	if err != nil {
		return false
	}
	parts := strings.Split(key, "/")
	return len(parts) == 3 && parts[0] == strings.TrimSuffix(ingestAttemptPrefix, "/") && parts[1] != "" && d.owns(parts[1], location) && strings.HasSuffix(parts[2], "-structured")
}

// openPrivateRoot traverses through native os.Root handles. The private attempt
// directory is new, mode 0700; file creation is exclusive, mode 0600. Root keeps
// accesses confined even if a concurrent filesystem rename changes a path.
func (d ingestDestination) openPrivateRoot(create bool) (*os.Root, error) {
	volume := filepath.VolumeName(d.root) + string(filepath.Separator)
	root, err := os.OpenRoot(volume)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(d.root, volume), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		if create {
			err := root.Mkdir(part, 0700)
			if err != nil && !os.IsExist(err) {
				root.Close()
				return nil, err
			}
			if err == nil {
				if err := syncIngestDirectory(root, "."); err != nil {
					root.Close()
					return nil, err
				}
			}
		}
		info, err := root.Lstat(part)
		if err != nil {
			root.Close()
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, errors.New("local ingest root contains symlink")
		}
		next, err := root.OpenRoot(part)
		root.Close()
		if err != nil {
			return nil, err
		}
		root = next
	}
	return root, nil
}
func (d ingestDestination) save(ctx context.Context, location string, data []byte) error {
	key, err := d.key(location)
	if err != nil {
		return err
	}
	if d.backend != nil {
		return d.backend.Save(ctx, key, bytes.NewReader(data))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := d.openPrivateRoot(true)
	if err != nil {
		return err
	}
	defer root.Close()
	dir := filepath.Dir(key)
	path := ""
	for _, part := range strings.Split(dir, string(filepath.Separator)) {
		path = filepath.Join(path, part)
		err := root.Mkdir(path, 0700)
		if err != nil && !os.IsExist(err) {
			return err
		}
		if err == nil {
			if err := syncIngestDirectory(root, filepath.Dir(path)); err != nil {
				return err
			}
		}
		info, err := root.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return errors.New("ingest directory must be private and not a symlink")
		}
	}
	file, err := root.OpenFile(key, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr, syncIngestDirectory(root, dir), ctx.Err())
}
func (d ingestDestination) remove(ctx context.Context, location string) error {
	key, err := d.key(location)
	if err != nil {
		return err
	}
	if d.backend != nil {
		return d.backend.Delete(ctx, key)
	}
	root, err := d.openPrivateRoot(false)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	// Never follow an object/attempt symlink while recovering a journal.
	path := ""
	for _, part := range strings.Split(key, string(filepath.Separator)) {
		path = filepath.Join(path, part)
		info, err := root.Lstat(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("ingest cleanup contains symlink")
		}
	}
	err = root.Remove(key)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncIngestDirectory(root, filepath.Dir(key))
}

// Persist directory entries before publishing SQL refs or retiring the journal.
func syncIngestDirectory(root *os.Root, name string) error {
	dir, err := root.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
