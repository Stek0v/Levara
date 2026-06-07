package http

import (
	"bytes"
	"context"
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
