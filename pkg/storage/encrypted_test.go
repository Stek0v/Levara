package storage

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func encryptedFixture(t *testing.T, cfg EncryptedStorageConfig) (*EncryptedStorage, *mockS3, *kmsProtocolServer) {
	t.Helper()
	mock, server := newMockS3(t, "bucket")
	inner := newS3Client(t, server, "bucket")
	provider, kms := newKMSProtocol(t)
	cfg.WriteKeyARN = testKeyARN
	if cfg.SpoolDirectory == "" {
		cfg.SpoolDirectory = t.TempDir()
	}
	enc, err := NewEncryptedStorage(inner, kms, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return enc, mock, provider
}
func encryptedReadAll(t *testing.T, s Storage, key string) []byte {
	t.Helper()
	r, err := s.Load(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	closeErr := r.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("read=%v close=%v", err, closeErr)
	}
	return data
}
func encryptedBlob(mock *mockS3, key string) []byte {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	return append([]byte(nil), mock.objects[key]...)
}
func putEncryptedBlob(mock *mockS3, key string, data []byte) {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	mock.objects[key] = append([]byte(nil), data...)
}
func TestEncryptedStorageS3KMSRoundtripAndFreshKeys(t *testing.T) {
	enc, mock, kms := encryptedFixture(t, EncryptedStorageConfig{})
	for i, data := range [][]byte{nil, {0, 1, 0xff, 0xfe, 0}, []byte("Привет мир — секрет"), bytes.Repeat([]byte("streamed\x00"), 1<<20)} {
		key := fmt.Sprintf("документы/%d +?#%%.bin", i)
		if err := enc.Save(context.Background(), key, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		cipher := encryptedBlob(mock, key)
		if !bytes.HasPrefix(cipher, []byte(encryptedMagic)) || len(cipher) <= len(data) || len(data) > 0 && bytes.Contains(cipher, data) {
			t.Fatal("underlying S3 did not receive ciphertext envelope")
		}
		if got := encryptedReadAll(t, enc, key); !bytes.Equal(got, data) {
			t.Fatalf("roundtrip size=%d want=%d", len(got), len(data))
		}
		first := append([]byte(nil), cipher...)
		if err := enc.Save(context.Background(), key, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(first, encryptedBlob(mock, key)) {
			t.Fatal("repeated write reused ciphertext/data key")
		}
	}
	kms.mu.Lock()
	for i, a := range kms.lastPlaintexts {
		for j, b := range kms.lastPlaintexts {
			if i != j && bytes.Equal(a, b) {
				t.Error("write reused DEK")
			}
		}
	}
	kms.mu.Unlock()
	if _, ok := any(enc).(Presigner); ok {
		t.Fatal("encrypted storage exposes plaintext-bypass presign")
	}
	if _, ok := any(enc).(DirectReader); ok {
		t.Fatal("encrypted storage exposes direct reader")
	}
	if stats := enc.Stats(); stats.InFlight != 0 || stats.ReservedSpoolBytes != 0 {
		t.Fatalf("resource leak: %+v", stats)
	}
	entries, err := os.ReadDir(enc.cfg.SpoolDirectory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("spool leftovers %v %v", entries, err)
	}
}
func TestEncryptedStorageRejectsCorruptionBeforeReturningReader(t *testing.T) {
	enc, mock, kms := encryptedFixture(t, EncryptedStorageConfig{})
	key := "docs/source"
	if err := enc.Save(context.Background(), key, bytes.NewReader(bytes.Repeat([]byte("payload"), 20000))); err != nil {
		t.Fatal(err)
	}
	original := encryptedBlob(mock, key)
	offset := len(encryptedMagic) + 4 + int(binary.BigEndian.Uint32(original[len(encryptedMagic):]))
	cases := map[string][]byte{"plaintext": []byte("unprotected secret"), "empty": {}, "truncated-header": original[:12], "missing-final": original[:len(original)-1], "trailing": append(append([]byte(nil), original...), 1), "header-only": original[:offset]}
	corrupt := append([]byte(nil), original...)
	corrupt[len(corrupt)-10] ^= 1
	cases["tag"] = corrupt
	reorder := append([]byte(nil), original...)
	copy(reorder[offset:offset+100], original[offset+65568:offset+65668])
	cases["reorder"] = reorder
	headerChange := append([]byte(nil), original...)
	headerChange[14] ^= 1
	cases["header"] = headerChange
	for name, blob := range cases {
		t.Run(name, func(t *testing.T) {
			putEncryptedBlob(mock, key, blob)
			reader, err := enc.Load(context.Background(), key)
			if err == nil || reader != nil {
				if reader != nil {
					reader.Close()
				}
				t.Fatal("corrupt object yielded reader/plaintext")
			}
		})
	}
	putEncryptedBlob(mock, key, original)
	before := kms.count("Decrypt")
	putEncryptedBlob(mock, "docs/copied", original)
	if reader, err := enc.Load(context.Background(), "docs/copied"); err == nil || reader != nil {
		t.Fatal("ciphertext copied to another path decrypted")
	}
	if kms.count("Decrypt") != before {
		t.Fatal("path mismatch reached KMS")
	}
	// Empty objects are authenticated too; removing their complete DARE payload
	// must fail, even though an empty unframed DARE stream has no packets.
	if err := enc.Save(context.Background(), "empty-object", bytes.NewReader(nil)); err != nil {
		t.Fatal(err)
	}
	empty := encryptedBlob(mock, "empty-object")
	offset = len(encryptedMagic) + 4 + int(binary.BigEndian.Uint32(empty[len(encryptedMagic):]))
	putEncryptedBlob(mock, "empty-object", empty[:offset])
	if r, err := enc.Load(context.Background(), "empty-object"); err == nil || r != nil {
		t.Fatal("empty object lost authentication")
	}
}
func TestEncryptedStorageRotationReadsOnlyAllowedARNs(t *testing.T) {
	enc, mock, _ := encryptedFixture(t, EncryptedStorageConfig{AllowedReadKeyARNs: []string{testNewKeyARN}})
	if err := enc.Save(context.Background(), "old", strings.NewReader("old value")); err != nil {
		t.Fatal(err)
	}
	if err := enc.RotateWriteKey(context.Background(), testNewKeyARN); err != nil {
		t.Fatal(err)
	}
	if err := enc.Save(context.Background(), "new", strings.NewReader("new value")); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"old": "old value", "new": "new value"} {
		if got := string(encryptedReadAll(t, enc, key)); got != want {
			t.Fatal("rotation lost object")
		}
	}
	if !bytes.Contains(encryptedBlob(mock, "new"), []byte(testNewKeyARN)) {
		t.Fatal("new object did not use current write key")
	}
	restricted, err := NewEncryptedStorage(enc.inner, enc.kms, EncryptedStorageConfig{WriteKeyARN: testNewKeyARN})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := restricted.Load(context.Background(), "old"); err == nil || r != nil {
		t.Fatal("removed key reference remained readable")
	}
	if err := enc.RotateWriteKey(context.Background(), "arn:aws:kms:us-east-1:123456789012:key/not-configured"); err == nil {
		t.Fatal("rotation expanded key authority")
	}
}

type failingEncryptedSource struct{ sent bool }

func (r *failingEncryptedSource) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "some plaintext"), nil
	}
	return 0, errors.New("source failed")
}
func TestEncryptedStorageFailurePreservesObjectAndCleansSpool(t *testing.T) {
	enc, mock, _ := encryptedFixture(t, EncryptedStorageConfig{MaxObjectBytes: 32})
	if err := enc.Save(context.Background(), "object", strings.NewReader("previous")); err != nil {
		t.Fatal(err)
	}
	old := encryptedBlob(mock, "object")
	for _, source := range []io.Reader{&failingEncryptedSource{}, strings.NewReader(strings.Repeat("x", 33))} {
		if err := enc.Save(context.Background(), "object", source); err == nil {
			t.Fatal("failed input published")
		}
		if !bytes.Equal(old, encryptedBlob(mock, "object")) {
			t.Fatal("failed save replaced prior archive")
		}
	}
	entries, _ := os.ReadDir(enc.cfg.SpoolDirectory)
	if len(entries) != 0 || enc.Stats().InFlight != 0 {
		t.Fatal("failed save leaked spool/capacity")
	}
	for _, key := range []string{"../outside", "/abs", "a/../b", "a//b", "a/", "a\\b"} {
		if err := enc.Save(context.Background(), key, strings.NewReader("x")); err == nil {
			t.Fatalf("invalid path %q accepted", key)
		}
	}
}
func waitEncryptedStats(t *testing.T, enc *EncryptedStorage, predicate func(EncryptedStorageStats) bool) {
	t.Helper()
	for until := time.Now().Add(time.Second); time.Now().Before(until); {
		if predicate(enc.Stats()) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("unexpected capacity %+v", enc.Stats())
}
func TestEncryptedStorageCancellationSaturationAndCleanup(t *testing.T) {
	enc, _, _ := encryptedFixture(t, EncryptedStorageConfig{MaxInFlight: 1, MaxWaiters: 1})
	if err := enc.Save(context.Background(), "object", strings.NewReader("payload")); err != nil {
		t.Fatal(err)
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	first, err := enc.Load(firstCtx, "object")
	if err != nil {
		t.Fatal(err)
	}
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		r, err := enc.Load(secondCtx, "object")
		if r != nil {
			r.Close()
		}
		done <- err
	}()
	waitEncryptedStats(t, enc, func(s EncryptedStorageStats) bool { return s.InFlight == 1 && s.Waiters == 1 })
	if _, err := enc.Load(context.Background(), "object"); !errors.Is(err, ErrEncryptedStorageSaturated) {
		t.Fatalf("saturation=%v", err)
	}
	cancelSecond()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter cancel=%v", err)
	}
	cancelFirst()
	waitEncryptedStats(t, enc, func(s EncryptedStorageStats) bool { return s.InFlight == 0 && s.Waiters == 0 })
	first.Close()
	pipeR, pipeW := io.Pipe()
	defer pipeW.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := enc.Save(ctx, "blocked-source", pipeR); !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("blocked source cancellation=%v", err)
	}
	waitEncryptedStats(t, enc, func(s EncryptedStorageStats) bool { return s.InFlight == 0 })
}
func TestEncryptedStorageStrictEnvelopeRejectsUnknownOrForgedHeaders(t *testing.T) {
	enc, mock, _ := encryptedFixture(t, EncryptedStorageConfig{})
	if err := enc.Save(context.Background(), "object", strings.NewReader("safe")); err != nil {
		t.Fatal(err)
	}
	original := encryptedBlob(mock, "object")
	offset := 12 + int(binary.BigEndian.Uint32(original[8:12]))
	var header map[string]any
	json.Unmarshal(original[12:offset], &header)
	for _, field := range []string{"v", "algorithm", "key_arn", "wrapped_key", "extra"} {
		t.Run(field, func(t *testing.T) {
			var changed map[string]any
			json.Unmarshal(original[12:offset], &changed)
			changed[field] = "forged"
			h, _ := json.Marshal(changed)
			var b bytes.Buffer
			b.WriteString(encryptedMagic)
			binary.Write(&b, binary.BigEndian, uint32(len(h)))
			b.Write(h)
			b.Write(original[offset:])
			putEncryptedBlob(mock, "object", b.Bytes())
			if r, err := enc.Load(context.Background(), "object"); err == nil || r != nil {
				t.Fatal("forged envelope accepted")
			}
		})
	}
}

func TestEncryptedStorageKMSOutageNeverFallsBackToPlaintext(t *testing.T) {
	enc, mock, provider := encryptedFixture(t, EncryptedStorageConfig{})
	if err := enc.Save(context.Background(), "object", strings.NewReader("previous")); err != nil {
		t.Fatal(err)
	}
	old := encryptedBlob(mock, "object")
	provider.mu.Lock()
	provider.fail = true
	provider.mu.Unlock()
	if err := enc.Save(context.Background(), "object", strings.NewReader("new sensitive value")); err == nil {
		t.Fatal("KMS outage save succeeded")
	}
	if !bytes.Equal(old, encryptedBlob(mock, "object")) {
		t.Fatal("KMS outage changed object")
	}
	if r, err := enc.Load(context.Background(), "object"); err == nil || r != nil {
		t.Fatal("KMS outage leaked readable object")
	}
	if stats := enc.Stats(); stats.InFlight != 0 || stats.Waiters != 0 {
		t.Fatalf("KMS failure leaked capacity: %+v", stats)
	}
}
func TestEncryptedStorageSpoolBudgetBoundsConcurrentLoads(t *testing.T) {
	original, _, _ := encryptedFixture(t, EncryptedStorageConfig{MaxObjectBytes: 1024})
	constrained, err := NewEncryptedStorage(original.inner, original.kms, EncryptedStorageConfig{WriteKeyARN: testKeyARN, MaxObjectBytes: 1024, MaxInFlight: 4, MaxWaiters: 1, MaxSpoolBytes: original.maxCiphertext + 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := constrained.Save(context.Background(), "object", strings.NewReader("bounded")); err != nil {
		t.Fatal(err)
	}
	first, err := constrained.Load(context.Background(), "object")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if r, err := constrained.Load(ctx, "object"); r != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("spool budget allowed second reader: %v", err)
	}
	if stats := constrained.Stats(); stats.ReservedSpoolBytes > constrained.cfg.MaxSpoolBytes || stats.InFlight != 1 {
		t.Fatalf("spool budget exceeded: %+v", stats)
	}
}
