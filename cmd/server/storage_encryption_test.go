package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stek0v/levara/pkg/ingest"
	"github.com/stek0v/levara/pkg/storage"
)

const storageTestKey = "arn:aws:kms:eu-central-1:123456789012:key/test-key"

// This fake only isolates server wiring. Real KMS request/signature/error
// contracts are exercised by pkg/storage's protocol tests.
type storageWiringKMS struct {
	storage.KMS
	key  []byte
	down bool
}

func (k *storageWiringKMS) EncryptDataKey(_ context.Context, r storage.EncryptDataKeyRequest) (storage.EncryptDataKeyResponse, error) {
	if k.down {
		return storage.EncryptDataKeyResponse{}, errors.New("unavailable")
	}
	k.key = append([]byte(nil), r.Plaintext...)
	return storage.EncryptDataKeyResponse{KeyRef: r.KeyRef, CiphertextKeyRef: "opaque-test-reference"}, nil
}
func (k *storageWiringKMS) DecryptDataKey(context.Context, storage.DecryptDataKeyRequest) (storage.DecryptDataKeyResponse, error) {
	if k.down {
		return storage.DecryptDataKeyResponse{}, errors.New("unavailable")
	}
	return storage.DecryptDataKeyResponse{KeyRef: storageTestKey, Plaintext: append([]byte(nil), k.key...)}, nil
}

func TestStorageEncryptionConfigAndIngestWiring(t *testing.T) {
	env := map[string]string{"STORAGE_ENCRYPTION": "aws-kms", "KMS_KEY_ARN": storageTestKey, "KMS_REGION": "eu-central-1", "STORAGE_ENCRYPTION_MAX_OBJECT_BYTES": "4096", "STORAGE_ENCRYPTION_TIMEOUT": "1s"}
	get := func(key string) string { return env[key] }
	cfg, err := storageEncryptionFromEnv(get)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.enabled || cfg.cache.TTL != 0 || cfg.objects.Timeout != time.Second {
		t.Fatalf("unexpected config %+v", cfg)
	}
	inner := storage.NewLocalStorage(t.TempDir())
	kms := &storageWiringKMS{}
	registry := prometheus.NewRegistry()
	backend, err := wrapEncryptedStorage(inner, kms, cfg, registry)
	if err != nil {
		t.Fatal(err)
	}
	secret := "Закрытый документ — 123456789"
	results, err := ingest.IngestStored(context.Background(), []ingest.Item{{Text: secret, OwnerID: "alice"}}, t.TempDir(), backend)
	if err != nil || len(results) != 1 {
		t.Fatalf("ingest %+v %v", results, err)
	}
	key := strings.TrimPrefix(results[0].FilePath, "storage://")
	raw, err := inner.Load(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := io.ReadAll(raw)
	closeErr := raw.Close()
	if err != nil || closeErr != nil || bytes.Contains(ciphertext, []byte(secret)) {
		t.Fatalf("underlying plaintext/error %v %v", err, closeErr)
	}
	reader, err := backend.Load(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	text, err := io.ReadAll(reader)
	closeErr = reader.Close()
	if err != nil || closeErr != nil || string(text) != secret {
		t.Fatalf("roundtrip %q %v %v", text, err, closeErr)
	}
	kms.down = true
	if reader, err := backend.Load(context.Background(), key); err == nil {
		reader.Close()
		t.Fatal("disabled default cache allowed KMS outage")
	}
	metrics, err := registry.Gather()
	if err != nil || len(metrics) != 7 {
		t.Fatalf("metrics %d %v", len(metrics), err)
	}
	for _, metric := range metrics {
		if strings.Contains(metric.GetName(), "in_flight") && metric.Metric[0].Gauge.GetValue() != 0 {
			t.Fatal("operation slot leaked")
		}
	}
	for key, value := range map[string]string{"KMS_CACHE_TTL": "-1s", "STORAGE_ENCRYPTION_MAX_OBJECT_BYTES": "0", "STORAGE_ENCRYPTION_MAX_IN_FLIGHT": "-1", "STORAGE_ENCRYPTION_TIMEOUT": "0s"} {
		old := env[key]
		env[key] = value
		if _, err := storageEncryptionFromEnv(get); err == nil {
			t.Fatalf("invalid %s accepted", key)
		}
		env[key] = old
	}
	env["STORAGE_ENCRYPTION"] = ""
	if _, err := storageEncryptionFromEnv(get); err == nil {
		t.Fatal("orphan KMS settings silently ignored")
	}
	if _, err := storageEncryptionFromEnv(func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
}
