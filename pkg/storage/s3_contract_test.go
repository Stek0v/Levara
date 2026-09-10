package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestS3ContractRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct{ bucket, region, endpoint, key, secret string }{
		{"../other", "us-east-1", "https://storage.test", "key", "secret"},
		{"bucket", "us-east-1", "file:///tmp/storage", "key", "secret"},
		{"bucket", "us-east-1", "https://user:secret@storage.test", "key", "secret"},
		{"bucket", "us-east-1", "https://storage.test?redirect=1", "key", "secret"},
		{"bucket", "us-east-1", "https://storage.test#fragment", "key", "secret"},
		{"bucket", "us-east-1", "https://storage.test", "", "secret"},
		{"bucket", "us-east-1", "https://storage.test", "key", ""},
		{"bucket", "bad/region", "", "key", "secret"},
	} {
		if _, err := NewS3Storage(tc.bucket, tc.region, tc.endpoint, tc.key, tc.secret); err == nil {
			t.Errorf("invalid config accepted: bucket=%q region=%q endpoint=%q", tc.bucket, tc.region, tc.endpoint)
		}
	}
	t.Setenv("STORAGE_BACKEND", "s33")
	root := filepath.Join(t.TempDir(), "must-not-create")
	if _, err := NewFromEnv(root); err == nil {
		t.Error("unknown backend silently fell back to local storage")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Error("unknown backend created a local storage root")
	}
}

func TestS3ContractEscapedKeysRoundTrip(t *testing.T) {
	_, ts := newMockS3(t, "bucket")
	s := newS3Client(t, ts, "bucket")
	for i, payload := range [][]byte{nil, {0, 1, 0xff, 0xfe, '\n', 0}} {
		key := fmt.Sprintf("документы/%d +?#%%.bin", i)
		if err := s.Save(context.Background(), key, bytes.NewReader(payload)); err != nil {
			t.Fatal(err)
		}
		keys, err := s.List(context.Background(), "документы/")
		if err != nil || !containsKey(keys, key) {
			t.Errorf("saved key was changed: got %q, want %q, err=%v", keys, key, err)
		}
		rc, err := s.Load(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(rc)
		closeErr := rc.Close()
		if err != nil || closeErr != nil || !bytes.Equal(got, payload) {
			t.Errorf("binary roundtrip failed: got=%v err=%v close=%v", got, err, closeErr)
		}
		presigned, err := s.PresignGet(context.Background(), key, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(presigned)
		if err != nil || u.Path != "/bucket/"+key || u.Fragment != "" || u.Query().Get("X-Amz-Signature") == "" {
			t.Errorf("key injected into presigned query/fragment: %q, %v", presigned, err)
		}
		if ok, err := s.Exists(context.Background(), key); !ok || err != nil {
			t.Errorf("Exists=%v err=%v", ok, err)
		}
		if err := s.Delete(context.Background(), key); err != nil {
			t.Fatal(err)
		}
	}
}

func containsKey(keys []string, key string) bool {
	for _, candidate := range keys {
		if candidate == key {
			return true
		}
	}
	return false
}

func TestS3ContractListRejectsFailureAndNonprogress(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"status", `<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`, 403},
		{"missing-truncation", `<ListBucketResult/>`, 200},
		{"missing-token", `<ListBucketResult><IsTruncated>true</IsTruncated></ListBucketResult>`, 200},
		{"repeated-token", `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>same</NextContinuationToken></ListBucketResult>`, 200},
		{"trailing-document", `<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult><Error/>`, 200},
		{"invalid-xml", `<ListBucketResult><IsTruncated>no</IsTruncated>`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer ts.Close()
			s := newS3Client(t, ts, "bucket")
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			if _, err := s.List(ctx, ""); err == nil {
				t.Error("invalid ListObjectsV2 response accepted")
			}
			if calls.Load() > 2 {
				t.Errorf("nonprogress pagination made %d requests", calls.Load())
			}
		})
	}
}

func TestS3ContractListPaginatesAndPreservesToken(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("prefix") != "docs/ " {
			t.Errorf("prefix changed: %q", r.URL.RawQuery)
		}
		if r.URL.Query().Get("continuation-token") == "" {
			fmt.Fprint(w, `<ListBucketResult><Contents><Key>docs/ a</Key></Contents><IsTruncated>true</IsTruncated><NextContinuationToken>next +/=</NextContinuationToken></ListBucketResult>`)
		} else {
			if r.URL.Query().Get("continuation-token") != "next +/=" {
				t.Errorf("token changed: %q", r.URL.RawQuery)
			}
			fmt.Fprint(w, `<ListBucketResult><Contents><Key>docs/ b</Key></Contents><IsTruncated>false</IsTruncated></ListBucketResult>`)
		}
	}))
	defer ts.Close()
	s := newS3Client(t, ts, "bucket")
	keys, err := s.List(context.Background(), "docs/ ")
	if err != nil || !reflect.DeepEqual(keys, []string{"docs/ a", "docs/ b"}) || calls.Load() != 2 {
		t.Errorf("List=%q err=%v calls=%d", keys, err, calls.Load())
	}
}

func TestS3ContractDoesNotFollowRedirects(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		w.WriteHeader(200)
	}))
	defer target.Close()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	defer ts.Close()
	s := newS3Client(t, ts, "bucket")
	if err := s.Save(context.Background(), "key", strings.NewReader("private bytes")); err == nil {
		t.Error("PUT redirect was accepted")
	}
	if received.Load() != 0 {
		t.Error("redirect forwarded an object to a different endpoint")
	}
}

func TestS3ContractRejectsUploadReadFailure(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)
	var received atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { received.Add(1) }))
	defer ts.Close()
	s := newS3Client(t, ts, "bucket")
	failure := errors.New("injected reader failure")
	if err := s.Save(context.Background(), "key", failingStorageReader{failure}); !errors.Is(err, failure) {
		t.Errorf("Save error=%v, want reader failure", err)
	}
	if received.Load() != 0 {
		t.Error("invalid source reached storage server")
	}
	if entries, err := os.ReadDir(tmpDir); err != nil || len(entries) != 0 {
		t.Errorf("failed upload leaked temporary data: %v, %v", entries, err)
	}
}

func TestS3ContractStreamsKnownAndUnknownLength(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)
	payload := bytes.Repeat([]byte{0, 0xff, 1}, 1<<18)
	wantHash := sha256.Sum256(payload)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hash := sha256.New()
		n, err := io.Copy(hash, r.Body)
		if err != nil || n != int64(len(payload)) || r.ContentLength != n || !bytes.Equal(hash.Sum(nil), wantHash[:]) || r.Header.Get("x-amz-content-sha256") != hex.EncodeToString(wantHash[:]) {
			t.Errorf("stream body/size/signature hash mismatch: bytes=%d content-length=%d err=%v", n, r.ContentLength, err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	s := newS3Client(t, ts, "bucket")
	seekable := bytes.NewReader(append([]byte("skip"), payload...))
	if _, err := seekable.Seek(4, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	for _, source := range []io.Reader{seekable, struct{ io.Reader }{bytes.NewReader(payload)}} {
		if err := s.Save(context.Background(), "binary", source); err != nil {
			t.Fatal(err)
		}
		if entries, err := os.ReadDir(tmpDir); err != nil || len(entries) != 0 {
			t.Errorf("upload leaked temporary data: %v, %v", entries, err)
		}
	}
}

func TestS3ContractListHasPageAndBodyLimits(t *testing.T) {
	t.Run("page-limit", func(t *testing.T) {
		var calls atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			page := calls.Add(1)
			fmt.Fprintf(w, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>%d</NextContinuationToken></ListBucketResult>`, page)
		}))
		defer ts.Close()
		s := newS3Client(t, ts, "bucket")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := s.List(ctx, ""); err == nil || ctx.Err() != nil || calls.Load() != maxS3ListPages {
			t.Errorf("pagination not bounded: calls=%d err=%v ctx=%v", calls.Load(), err, ctx.Err())
		}
	})
	t.Run("body-limit", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`)
			fmt.Fprint(w, strings.Repeat(" ", maxS3ListPageBytes))
		}))
		defer ts.Close()
		s := newS3Client(t, ts, "bucket")
		if _, err := s.List(context.Background(), ""); err == nil {
			t.Error("oversized list response accepted")
		}
	})
}

func TestS3ContractRejectsTruncatedSuccessBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "10")
		fmt.Fprint(w, "x")
	}))
	defer ts.Close()
	s := newS3Client(t, ts, "bucket")
	if err := s.Save(context.Background(), "key", strings.NewReader("body")); err == nil {
		t.Error("PUT success ignored response read failure")
	}
	if err := s.Delete(context.Background(), "key"); err == nil {
		t.Error("DELETE success ignored response read failure")
	}
}

type failingStorageReader struct{ err error }

func (r failingStorageReader) Read([]byte) (int, error) { return 0, r.err }
