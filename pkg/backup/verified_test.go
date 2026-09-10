package backup

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/bm25"
	"github.com/stek0v/levara/pkg/workspace"
)

func verifiedFixture(t *testing.T) VerifiedOptions {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o := VerifiedOptions{DataDir: filepath.Join(base, "source"), WorkspacePath: filepath.Join(base, "workspace"), UploadsPath: filepath.Join(base, "uploads"), OutputDir: filepath.Join(base, "backups"), DBProvider: "sqlite", StorageBackend: "local", Standalone: true, NodeID: "node-1", Shards: 1, Dimension: 2, Timeout: time.Minute}
	for _, p := range []string{filepath.Join(o.DataDir, o.NodeID, "collections", "docs"), filepath.Join(o.DataDir, o.NodeID, "shard_0"), filepath.Join(o.DataDir, "bm25"), filepath.Join(o.WorkspacePath, "empty"), filepath.Join(o.UploadsPath, "structured_extractions")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, relative := range []string{filepath.Join(o.NodeID, "shard_0"), filepath.Join(o.NodeID, "collections", "docs")} {
		db, err := store.NewLevara(2, filepath.Join(o.DataDir, relative, "meta.bin"))
		if err != nil {
			t.Fatal(err)
		}
		if err = db.Insert("record-1", []float32{1, 0}, map[string]any{"text": "тест Unicode", "answer": 42}); err != nil {
			t.Fatal(err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	writeVerifiedFile(t, filepath.Join(o.DataDir, o.NodeID, "collections", "docs", "collection_meta.json"), []byte(`{"name":"docs","embedding_dim":2}`))
	writeVerifiedFile(t, filepath.Join(o.WorkspacePath, "документ.md"), []byte("# workspace\nпроверка\n"))
	project := workspace.ProjectRoot(o.WorkspacePath, "sample", "main")
	if err = os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	document := []byte("# indexed document\n")
	writeVerifiedFile(t, filepath.Join(project, "note.md"), document)
	digest := sha256.Sum256(document)
	wm := workspace.NewManifest("sample", "main")
	wm.ActiveGeneration = "gen-1"
	wm.Generations["gen-1"] = workspace.Generation{ID: "gen-1", Status: workspace.GenerationActive}
	wm.Chunks["chunk-1"] = workspace.ChunkRecord{ProjectID: "sample", Branch: "main", Generation: "gen-1", Path: "note.md", FileDigest: "sha256:" + hex.EncodeToString(digest[:]), ChunkID: "chunk-1", VectorID: "record-1", Collection: "docs"}
	if err = wm.Save(workspace.ManifestPath(o.WorkspacePath, "sample", "main")); err != nil {
		t.Fatal(err)
	}

	writeVerifiedFile(t, filepath.Join(o.UploadsPath, "raw.bin"), []byte{0, 1, 255, 13, 0})
	writeVerifiedFile(t, filepath.Join(o.UploadsPath, "structured_extractions", "result.json"), []byte(`{"text":"extracted"}`))
	idx := bm25.NewIndex()
	idx.Add("record-1", "тест recovery", `{"kind":"document"}`)
	if err := bm25.SaveSnapshot(filepath.Join(o.DataDir, "bm25", "ZG9jcw.jsonl"), idx); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(o.DataDir, "levara.db"))
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{"CREATE TABLE data (id TEXT PRIMARY KEY, raw_data_location TEXT, original_data_location TEXT)", "CREATE TABLE ontologies (id INTEGER PRIMARY KEY, file_path TEXT)", "CREATE TABLE rows_generic (id INTEGER PRIMARY KEY, payload BLOB, note TEXT)", "INSERT INTO rows_generic VALUES (1, x'0001ff', 'δοκιμή')"}
	for _, q := range statements {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("INSERT INTO data VALUES ($1,$2,$3)", "record-1", "file://"+filepath.Join(o.UploadsPath, "structured_extractions", "result.json"), "file://"+filepath.Join(o.UploadsPath, "raw.bin")); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO ontologies VALUES (1,$1)", filepath.Join(o.WorkspacePath, "документ.md")); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	return o
}
func writeVerifiedFile(t *testing.T, p string, data []byte) {
	t.Helper()
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestStructuredArtifactInventoryReferenceAndValidation(t *testing.T) {
	if !isReferenceColumn("document_structured_artifacts", "storage_location") {
		t.Fatal("structured artifact storage location is not part of backup references")
	}
	stage := t.TempDir()
	path := "ingest-authorized/attempt/0-structured"
	full := filepath.Join(stage, "uploads", filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"value":"ok"}`)
	writeVerifiedFile(t, full, body)
	manifest := VerifiedManifest{Files: []InventoryFile{{Root: "uploads", Path: path, Size: int64(len(body))}}}
	if _, err := verifyStructuredArtifacts(t.Context(), stage, manifest, VerifiedOptions{}); err != nil {
		t.Fatalf("valid journaled structured artifact: %v", err)
	}
	writeVerifiedFile(t, full, []byte(`{"value":`))
	if _, err := verifyStructuredArtifacts(t.Context(), stage, manifest, VerifiedOptions{}); err == nil {
		t.Fatal("malformed journaled structured artifact accepted")
	}
}

func TestVerifiedSQLiteRoundTripWithoutOriginalRoots(t *testing.T) {
	o := verifiedFixture(t)
	first, err := CreateVerifiedBackup(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if first.Tables != 3 || first.Indexes != 3 || first.CompletedAt == "" {
		t.Fatalf("incomplete receipt: %+v", first)
	}
	prior, err := ReadLastSuccessfulRestore(o.OutputDir)
	if err != nil || !sameJSON(first, prior) {
		t.Fatalf("last success=%+v err=%v", prior, err)
	}
	second, err := CreateVerifiedBackup(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if second.Archive == first.Archive {
		t.Fatal("captures must retain independent timestamped inventory")
	}
	if _, err = os.Stat(first.Archive); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{o.DataDir, o.WorkspacePath, o.UploadsPath} {
		if err = os.Rename(p, p+"-unavailable"); err != nil {
			t.Fatal(err)
		}
	}
	restored, err := VerifyArchive(context.Background(), first.Archive, o)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ArchiveSHA256 != first.ArchiveSHA256 {
		t.Fatal("verification depended on live originals")
	}
}
func TestVerifiedActiveWriterAndFailurePreservePreviousSuccess(t *testing.T) {
	o := verifiedFixture(t)
	r, err := CreateVerifiedBackup(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(o.OutputDir, "last_successful_restore.json"))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := AcquireWriterLease(o.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = CreateVerifiedBackup(context.Background(), o); !errors.Is(err, ErrSourceActive) {
		t.Fatalf("active writer accepted: %v", err)
	}
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	wal := filepath.Join(o.DataDir, o.NodeID, "shard_0", "meta.bin.wal")
	f, err := os.OpenFile(wal, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte{1, 2})
	_ = f.Close()
	if _, err = CreateVerifiedBackup(context.Background(), o); err == nil {
		t.Fatal("truncated WAL accepted")
	}
	after, _ := os.ReadFile(filepath.Join(o.OutputDir, "last_successful_restore.json"))
	if !bytes.Equal(original, after) {
		t.Fatal("failed attempt advanced last success")
	}
	if _, err = os.Stat(r.Archive); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(o.OutputDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".verified-") {
			t.Fatalf("staging leak: %s", e.Name())
		}
	}
}
func TestVerifiedPreflightAndCorruption(t *testing.T) {
	o := verifiedFixture(t)
	for _, tc := range []struct {
		name   string
		change func(*VerifiedOptions)
	}{
		{"online mode", func(o *VerifiedOptions) { o.Standalone = false }}, {"S3", func(o *VerifiedOptions) { o.StorageBackend = "s3" }}, {"Neo4j", func(o *VerifiedOptions) { o.Neo4jURL = "bolt://127.0.0.1:1" }}, {"missing external root", func(o *VerifiedOptions) { o.WorkspacePath += "-missing" }}, {"inside source", func(o *VerifiedOptions) { o.OutputDir = filepath.Join(o.UploadsPath, "backups") }}, {"byte budget", func(o *VerifiedOptions) { o.MaxBytes = 10 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := o
			tc.change(&copy)
			if _, err := CreateVerifiedBackup(context.Background(), copy); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	r, err := CreateVerifiedBackup(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(r.Archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		body []byte
	}{{"truncated", body[:len(body)-4]}, {"trailing", append(append([]byte(nil), body...), 1, 2, 3)}, {"checksum", append([]byte(nil), body...)}} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "checksum" {
				tc.body[len(tc.body)/2] ^= 255
			}
			p := filepath.Join(t.TempDir(), "broken.gz")
			writeVerifiedFile(t, p, tc.body)
			dest := t.TempDir()
			defaults, _ := verifiedDefaults(o)
			if _, _, _, err := extractVerified(context.Background(), p, dest, defaults); err == nil {
				t.Fatal("corrupt archive accepted")
			}
			files, _ := os.ReadDir(dest)
			if len(files) != 0 {
				t.Fatal("invalid archive extracted before validation")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = CreateVerifiedBackup(ctx, o); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel lost: %v", err)
	}
}
func TestVerifiedMissingReferenceAndChangedSQL(t *testing.T) {
	o := verifiedFixture(t)
	if err := os.Remove(filepath.Join(o.UploadsPath, "raw.bin")); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateVerifiedBackup(context.Background(), o); err == nil {
		t.Fatal("dangling raw document reference accepted")
	}
}

type fullDiskWriter struct{}

func (fullDiskWriter) Write([]byte) (int, error) { return 0, syscall.ENOSPC }
func TestVerifiedArchiveWritePropagatesDiskFull(t *testing.T) {
	if err := writeVerifiedArchive(context.Background(), fullDiskWriter{}, t.TempDir(), VerifiedManifest{Format: verifiedFormat}); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("disk full lost: %v", err)
	}
}
func TestVerifiedPostgresRoundTrip(t *testing.T) {
	dsn := os.Getenv("LEVARA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("isolated test PostgreSQL DSN required")
	}
	o := verifiedFixture(t)
	o.DBProvider = "postgres"
	o.PostgresBinDir = os.Getenv("LEVARA_TEST_POSTGRES_BIN")
	o.Timeout = 2 * time.Minute
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("backup_unit_%d", time.Now().UnixNano())
	if _, err = admin.Exec("CREATE DATABASE " + quoteSQL(name)); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec("DROP DATABASE " + quoteSQL(name) + " WITH (FORCE)") }()
	// Keep the isolated cluster's connection options while selecting only our DB.
	split := strings.SplitN(dsn, "?", 2)
	slash := strings.LastIndex(split[0], "/")
	o.PostgresDSN = split[0][:slash+1] + name
	if len(split) == 2 {
		o.PostgresDSN += "?" + split[1]
	}
	db, err := sql.Open("pgx", o.PostgresDSN)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"CREATE TABLE data (id TEXT PRIMARY KEY, raw_data_location TEXT, original_data_location TEXT)", "CREATE TABLE ontologies (id BIGINT PRIMARY KEY, file_path TEXT)", "CREATE TABLE rows_generic (id BIGSERIAL PRIMARY KEY, payload BYTEA, note TEXT)", "INSERT INTO rows_generic(payload,note) VALUES ('\\x0001ff','δοκιμή')"} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("INSERT INTO data VALUES ($1,$2,$3)", "record-1", "file://"+filepath.Join(o.UploadsPath, "structured_extractions", "result.json"), "file://"+filepath.Join(o.UploadsPath, "raw.bin")); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO ontologies VALUES (1,$1)", filepath.Join(o.WorkspacePath, "документ.md")); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := CreateVerifiedBackup(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{o.DataDir, o.WorkspacePath, o.UploadsPath} {
		if err = os.Rename(p, p+"-unavailable"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = VerifyArchive(context.Background(), r.Archive, o); err != nil {
		t.Fatal(err)
	}
}

func TestVerifiedRejectsMalformedStructuredArtifacts(t *testing.T) {
	o := verifiedFixture(t)
	writeVerifiedFile(t, filepath.Join(o.UploadsPath, "structured_extractions", "result.json"), []byte(`{"text":`))
	if _, err := CreateVerifiedBackup(context.Background(), o); err == nil {
		t.Fatal("malformed structured extraction accepted as verified recoverable artifact")
	}
}

func TestVerifiedDumpFailureTimeoutAndBudgetPreserveSuccess(t *testing.T) {
	o := verifiedFixture(t)
	r, err := CreateVerifiedBackup(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(o.OutputDir, "last_successful_restore.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, script string
		timeout      time.Duration
		max          int64
	}{
		{"failure", "printf partial; exit 7", time.Second, 1 << 20},
		{"deadline", "exec sleep 10", 30 * time.Millisecond, 1 << 20},
		{"budget", "printf 123456789", time.Second, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := o
			copy.DBProvider = "postgres"
			copy.PostgresDSN = "postgres://user:test-secret@127.0.0.1:1/db"
			copy.PostgresBinDir = t.TempDir()
			copy.Timeout = tc.timeout
			copy.MaxBytes = tc.max
			if err := os.WriteFile(filepath.Join(copy.PostgresBinDir, "pg_dump"), []byte("#!/bin/sh\n"+tc.script+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			_, err := CreateVerifiedBackup(context.Background(), copy)
			if err == nil || strings.Contains(err.Error(), "test-secret") {
				t.Fatalf("unsafe failure result %v", err)
			}
			if tc.name == "deadline" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline lost: %v", err)
			}
			after, _ := os.ReadFile(filepath.Join(o.OutputDir, "last_successful_restore.json"))
			if !bytes.Equal(before, after) {
				t.Fatal("failure advanced successful receipt")
			}
			if _, err = os.Stat(r.Archive); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVerifiedWorkspaceIndexReferences(t *testing.T) {
	for _, kind := range []string{"missing vector", "changed file"} {
		t.Run(kind, func(t *testing.T) {
			o := verifiedFixture(t)
			if kind == "changed file" {
				writeVerifiedFile(t, filepath.Join(workspace.ProjectRoot(o.WorkspacePath, "sample", "main"), "note.md"), []byte("unindexed change"))
			} else {
				p := workspace.ManifestPath(o.WorkspacePath, "sample", "main")
				body, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				var m workspace.Manifest
				if err = json.Unmarshal(body, &m); err != nil {
					t.Fatal(err)
				}
				c := m.Chunks["chunk-1"]
				c.VectorID = "missing"
				m.Chunks["chunk-1"] = c
				if err = m.Save(p); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := CreateVerifiedBackup(context.Background(), o); err == nil {
				t.Fatal("inconsistent workspace index accepted")
			}
		})
	}
}

func TestVerifiedProcessWriterLeaseHelper(t *testing.T) {
	if root := os.Getenv("LEVARA_BACKUP_LEASE_TEST_ROOT"); root != "" {
		lease, err := AcquireWriterLease(root)
		if err != nil {
			os.Exit(2)
		}
		_, _ = os.Stdout.WriteString("ready\n")
		_, _ = io.Copy(io.Discard, os.Stdin)
		_ = lease
		os.Exit(0)
	}
}
func TestVerifiedWriterLeaseSurvivesUntilProcessExit(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestVerifiedProcessWriterLeaseHelper$")
	cmd.Env = append(os.Environ(), "LEVARA_BACKUP_LEASE_TEST_ROOT="+root)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = in.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("helper readiness %q %v", line, err)
	}
	if lease, err := AcquireOfflineLease(root); !errors.Is(err, ErrSourceActive) {
		if lease != nil {
			_ = lease.Close()
		}
		t.Fatalf("live process lease bypassed: %v", err)
	}
	_ = in.Close()
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	lease, err := AcquireOfflineLease(root)
	if err != nil {
		t.Fatalf("OS did not release exited writer lease: %v", err)
	}
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
}
