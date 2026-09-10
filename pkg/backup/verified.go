package backup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/bm25"
	"github.com/stek0v/levara/pkg/workspace"
)

const verifiedFormat = "levara-offline-v2"
const verifiedLockName = ".levara-runtime.lock"
const ingestAuthorizedBackupPrefix = "ingest-authorized/"

// VerifiedOptions describes one local standalone node. Provider credentials are
// process inputs only and are never written to the archive or receipt.
type VerifiedOptions struct {
	DataDir, WorkspacePath, UploadsPath, SQLitePath, PostgresDSN string
	DBProvider, NodeID, StorageBackend, Neo4jURL                 string
	Standalone                                                   bool
	Shards, Dimension                                            int
	OutputDir, PostgresBinDir                                    string
	Timeout                                                      time.Duration
	MaxBytes                                                     int64
	MaxFiles, MaxRows                                            int
}
type InventoryFile struct {
	Root, Path, SHA256 string
	Size               int64
}
type SQLTableProof struct {
	Columns []string
	Rows    int
	SHA256  string
}
type IndexProof struct {
	Records int
	SHA256  string
}
type VerifiedManifest struct {
	Directories       []string `json:"directories"`
	Format            string   `json:"format"`
	CreatedAt         string   `json:"created_at"`
	DBProvider        string   `json:"db_provider"`
	NodeID            string   `json:"node_id"`
	Shards, Dimension int
	Roots             map[string]string        `json:"roots"`
	Files             []InventoryFile          `json:"files"`
	SQLSchemaSHA256   string                   `json:"sql_schema_sha256"`
	Tables            map[string]SQLTableProof `json:"tables"`
	Indexes           map[string]IndexProof    `json:"indexes"`
}
type RestoreReceipt struct {
	Archive                string `json:"archive"`
	ArchiveSHA256          string `json:"archive_sha256"`
	ManifestSHA256         string `json:"manifest_sha256"`
	CompletedAt            string `json:"completed_at"`
	DBProvider             string `json:"db_provider"`
	Files, Tables, Indexes int
	Isolation              string `json:"isolation"`
}

func verifiedDefaults(o VerifiedOptions) (VerifiedOptions, error) {
	if o.Timeout == 0 {
		o.Timeout = 15 * time.Minute
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = 4 << 30
	}
	if o.MaxFiles == 0 {
		o.MaxFiles = 100000
	}
	if o.MaxRows == 0 {
		o.MaxRows = 100000
	}
	if o.Timeout < time.Millisecond || o.Timeout > 24*time.Hour || o.MaxBytes < 1 || o.MaxBytes > maxRestoreTotalBytes || o.MaxFiles < 1 || o.MaxFiles > 200000 || o.MaxRows < 1 || o.MaxRows > 1000000 {
		return o, errors.New("backup: invalid verification limits")
	}
	return o, nil
}
func verifiedRoots(o VerifiedOptions) (map[string]string, error) {
	if !o.Standalone || o.Neo4jURL != "" || o.StorageBackend != "" && o.StorageBackend != "local" {
		return nil, errors.New("backup: verified backup supports one standalone node with local artifacts and no external Neo4j")
	}
	if o.DBProvider != "sqlite" && o.DBProvider != "postgres" {
		return nil, errors.New("backup: explicit SQLite or PostgreSQL provider required")
	}
	if o.NodeID == "" || filepath.Base(o.NodeID) != o.NodeID || o.NodeID == "." || o.NodeID == ".." || strings.ContainsAny(o.NodeID, "/\\") || o.Shards < 1 || o.Shards > 64 || o.Dimension < 1 || o.Dimension > 65536 {
		return nil, errors.New("backup: valid node identity, shard count and dimension required")
	}
	if o.DataDir == "" {
		return nil, errors.New("backup: data root required")
	}
	if o.WorkspacePath == "" {
		o.WorkspacePath = filepath.Join(o.DataDir, "workspace")
	}
	if o.UploadsPath == "" {
		o.UploadsPath = filepath.Join(o.DataDir, "uploads")
	}
	roots := map[string]string{"data": o.DataDir, "workspace": o.WorkspacePath, "uploads": o.UploadsPath}
	for name, p := range roots {
		a, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		real, err := filepath.EvalSymlinks(a)
		if err != nil || real != a {
			return nil, errors.New("backup: root missing or contains symlinks")
		}
		st, err := os.Stat(a)
		if err != nil || !st.IsDir() {
			return nil, errors.New("backup: required root is not a directory")
		}
		roots[name] = a
	}
	entries, err := os.ReadDir(roots["data"])
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		_, err := os.Stat(filepath.Join(roots["data"], e.Name(), "collections"))
		if err == nil && e.Name() != o.NodeID {
			return nil, errors.New("backup: multiple node inventories are unsupported")
		}
	}
	required := []string{filepath.Join(roots["data"], o.NodeID, "collections")}
	for i := 0; i < o.Shards; i++ {
		required = append(required, filepath.Join(roots["data"], o.NodeID, fmt.Sprintf("shard_%d", i), "meta.bin.wal"))
	}
	for _, p := range required {
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("backup: required node component missing: %w", err)
		}
	}
	return roots, nil
}
func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// CreateVerifiedBackup takes an offline snapshot, releases its lease, then
// restores and verifies fresh resources. Only a successful verification advances
// last_successful_restore.json; previous immutable archives are retained.
func CreateVerifiedBackup(parent context.Context, o VerifiedOptions) (receipt RestoreReceipt, retErr error) {
	o, err := verifiedDefaults(o)
	if err != nil {
		return receipt, err
	}
	ctx, cancel := context.WithTimeout(parent, o.Timeout)
	defer cancel()
	roots, err := verifiedRoots(o)
	if err != nil {
		return receipt, err
	}
	output, err := filepath.Abs(o.OutputDir)
	if err != nil || o.OutputDir == "" {
		return receipt, errors.New("backup: output directory required")
	}
	for _, root := range roots {
		if within(root, output) || within(output, root) {
			return receipt, errors.New("backup: destination overlaps source roots")
		}
	}
	if err = os.MkdirAll(output, 0700); err != nil {
		return receipt, err
	}
	realOutput, err := filepath.EvalSymlinks(output)
	if err != nil {
		return receipt, err
	}
	output = realOutput
	for _, root := range roots {
		if within(root, output) || within(output, root) {
			return receipt, errors.New("backup: destination overlaps source roots")
		}
	}
	outputLease, err := AcquireOfflineLease(output)
	if err != nil {
		return receipt, err
	}
	defer outputLease.Close()
	lease, err := AcquireOfflineLease(roots["data"])
	if err != nil {
		return receipt, err
	}
	defer lease.Close()
	// Include explicitly configured roots even when nested. Each has its own
	// restoration identity; duplicate bytes are preferable to silently lost roots.
	stage, err := os.MkdirTemp(output, ".verified-stage-")
	if err != nil {
		return receipt, err
	}
	defer os.RemoveAll(stage)
	manifest := VerifiedManifest{Format: verifiedFormat, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), DBProvider: o.DBProvider, NodeID: o.NodeID, Shards: o.Shards, Dimension: o.Dimension, Roots: roots}
	sqlitePath := o.SQLitePath
	if sqlitePath == "" {
		sqlitePath = filepath.Join(roots["data"], "levara.db")
	}
	sqlitePath, _ = filepath.Abs(sqlitePath)
	dbArtifact := filepath.Join(stage, "sql", map[bool]string{true: "sqlite.db", false: "postgres.dump"}[o.DBProvider == "sqlite"])
	if err = os.MkdirAll(filepath.Dir(dbArtifact), 0700); err != nil {
		return receipt, err
	}
	if o.DBProvider == "sqlite" {
		if err = backupSQLite(ctx, sqlitePath, dbArtifact, o.MaxBytes); err != nil {
			return receipt, err
		}
	} else {
		if !strings.HasPrefix(o.PostgresDSN, "postgres://") && !strings.HasPrefix(o.PostgresDSN, "postgresql://") {
			return receipt, errors.New("backup: PostgreSQL source URI required")
		}
		if err = dumpVerifiedPostgres(ctx, o, dbArtifact); err != nil {
			return receipt, err
		}
	}
	var total int64
	for _, name := range []string{"data", "workspace", "uploads"} {
		root := roots[name]
		dest := filepath.Join(stage, name)
		if err = os.MkdirAll(dest, 0700); err != nil {
			return receipt, err
		}
		err = filepath.WalkDir(root, func(p string, e os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			if rel == "." {
				return nil
			}
			if e.Type()&os.ModeSymlink != 0 {
				return errors.New("backup: source contains a symlink")
			}
			if e.IsDir() {
				if len(manifest.Directories) >= o.MaxFiles {
					return errors.New("backup: directory inventory exceeds limits")
				}
				manifest.Directories = append(manifest.Directories, name+"/"+filepath.ToSlash(rel))
				return os.MkdirAll(filepath.Join(dest, rel), 0700)
			}
			if p == filepath.Join(roots["data"], verifiedLockName) || o.DBProvider == "sqlite" && (p == sqlitePath || p == sqlitePath+"-wal" || p == sqlitePath+"-shm") {
				return nil
			}
			st, err := e.Info()
			if err != nil || !st.Mode().IsRegular() {
				return errors.New("backup: source contains unsupported file type")
			}
			if len(manifest.Files) >= o.MaxFiles || st.Size() < 0 || st.Size() > maxRestoreEntryBytes || total > o.MaxBytes-st.Size() {
				return errors.New("backup: inventory exceeds limits")
			}
			proof, err := copyInventoryFile(ctx, p, filepath.Join(dest, rel), name, filepath.ToSlash(rel))
			if err != nil {
				return err
			}
			total += proof.Size
			manifest.Files = append(manifest.Files, proof)
			return nil
		})
		if err != nil {
			return receipt, err
		}
	}
	sqlProof, err := hashInventoryFile(ctx, dbArtifact, "sql", filepath.Base(dbArtifact))
	if err != nil {
		return receipt, err
	}
	total += sqlProof.Size
	if total > o.MaxBytes || len(manifest.Files) >= o.MaxFiles || sqlProof.Size > maxRestoreEntryBytes {
		return receipt, errors.New("backup: SQL snapshot exceeds byte budget")
	}
	manifest.Files = append(manifest.Files, sqlProof)
	db, closeDB, err := snapshotSQLForProof(ctx, o, dbArtifact)
	if err != nil {
		return receipt, err
	}
	manifest.SQLSchemaSHA256, manifest.Tables, err = sqlProofs(ctx, db, o.DBProvider, roots, o.MaxRows)
	closeErr := closeDB()
	if err != nil || closeErr != nil {
		return receipt, errors.Join(err, closeErr)
	}
	manifest.Indexes, err = verifyIndexes(ctx, stage, manifest, o)
	if err != nil {
		return receipt, err
	}
	sort.Strings(manifest.Directories)
	sort.Slice(manifest.Files, func(i, j int) bool {
		return manifest.Files[i].Root+"/"+manifest.Files[i].Path < manifest.Files[j].Root+"/"+manifest.Files[j].Path
	})
	if err = lease.Close(); err != nil {
		return receipt, err
	}
	tmp, err := os.CreateTemp(output, ".verified-archive-*.tar.gz")
	if err != nil {
		return receipt, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	err = writeVerifiedArchive(ctx, tmp, stage, manifest)
	err = errors.Join(err, tmp.Sync(), tmp.Close())
	if err != nil {
		return receipt, err
	}
	receipt, err = VerifyArchive(ctx, tmpName, o)
	if err != nil {
		return receipt, err
	}
	if err = ctx.Err(); err != nil {
		return receipt, err
	}
	target := filepath.Join(output, "verified-"+receipt.ArchiveSHA256+".tar.gz")
	if err = os.Rename(tmpName, target); err != nil {
		return receipt, err
	}
	receipt.Archive = target
	if err = syncDir(output); err != nil {
		return receipt, err
	}
	if err = writeReceipt(output, receipt); err != nil {
		return receipt, err
	}
	return receipt, nil
}
func copyInventoryFile(ctx context.Context, source, dest, root, rel string) (p InventoryFile, retErr error) {
	f, err := os.Open(source)
	if err != nil {
		return p, err
	}
	defer func() { retErr = errors.Join(retErr, f.Close()) }()
	before, err := f.Stat()
	if err != nil {
		return p, err
	}
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return p, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(backupContextReader{ctx, f}, before.Size()+1))
	err = errors.Join(err, out.Sync(), out.Close())
	if err != nil {
		return p, err
	}
	after, err := f.Stat()
	if err != nil {
		return p, err
	}
	if n != before.Size() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return p, errors.New("backup: source changed during offline capture")
	}
	return InventoryFile{root, rel, hex.EncodeToString(h.Sum(nil)), n}, nil
}
func hashInventoryFile(ctx context.Context, p, root, rel string) (proof InventoryFile, retErr error) {
	f, err := os.Open(p)
	if err != nil {
		return proof, err
	}
	defer func() { retErr = errors.Join(retErr, f.Close()) }()
	h := sha256.New()
	n, err := io.Copy(h, backupContextReader{ctx, f})
	return InventoryFile{root, rel, hex.EncodeToString(h.Sum(nil)), n}, err
}

type backupContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r backupContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func writeVerifiedArchive(ctx context.Context, w io.Writer, stage string, m VerifiedManifest) error {
	body, err := json.Marshal(m)
	if err != nil || len(body) > 16<<20 {
		return errors.New("backup: manifest exceeds limits")
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	write := func() error {
		if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(body))}); err != nil {
			return err
		}
		if _, err := tw.Write(body); err != nil {
			return err
		}
		for _, p := range m.Files {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := writeVerifiedEntry(ctx, tw, filepath.Join(stage, p.Root, filepath.FromSlash(p.Path)), p); err != nil {
				return err
			}
		}
		return nil
	}
	return errors.Join(write(), tw.Close(), gz.Close())
}

// VerifyArchive operates only on fresh temporary resources. It rebases known
// local SQL references and confines verifier file reads to restored os.Roots.
// This is path confinement, not an OS namespace sandbox for malicious SQL.
func VerifyArchive(parent context.Context, input string, o VerifiedOptions) (receipt RestoreReceipt, retErr error) {
	o, err := verifiedDefaults(o)
	if err != nil {
		return receipt, err
	}
	ctx, cancel := context.WithTimeout(parent, o.Timeout)
	defer cancel()
	sandbox, err := os.MkdirTemp("", "levara-verify-")
	if err != nil {
		return receipt, err
	}
	defer os.RemoveAll(sandbox)
	m, manifestHash, archiveHash, err := extractVerified(ctx, input, sandbox, o)
	if err != nil {
		return receipt, err
	}
	artifact := filepath.Join(sandbox, "sql", map[bool]string{true: "sqlite.db", false: "postgres.dump"}[m.DBProvider == "sqlite"])
	o.DBProvider = m.DBProvider
	db, closeDB, err := snapshotSQLForProof(ctx, o, artifact)
	if err != nil {
		return receipt, err
	}
	defer func() { retErr = errors.Join(retErr, closeDB()) }()
	schema, tables, err := sqlProofs(ctx, db, m.DBProvider, m.Roots, o.MaxRows)
	if err != nil {
		return receipt, err
	}
	if schema != m.SQLSchemaSHA256 || !sameJSON(tables, m.Tables) {
		return receipt, errors.New("backup: restored SQL inventory mismatch")
	}
	restoredRoots := map[string]string{}
	for name := range m.Roots {
		restoredRoots[name] = filepath.Join(sandbox, name)
		if err = os.MkdirAll(restoredRoots[name], 0700); err != nil {
			return receipt, err
		}
	}
	if err = rebaseAndCheckReferences(ctx, db, m, sandbox, o.MaxRows); err != nil {
		return receipt, err
	}
	_, after, err := sqlProofs(ctx, db, m.DBProvider, restoredRoots, o.MaxRows)
	if err != nil || !sameJSON(after, m.Tables) {
		return receipt, errors.New("backup: reference rebasing changed logical SQL content")
	}
	indexes, err := verifyIndexes(ctx, sandbox, m, o)
	if err != nil {
		return receipt, err
	}
	if !sameJSON(indexes, m.Indexes) {
		return receipt, errors.New("backup: restored index logical content mismatch")
	}
	if err = verifyNativeRecovery(ctx, sandbox, m, o); err != nil {
		return receipt, err
	}
	receipt = RestoreReceipt{Archive: input, ArchiveSHA256: archiveHash, ManifestSHA256: manifestHash, CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), DBProvider: m.DBProvider, Files: len(m.Files), Tables: len(m.Tables), Indexes: len(indexes), Isolation: "fresh SQL and filesystem resources; known-reference rebasing; os.Root-confined file verification"}
	return receipt, nil
}
func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
func extractVerified(ctx context.Context, input, dest string, o VerifiedOptions) (m VerifiedManifest, manifestHash, archiveHash string, retErr error) {
	// Validate the complete archive once before extracting any payload.
	for pass := 0; pass < 2; pass++ {
		f, err := os.Open(input)
		if err != nil {
			return m, "", "", err
		}
		h := sha256.New()
		compressed := bufio.NewReader(io.TeeReader(backupContextReader{ctx, f}, h))
		gz, err := gzip.NewReader(compressed)
		if err != nil {
			_ = f.Close()
			return m, "", "", err
		}
		gz.Multistream(false)
		tr := tar.NewReader(gz)
		run := func() error {
			hdr, err := tr.Next()
			if err != nil || hdr.Name != "manifest.json" || hdr.Typeflag != tar.TypeReg || hdr.Size < 1 || hdr.Size > 16<<20 {
				return errors.New("backup: manifest must be first bounded regular entry")
			}
			body, err := io.ReadAll(tr)
			if err != nil {
				return err
			}
			var current VerifiedManifest
			d := json.NewDecoder(strings.NewReader(string(body)))
			d.DisallowUnknownFields()
			if err = d.Decode(&current); err != nil {
				return errors.New("backup: invalid manifest")
			}
			if err = d.Decode(new(any)); err != io.EOF {
				return errors.New("backup: trailing manifest data")
			}
			if current.Format != verifiedFormat || current.DBProvider != "sqlite" && current.DBProvider != "postgres" || current.NodeID == "" || current.Dimension < 1 || current.Dimension > 65536 || current.Shards < 1 || current.Shards > 64 || len(current.Roots) != 3 || len(current.Files) < 1 || len(current.Files) > o.MaxFiles {
				return errors.New("backup: unsupported manifest version or layout")
			}
			for _, name := range []string{"data", "workspace", "uploads"} {
				root := current.Roots[name]
				if !filepath.IsAbs(root) || filepath.Clean(root) != root {
					return errors.New("backup: invalid source root mapping")
				}
			}
			wanted := map[string]InventoryFile{}
			var total int64
			for _, p := range current.Files {
				n := p.Root + "/" + p.Path
				clean, err := safeArchiveName(n)
				if err != nil || clean != n || p.Root != "data" && p.Root != "workspace" && p.Root != "uploads" && p.Root != "sql" || p.Path == "" || len(p.SHA256) != 64 || p.Size < 0 || p.Size > maxRestoreEntryBytes || total > o.MaxBytes-p.Size {
					return errors.New("backup: invalid file inventory")
				}
				if _, ok := wanted[n]; ok {
					return errors.New("backup: duplicate file inventory")
				}
				wanted[n] = p
				total += p.Size
			}
			sqlName := "sql/postgres.dump"
			if current.DBProvider == "sqlite" {
				sqlName = "sql/sqlite.db"
			}
			if _, ok := wanted[sqlName]; !ok {
				return errors.New("backup: required SQL artifact missing")
			}
			if err := validateManifestLayout(current, wanted, o); err != nil {
				return err
			}
			if pass == 1 {
				for _, name := range current.Directories {
					if err := os.MkdirAll(filepath.Join(dest, filepath.FromSlash(name)), 0700); err != nil {
						return err
					}
				}
			}
			if pass == 0 {
				m = current
				digest := sha256.Sum256(body)
				manifestHash = hex.EncodeToString(digest[:])
			} else if !sameJSON(m, current) {
				return errors.New("backup: archive changed between validation passes")
			}
			seen := map[string]bool{}
			for {
				hdr, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}
				p, ok := wanted[hdr.Name]
				if !ok || seen[hdr.Name] || hdr.Typeflag != tar.TypeReg || hdr.Size != p.Size {
					return errors.New("backup: archive does not match exact inventory")
				}
				seen[hdr.Name] = true
				sum := sha256.New()
				var writer io.Writer = sum
				var out *os.File
				if pass == 1 {
					target := filepath.Join(dest, filepath.FromSlash(hdr.Name))
					if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
						return err
					}
					out, err = os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
					if err != nil {
						return err
					}
					writer = io.MultiWriter(out, sum)
				}
				n, err := io.Copy(writer, backupContextReader{ctx, tr})
				if out != nil {
					err = errors.Join(err, out.Sync(), out.Close())
				}
				if err != nil {
					return err
				}
				if n != p.Size || hex.EncodeToString(sum.Sum(nil)) != p.SHA256 {
					return errors.New("backup: file checksum mismatch")
				}
			}
			if len(seen) != len(wanted) {
				return errors.New("backup: archive is missing required files")
			}
			tail, err := io.ReadAll(io.LimitReader(gz, 1))
			if err != nil || len(tail) != 0 {
				return errors.New("backup: invalid gzip trailer or trailing payload")
			}
			if _, err := compressed.ReadByte(); err != io.EOF {
				return errors.New("backup: trailing compressed payload")
			}
			return nil
		}()
		closeErr := errors.Join(gz.Close(), f.Close())
		if run != nil || closeErr != nil {
			return m, "", "", errors.Join(run, closeErr)
		}
		sum := hex.EncodeToString(h.Sum(nil))
		if pass == 0 {
			archiveHash = sum
		} else if sum != archiveHash {
			return m, "", "", errors.New("backup: archive changed between passes")
		}
	}
	return m, manifestHash, archiveHash, nil
}

func backupSQLite(ctx context.Context, source, dest string, maxBytes int64) (retErr error) {
	if st, err := os.Lstat(source); err != nil || !st.Mode().IsRegular() {
		return errors.New("backup: SQLite source must be an existing regular file")
	}
	u := url.URL{Scheme: "file", Path: source}
	db, err := sql.Open("sqlite3", u.String()+"?mode=ro")
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, db.Close()) }()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, conn.Close()) }()
	var pages, pageSize int64
	if err = conn.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return err
	}
	if err = conn.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return err
	}
	if pages < 1 || pageSize < 1 || pages > maxBytes/pageSize {
		return errors.New("backup: SQLite snapshot exceeds byte budget")
	}
	return conn.Raw(func(raw any) error {
		c := raw.(sqlitedriver.Conn).Raw()
		b, err := c.BackupInit("main", dest)
		if err != nil {
			return err
		}
		var runErr error
		for {
			if runErr = ctx.Err(); runErr != nil {
				break
			}
			done, err := b.Step(256)
			if err != nil {
				runErr = err
				break
			}
			if done {
				break
			}
		}
		return errors.Join(runErr, b.Close())
	})
}
func pgCommand(ctx context.Context, bin, name, dsn string, extra ...string) error {
	args, password, err := parseDSNToArgs(dsn)
	if err != nil {
		return err
	}
	args = append(args, extra...)
	exe := name
	if bin != "" {
		exe = filepath.Join(bin, name)
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	setPgPassword(cmd, password)
	if err = cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("backup: %s failed: %w", name, err)
	}
	return nil
}
func snapshotSQLForProof(ctx context.Context, o VerifiedOptions, artifact string) (*sql.DB, func() error, error) {
	if o.DBProvider == "sqlite" {
		u := url.URL{Scheme: "file", Path: artifact}
		db, err := sql.Open("sqlite3", u.String())
		if err != nil {
			return nil, nil, err
		}
		db.SetMaxOpenConns(1)
		return db, db.Close, nil
	}
	root, err := os.MkdirTemp("/tmp", "lvpg-")
	if err != nil {
		return nil, nil, err
	}
	data := filepath.Join(root, "db")
	socket := filepath.Join(root, "socket")
	if err = os.Mkdir(socket, 0700); err != nil {
		_ = os.RemoveAll(root)
		return nil, nil, err
	}
	run := func(c context.Context, name string, args ...string) error {
		exe := name
		if o.PostgresBinDir != "" {
			exe = filepath.Join(o.PostgresBinDir, name)
		}
		cmd := exec.CommandContext(c, exe, args...)
		if err := cmd.Run(); err != nil {
			if c.Err() != nil {
				return c.Err()
			}
			return fmt.Errorf("backup: private PostgreSQL %s failed", name)
		}
		return nil
	}
	cleanup := func() error {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		err := run(c, "pg_ctl", "-D", data, "-m", "immediate", "-w", "stop")
		if err == nil {
			err = os.RemoveAll(root)
		}
		return err
	}
	if err = run(ctx, "initdb", "-D", data, "-A", "trust", "-U", "backup_admin", "--no-instructions"); err != nil {
		_ = os.RemoveAll(root)
		return nil, nil, err
	}
	if err = run(ctx, "pg_ctl", "-D", data, "-l", filepath.Join(root, "postgres.log"), "-o", "-c listen_addresses='' -k "+socket, "-w", "start"); err != nil {
		_ = cleanup()
		return nil, nil, err
	}
	dsn := func(user, database string) string {
		return "postgres://" + user + "@/" + database + "?host=" + url.QueryEscape(socket) + "&sslmode=disable"
	}
	for _, statement := range []string{"CREATE ROLE backup_verifier LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION", "CREATE DATABASE verify OWNER backup_verifier"} {
		if err = pgCommand(ctx, o.PostgresBinDir, "psql", dsn("backup_admin", "postgres"), "--no-psqlrc", "--set=ON_ERROR_STOP=1", "-c", statement); err != nil {
			_ = cleanup()
			return nil, nil, err
		}
	}
	restoreDSN := dsn("backup_verifier", "verify")
	if err = pgCommand(ctx, o.PostgresBinDir, "pg_restore", restoreDSN, "--single-transaction", "--exit-on-error", "--no-owner", "--no-acl", artifact); err != nil {
		_ = cleanup()
		return nil, nil, err
	}
	db, err := sql.Open("pgx", restoreDSN)
	if err != nil {
		_ = cleanup()
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	return db, func() error { return errors.Join(db.Close(), cleanup()) }, nil
}
func sqlProofs(ctx context.Context, db *sql.DB, provider string, roots map[string]string, maxRows int) (string, map[string]SQLTableProof, error) {
	tableQuery := "SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name"
	schemaQuery := "SELECT type,name,tbl_name,COALESCE(sql,'') FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name"
	if provider == "postgres" {
		tableQuery = "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename"
		schemaQuery = "SELECT table_name,column_name,data_type,is_nullable,COALESCE(column_default,'') FROM information_schema.columns WHERE table_schema='public' ORDER BY table_name,ordinal_position"
		if _, err := db.ExecContext(ctx, "SET TIME ZONE 'UTC'"); err != nil {
			return "", nil, err
		}
	}
	if provider == "postgres" {
		var outside int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT IN ('public','pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND n.nspname NOT LIKE 'pg_temp%' AND c.relkind IN ('r','p','v','m','S','f')").Scan(&outside); err != nil {
			return "", nil, err
		}
		if outside != 0 {
			return "", nil, errors.New("backup: only public PostgreSQL schema is supported")
		}
		schemaQuery = `SELECT 'column' AS kind, table_name||'.'||column_name AS name, data_type||':'||is_nullable||':'||COALESCE(column_default,'') AS definition FROM information_schema.columns WHERE table_schema='public'
 UNION ALL SELECT 'index',indexname,indexdef FROM pg_indexes WHERE schemaname='public'
 UNION ALL SELECT 'constraint',c.conname||':'||c.conrelid::regclass::text,pg_get_constraintdef(c.oid)||':'||c.convalidated::text FROM pg_constraint c JOIN pg_namespace n ON n.oid=c.connamespace WHERE n.nspname='public'
 UNION ALL SELECT 'view',viewname,definition FROM pg_views WHERE schemaname='public'
 UNION ALL SELECT 'trigger',t.tgname||':'||t.tgrelid::regclass::text,pg_get_triggerdef(t.oid) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND NOT t.tgisinternal
 UNION ALL SELECT 'sequence',sequencename,start_value::text||':'||min_value::text||':'||max_value::text||':'||increment_by::text||':'||cycle::text||':'||COALESCE(last_value::text,'') FROM pg_sequences WHERE schemaname='public'
 UNION ALL SELECT 'routine',p.proname||':'||pg_get_function_identity_arguments(p.oid),pg_get_functiondef(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.prokind IN ('f','p')`
	}
	schema, _, err := hashSQLRows(ctx, db, schemaQuery, "", roots, maxRows)
	if err != nil {
		return "", nil, err
	}
	rows, err := db.QueryContext(ctx, tableQuery)
	if err != nil {
		return "", nil, err
	}
	var names []string
	for rows.Next() {
		var n string
		if err = rows.Scan(&n); err != nil {
			break
		}
		names = append(names, n)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return "", nil, err
	}
	tables := map[string]SQLTableProof{}
	for _, name := range names {
		if err := checkSQLRowBudget(ctx, db, provider, name); err != nil {
			return "", nil, err
		}
		hash, p, err := hashSQLRows(ctx, db, "SELECT * FROM "+quoteSQL(name), name, roots, maxRows)
		if err != nil {
			return "", nil, err
		}
		p.SHA256 = hash
		tables[name] = p
	}
	if provider == "sqlite" {
		r, err := db.QueryContext(ctx, "PRAGMA integrity_check")
		if err != nil {
			return "", nil, err
		}
		for r.Next() {
			var status string
			if err = r.Scan(&status); err != nil || status != "ok" {
				_ = r.Close()
				return "", nil, errors.New("backup: SQLite integrity check failed")
			}
		}
		err = errors.Join(r.Err(), r.Close())
		if err != nil {
			return "", nil, err
		}
		r, err = db.QueryContext(ctx, "PRAGMA foreign_key_check")
		if err != nil {
			return "", nil, err
		}
		bad := r.Next()
		err = errors.Join(r.Err(), r.Close())
		if err != nil || bad {
			return "", nil, errors.New("backup: SQLite foreign key check failed")
		}
	}
	return schema, tables, nil
}
func quoteSQL(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
func hashSQLRows(ctx context.Context, db *sql.DB, query, table string, roots map[string]string, maxRows int) (string, SQLTableProof, error) {
	var proof SQLTableProof
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return "", proof, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return "", proof, err
	}
	proof.Columns = columns
	var hashes []string
	for rows.Next() {
		if proof.Rows >= maxRows {
			return "", proof, errors.New("backup: SQL row budget exceeded")
		}
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err = rows.Scan(pointers...); err != nil {
			return "", proof, err
		}
		canonical := make([]any, len(values))
		for i, v := range values {
			if isReferenceColumn(table, columns[i]) && v != nil {
				loc := fmt.Sprint(v)
				if b, ok := v.([]byte); ok {
					loc = string(b)
				}
				if loc != "" {
					name, rel, err := resolveReference(loc, roots)
					if err != nil {
						return "", proof, err
					}
					v = name + "/" + rel
				}
			}
			switch x := v.(type) {
			case []byte:
				canonical[i] = []any{"bytes", hex.EncodeToString(x)}
			case time.Time:
				canonical[i] = []any{"time", x.UTC().Format(time.RFC3339Nano)}
			default:
				canonical[i] = []any{fmt.Sprintf("%T", v), v}
			}
		}
		body, err := json.Marshal(canonical)
		if err != nil {
			return "", proof, err
		}
		sum := sha256.Sum256(body)
		hashes = append(hashes, hex.EncodeToString(sum[:]))
		proof.Rows++
	}
	if err = rows.Err(); err != nil {
		return "", proof, err
	}
	sort.Strings(hashes)
	h := sha256.New()
	for _, v := range hashes {
		_, _ = io.WriteString(h, v)
	}
	return hex.EncodeToString(h.Sum(nil)), proof, nil
}
func isReferenceColumn(table, column string) bool {
	return table == "data" && (column == "raw_data_location" || column == "original_data_location") ||
		table == "document_structured_artifacts" && column == "storage_location" ||
		table == "ontologies" && column == "file_path"
}
func resolveReference(location string, roots map[string]string) (string, string, error) {
	p := location
	if strings.HasPrefix(p, "file://") {
		p = strings.TrimPrefix(p, "file://")
	} else if strings.Contains(p, "://") {
		return "", "", errors.New("backup: unsupported external artifact reference")
	}
	if !filepath.IsAbs(p) {
		return "", "", errors.New("backup: relative artifact reference cannot be restored unambiguously")
	}
	// Prefer dedicated roots over data when paths overlap.
	for _, name := range []string{"workspace", "uploads", "data"} {
		root := roots[name]
		if root != "" && within(root, p) {
			rel, _ := filepath.Rel(root, p)
			if rel == "." {
				return "", "", errors.New("backup: artifact reference names a directory")
			}
			return name, filepath.ToSlash(rel), nil
		}
	}
	return "", "", errors.New("backup: artifact reference is outside inventory roots")
}
func rebaseAndCheckReferences(ctx context.Context, db *sql.DB, m VerifiedManifest, sandbox string, maxRows int) (retErr error) {
	inventory := map[string]InventoryFile{}
	for _, p := range m.Files {
		inventory[p.Root+"/"+p.Path] = p
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for table, proof := range m.Tables {
		for _, column := range proof.Columns {
			if !isReferenceColumn(table, column) {
				continue
			}
			rows, err := tx.QueryContext(ctx, "SELECT DISTINCT "+quoteSQL(column)+" FROM "+quoteSQL(table)+" WHERE "+quoteSQL(column)+" IS NOT NULL AND "+quoteSQL(column)+" <> ''")
			if err != nil {
				return err
			}
			var locations []string
			for rows.Next() {
				var loc string
				if err = rows.Scan(&loc); err != nil {
					break
				}
				locations = append(locations, loc)
				if len(locations) > maxRows {
					err = errors.New("backup: reference budget exceeded")
					break
				}
			}
			err = errors.Join(err, rows.Err(), rows.Close())
			if err != nil {
				return err
			}
			for _, loc := range locations {
				name, rel, err := resolveReference(loc, m.Roots)
				if err != nil {
					return err
				}
				want, ok := inventory[name+"/"+rel]
				if !ok {
					return errors.New("backup: referenced artifact missing from inventory")
				}
				root, err := os.OpenRoot(filepath.Join(sandbox, name))
				if err != nil {
					return err
				}
				f, err := root.Open(filepath.FromSlash(rel))
				if err != nil {
					_ = root.Close()
					return err
				}
				sum := sha256.New()
				n, err := io.Copy(sum, backupContextReader{ctx, f})
				err = errors.Join(err, f.Close(), root.Close())
				if err != nil {
					return err
				}
				if n != want.Size || hex.EncodeToString(sum.Sum(nil)) != want.SHA256 {
					return errors.New("backup: referenced artifact failed verification")
				}
				replacement := filepath.Join(sandbox, name, filepath.FromSlash(rel))
				if strings.HasPrefix(loc, "file://") {
					replacement = "file://" + replacement
				}
				query := "UPDATE " + quoteSQL(table) + " SET " + quoteSQL(column) + "=$1 WHERE " + quoteSQL(column) + "=$2"
				if _, err = tx.ExecContext(ctx, query, replacement, loc); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}

func verifyIndexes(ctx context.Context, stage string, m VerifiedManifest, o VerifiedOptions) (map[string]IndexProof, error) {
	result := map[string]IndexProof{}
	workspaceRefs, err := verifyStructuredArtifacts(ctx, stage, m, o)
	if err != nil {
		return nil, err
	}
	for _, entry := range m.Files {
		if entry.Root != "data" {
			continue
		}
		name := entry.Root + "/" + entry.Path
		file := filepath.Join(stage, entry.Root, filepath.FromSlash(entry.Path))
		if strings.HasSuffix(entry.Path, "/meta.bin.wal") {
			if entry.Size > 256<<20 {
				return nil, errors.New("backup: native index replay exceeds 256 MiB budget")
			}
			dimension := m.Dimension
			metaPath := filepath.Join(filepath.Dir(file), "collection_meta.json")
			if st, err := os.Stat(metaPath); err == nil && st.Size() > 1<<20 {
				return nil, errors.New("backup: collection metadata exceeds validation budget")
			}
			if data, err := os.ReadFile(metaPath); err == nil {
				var meta store.CollectionMeta
				if json.Unmarshal(data, &meta) != nil || meta.EmbeddingDim < 1 {
					return nil, errors.New("backup: invalid collection metadata")
				}
				dimension = meta.EmbeddingDim
			} else if !os.IsNotExist(err) {
				return nil, err
			}
			f, err := os.Open(file)
			if err != nil {
				return nil, err
			}
			state := map[string]string{}
			identityBytes := 0
			operations := 0
			err = store.WalkWALStrict(ctx, f, dimension, entry.Size, func(op byte, r store.SnapshotRecord) error {
				operations++
				if operations > o.MaxRows {
					return errors.New("backup: WAL operation budget exceeded")
				}
				if op == store.OpDelete {
					if _, ok := state[r.ID]; ok {
						identityBytes -= len(r.ID)
					}
					delete(state, r.ID)
					return nil
				}
				if _, ok := state[r.ID]; !ok {
					identityBytes += len(r.ID)
				}
				if _, exists := state[r.ID]; !exists && len(state) >= o.MaxRows || identityBytes > 16<<20 {
					return errors.New("backup: index identity budget exceeded")
				}
				body, err := json.Marshal(r)
				if err != nil {
					return err
				}
				sum := sha256.Sum256(body)
				state[r.ID] = hex.EncodeToString(sum[:])
				return nil
			})
			err = errors.Join(err, f.Close())
			if err != nil {
				return nil, err
			}
			for id := range workspaceRefs[name] {
				if _, ok := state[id]; !ok {
					return nil, errors.New("backup: active workspace vector missing from WAL")
				}
				delete(workspaceRefs[name], id)
			}
			keys := make([]string, 0, len(state))
			for id := range state {
				keys = append(keys, id)
			}
			sort.Strings(keys)
			h := sha256.New()
			for _, id := range keys {
				_, _ = io.WriteString(h, state[id])
			}
			result[name] = IndexProof{len(state), hex.EncodeToString(h.Sum(nil))}
		} else if strings.HasPrefix(entry.Path, "bm25/") && strings.HasSuffix(entry.Path, ".jsonl") {
			if entry.Size > 16<<20 {
				return nil, errors.New("backup: lexical verification byte budget exceeded")
			}
			f, err := os.Open(file)
			if err != nil {
				return nil, err
			}
			idx, err := bm25.LoadSnapshotStrict(ctx, f, entry.Size, o.MaxRows)
			err = errors.Join(err, f.Close())
			if err != nil {
				return nil, err
			}
			docs := idx.Documents()
			sort.Slice(docs, func(i, j int) bool { return docs[i].ID < docs[j].ID })
			body, _ := json.Marshal(docs)
			sum := sha256.Sum256(body)
			result[name] = IndexProof{len(docs), hex.EncodeToString(sum[:])}
		}
	}
	for i := 0; i < m.Shards; i++ {
		name := "data/" + m.NodeID + fmt.Sprintf("/shard_%d/meta.bin.wal", i)
		if _, ok := result[name]; !ok {
			return nil, errors.New("backup: required shard WAL missing")
		}
	}
	for _, ids := range workspaceRefs {
		if len(ids) != 0 {
			return nil, errors.New("backup: workspace index collection missing")
		}
	}
	return result, nil
}
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
func writeReceipt(dir string, r RestoreReceipt) (retErr error) {
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".restore-receipt-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(body)
	err = errors.Join(err, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(dir, "last_successful_restore.json")); err != nil {
		return err
	}
	return syncDir(dir)
}
func ReadLastSuccessfulRestore(dir string) (RestoreReceipt, error) {
	var r RestoreReceipt
	f, err := os.Open(filepath.Join(dir, "last_successful_restore.json"))
	if err != nil {
		return r, err
	}
	body, err := io.ReadAll(io.LimitReader(f, 64<<10+1))
	err = errors.Join(err, f.Close())
	if err != nil {
		return r, err
	}
	if len(body) > 64<<10 {
		return r, errors.New("backup: receipt exceeds limit")
	}
	err = json.Unmarshal(body, &r)
	return r, err
}

func validateManifestLayout(m VerifiedManifest, wanted map[string]InventoryFile, o VerifiedOptions) error {
	if filepath.Base(m.NodeID) != m.NodeID || m.NodeID == "." || m.NodeID == ".." || strings.ContainsAny(m.NodeID, "/\\") || len(m.Directories) > o.MaxFiles {
		return errors.New("backup: invalid directory inventory")
	}
	dirs := map[string]bool{"data": true, "workspace": true, "uploads": true, "sql": true}
	for _, name := range m.Directories {
		clean, err := safeArchiveName(name)
		first, _, _ := strings.Cut(name, "/")
		if err != nil || clean != name || first != "data" && first != "workspace" && first != "uploads" || dirs[name] {
			return errors.New("backup: invalid directory inventory")
		}
		if _, exists := wanted[name]; exists {
			return errors.New("backup: file/directory inventory collision")
		}
		dirs[name] = true
	}
	for name, p := range wanted {
		if _, err := hex.DecodeString(p.SHA256); err != nil {
			return errors.New("backup: invalid checksum encoding")
		}
		if !dirs[filepath.ToSlash(filepath.Dir(name))] {
			return errors.New("backup: missing parent directory inventory")
		}
	}
	for name := range dirs {
		if strings.Contains(name, "/") && !dirs[filepath.ToSlash(filepath.Dir(name))] {
			return errors.New("backup: missing directory ancestor")
		}
	}
	if !dirs["data/"+m.NodeID+"/collections"] {
		return errors.New("backup: collection inventory required")
	}
	for i := 0; i < m.Shards; i++ {
		if _, ok := wanted[fmt.Sprintf("data/%s/shard_%d/meta.bin.wal", m.NodeID, i)]; !ok {
			return errors.New("backup: shard inventory missing")
		}
	}
	for name := range dirs {
		prefix := "data/" + m.NodeID + "/collections/"
		if strings.HasPrefix(name, prefix) && !strings.Contains(strings.TrimPrefix(name, prefix), "/") {
			for _, base := range []string{"collection_meta.json", "meta.bin.wal"} {
				if _, ok := wanted[name+"/"+base]; !ok {
					return errors.New("backup: incomplete collection inventory")
				}
			}
		}
	}
	return nil
}

// Replay only already validated, restored WALs. A tolerant runtime loader must
// produce exactly the same logical records as the strict preflight decoder.
func verifyNativeRecovery(ctx context.Context, sandbox string, m VerifiedManifest, o VerifiedOptions) error {
	for name, want := range m.Indexes {
		if err := ctx.Err(); err != nil {
			return err
		}
		file := filepath.Join(sandbox, filepath.FromSlash(name))
		if strings.HasSuffix(name, "/meta.bin.wal") {
			dim := m.Dimension
			if body, err := os.ReadFile(filepath.Join(filepath.Dir(file), "collection_meta.json")); err == nil {
				var meta store.CollectionMeta
				if err = json.Unmarshal(body, &meta); err != nil {
					return err
				}
				dim = meta.EmbeddingDim
			}
			recovered, err := store.NewLevara(dim, strings.TrimSuffix(file, ".wal"))
			if err != nil {
				return err
			}
			check := func() error {
				ids := recovered.AllIDs()
				if len(ids) != want.Records || len(ids) > o.MaxRows {
					return errors.New("backup: native WAL recovery record count mismatch")
				}
				sort.Strings(ids)
				h := sha256.New()
				for _, id := range ids {
					if err := ctx.Err(); err != nil {
						return err
					}
					vector, data, ok := recovered.Get(id)
					if !ok || data == nil {
						return errors.New("backup: native WAL recovery could not read record")
					}
					body, err := json.Marshal(store.SnapshotRecord{ID: id, Vector: vector, Data: data})
					if err != nil {
						return err
					}
					sum := sha256.Sum256(body)
					_, _ = io.WriteString(h, hex.EncodeToString(sum[:]))
				}
				if hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
					return errors.New("backup: native WAL recovery logical mismatch")
				}
				return nil
			}
			if err = errors.Join(check(), recovered.Close()); err != nil {
				return err
			}
		} else if strings.HasSuffix(name, ".jsonl") {
			recovered, err := bm25.LoadSnapshot(file)
			if err != nil {
				return err
			}
			docs := recovered.Documents()
			sort.Slice(docs, func(i, j int) bool { return docs[i].ID < docs[j].ID })
			body, err := json.Marshal(docs)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(body)
			if len(docs) != want.Records || hex.EncodeToString(sum[:]) != want.SHA256 {
				return errors.New("backup: native lexical recovery mismatch")
			}
		}
	}
	return ctx.Err()
}

func verifyStructuredArtifacts(ctx context.Context, stage string, m VerifiedManifest, o VerifiedOptions) (map[string]map[string]bool, error) {
	inventory := map[string]InventoryFile{}
	for _, f := range m.Files {
		inventory[f.Root+"/"+f.Path] = f
	}
	references := map[string]map[string]bool{}
	count := 0
	for _, f := range m.Files {
		structured := f.Root == "uploads" && (strings.HasPrefix(f.Path, "structured_extractions/") && strings.HasSuffix(f.Path, ".json") ||
			strings.HasPrefix(f.Path, ingestAuthorizedBackupPrefix) && strings.HasSuffix(f.Path, "-structured"))
		workspaceManifest := f.Root == "workspace" && strings.HasPrefix(f.Path, ".kb/manifests/") && strings.HasSuffix(f.Path, ".json")
		if !structured && !workspaceManifest {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if f.Size > 16<<20 {
			return nil, errors.New("backup: structured artifact exceeds validation budget")
		}
		root, err := os.OpenRoot(filepath.Join(stage, f.Root))
		if err != nil {
			return nil, err
		}
		body, err := root.ReadFile(filepath.FromSlash(f.Path))
		err = errors.Join(err, root.Close())
		if err != nil {
			return nil, err
		}
		if !json.Valid(body) {
			return nil, errors.New("backup: malformed structured artifact")
		}
		if !workspaceManifest {
			continue
		}
		var wm workspace.Manifest
		if err = json.Unmarshal(body, &wm); err != nil {
			return nil, err
		}
		if wm.Version != workspace.ManifestVersion || wm.ProjectID == "" || wm.Branch == "" {
			return nil, errors.New("backup: invalid workspace manifest")
		}
		if wm.ActiveGeneration == "" {
			continue
		}
		gen, ok := wm.Generations[wm.ActiveGeneration]
		if !ok || gen.Status != workspace.GenerationActive {
			return nil, errors.New("backup: invalid active workspace generation")
		}
		for _, chunk := range wm.Chunks {
			if chunk.Generation != wm.ActiveGeneration {
				continue
			}
			count++
			if count > o.MaxRows {
				return nil, errors.New("backup: workspace chunk budget exceeded")
			}
			if chunk.ProjectID != wm.ProjectID || chunk.Branch != wm.Branch || chunk.Collection == "" || chunk.VectorID == "" {
				return nil, errors.New("backup: inconsistent active workspace chunk")
			}
			rel, err := safeArchiveName(chunk.Path)
			if err != nil || rel != chunk.Path {
				return nil, errors.New("backup: invalid workspace chunk path")
			}
			project := workspace.ProjectRoot("", wm.ProjectID, wm.Branch)
			key := "workspace/" + filepath.ToSlash(filepath.Join(project, filepath.FromSlash(rel)))
			file, ok := inventory[key]
			if !ok || chunk.FileDigest != "sha256:"+file.SHA256 {
				return nil, errors.New("backup: active workspace file digest mismatch")
			}
			if filepath.Base(chunk.Collection) != chunk.Collection || strings.ContainsAny(chunk.Collection, "/\\") {
				return nil, errors.New("backup: unsupported workspace collection path")
			}
			wal := "data/" + m.NodeID + "/collections/" + chunk.Collection + "/meta.bin.wal"
			if references[wal] == nil {
				references[wal] = map[string]bool{}
			}
			references[wal][chunk.VectorID] = true
		}
	}
	return references, nil
}

func writeVerifiedEntry(ctx context.Context, tw *tar.Writer, file string, p InventoryFile) (retErr error) {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, f.Close()) }()
	if err = tw.WriteHeader(&tar.Header{Name: p.Root + "/" + p.Path, Mode: 0600, Size: p.Size, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	n, err := io.Copy(tw, backupContextReader{ctx, f})
	if err != nil {
		return err
	}
	if n != p.Size {
		return errors.New("backup: staged file changed")
	}
	return nil
}

type backupBudgetWriter struct {
	w         io.Writer
	remaining int64
}

func (w *backupBudgetWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errors.New("backup: SQL snapshot exceeds byte budget")
	}
	n, err := w.w.Write(p)
	w.remaining -= int64(n)
	return n, err
}
func dumpVerifiedPostgres(ctx context.Context, o VerifiedOptions, dest string) error {
	args, password, err := parseDSNToArgs(o.PostgresDSN)
	if err != nil {
		return err
	}
	args = append(args, "--format=custom", "--compress=0", "--no-owner", "--no-acl")
	exe := "pg_dump"
	if o.PostgresBinDir != "" {
		exe = filepath.Join(o.PostgresBinDir, exe)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	setPgPassword(cmd, password)
	cmd.Stdout = &backupBudgetWriter{w: f, remaining: o.MaxBytes}
	runErr := cmd.Run()
	if runErr != nil {
		if ctx.Err() != nil {
			runErr = ctx.Err()
		} else {
			runErr = errors.New("backup: pg_dump failed or exceeded output budget")
		}
	}
	return errors.Join(runErr, f.Sync(), f.Close())
}

// Check sizes in the database before materializing values in the verifier.
func checkSQLRowBudget(ctx context.Context, db *sql.DB, provider, table string) error {
	r, err := db.QueryContext(ctx, "SELECT * FROM "+quoteSQL(table)+" LIMIT 0")
	if err != nil {
		return err
	}
	cols, err := r.Columns()
	err = errors.Join(err, r.Close())
	if err != nil {
		return err
	}
	if len(cols) > 512 {
		return errors.New("backup: SQL column budget exceeded")
	}
	parts := make([]string, len(cols))
	for i, col := range cols {
		if provider == "postgres" {
			parts[i] = "COALESCE(octet_length(" + quoteSQL(col) + "::text),0)"
		} else {
			parts[i] = "COALESCE(length(CAST(" + quoteSQL(col) + " AS BLOB)),0)"
		}
	}
	if len(parts) == 0 {
		return nil
	}
	var bad int
	err = db.QueryRowContext(ctx, "SELECT 1 FROM "+quoteSQL(table)+" WHERE ("+strings.Join(parts, "+")+") > 16777216 LIMIT 1").Scan(&bad)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("backup: SQL row byte budget exceeded")
}
