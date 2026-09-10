package backup

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func fakePostgres(t *testing.T, name, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nset -eu\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestFullBackupFailurePreservesArchive(t *testing.T) {
	fakePostgres(t, "pg_dump", "echo database-failed >&2\nexit 23\n")
	for _, failure := range []string{"dump", "missing-data", "cache-directory"} {
		t.Run(failure, func(t *testing.T) {
			dataDir, destDir := t.TempDir(), t.TempDir()
			dsn := ""
			switch failure {
			case "dump":
				dsn = "host=localhost dbname=test"
			case "missing-data":
				dataDir = filepath.Join(dataDir, "missing")
			case "cache-directory":
				if err := os.Mkdir(filepath.Join(dataDir, "llm_cache.jsonl"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			output := filepath.Join(destDir, "backup.tar.gz")
			if err := os.WriteFile(output, []byte("previous archive"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := FullBackup(dataDir, dsn, output); err == nil {
				t.Error("FullBackup accepted a required-component failure")
			}
			if data, err := os.ReadFile(output); err != nil || string(data) != "previous archive" {
				t.Errorf("previous archive was changed (%d bytes): %v", len(data), err)
			}
			entries, err := os.ReadDir(destDir)
			if err != nil || len(entries) != 1 {
				t.Errorf("staging file leaked: %v, %v", entries, err)
			}
		})
	}
}

func TestFullRestoreReturnsDatabaseFailure(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)
	fakePostgres(t, "psql", "echo database-failed >&2\nexit 23\n")
	archive := writeRestoreArchive(t, restoreTestEntry{name: "db.sql", body: "SELECT 1;"})
	if err := FullRestore(archive, t.TempDir(), "host=localhost dbname=test"); err == nil {
		t.Fatal("FullRestore accepted a psql failure")
	}
	if entries, err := os.ReadDir(tmpDir); err != nil || len(entries) != 0 {
		t.Errorf("temporary restore dump leaked: %v, %v", entries, err)
	}
}

func TestFullRestoreRejectsMissingOrDuplicateDatabase(t *testing.T) {
	called := filepath.Join(t.TempDir(), "called")
	t.Setenv("BACKUP_TEST_CALLED", called)
	fakePostgres(t, "psql", "touch \"$BACKUP_TEST_CALLED\"\n")
	for _, entries := range [][]restoreTestEntry{
		{{name: "manifest.json", body: "{}"}},
		{{name: "db.sql", body: "SELECT 1;"}, {name: "db.sql", body: "SELECT 2;"}},
	} {
		archive := writeRestoreArchive(t, entries...)
		if err := FullRestore(archive, t.TempDir(), "host=localhost dbname=test"); err == nil {
			t.Error("FullRestore accepted missing or duplicated database dump")
		}
	}
	if _, err := os.Stat(called); !os.IsNotExist(err) {
		t.Error("psql ran for an invalid database archive")
	}
}

func TestFullBackupPublicationFailureCleansStaging(t *testing.T) {
	destDir := t.TempDir()
	output := filepath.Join(destDir, "existing-directory")
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	if err := FullBackup(t.TempDir(), "", output); err == nil {
		t.Fatal("FullBackup ignored publication failure")
	}
	if entries, err := os.ReadDir(destDir); err != nil || len(entries) != 1 || !entries[0].IsDir() {
		t.Errorf("publication failure changed target or leaked staging: %v, %v", entries, err)
	}
}

type archiveFailureWriter struct {
	calls int
	err   error
}

func (w *archiveFailureWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls > 1 { // Accept the gzip header, fail when its buffered body is closed.
		return 0, w.err
	}
	return len(p), nil
}

func TestBackupReturnsArchiveFinalizationFailure(t *testing.T) {
	failure := errors.New("injected archive write failure")
	w := &archiveFailureWriter{err: failure}
	if err := writeBackupArchive(w, t.TempDir(), ""); !errors.Is(err, failure) {
		t.Fatalf("archive finalization error = %v, want injected failure", err)
	}
}

func TestPostgresErrorsDoNotEchoCredentials(t *testing.T) {
	for _, command := range []string{"pg_dump", "psql"} {
		fakePostgres(t, command, "printf '%s' \"$PGPASSWORD\" >&2\nexit 23\n")
	}
	for _, call := range []func(string, string) error{PgDump, PgRestore} {
		err := call("postgres://alice:do-not-echo-me@localhost/test", "unused.sql")
		if err == nil || strings.Contains(err.Error(), "do-not-echo-me") {
			t.Errorf("client error leaked credentials or disappeared: %v", err)
		}
		err = call("postgres://alice:do-not-echo-me%zz@localhost/test", "unused.sql")
		if err == nil || strings.Contains(err.Error(), "do-not-echo-me") {
			t.Errorf("URI parser error leaked credentials or disappeared: %v", err)
		}
	}
}

func TestFullRestoreChecksGzipBeforeDatabase(t *testing.T) {
	called := filepath.Join(t.TempDir(), "called")
	t.Setenv("BACKUP_TEST_CALLED", called)
	fakePostgres(t, "psql", "touch \"$BACKUP_TEST_CALLED\"\n")
	archive := writeRestoreArchive(t, restoreTestEntry{name: "db.sql", body: "SELECT 1;"})
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-8] ^= 0xff // Corrupt the gzip CRC after the tar end marker.
	if err := os.WriteFile(archive, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := FullRestore(archive, t.TempDir(), "host=localhost dbname=test"); err == nil {
		t.Error("FullRestore accepted an invalid gzip checksum")
	}
	if _, err := os.Stat(called); !os.IsNotExist(err) {
		t.Error("psql ran before the complete archive was validated")
	}
}

func TestPostgresCommandsKeepURICredentialsOutOfArgv(t *testing.T) {
	for _, command := range []string{"pg_dump", "psql"} {
		t.Run(command, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "args")
			t.Setenv("BACKUP_TEST_ARGS", capture)
			fakePostgres(t, command, "printf '%s\\n' \"$@\" > \"$BACKUP_TEST_ARGS\"\nprintf '%s' \"${PGPASSWORD-}\" > \"$BACKUP_TEST_ARGS.password\"\n")
			dsn := "postgres://alice:s%20ecret@localhost:5432/test?sslmode=verify-full&application_name=backup"
			var err error
			if command == "pg_dump" {
				err = PgDump(dsn, filepath.Join(t.TempDir(), "dump.sql"))
			} else {
				err = PgRestore(dsn, filepath.Join(t.TempDir(), "dump.sql"))
			}
			if err != nil {
				t.Fatal(err)
			}
			argv, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(argv), "s%20ecret") || strings.Contains(string(argv), "s ecret") {
				t.Error("URI password is present in process arguments")
			}
			password, err := os.ReadFile(capture + ".password")
			if err != nil || string(password) != "s ecret" {
				t.Errorf("PGPASSWORD was not preserved: %q, %v", password, err)
			}
			if !strings.Contains(string(argv), "sslmode=verify-full") || !strings.Contains(string(argv), "application_name=backup") {
				t.Errorf("connection options lost: %s", argv)
			}
			if command == "psql" && (!strings.Contains(string(argv), "ON_ERROR_STOP=1") || !strings.Contains(string(argv), "--single-transaction") || !strings.Contains(string(argv), "--no-psqlrc")) {
				t.Errorf("restore does not fail atomically on SQL errors: %s", argv)
			}
		})
	}
}

func TestPostgresCommandsPreserveLibpqAuthorities(t *testing.T) {
	cases := []struct{ authority, userinfo, password string }{
		{"%2Fvar%2Flib%2Fpostgresql", "", ""},
		{"%2Fvar%2Flib%2Fpostgresql", "alice:p%40ss@", "p@ss"},
		{"host1:5433,host2", "alice:p%40ss@", "p@ss"},
		{"host1,host2:5433", "alice:p%40ss@", "p@ss"},
		{"[::1]:5433,host2,[::2]", "alice:p%40ss@", "p@ss"},
	}
	for _, command := range []string{"pg_dump", "psql"} {
		t.Run(command, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "args")
			t.Setenv("BACKUP_TEST_ARGS", capture)
			t.Setenv("PGPASSWORD", "")
			fakePostgres(t, command, "printf '%s\\n' \"$@\" > \"$BACKUP_TEST_ARGS\"\nprintf '%s' \"${PGPASSWORD-}\" > \"$BACKUP_TEST_ARGS.password\"\n")
			for _, tc := range cases {
				dsn := "postgresql://" + tc.userinfo + tc.authority + "/dbname?sslmode=prefer&target_session_attrs=read-write&application_name=backup%20test"
				call := PgDump
				if command == "psql" {
					call = PgRestore
				}
				if err := call(dsn, "unused.sql"); err != nil {
					t.Errorf("libpq authority %q rejected: %v", tc.authority, err)
					continue
				}
				argv, err := os.ReadFile(capture)
				if err != nil {
					t.Fatal(err)
				}
				want := strings.Replace(dsn, ":p%40ss@", "@", 1)
				if !strings.HasPrefix(string(argv), "--dbname\n"+want+"\n") {
					t.Errorf("libpq URI changed: got %q, want %q", argv, want)
				}
				password, err := os.ReadFile(capture + ".password")
				if err != nil || string(password) != tc.password {
					t.Errorf("password did not reach environment: %v", err)
				}
			}
		})
	}
}

func TestFullBackupConcurrentTemporaryIsolation(t *testing.T) {
	tmpDir, workDir := t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", tmpDir)
	t.Setenv("BACKUP_TEST_PATHS", filepath.Join(workDir, "paths"))
	fakePostgres(t, "pg_dump", `while [ "$#" -gt 0 ]; do
  if [ "$1" = '-f' ]; then shift; output="$1"; fi
  shift
done
printf '%s\n' "$output" >> "$BACKUP_TEST_PATHS"
printf '%s' "$PGPASSWORD" > "$output"
`)
	var wg sync.WaitGroup
	for i := range 6 {
		dataDir, output := t.TempDir(), filepath.Join(workDir, string(rune('a'+i))+".tar.gz")
		password := string(rune('a' + i))
		wg.Go(func() {
			if err := FullBackup(dataDir, "password="+password+" dbname=test", output); err != nil {
				t.Errorf("FullBackup: %v", err)
				return
			}
			if got := archiveFile(t, output, "db.sql"); got != password {
				t.Errorf("concurrent dump mixed: got %q, want %q", got, password)
			}
		})
	}
	wg.Wait()
	paths, err := os.ReadFile(filepath.Join(workDir, "paths"))
	if err != nil {
		t.Fatal(err)
	}
	unique := make(map[string]bool)
	for _, path := range strings.Fields(string(paths)) {
		unique[path] = true
	}
	if len(unique) != 6 {
		t.Errorf("dump temporary paths were reused: %s", paths)
	}
	if entries, err := os.ReadDir(tmpDir); err != nil || len(entries) != 0 {
		t.Errorf("temporary dumps leaked: %v, %v", entries, err)
	}
}

func archiveFile(t *testing.T, archive, name string) string {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Error(err)
		return ""
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Error(err)
		return ""
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err != nil {
			t.Errorf("missing archive entry %s: %v", name, err)
			return ""
		}
		if hdr.Name == name {
			data, err := io.ReadAll(tr)
			if err != nil {
				t.Error(err)
			}
			return string(data)
		}
	}
}
