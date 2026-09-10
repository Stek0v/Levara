package http

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/ingest"
	"github.com/stek0v/levara/pkg/storage"
)

type memStorage struct {
	objects map[string][]byte
}

func (m *memStorage) DestinationIdentity() string { return "http-test-memory-storage" }

func newMemStorage() *memStorage {
	return &memStorage{objects: make(map[string][]byte)}
}

func (m *memStorage) Save(_ context.Context, path string, data io.Reader) error {
	b, err := io.ReadAll(data)
	if err != nil {
		return err
	}
	m.objects[path] = b
	return nil
}

func (m *memStorage) Load(_ context.Context, path string) (io.ReadCloser, error) {
	b, ok := m.objects[path]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memStorage) Delete(_ context.Context, path string) error {
	delete(m.objects, path)
	return nil
}

func (m *memStorage) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *memStorage) Exists(_ context.Context, path string) (bool, error) {
	_, ok := m.objects[path]
	return ok, nil
}

type presignMemStorage struct {
	*memStorage
}

func (m *presignMemStorage) PresignGet(_ context.Context, path string, ttl time.Duration) (string, error) {
	return fmt.Sprintf("https://signed.example/%s?ttl=%d", path, int(ttl.Seconds())), nil
}

func TestMirrorResultsToFileStorage_LocalBackendKeepsFileURI(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(artifact, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	localStore := storage.NewLocalStorage(filepath.Join(dir, "uploads"))
	cfg := APIConfig{FileStorage: localStore}
	in := []ingest.Result{{
		ID:        "abc",
		Extension: ".txt",
		FilePath:  "file://" + artifact,
	}}

	out, err := mirrorResultsToFileStorage(context.Background(), cfg, in)
	if err != nil {
		t.Fatalf("mirrorResultsToFileStorage: %v", err)
	}
	if out[0].FilePath != in[0].FilePath {
		t.Fatalf("file path changed for local backend: got %q want %q", out[0].FilePath, in[0].FilePath)
	}
}

func TestMirrorResultsToFileStorage_RemoteBackendRewritesURI(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "artifact.txt")
	payload := []byte("hello-storage")
	if err := os.WriteFile(artifact, payload, 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	store := newMemStorage()
	cfg := APIConfig{FileStorage: store}
	in := []ingest.Result{{
		ID:        "abc",
		Extension: ".txt",
		FilePath:  "file://" + artifact,
	}}

	out, err := mirrorResultsToFileStorage(context.Background(), cfg, in)
	if err != nil {
		t.Fatalf("mirrorResultsToFileStorage: %v", err)
	}
	if got := out[0].FilePath; got != "storage://ingest/abc.txt" {
		t.Fatalf("rewritten FilePath = %q, want storage://ingest/abc.txt", got)
	}
	if got := string(store.objects["ingest/abc.txt"]); got != string(payload) {
		t.Fatalf("stored payload = %q, want %q", got, string(payload))
	}
}

func TestLoadRawDataByLocation_FileAndStorageSchemes(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(artifact, []byte("from-file"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	store := newMemStorage()
	store.objects["ingest/a.txt"] = []byte("from-storage")
	cfg := APIConfig{FileStorage: store}

	fileData, err := loadRawDataByLocation(context.Background(), cfg, "file://"+artifact)
	if err != nil {
		t.Fatalf("load file://: %v", err)
	}
	if string(fileData) != "from-file" {
		t.Fatalf("file data = %q, want from-file", string(fileData))
	}

	storeData, err := loadRawDataByLocation(context.Background(), cfg, "storage://ingest/a.txt")
	if err != nil {
		t.Fatalf("load storage://: %v", err)
	}
	if string(storeData) != "from-storage" {
		t.Fatalf("storage data = %q, want from-storage", string(storeData))
	}
}

func TestPresignRawLocation_SupportedBackend(t *testing.T) {
	cfg := APIConfig{FileStorage: &presignMemStorage{memStorage: newMemStorage()}}
	url, ok, err := presignRawLocation(context.Background(), cfg, "storage://ingest/a.txt", 5*time.Minute)
	if err != nil {
		t.Fatalf("presignRawLocation: %v", err)
	}
	if !ok {
		t.Fatalf("presignRawLocation should report supported backend")
	}
	if !strings.Contains(url, "signed.example/ingest/a.txt") {
		t.Fatalf("presigned URL = %q", url)
	}
}

func TestPresignRawLocation_UnsupportedBackend(t *testing.T) {
	cfg := APIConfig{FileStorage: newMemStorage()}
	_, ok, err := presignRawLocation(context.Background(), cfg, "storage://ingest/a.txt", 5*time.Minute)
	if err != nil {
		t.Fatalf("presignRawLocation: %v", err)
	}
	if ok {
		t.Fatalf("backend should not support presign")
	}
}

func TestLoadRawDataCanonicalRootAndSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "actual", "uploads")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "текст.txt")
	if err := os.WriteFile(file, []byte("inside"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(filepath.Dir(root), alias); err != nil {
		t.Fatal(err)
	}
	cfg := APIConfig{StoragePath: filepath.Join(alias, "uploads"), RequireAuth: true}
	canonical, err := filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, location := range []string{"file://" + canonical, canonical, "file://" + filepath.Join(alias, "uploads", "текст.txt")} {
		data, err := loadRawDataByLocation(t.Context(), cfg, location)
		if err != nil || string(data) != "inside" {
			t.Errorf("canonical location %q: %q %v", location, data, err)
		}
	}
	outside := filepath.Join(parent, "secret.txt")
	if err := os.WriteFile(outside, []byte("must-not-leak"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	cfg.StoragePath = root
	for _, location := range []string{"file://" + link, link, "file://" + outside} {
		data, err := loadRawDataByLocation(t.Context(), cfg, location)
		if err == nil || len(data) != 0 {
			t.Errorf("outside raw data exposed: %q %v", data, err)
		}
	}
}
func TestLoadRawDataEmptyRootAuthenticatedFailsClosed(t *testing.T) {
	file := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(file, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := loadRawDataByLocation(t.Context(), APIConfig{RequireAuth: true}, "file://"+file); err == nil || len(data) != 0 {
		t.Fatalf("authenticated unconfined read %q %v", data, err)
	}
	ctx := context.WithValue(t.Context(), searchEgressKey{}, searchEgress{kind: "jwt"})
	if data, err := loadRawDataByLocation(ctx, APIConfig{}, file); err == nil || len(data) != 0 {
		t.Fatalf("optional authenticated unconfined read %q %v", data, err)
	}
}

type failingRawReader struct {
	readErr, closeErr error
	closes            int
}

func (r *failingRawReader) Read(p []byte) (int, error) { return copy(p, "partial"), r.readErr }
func (r *failingRawReader) Close() error               { r.closes++; return r.closeErr }

type rawReaderStorage struct {
	storage.Storage
	reader io.ReadCloser
	err    error
}

func (s rawReaderStorage) Load(context.Context, string) (io.ReadCloser, error) {
	return s.reader, s.err
}
func TestLoadRawDataBackendCloseFailure(t *testing.T) {
	failure := fmt.Errorf("close validation failed")
	reader := &failingRawReader{readErr: io.EOF, closeErr: failure}
	data, err := loadRawDataByLocation(t.Context(), APIConfig{FileStorage: rawReaderStorage{reader: reader}}, "storage://key")
	if err == nil || len(data) != 0 || reader.closes != 1 {
		t.Fatalf("close failure ignored: %q %v closes=%d", data, err, reader.closes)
	}
}

func TestLoadRawDataRegularFileAndCancellation(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "data.txt")
	if err := os.WriteFile(file, []byte("bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := APIConfig{StoragePath: root, RequireAuth: true}
	if data, err := loadRawDataByLocation(t.Context(), cfg, "file://"+root); err == nil || len(data) != 0 {
		t.Fatal("directory accepted as raw data", err)
	}
	missing := filepath.Join(root, "missing")
	if _, err := loadRawDataByLocation(t.Context(), cfg, missing); !os.IsNotExist(err) {
		t.Fatalf("missing-file error lost: %v", err)
	}
	link := filepath.Join(root, "inside-link")
	if err := os.Symlink("data.txt", link); err != nil {
		t.Fatal(err)
	}
	if data, err := loadRawDataByLocation(t.Context(), cfg, link); err != nil || string(data) != "bytes" {
		t.Fatalf("contained relative symlink %q %v", data, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if data, err := loadRawDataByLocation(ctx, cfg, file); !errors.Is(err, context.Canceled) || len(data) != 0 {
		t.Fatalf("canceled file read %q %v", data, err)
	}
	// Empty-root access remains only a trusted no-SQL development compatibility.
	if data, err := loadRawDataByLocation(t.Context(), APIConfig{}, file); err != nil || string(data) != "bytes" {
		t.Fatal("legacy dev read", err)
	}
	if data, err := loadRawDataByLocation(t.Context(), APIConfig{DB: new(sql.DB)}, file); err == nil || len(data) != 0 {
		t.Fatal("configured SQL caller got unconfined fallback")
	}
}
func TestLoadRawDataBackendFailuresCloseOnce(t *testing.T) {
	for _, kind := range []string{"load", "read", "nil-reader", "load-with-reader"} {
		t.Run(kind, func(t *testing.T) {
			failure := fmt.Errorf("backend failure")
			reader := &failingRawReader{readErr: failure}
			backend := rawReaderStorage{reader: reader}
			closes := 1
			switch kind {
			case "load":
				backend.reader = nil
				backend.err = failure
				closes = 0
			case "nil-reader":
				backend.reader = nil
				closes = 0
			case "load-with-reader":
				backend.err = failure
			}
			data, err := loadRawDataByLocation(t.Context(), APIConfig{FileStorage: backend}, "storage://key")
			if err == nil || len(data) != 0 || reader.closes != closes {
				t.Fatalf("backend error %q %v closes=%d", data, err, reader.closes)
			}
			if kind != "nil-reader" && !errors.Is(err, failure) {
				t.Fatalf("original error lost: %v", err)
			}
		})
	}
}
func TestMirrorResultsConfinesLocalArtifacts(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("no-mirror"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	backend := newMemStorage()
	_, err := mirrorResultsToFileStorage(t.Context(), APIConfig{StoragePath: root, RequireAuth: true, FileStorage: backend}, []ingest.Result{{ID: "unsafe", FilePath: "file://" + link}})
	if err == nil || len(backend.objects) != 0 {
		t.Fatalf("unsafe artifact mirrored: %v", err)
	}
}
