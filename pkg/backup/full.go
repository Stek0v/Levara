// Package backup provides full and selective backup/restore for Levara data.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	maxRestoreEntryBytes = int64(4 << 30)
	maxRestoreTotalBytes = int64(32 << 30)
	maxManifestBytes     = int64(1 << 20)
)

// FullBackup creates a tar.gz archive with all Levara data.
// Includes: collections/, uploads/, *.jsonl caches, and pg_dump output.
func FullBackup(dataDir, dbDSN, output string) error {
	f, err := os.CreateTemp(filepath.Dir(output), ".levara-backup-*.tar.gz")
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer os.Remove(f.Name())
	// Publish only a complete archive; any failure leaves the previous one intact.
	err = errors.Join(writeBackupArchive(f, dataDir, dbDSN), f.Sync(), f.Close())
	if err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	if err := os.Rename(f.Name(), output); err != nil {
		return fmt.Errorf("publish backup: %w", err)
	}
	log.Printf("[backup] complete: %s", output)
	return nil
}

func writeBackupArchive(w io.Writer, dataDir, dbDSN string) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	return errors.Join(writeBackupEntries(tw, dataDir, dbDSN), tw.Close(), gz.Close())
}

func writeBackupEntries(tw *tar.Writer, dataDir, dbDSN string) error {
	// Find node dir (first subdir with collections/)
	nodeDir := ""
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return fmt.Errorf("read data directory: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			colPath := filepath.Join(dataDir, e.Name(), "collections")
			if _, err := os.Lstat(colPath); err == nil {
				nodeDir = filepath.Join(dataDir, e.Name())
				break
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("inspect collections: %w", err)
			}
		}
	}

	manifest := NewManifest(dataDir, "postgres")

	// 1. Backup collections/
	if nodeDir != "" {
		colDir := filepath.Join(nodeDir, "collections")
		if _, err := os.Stat(colDir); err == nil {
			log.Printf("[backup] backing up collections from %s", colDir)
			collections, err := os.ReadDir(colDir)
			if err != nil {
				return fmt.Errorf("read collections: %w", err)
			}
			for _, c := range collections {
				if c.IsDir() {
					manifest.Collections = append(manifest.Collections, c.Name())
				}
			}
			if err := addDirToTar(tw, colDir, "collections"); err != nil {
				return fmt.Errorf("tar collections: %w", err)
			}
		} else {
			return fmt.Errorf("inspect collections: %w", err)
		}
	}

	// 2. Backup uploads/
	uploadsDir := filepath.Join(dataDir, "uploads")
	if info, err := os.Lstat(uploadsDir); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("backup uploads source is not a directory")
		}
		log.Printf("[backup] backing up uploads from %s", uploadsDir)
		count, size, err := countFiles(uploadsDir)
		if err != nil {
			return fmt.Errorf("count uploads: %w", err)
		}
		manifest.UploadsCount = count
		manifest.UploadsSizeB = size
		if err := addDirToTar(tw, uploadsDir, "uploads"); err != nil {
			return fmt.Errorf("tar uploads: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect uploads: %w", err)
	}

	// 3. Backup JSONL caches
	for _, name := range []string{"llm_cache.jsonl", "bm25_index.jsonl", "embed_cache.jsonl"} {
		p := filepath.Join(dataDir, name)
		if _, err := os.Lstat(p); err == nil {
			log.Printf("[backup] backing up %s", name)
			if err := addFileToTar(tw, p, name); err != nil {
				return fmt.Errorf("tar %s: %w", name, err)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect cache %s: %w", name, err)
		}
	}

	// 4. PostgreSQL dump
	if dbDSN != "" {
		log.Printf("[backup] dumping PostgreSQL...")
		sqlFile, err := os.CreateTemp("", "levara-backup-*.sql")
		if err != nil {
			return fmt.Errorf("create temporary database dump: %w", err)
		}
		sqlPath := sqlFile.Name()
		defer os.Remove(sqlPath)
		if err := sqlFile.Close(); err != nil {
			return fmt.Errorf("close temporary database dump: %w", err)
		}
		if err := PgDump(dbDSN, sqlPath); err != nil {
			return err
		}
		if err := addFileToTar(tw, sqlPath, "db.sql"); err != nil {
			return fmt.Errorf("tar database dump: %w", err)
		}
	}

	// 5. Write manifest
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Size: int64(len(data)), Mode: 0600}); err != nil {
		return fmt.Errorf("tar manifest: %w", err)
	}
	_, err = tw.Write(data)
	return err
}

// FullRestore extracts a tar.gz backup into data-dir and restores PostgreSQL.
func FullRestore(input, dataDir, dbDSN string) (restoreErr error) {
	defer func() {
		if restoreErr == nil {
			log.Printf("[restore] complete: data extracted to %s", dataDir)
		}
	}()
	f, err := os.Open(input)
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}
	defer func() { restoreErr = errors.Join(restoreErr, f.Close()) }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer func() { restoreErr = errors.Join(restoreErr, gz.Close()) }()

	tr := tar.NewReader(gz)
	dbSQL := ""
	var restoredBytes int64
	defer func() {
		if dbSQL != "" {
			_ = os.Remove(dbSQL)
		}
	}()

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}

		cleanName, err := safeArchiveName(hdr.Name)
		if err != nil {
			return err
		}
		if hdr.Size < 0 || hdr.Size > maxRestoreEntryBytes || restoredBytes > maxRestoreTotalBytes-hdr.Size {
			return fmt.Errorf("restore entry %q exceeds size limits", cleanName)
		}
		regularFile := hdr.Typeflag == tar.TypeReg || hdr.Typeflag == 0
		if hdr.Typeflag != tar.TypeDir && !regularFile {
			return fmt.Errorf("restore entry %q has unsupported type %d", cleanName, hdr.Typeflag)
		}

		if cleanName == "db.sql" {
			if dbSQL != "" {
				return fmt.Errorf("duplicate database dump entry")
			}
			if !regularFile {
				return fmt.Errorf("database dump entry must be a regular file")
			}
			// Save DB dump to temp
			out, err := os.CreateTemp("", "levara-restore-*.sql")
			if err != nil {
				return fmt.Errorf("create temporary database dump: %w", err)
			}
			tmpSQL := out.Name()
			if _, err := io.CopyN(out, tr, hdr.Size); err != nil {
				_ = out.Close()
				_ = os.Remove(tmpSQL)
				return fmt.Errorf("extract database dump: %w", err)
			}
			if err := out.Close(); err != nil {
				_ = os.Remove(tmpSQL)
				return fmt.Errorf("close database dump: %w", err)
			}
			restoredBytes += hdr.Size
			dbSQL = tmpSQL
			continue
		}

		if cleanName == "manifest.json" {
			if !regularFile {
				return fmt.Errorf("manifest entry must be a regular file")
			}
			// Read and log manifest
			if hdr.Size > maxManifestBytes {
				return fmt.Errorf("restore manifest exceeds %d bytes", maxManifestBytes)
			}
			data, err := io.ReadAll(io.LimitReader(tr, maxManifestBytes+1))
			if err != nil {
				return fmt.Errorf("read restore manifest: %w", err)
			}
			restoredBytes += int64(len(data))
			log.Printf("[restore] manifest: %s", string(data))
			continue
		}

		// Determine target path
		targetRoot := dataDir
		if cleanName == "collections" || strings.HasPrefix(cleanName, "collections/") {
			// Find or create node dir
			targetRoot, err = findOrCreateNodeDir(dataDir)
			if err != nil {
				return err
			}
		}
		targetPath, err := secureRestorePath(targetRoot, cleanName)
		if err != nil {
			return err
		}

		if hdr.Typeflag == tar.TypeDir {
			if err := mkdirAllNoSymlink(targetRoot, targetPath); err != nil {
				return err
			}
			continue
		}

		// Create parent dirs
		if err := mkdirAllNoSymlink(targetRoot, filepath.Dir(targetPath)); err != nil {
			return err
		}
		if info, err := os.Lstat(targetPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("restore target %q is a symlink", targetPath)
		} else if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("inspect restore target: %w", err)
		}

		out, err := os.CreateTemp(filepath.Dir(targetPath), ".levara-restore-*")
		if err != nil {
			return fmt.Errorf("create restore target %q: %w", cleanName, err)
		}
		tmpPath := out.Name()
		if err := out.Chmod(0600); err != nil {
			_ = out.Close()
			_ = os.Remove(tmpPath)
			return fmt.Errorf("set restore target permissions %q: %w", cleanName, err)
		}
		if _, err := io.CopyN(out, tr, hdr.Size); err != nil {
			_ = out.Close()
			_ = os.Remove(tmpPath)
			return fmt.Errorf("extract %q: %w", cleanName, err)
		}
		if err := out.Close(); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("close restore target %q: %w", cleanName, err)
		}
		if err := os.Rename(tmpPath, targetPath); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("install restore target %q: %w", cleanName, err)
		}
		restoredBytes += hdr.Size
		log.Printf("[restore] extracted: %s", cleanName)
	}

	// tar EOF does not validate the gzip trailer. Consume it before running SQL.
	// Bound trailing data too, so appended gzip streams cannot bypass size limits.
	if n, err := io.Copy(io.Discard, io.LimitReader(gz, maxRestoreTotalBytes-restoredBytes+1)); err != nil {
		return fmt.Errorf("finish gzip archive: %w", err)
	} else if n > maxRestoreTotalBytes-restoredBytes {
		return fmt.Errorf("restore archive exceeds size limits")
	}
	if dbDSN != "" && dbSQL == "" {
		return fmt.Errorf("backup has no database dump to restore")
	}
	// Restore PostgreSQL
	if dbSQL != "" && dbDSN != "" {
		log.Printf("[restore] restoring PostgreSQL...")
		if err := PgRestore(dbDSN, dbSQL); err != nil {
			return err
		}
		_ = os.Remove(dbSQL)
		dbSQL = ""
	}

	return nil
}

func safeArchiveName(name string) (string, error) {
	if name == "" || strings.Contains(name, "\\") {
		return "", fmt.Errorf("unsafe restore entry name %q", name)
	}
	clean := path.Clean(name)
	if clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("unsafe restore entry name %q", name)
	}
	return clean, nil
}

func secureRestorePath(root, name string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve restore root: %w", err)
	}
	target, err := filepath.Abs(filepath.Join(rootAbs, filepath.FromSlash(name)))
	if err != nil {
		return "", fmt.Errorf("resolve restore target: %w", err)
	}
	rel, err := filepath.Rel(rootAbs, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("restore entry %q escapes target root", name)
	}
	return target, nil
}

func mkdirAllNoSymlink(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("restore directory escapes target root")
	}
	current := rootAbs
	if info, err := os.Lstat(current); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("restore root %q is a symlink", current)
	}
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		switch {
		case err == nil && info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("restore path component %q is a symlink", current)
		case err == nil && !info.IsDir():
			return fmt.Errorf("restore path component %q is not a directory", current)
		case os.IsNotExist(err):
			if err := os.Mkdir(current, 0755); err != nil && !os.IsExist(err) {
				return fmt.Errorf("create restore directory %q: %w", current, err)
			}
		case err != nil:
			return fmt.Errorf("inspect restore directory %q: %w", current, err)
		}
	}
	return nil
}

// helpers

func findOrCreateNodeDir(dataDir string) (string, error) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return "", fmt.Errorf("read restore data directory: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			colPath := filepath.Join(dataDir, e.Name(), "collections")
			if _, err := os.Stat(colPath); err == nil {
				return filepath.Join(dataDir, e.Name()), nil
			} else if !os.IsNotExist(err) {
				return "", fmt.Errorf("inspect restore collections: %w", err)
			}
		}
	}
	// Create default node dir
	nodeDir := filepath.Join(dataDir, "restored")
	if err := mkdirAllNoSymlink(dataDir, filepath.Join(nodeDir, "collections")); err != nil {
		return "", err
	}
	return nodeDir, nil
}

func addDirToTar(tw *tar.Writer, srcDir, prefix string) error {
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		name := filepath.Join(prefix, rel)

		if info.IsDir() {
			hdr := &tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: 0755, ModTime: info.ModTime()}
			return tw.WriteHeader(hdr)
		}
		return addFileToTar(tw, path, name)
	})
}

func addFileToTar(tw *tar.Writer, path, name string) (addErr error) {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("backup source %q is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { addErr = errors.Join(addErr, f.Close()) }()
	info, err = f.Stat()
	if err != nil {
		return err
	}
	hdr := &tar.Header{Name: name, Size: info.Size(), Mode: 0644, ModTime: info.ModTime()}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func countFiles(dir string) (int, int64, error) {
	count := 0
	var size int64
	err := filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info != nil && !info.IsDir() {
			count++
			size += info.Size()
		}
		return nil
	})
	return count, size, err
}
