package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	errObjectMissing = errors.New("object missing")
	errLegalHold     = errors.New("legal hold blocks delete")
	errRetention     = errors.New("retention blocks delete")
)

type fakeEnterpriseObject struct {
	data     []byte
	metadata ObjectMetadata
	modified time.Time
}

type fakeEnterpriseStorage struct {
	objects  map[string]fakeEnterpriseObject
	failSave bool
}

func newFakeEnterpriseStorage() *fakeEnterpriseStorage {
	return &fakeEnterpriseStorage{objects: make(map[string]fakeEnterpriseObject)}
}

func (s *fakeEnterpriseStorage) Save(ctx context.Context, path string, data io.Reader) error {
	return s.SaveWithMetadata(ctx, path, data, ObjectMetadata{})
}

func (s *fakeEnterpriseStorage) SaveWithMetadata(_ context.Context, path string, data io.Reader, metadata ObjectMetadata) error {
	if s.failSave {
		return errors.New("backend unavailable")
	}
	buf, err := io.ReadAll(data)
	if err != nil {
		return err
	}
	s.objects[path] = fakeEnterpriseObject{
		data:     append([]byte(nil), buf...),
		metadata: metadata,
		modified: time.Now().UTC(),
	}
	return nil
}

func (s *fakeEnterpriseStorage) Load(_ context.Context, path string) (io.ReadCloser, error) {
	obj, ok := s.objects[path]
	if !ok {
		return nil, errObjectMissing
	}
	return io.NopCloser(bytes.NewReader(obj.data)), nil
}

func (s *fakeEnterpriseStorage) Delete(ctx context.Context, path string) error {
	return s.DeleteWithOptions(ctx, path, DeleteOptions{})
}

func (s *fakeEnterpriseStorage) DeleteWithOptions(_ context.Context, path string, opts DeleteOptions) error {
	obj, ok := s.objects[path]
	if !ok {
		return nil
	}
	if obj.metadata.LegalHold {
		return errLegalHold
	}
	if obj.metadata.RetainUntil.After(time.Now()) {
		switch obj.metadata.RetentionClass {
		case RetentionCompliance:
			return errRetention
		case RetentionGovernance:
			if !opts.BypassGovernance {
				return errRetention
			}
		}
	}
	delete(s.objects, path)
	return nil
}

func (s *fakeEnterpriseStorage) List(_ context.Context, prefix string) ([]string, error) {
	var paths []string
	for path := range s.objects {
		if strings.HasPrefix(path, prefix) {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func (s *fakeEnterpriseStorage) Exists(_ context.Context, path string) (bool, error) {
	_, ok := s.objects[path]
	return ok, nil
}

func (s *fakeEnterpriseStorage) Stat(_ context.Context, path string) (ObjectInfo, error) {
	obj, ok := s.objects[path]
	if !ok {
		return ObjectInfo{}, errObjectMissing
	}
	return ObjectInfo{
		Path:     path,
		Size:     int64(len(obj.data)),
		Modified: obj.modified,
		Metadata: obj.metadata,
	}, nil
}

func (s *fakeEnterpriseStorage) PresignRead(_ context.Context, path string, ttl time.Duration) (string, error) {
	if _, ok := s.objects[path]; !ok {
		return "", errObjectMissing
	}
	return fmt.Sprintf("https://objects.example.test/%s?ttl=%d", path, int(ttl.Seconds())), nil
}

type fakeKMS struct {
	lastEncrypt EncryptDataKeyRequest
}

func (k *fakeKMS) EncryptDataKey(_ context.Context, req EncryptDataKeyRequest) (EncryptDataKeyResponse, error) {
	k.lastEncrypt = req
	return EncryptDataKeyResponse{
		CiphertextKeyRef: "ciphertext:" + req.KeyRef,
		KeyRef:           req.KeyRef,
		Algorithm:        req.Algorithm,
	}, nil
}

func (k *fakeKMS) DecryptDataKey(_ context.Context, req DecryptDataKeyRequest) (DecryptDataKeyResponse, error) {
	return DecryptDataKeyResponse{
		Plaintext: []byte("transient-data-key"),
		KeyRef:    strings.TrimPrefix(req.CiphertextKeyRef, "ciphertext:"),
	}, nil
}

func (k *fakeKMS) RotateKeyRef(_ context.Context, req RotateKeyRefRequest) (RotateKeyRefResponse, error) {
	return RotateKeyRefResponse{KeyRef: req.NewKeyRef}, nil
}

func (k *fakeKMS) KeyMetadata(_ context.Context, keyRef string) (KeyMetadata, error) {
	return KeyMetadata{
		KeyRef:    keyRef,
		Provider:  "test-kms",
		Algorithm: "AES-256-GCM",
		CreatedAt: time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC),
	}, nil
}

var (
	_ EnterpriseStorage = (*fakeEnterpriseStorage)(nil)
	_ DirectReader      = (*fakeEnterpriseStorage)(nil)
	_ KMS               = (*fakeKMS)(nil)
)

func TestEnterpriseStorageContract_MetadataRetentionAndLegalHold(t *testing.T) {
	ctx := context.Background()
	store := newFakeEnterpriseStorage()
	retainUntil := time.Now().Add(time.Hour)
	metadata := ObjectMetadata{
		TenantID:             "tenant-a",
		ProjectID:            "project-1",
		ContentDigest:        "sha256:abc123",
		RetentionClass:       RetentionGovernance,
		RetainUntil:          retainUntil,
		LegalHold:            true,
		EncryptionKeyRef:     "kms://tenant-a/key-1",
		EncryptionAlgorithm:  "AES-256-GCM",
		EncryptedDataKeyRef:  "wrapped:key-1:object-1",
		PlaintextKeyMaterial: true,
	}

	if err := store.SaveWithMetadata(ctx, "tenant-a/object.bin", strings.NewReader("payload"), metadata); err != nil {
		t.Fatalf("SaveWithMetadata: %v", err)
	}

	info, err := store.Stat(ctx, "tenant-a/object.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size != int64(len("payload")) {
		t.Fatalf("size = %d, want %d", info.Size, len("payload"))
	}
	if info.Metadata.TenantID != metadata.TenantID ||
		info.Metadata.ProjectID != metadata.ProjectID ||
		info.Metadata.ContentDigest != metadata.ContentDigest ||
		info.Metadata.RetentionClass != metadata.RetentionClass ||
		info.Metadata.EncryptionKeyRef != metadata.EncryptionKeyRef {
		t.Fatalf("metadata not preserved: %+v", info.Metadata)
	}

	if err := store.Delete(ctx, "tenant-a/object.bin"); !errors.Is(err, errLegalHold) {
		t.Fatalf("Delete with legal hold error = %v, want %v", err, errLegalHold)
	}
	info.Metadata.LegalHold = false
	store.objects["tenant-a/object.bin"] = fakeEnterpriseObject{
		data:     store.objects["tenant-a/object.bin"].data,
		metadata: info.Metadata,
		modified: info.Modified,
	}
	if err := store.Delete(ctx, "tenant-a/object.bin"); !errors.Is(err, errRetention) {
		t.Fatalf("Delete under governance retention error = %v, want %v", err, errRetention)
	}
	if err := store.DeleteWithOptions(ctx, "tenant-a/object.bin", DeleteOptions{BypassGovernance: true}); err != nil {
		t.Fatalf("DeleteWithOptions bypass governance: %v", err)
	}
	if err := store.DeleteWithOptions(ctx, "tenant-a/object.bin", DeleteOptions{}); err != nil {
		t.Fatalf("DeleteWithOptions missing object should be idempotent: %v", err)
	}
}

func TestEnterpriseStorageContract_DirectReadAndFailureObservability(t *testing.T) {
	ctx := context.Background()
	store := newFakeEnterpriseStorage()

	if err := store.SaveWithMetadata(ctx, "tenant-a/index-snapshot.bin", strings.NewReader("stable"), ObjectMetadata{
		TenantID:      "tenant-a",
		ContentDigest: "sha256:stable",
	}); err != nil {
		t.Fatalf("SaveWithMetadata: %v", err)
	}
	url, err := store.PresignRead(ctx, "tenant-a/index-snapshot.bin", 15*time.Minute)
	if err != nil {
		t.Fatalf("PresignRead: %v", err)
	}
	if !strings.Contains(url, "tenant-a/index-snapshot.bin") || !strings.Contains(url, "ttl=900") {
		t.Fatalf("presigned URL = %q, want object path and ttl", url)
	}

	store.failSave = true
	if err := store.SaveWithMetadata(ctx, "tenant-a/index-snapshot.bin", strings.NewReader("corrupt"), ObjectMetadata{}); err == nil {
		t.Fatal("SaveWithMetadata should report backend failure")
	}
	rc, err := store.Load(ctx, "tenant-a/index-snapshot.bin")
	if err != nil {
		t.Fatalf("Load after failed save: %v", err)
	}
	got, err := io.ReadAll(rc)
	if closeErr := rc.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "stable" {
		t.Fatalf("failed save corrupted existing object: got %q", got)
	}
}

func TestKMSContract_DoesNotLeakPlaintextThroughJSON(t *testing.T) {
	ctx := context.Background()
	kms := &fakeKMS{}
	req := EncryptDataKeyRequest{
		KeyRef:      "kms://tenant-a/key-1",
		Plaintext:   []byte("plaintext-data-key"),
		Context:     map[string]string{"tenant_id": "tenant-a"},
		Algorithm:   "AES-256-GCM",
		RequestedBy: "test",
	}

	resp, err := kms.EncryptDataKey(ctx, req)
	if err != nil {
		t.Fatalf("EncryptDataKey: %v", err)
	}
	if !bytes.Equal(kms.lastEncrypt.Plaintext, req.Plaintext) {
		t.Fatal("KMS hook did not receive plaintext data key for wrapping")
	}
	assertJSONDoesNotContain(t, req, "plaintext-data-key", "Plaintext")
	assertJSONDoesNotContain(t, DecryptDataKeyResponse{Plaintext: []byte("plaintext-data-key"), KeyRef: req.KeyRef}, "plaintext-data-key", "Plaintext")
	assertJSONDoesNotContain(t, ObjectMetadata{PlaintextKeyMaterial: true, EncryptionKeyRef: req.KeyRef}, "PlaintextKeyMaterial", "plaintext")

	if resp.CiphertextKeyRef == "" || resp.KeyRef != req.KeyRef {
		t.Fatalf("EncryptDataKey response missing key refs: %+v", resp)
	}
	if rotated, err := kms.RotateKeyRef(ctx, RotateKeyRefRequest{OldKeyRef: req.KeyRef, NewKeyRef: "kms://tenant-a/key-2"}); err != nil || rotated.KeyRef != "kms://tenant-a/key-2" {
		t.Fatalf("RotateKeyRef = %+v, %v", rotated, err)
	}
	meta, err := kms.KeyMetadata(ctx, req.KeyRef)
	if err != nil {
		t.Fatalf("KeyMetadata: %v", err)
	}
	assertJSONDoesNotContain(t, meta, "plaintext", "secret", "private")
}

func TestEnterpriseStorageBoundary_CorePackagesDoNotImportStorage(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	coreDirs := []string{
		filepath.Join(repoRoot, "pkg", "bm25"),
		filepath.Join(repoRoot, "pkg", "vectorstore"),
		filepath.Join(repoRoot, "pkg", "graphstore"),
		filepath.Join(repoRoot, "pkg", "orchestrator"),
	}

	for _, dir := range coreDirs {
		dir := dir
		t.Run(filepath.ToSlash(dir), func(t *testing.T) {
			err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() || !strings.HasSuffix(path, ".go") {
					return nil
				}
				body, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				text := string(body)
				if strings.Contains(text, `"github.com/stek0v/cognevra/pkg/storage"`) {
					t.Fatalf("%s imports pkg/storage; enterprise storage must stay outside core algorithms", path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func assertJSONDoesNotContain(t *testing.T, value any, forbidden ...string) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	text := string(body)
	for _, needle := range forbidden {
		if strings.Contains(text, needle) {
			t.Fatalf("JSON %s contains forbidden %q", text, needle)
		}
	}
}
