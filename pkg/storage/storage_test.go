package storage

import (
	"context"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// T-9 smoke tests for pkg/storage:
//   - LocalStorage round-trip (Save → Exists → Load → List → Delete)
//   - NewFromEnv dispatch

func TestLocalStorage_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewLocalStorage(dir)
	ctx := context.Background()

	// Save two files
	if err := s.Save(ctx, "a/hello.txt", strings.NewReader("hi")); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, "a/b/deep.txt", strings.NewReader("deep")); err != nil {
		t.Fatal(err)
	}

	// Exists (positive + negative)
	if ok, _ := s.Exists(ctx, "a/hello.txt"); !ok {
		t.Error("Exists: hello.txt should be true")
	}
	if ok, _ := s.Exists(ctx, "a/missing.txt"); ok {
		t.Error("Exists: missing.txt should be false")
	}

	// Load content back
	rc, err := s.Load(ctx, "a/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "hi" {
		t.Errorf("Load content = %q, want hi", got)
	}

	// List under prefix
	paths, err := s.List(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	want := []string{"a/b/deep.txt", "a/hello.txt"}
	if len(paths) != 2 || paths[0] != want[0] || paths[1] != want[1] {
		t.Errorf("List = %v, want %v", paths, want)
	}

	// Delete: removes the file and is idempotent (second Delete is a no-op).
	if err := s.Delete(ctx, "a/hello.txt"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "a/hello.txt"); err != nil {
		t.Errorf("second Delete should be nil (idempotent), got %v", err)
	}
	if ok, _ := s.Exists(ctx, "a/hello.txt"); ok {
		t.Error("Exists after Delete should be false")
	}

	// Save durability: Sync must have fired; stat size > 0 on deep.txt.
	st, err := os.Stat(filepath.Join(dir, "a/b/deep.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != 4 {
		t.Errorf("deep.txt size = %d, want 4", st.Size())
	}
}

func TestLocalStorage_LoadMissing(t *testing.T) {
	s := NewLocalStorage(t.TempDir())
	if _, err := s.Load(context.Background(), "nope"); err == nil {
		t.Error("Load on missing key should error")
	}
}

func TestLocalStorage_ListEmpty(t *testing.T) {
	s := NewLocalStorage(t.TempDir())
	paths, err := s.List(context.Background(), "anything")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Errorf("empty prefix should yield 0 paths, got %v", paths)
	}
}

func TestNewFromEnv_DefaultsToLocal(t *testing.T) {
	// Don't disturb ambient env beyond the scope of this test.
	t.Setenv("STORAGE_BACKEND", "")
	dir := t.TempDir()
	s, err := NewFromEnv(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(*LocalStorage); !ok {
		t.Errorf("default backend should be *LocalStorage, got %T", s)
	}
}

func TestNewS3Storage_RequiresBucket(t *testing.T) {
	if _, err := NewS3Storage("", "us-east-1", "", "k", "s"); err == nil {
		t.Error("empty bucket should error")
	}
}

func TestNewS3Storage_DefaultEndpoint(t *testing.T) {
	s, err := NewS3Storage("mybkt", "us-west-2", "", "k", "s")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.endpoint, "us-west-2") {
		t.Errorf("default endpoint missing region: %q", s.endpoint)
	}
}

func TestS3_PresignGet_IncludesSigV4Query(t *testing.T) {
	s, err := NewS3Storage("mybkt", "us-east-1", "https://example.test", "AKID", "SECRET")
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.PresignGet(context.Background(), "ingest/a.txt", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	if !strings.Contains(parsed.Path, "/mybkt/ingest/a.txt") {
		t.Fatalf("path = %q, want /mybkt/ingest/a.txt", parsed.Path)
	}
	q := parsed.Query()
	for _, key := range []string{
		"X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date", "X-Amz-Expires", "X-Amz-SignedHeaders", "X-Amz-Signature",
	} {
		if q.Get(key) == "" {
			t.Fatalf("missing query param %s in presigned URL", key)
		}
	}
	if got := q.Get("X-Amz-Algorithm"); got != "AWS4-HMAC-SHA256" {
		t.Fatalf("algorithm = %q, want AWS4-HMAC-SHA256", got)
	}
	if got := q.Get("X-Amz-SignedHeaders"); got != "host" {
		t.Fatalf("signed headers = %q, want host", got)
	}
	if got := q.Get("X-Amz-Expires"); got != strconv.Itoa(600) {
		t.Fatalf("expires = %q, want 600", got)
	}
}

// Keys must never escape the storage root (finding H10, 2026-09-03 review).
func TestLocalStorageRejectsPathTraversal(t *testing.T) {
	s := NewLocalStorage(t.TempDir())
	for _, key := range []string{
		"../escape.txt",
		"a/../../escape.txt",
		"..",
		"/etc/passwd",
	} {
		if _, err := s.fullPath(key); err == nil {
			t.Errorf("fullPath(%q) accepted, want traversal rejection", key)
		}
	}
	// Legitimate keys still resolve inside the root.
	for _, key := range []string{"docs/file.txt", "a/b/c.bin", "./relative.txt"} {
		fp, err := s.fullPath(key)
		if err != nil {
			t.Errorf("fullPath(%q) rejected: %v", key, err)
			continue
		}
		if !strings.HasPrefix(fp, s.basePath) {
			t.Errorf("fullPath(%q) = %q escapes root %q", key, fp, s.basePath)
		}
	}
}
