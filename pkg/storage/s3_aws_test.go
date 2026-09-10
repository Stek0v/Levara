package storage

import (
	"bytes"
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestS3SDKEnvironmentSessionCredentials(t *testing.T) {
	var token string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token = r.Header.Get("X-Amz-Security-Token")
		w.WriteHeader(200)
	}))
	defer server.Close()
	for k, v := range map[string]string{"STORAGE_BACKEND": "s3", "S3_BUCKET": "bucket", "S3_REGION": "us-east-1", "S3_ENDPOINT": server.URL, "AWS_ACCESS_KEY_ID": "TEST_KEY", "AWS_SECRET_ACCESS_KEY": "TEST_SECRET", "AWS_SESSION_TOKEN": "TEST_SESSION"} {
		t.Setenv(k, v)
	}
	store, err := NewFromEnv(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), "object", strings.NewReader("body")); err != nil {
		t.Fatal(err)
	}
	if token != "TEST_SESSION" {
		t.Fatal("S3 request lost session credentials")
	}
}

func TestS3SDKPinnedRange(t *testing.T) {
	payload := []byte{0, 1, 2, 0xff, 0xfe, 5, 6}
	for _, tc := range []string{"valid", "ignored-range", "wrong-bounds", "wrong-etag", "wrong-version", "truncated"} {
		t.Run(tc, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/bucket/док +?#%.bin" {
					t.Errorf("key changed: %s", r.URL.Path)
				}
				w.Header().Set("ETag", `"v1"`)
				w.Header().Set("x-amz-version-id", "generation-1")
				if r.Method == http.MethodHead {
					w.Header().Set("Content-Length", "7")
					return
				}
				if r.Header.Get("If-Match") != `"v1"` || r.Header.Get("Range") != "bytes=2-4" || r.URL.Query().Get("versionId") != "generation-1" {
					t.Errorf("unpinned request: %v %s", r.Header, r.URL.RawQuery)
				}
				w.Header().Set("Content-Range", "bytes 2-4/7")
				w.Header().Set("Content-Length", "3")
				switch tc {
				case "ignored-range":
					w.WriteHeader(200)
				case "wrong-bounds":
					w.Header().Set("Content-Range", "bytes 1-3/7")
					w.WriteHeader(206)
				case "wrong-etag":
					w.Header().Set("ETag", `"v2"`)
					w.WriteHeader(206)
				case "wrong-version":
					w.Header().Set("x-amz-version-id", "generation-2")
					w.WriteHeader(206)
				default:
					w.WriteHeader(206)
				}
				if tc == "truncated" {
					_, _ = w.Write(payload[2:3])
				} else {
					_, _ = w.Write(payload[2:5])
				}
			}))
			defer server.Close()
			s := newS3Client(t, server, "bucket")
			snapshot, err := s.Snapshot(context.Background(), "док +?#%.bin")
			if err != nil {
				t.Fatal(err)
			}
			body, err := s.LoadRange(context.Background(), "док +?#%.bin", 2, 3, snapshot)
			if err == nil {
				data, readErr := io.ReadAll(body)
				closeErr := body.Close()
				err = errors.Join(readErr, closeErr)
				if tc == "valid" && !bytes.Equal(data, payload[2:5]) {
					t.Fatalf("range bytes: %v", data)
				}
			}
			if tc == "valid" && err != nil {
				t.Fatal(err)
			}
			if tc != "valid" && err == nil {
				t.Fatal("invalid range accepted")
			}
		})
	}
}

func TestS3SDKRangeRejectsInvalidInputBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	s := newS3Client(t, server, "bucket")
	for _, tc := range []struct {
		o, n int64
		s    ObjectSnapshot
	}{{-1, 1, ObjectSnapshot{ETag: "v", Size: 5}}, {0, 0, ObjectSnapshot{ETag: "v", Size: 5}}, {4, 2, ObjectSnapshot{ETag: "v", Size: 5}}, {0, 1, ObjectSnapshot{Size: 5}}, {0, 1, ObjectSnapshot{ETag: "v\r\n", Size: 5}}} {
		if _, err := s.LoadRange(context.Background(), "key", tc.o, tc.n, tc.s); err == nil {
			t.Fatal("invalid range accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid range reached server")
	}
}

func TestS3SDKSigningEscapedHTTPAndPresign(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/bucket/a%20+%3F%23%25.bin" && r.URL.EscapedPath() != "/bucket/a%20%2B%3F%23%25.bin" {
			t.Errorf("escaped wire path: %s", r.URL.EscapedPath())
		}
		if !strings.Contains(r.Header.Get("Authorization"), "Credential=ROTATING_KEY/") || r.Header.Get("X-Amz-Security-Token") != "TEMP_TOKEN" || r.Header.Get("X-Amz-Date") == "" {
			t.Error("missing SDK signature or session token")
		}
	}))
	defer server.Close()
	s, err := NewS3StorageWithConfig(context.Background(), S3Config{Bucket: "bucket", Endpoint: server.URL, Credentials: credentials.NewStaticCredentialsProvider("ROTATING_KEY", "SECRET", "TEMP_TOKEN")})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Save(context.Background(), "a +?#%.bin", strings.NewReader("payload")); err != nil {
		t.Fatal(err)
	}
	signed, err := s.PresignGet(context.Background(), "a +?#%.bin", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/bucket/a +?#%.bin" || u.Query().Get("X-Amz-Security-Token") != "TEMP_TOKEN" || u.Query().Get("X-Amz-Signature") == "" || u.Fragment != "" {
		t.Fatal("invalid signed download URL")
	}
}

func TestS3SDKSpoolBudgetCancelAndAdmission(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}
	}))
	defer server.Close()
	s, err := NewS3StorageWithConfig(context.Background(), S3Config{Bucket: "bucket", Endpoint: server.URL, Credentials: credentials.NewStaticCredentialsProvider("key", "secret", ""), MaxObjectBytes: 1024, MaxSpoolBytes: 10, MaxInFlight: 1, MaxWaiters: 1, Timeout: 200 * time.Millisecond, SpoolDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Save(context.Background(), "key", struct{ io.Reader }{strings.NewReader("eleven bytes")}); err == nil {
		t.Fatal("spool budget exceeded silently")
	}
	ctx, cancel := context.WithCancel(context.Background())
	body, err := s.Load(ctx, "key")
	if err != nil {
		t.Fatal(err)
	}
	waiterCtx, stopWait := context.WithCancel(context.Background())
	defer stopWait()
	waiting := make(chan error, 1)
	go func() { _, err := s.Exists(waiterCtx, "key"); waiting <- err }()
	deadline := time.Now().Add(time.Second)
	for len(s.waiters) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, err = s.Exists(context.Background(), "key"); err == nil || !strings.Contains(err.Error(), "queue full") {
		t.Fatalf("saturation error: %v", err)
	}
	stopWait()
	if err = <-waiting; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter cancellation: %v", err)
	}
	cancel()
	if _, err = body.Read(make([]byte, 1)); err == nil {
		t.Fatal("cancelled body remained open")
	}
	_ = body.Close()
	if len(s.slots) != 0 {
		t.Fatal("body leaked admission slot")
	}
	if s.spoolBytes != 0 {
		t.Fatal("spool budget leaked")
	}
	entries, err := os.ReadDir(s.cfg.SpoolDirectory)
	if err != nil || len(entries) != 0 {
		t.Fatal("spool leaked files")
	}
}

func TestS3SDKCancelBlockedSpool(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	s, err := NewS3StorageWithConfig(context.Background(), S3Config{Bucket: "bucket", Endpoint: server.URL, Credentials: credentials.NewStaticCredentialsProvider("key", "secret", ""), Timeout: 40 * time.Millisecond, SpoolDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	defer writer.Close()
	done := make(chan error, 1)
	go func() { done <- s.Save(context.Background(), "blocked", reader) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("blocked cancelled upload succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not close the source")
	}
	if calls.Load() != 0 || len(s.slots) != 0 || s.spoolBytes != 0 {
		t.Fatal("cancelled spool retained resources or reached network")
	}
	entries, err := os.ReadDir(s.cfg.SpoolDirectory)
	if err != nil || len(entries) != 0 {
		t.Fatal("cancelled spool left temporary file")
	}
}

func TestS3SDKSaveConsumesSeekableSource(t *testing.T) {
	_, server := newMockS3(t, "bucket")
	s := newS3Client(t, server, "bucket")
	source := strings.NewReader("skip-data")
	_, _ = source.Seek(5, io.SeekStart)
	if err := s.Save(context.Background(), "key", source); err != nil {
		t.Fatal(err)
	}
	if position, err := source.Seek(0, io.SeekCurrent); err != nil || position != 9 {
		t.Fatalf("Save did not consume io.Reader source: position=%d err=%v", position, err)
	}
}
