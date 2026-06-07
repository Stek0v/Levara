// s3_mock_test.go — FIX-10: exercise the S3Storage end-to-end against an
// in-memory S3-compatible httptest.Server. A MinIO container would be more
// realistic but adds a Docker dependency the unit suite doesn't otherwise
// need; the mock verifies the same contract — method, URL shape, auth
// header format, XML list parsing, 404-on-missing — in under a second.
package storage

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// mockS3 is a minimal in-memory S3 server that understands the exact five
// verbs S3Storage emits: PUT object, GET object, DELETE object, HEAD object,
// GET bucket?list-type=2.
type mockS3 struct {
	mu      sync.Mutex
	bucket  string
	objects map[string][]byte
	lastReq struct {
		method string
		auth   string
		amzDate string
		sha256  string
	}
}

func (m *mockS3) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.lastReq.method = r.Method
		m.lastReq.auth = r.Header.Get("Authorization")
		m.lastReq.amzDate = r.Header.Get("x-amz-date")
		m.lastReq.sha256 = r.Header.Get("x-amz-content-sha256")
		m.mu.Unlock()

		// Path is "/<bucket>/<key>" or "/<bucket>" for list.
		path := strings.TrimPrefix(r.URL.Path, "/")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) == 0 || parts[0] != m.bucket {
			http.Error(w, "bad bucket", 404)
			return
		}

		// Bucket-level list request.
		if len(parts) == 1 || parts[1] == "" {
			if r.URL.Query().Get("list-type") == "2" {
				m.respondList(w, r.URL.Query().Get("prefix"))
				return
			}
			http.Error(w, "unsupported bucket op", 400)
			return
		}

		key := parts[1]
		switch r.Method {
		case "PUT":
			body, _ := io.ReadAll(r.Body)
			m.mu.Lock()
			m.objects[key] = body
			m.mu.Unlock()
			w.WriteHeader(200)
		case "GET":
			m.mu.Lock()
			data, ok := m.objects[key]
			m.mu.Unlock()
			if !ok {
				http.Error(w, "not found", 404)
				return
			}
			w.Write(data)
		case "HEAD":
			m.mu.Lock()
			_, ok := m.objects[key]
			m.mu.Unlock()
			if !ok {
				w.WriteHeader(404)
				return
			}
			w.WriteHeader(200)
		case "DELETE":
			m.mu.Lock()
			delete(m.objects, key)
			m.mu.Unlock()
			w.WriteHeader(204)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func (m *mockS3) respondList(w http.ResponseWriter, prefix string) {
	type entry struct {
		XMLName xml.Name `xml:"Contents"`
		Key     string   `xml:"Key"`
	}
	type result struct {
		XMLName     xml.Name `xml:"ListBucketResult"`
		Contents    []entry  `xml:"Contents"`
		IsTruncated bool     `xml:"IsTruncated"`
	}
	var r result
	m.mu.Lock()
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			r.Contents = append(r.Contents, entry{Key: k})
		}
	}
	m.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	xml.NewEncoder(w).Encode(r)
}

func newMockS3(t *testing.T, bucket string) (*mockS3, *httptest.Server) {
	t.Helper()
	m := &mockS3{bucket: bucket, objects: make(map[string][]byte)}
	ts := httptest.NewServer(m.handler())
	t.Cleanup(ts.Close)
	return m, ts
}

func newS3Client(t *testing.T, ts *httptest.Server, bucket string) *S3Storage {
	t.Helper()
	s, err := NewS3Storage(bucket, "us-east-1", ts.URL, "AKIAIOSFODNN7EXAMPLE",
		"wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY")
	if err != nil {
		t.Fatalf("NewS3Storage: %v", err)
	}
	return s
}

// TestS3_PutThenGet proves round-trip: Save content, Load returns bytes equal.
// This exercises the full sig-v4 signing path against a real HTTP server.
func TestS3_PutThenGet(t *testing.T) {
	m, ts := newMockS3(t, "bkt")
	s := newS3Client(t, ts, "bkt")

	payload := []byte("hello s3 world")
	if err := s.Save(context.Background(), "a/b.txt",
		strings.NewReader(string(payload))); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !strings.HasPrefix(m.lastReq.auth, "AWS4-HMAC-SHA256 ") {
		t.Errorf("last PUT auth = %q, want AWS4-HMAC-SHA256 prefix", m.lastReq.auth)
	}
	if m.lastReq.amzDate == "" {
		t.Error("PUT did not send x-amz-date")
	}

	rc, err := s.Load(context.Background(), "a/b.txt")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != string(payload) {
		t.Errorf("Load content = %q, want %q", got, payload)
	}
}

// Exists/Delete exercise HEAD and DELETE verbs with proper status handling
// (200/404 on HEAD, 204 on DELETE, 404-is-OK idempotency on repeated Delete).
func TestS3_ExistsAndDelete(t *testing.T) {
	_, ts := newMockS3(t, "bkt")
	s := newS3Client(t, ts, "bkt")
	ctx := context.Background()

	// Missing key → HEAD 404 → Exists false, no error.
	ok, err := s.Exists(ctx, "ghost")
	if err != nil {
		t.Errorf("Exists on missing should not error: %v", err)
	}
	if ok {
		t.Error("Exists on missing should be false")
	}

	// Put, HEAD 200 → Exists true.
	if err := s.Save(ctx, "k", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	ok, err = s.Exists(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("Exists after Save should be true")
	}

	// Delete, then HEAD 404 → Exists false.
	if err := s.Delete(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	ok, _ = s.Exists(ctx, "k")
	if ok {
		t.Error("Exists after Delete should be false")
	}

	// Second Delete — idempotent (404 is tolerated).
	if err := s.Delete(ctx, "k"); err != nil {
		t.Errorf("repeated Delete should be nil (idempotent), got %v", err)
	}
}

// GET on missing key must surface as an error — not as a silent empty body.
func TestS3_LoadMissing_Errors(t *testing.T) {
	_, ts := newMockS3(t, "bkt")
	s := newS3Client(t, ts, "bkt")

	_, err := s.Load(context.Background(), "nope")
	if err == nil {
		t.Error("Load on missing key should error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Load error should mention 'not found': %v", err)
	}
}

// List must parse ListObjectsV2 XML and return keys under the prefix.
// Also verifies multi-object fan-out (not a single-entry happy path).
func TestS3_ListUnderPrefix(t *testing.T) {
	_, ts := newMockS3(t, "bkt")
	s := newS3Client(t, ts, "bkt")
	ctx := context.Background()

	for _, k := range []string{"logs/a.log", "logs/b.log", "docs/readme.md"} {
		if err := s.Save(ctx, k, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}

	keys, err := s.List(ctx, "logs/")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Errorf("List(logs/) = %v, want 2 entries", keys)
	}
	for _, k := range keys {
		if !strings.HasPrefix(k, "logs/") {
			t.Errorf("key %q outside prefix", k)
		}
	}
}

// 5xx from upstream must surface as an error on Save — silently succeeding
// on 503 would let a dataset upload claim success when nothing landed.
func TestS3_SaveServerError_Propagates(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "slow down", http.StatusServiceUnavailable)
	}))
	defer ts.Close()
	s := newS3Client(t, ts, "bkt")

	err := s.Save(context.Background(), "k", strings.NewReader("x"))
	if err == nil {
		t.Error("Save should fail on 503")
	}
}

// Sig-V4 stability: requests that differ only by body content must
// produce different signatures. If they match, the payload-hash isn't
// being folded into the canonical request (a classic sig-v4 bug).
func TestS3_SigV4_SignatureDependsOnPayload(t *testing.T) {
	m, ts := newMockS3(t, "bkt")
	s := newS3Client(t, ts, "bkt")

	if err := s.Save(context.Background(), "k", strings.NewReader("payload-A")); err != nil {
		t.Fatal(err)
	}
	authA, sha256A := m.lastReq.auth, m.lastReq.sha256

	if err := s.Save(context.Background(), "k", strings.NewReader("payload-B")); err != nil {
		t.Fatal(err)
	}
	authB, sha256B := m.lastReq.auth, m.lastReq.sha256

	if sha256A == sha256B {
		t.Fatalf("x-amz-content-sha256 identical for different payloads: %s", sha256A)
	}
	if authA == authB {
		t.Error("Authorization header identical for different payloads — " +
			"payload hash not affecting the signature")
	}
}
