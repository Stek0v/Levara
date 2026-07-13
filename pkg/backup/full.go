// Package backup provides full and selective backup/restore for Levara data.
package backup

import (
	"archive/tar"
	"compress/gzip"
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
	f, err := os.Create(output)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	// Find node dir (first subdir with collections/)
	nodeDir := ""
	entries, _ := os.ReadDir(dataDir)
	for _, e := range entries {
		if e.IsDir() {
			colPath := filepath.Join(dataDir, e.Name(), "collections")
			if _, err := os.Stat(colPath); err == nil {
				nodeDir = filepath.Join(dataDir, e.Name())
				break
			}
		}
	}

	manifest := NewManifest(dataDir, "postgres")

	// 1. Backup collections/
	if nodeDir != "" {
		colDir := filepath.Join(nodeDir, "collections")
		if _, err := os.Stat(colDir); err == nil {
			log.Printf("[backup] backing up collections from %s", colDir)
			collections, _ := os.ReadDir(colDir)
			for _, c := range collections {
				if c.IsDir() {
					manifest.Collections = append(manifest.Collections, c.Name())
				}
			}
			if err := addDirToTar(tw, colDir, "collections"); err != nil {
				return fmt.Errorf("tar collections: %w", err)
			}
		}
	}

	// 2. Backup uploads/
	uploadsDir := filepath.Join(dataDir, "uploads")
	if _, err := os.Stat(uploadsDir); err == nil {
		log.Printf("[backup] backing up uploads from %s", uploadsDir)
		count, size := countFiles(uploadsDir)
		manifest.UploadsCount = count
		manifest.UploadsSizeB = size
		if err := addDirToTar(tw, uploadsDir, "uploads"); err != nil {
			return fmt.Errorf("tar uploads: %w", err)
		}
	}

	// 3. Backup JSONL caches
	for _, name := range []string{"llm_cache.jsonl", "bm25_index.jsonl", "embed_cache.jsonl"} {
		p := filepath.Join(dataDir, name)
		if _, err := os.Stat(p); err == nil {
			log.Printf("[backup] backing up %s", name)
			if err := addFileToTar(tw, p, name); err != nil {
				log.Printf("[backup] warning: %s: %v", name, err)
			}
		}
	}

	// 4. PostgreSQL dump
	if dbDSN != "" {
		log.Printf("[backup] dumping PostgreSQL...")
		sqlPath := filepath.Join(os.TempDir(), "levara_backup.sql")
		if err := PgDump(dbDSN, sqlPath); err != nil {
			log.Printf("[backup] WARNING: pg_dump failed: %v (continuing without DB)", err)
		} else {
			if err := addFileToTar(tw, sqlPath, "db.sql"); err != nil {
				log.Printf("[backup] warning: db.sql: %v", err)
			}
			os.Remove(sqlPath)
		}
	}

	// 5. Write manifest
	manifestPath := filepath.Join(os.TempDir(), "levara_manifest.json")
	if err := manifest.Write(manifestPath); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	_ = addFileToTar(tw, manifestPath, "manifest.json")
	os.Remove(manifestPath)

	log.Printf("[backup] complete: %s (%d collections, %d uploads)", output, len(manifest.Collections), manifest.UploadsCount)
	return nil
}

// FullRestore extracts a tar.gz backup into data-dir and restores PostgreSQL.
func FullRestore(input, dataDir, dbDSN string) error {
	f, err := os.Open(input)
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

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

	// Restore PostgreSQL
	if dbSQL != "" && dbDSN != "" {
		log.Printf("[restore] restoring PostgreSQL...")
		if err := PgRestore(dbDSN, dbSQL); err != nil {
			log.Printf("[restore] WARNING: pg_restore failed: %v", err)
		}
		_ = os.Remove(dbSQL)
		dbSQL = ""
	}

	log.Printf("[restore] complete: data extracted to %s", dataDir)
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
	entries, _ := os.ReadDir(dataDir)
	for _, e := range entries {
		if e.IsDir() {
			colPath := filepath.Join(dataDir, e.Name(), "collections")
			if _, err := os.Stat(colPath); err == nil {
				return filepath.Join(dataDir, e.Name()), nil
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
		rel, _ := filepath.Rel(srcDir, path)
		name := filepath.Join(prefix, rel)

		if info.IsDir() {
			hdr := &tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: 0755, ModTime: info.ModTime()}
			return tw.WriteHeader(hdr)
		}
		return addFileToTar(tw, path, name)
	})
}

func addFileToTar(tw *tar.Writer, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
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

func countFiles(dir string) (int, int64) {
	count := 0
	var size int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() {
			count++
			size += info.Size()
		}
		return nil
	})
	return count, size
}
