package backup

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// Parser and manifest checks complement the fake PostgreSQL client failure tests.

func TestParseDSNToArgs_URIFormat(t *testing.T) {
	cases := []struct{ dsn, sanitized, password string }{
		{"postgres://user:pass@localhost:5432/dbname", "postgres://user@localhost:5432/dbname", "pass"},
		{"postgresql://x:y@host/db", "postgresql://x@host/db", "y"},
		{"postgres://x:y@host/db?password=p%40ss&sslmode=require", "postgres://x@host/db?sslmode=require", "p@ss"},
		{"postgres://x:y@host/db?password=first&password=last", "postgres://x@host/db", "last"},
		{"postgres://x:@host/db", "postgres://x@host/db", ""},
		{"postgres://x@host/db?password=p+ass&application_name=a%20b&options=-c%20work_mem%3D64MB", "postgres://x@host/db?application_name=a%20b&options=-c%20work_mem%3D64MB", "p+ass"},
		{"postgres://x@host/db?%70assword=s%26ecret&sslmode=require&sslmode=verify-full", "postgres://x@host/db?sslmode=require&sslmode=verify-full", "s&ecret"},
	}
	for _, tc := range cases {
		args, password, err := parseDSNToArgs(tc.dsn)
		if err != nil || !reflect.DeepEqual(args, []string{"--dbname", tc.sanitized}) || password == nil || *password != tc.password {
			t.Errorf("URI args=%v password=%v err=%v", args, password, err)
		}
	}
}

func TestParseDSNToArgs_KeyValueFormat(t *testing.T) {
	dsn := "host=localhost port=5433 user=levara password=<test-secret> dbname=levara"
	args, _, _ := parseDSNToArgs(dsn)

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
		if strings.Contains(a, "<test-secret>") {
			t.Error("password leaked into argv (must use PGPASSWORD instead)")
		}
	}
}

func TestParseDSNToArgs_UsernameAlias(t *testing.T) {
	// Keep the backup utility's legacy username= alias.
	args, _, _ := parseDSNToArgs("username=alice dbname=db")
	got := strings.Join(args, " ")
	if !strings.Contains(got, "-U alice") {
		t.Errorf("username= alias not mapped: %v", args)
	}
}

func TestParseDSNToArgs_MalformedToken(t *testing.T) {
	// Tokens without "=" are silently skipped — keeps the parser robust.
	args, _, _ := parseDSNToArgs("host=localhost weirdtoken port=5432")
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
