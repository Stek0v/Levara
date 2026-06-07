package backup

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// T-9b smoke for backup utility. Covers the pure DSN parser and the
// manifest read/write roundtrip. PgDump/PgRestore/FullBackup require live
// binaries (pg_dump, psql) and a real PostgreSQL — out of scope for unit
// tests; covered by the cmd/backup integration suite.

func TestParseDSNToArgs_URIFormat(t *testing.T) {
	cases := []string{
		"postgres://user:pass@localhost:5432/dbname",
		"postgresql://x:y@host/db",
	}
	for _, dsn := range cases {
		args := parseDSNToArgs(dsn)
		if len(args) != 1 || args[0] != dsn {
			t.Errorf("URI dsn %q → args %v, want [%q]", dsn, args, dsn)
		}
	}
}

func TestParseDSNToArgs_KeyValueFormat(t *testing.T) {
	dsn := "host=localhost port=5433 user=levara password=<test-secret> dbname=levara"
	args := parseDSNToArgs(dsn)

	want := []string{
		"-h", "localhost",
		"-p", "5433",
		"-U", "levara",
		"-d", "levara",
	}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
	// Password must NOT leak into argv — it's passed via PGPASSWORD env.
	for _, a := range args {
		if a == "secret" {
			t.Error("password leaked into argv (must use PGPASSWORD instead)")
		}
	}
}

func TestParseDSNToArgs_UsernameAlias(t *testing.T) {
	// libpq accepts both `user=` and `username=`; we should map both to -U.
	args := parseDSNToArgs("username=alice dbname=db")
	got := strings.Join(args, " ")
	if !strings.Contains(got, "-U alice") {
		t.Errorf("username= alias not mapped: %v", args)
	}
}

func TestParseDSNToArgs_MalformedToken(t *testing.T) {
	// Tokens without "=" are silently skipped — keeps the parser robust.
	args := parseDSNToArgs("host=localhost weirdtoken port=5432")
	got := strings.Join(args, " ")
	if !strings.Contains(got, "-h localhost") || !strings.Contains(got, "-p 5432") {
		t.Errorf("unexpected args: %v", args)
	}
}

func TestManifest_RoundTrip(t *testing.T) {
	m := NewManifest("/data/levara", "postgres")
	m.Collections = []string{"col1", "col2"}
	m.Datasets = 5
	m.UploadsCount = 12
	m.UploadsSizeB = 1024 * 1024 * 50

	path := t.TempDir() + "/manifest.json"
	if err := m.Write(path); err != nil {
		t.Fatal(err)
	}

	got, err := ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "1.0" {
		t.Errorf("Version = %q", got.Version)
	}
	if got.DataDir != "/data/levara" {
		t.Errorf("DataDir = %q", got.DataDir)
	}
	if got.DBProvider != "postgres" {
		t.Errorf("DBProvider = %q", got.DBProvider)
	}
	if !reflect.DeepEqual(got.Collections, m.Collections) {
		t.Errorf("Collections roundtrip lost: got %v, want %v", got.Collections, m.Collections)
	}
	if got.Datasets != 5 || got.UploadsCount != 12 || got.UploadsSizeB != 1024*1024*50 {
		t.Errorf("counts roundtrip wrong: %+v", got)
	}
	if got.CreatedAt == "" {
		t.Error("CreatedAt empty after roundtrip")
	}
}

func TestReadManifest_MissingFile(t *testing.T) {
	_, err := ReadManifest("/does/not/exist.json")
	if err == nil {
		t.Fatal("expected error on missing manifest")
	}
}

func TestReadManifest_CorruptJSON(t *testing.T) {
	path := t.TempDir() + "/bad.json"
	if err := os.WriteFile(path, []byte("{not valid"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := ReadManifest(path)
	if err == nil {
		t.Fatal("expected unmarshal error on corrupt JSON")
	}
}
