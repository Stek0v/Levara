// Package backup provides full and selective backup/restore for Levara data.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
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
	addFileToTar(tw, manifestPath, "manifest.json")
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

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}

		// Skip directories — create them as needed
		cleanName := filepath.Clean(hdr.Name)

		if cleanName == "db.sql" {
			// Save DB dump to temp
			tmpSQL := filepath.Join(os.TempDir(), "levara_restore.sql")
			out, err := os.Create(tmpSQL)
			if err != nil {
				continue
			}
			io.Copy(out, tr)
			out.Close()
			dbSQL = tmpSQL
			continue
		}

		if cleanName == "manifest.json" {
			// Read and log manifest
			data, _ := io.ReadAll(tr)
			log.Printf("[restore] manifest: %s", string(data))
			continue
		}

		// Determine target path
		var targetPath string
		if strings.HasPrefix(cleanName, "collections/") || strings.HasPrefix(cleanName, "uploads/") {
			// Find or create node dir
			nodeDir := findOrCreateNodeDir(dataDir)
			if strings.HasPrefix(cleanName, "collections/") {
				targetPath = filepath.Join(nodeDir, cleanName)
			} else {
				targetPath = filepath.Join(dataDir, cleanName)
			}
		} else {
			targetPath = filepath.Join(dataDir, cleanName)
		}

		if hdr.Typeflag == tar.TypeDir {
			os.MkdirAll(targetPath, 0755)
			continue
		}

		// Create parent dirs
		os.MkdirAll(filepath.Dir(targetPath), 0755)

		out, err := os.Create(targetPath)
		if err != nil {
			log.Printf("[restore] skip %s: %v", cleanName, err)
			continue
		}
		io.Copy(out, tr)
		out.Close()
		log.Printf("[restore] extracted: %s", cleanName)
	}

	// Restore PostgreSQL
	if dbSQL != "" && dbDSN != "" {
		log.Printf("[restore] restoring PostgreSQL...")
		if err := PgRestore(dbDSN, dbSQL); err != nil {
			log.Printf("[restore] WARNING: pg_restore failed: %v", err)
		}
		os.Remove(dbSQL)
	}

	log.Printf("[restore] complete: data extracted to %s", dataDir)
	return nil
}

// helpers

func findOrCreateNodeDir(dataDir string) string {
	entries, _ := os.ReadDir(dataDir)
	for _, e := range entries {
		if e.IsDir() {
			colPath := filepath.Join(dataDir, e.Name(), "collections")
			if _, err := os.Stat(colPath); err == nil {
				return filepath.Join(dataDir, e.Name())
			}
		}
	}
	// Create default node dir
	nodeDir := filepath.Join(dataDir, "restored")
	os.MkdirAll(filepath.Join(nodeDir, "collections"), 0755)
	return nodeDir
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
	filepath.Walk(dir, func(_ string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() {
			count++
			size += info.Size()
		}
		return nil
	})
	return count, size
}
