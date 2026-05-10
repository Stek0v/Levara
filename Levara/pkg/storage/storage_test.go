package storage

import (
	"context"
	"encoding/hex"
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
//   - AWS Sig V4 primitives against canonical test vectors
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

// TestSigV4_DeriveSigningKey_CanonicalVector verifies the AWS Sig V4 signing
// key derivation against the canonical example from AWS docs (example from
// https://docs.aws.amazon.com/AmazonS3/latest/API/sigv4-test-suite.html).
//
//	secretKey: wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY
//	datestamp: 20120215
//	region:    us-east-1
//	service:   iam
//
// Expected signing key (hex):
//
//	f4780e2d9f65fa895f9c67b32ce1baf0b0d8a43505a000a1a9e090d414db404d
func TestSigV4_DeriveSigningKey_CanonicalVector(t *testing.T) {
	key := deriveSigningKey(
		"wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
		"20120215",
		"us-east-1",
		"iam",
	)
	want := "f4780e2d9f65fa895f9c67b32ce1baf0b0d8a43505a000a1a9e090d414db404d"
	if got := hex.EncodeToString(key); got != want {
		t.Errorf("signingKey = %s, want %s", got, want)
	}
}

func TestSigV4_Sha256Hex_EmptyBody(t *testing.T) {
	// SHA-256 of empty string is the well-known constant used for payloadHash
	// on GET/DELETE requests with no body.
	const empty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := sha256Hex(nil); got != empty {
		t.Errorf("sha256Hex(nil) = %s, want %s", got, empty)
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
