package storage

import (
	"bytes"
	"context"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/minio/sio"
)

const encryptedMagic = "LEVARAE1"
const encryptedMarker = "LEVARA-PAYLOAD-1\x00"
const encryptedAlgorithm = "DARE2-AES256-GCM-HKDF-SHA256"
const encryptedHeaderLimit = 24 << 10

var ErrEncryptedStorageSaturated = errors.New("encrypted storage: capacity exhausted")

// EncryptedStorageConfig bounds memory, ciphertext disk use and outstanding
// operations. Load keeps one slot until EOF/Close/cancellation; its deadline
// includes both verification and consumer reads. No plaintext reaches disk.
type EncryptedStorageConfig struct {
	WriteKeyARN        string
	AllowedReadKeyARNs []string
	MaxObjectBytes     int64
	MaxSpoolBytes      int64
	MaxInFlight        int
	MaxWaiters         int
	Timeout            time.Duration
	SpoolDirectory     string
}
type EncryptedStorageStats struct {
	InFlight, Waiters  int
	ReservedSpoolBytes int64
}
type EncryptedStorage struct {
	inner           Storage
	kms             KMS
	cfg             EncryptedStorageConfig
	allowed         map[string]bool
	maxCiphertext   int64
	mu              sync.Mutex
	writeKey        string
	active, waiters int
	changed         chan struct{}
}

func NewEncryptedStorage(inner Storage, kms KMS, cfg EncryptedStorageConfig) (*EncryptedStorage, error) {
	if inner == nil || kms == nil {
		return nil, errors.New("encrypted storage: storage, KMS and immutable write key ARN required")
	}
	s, err := newEncryptedStorageConfig(cfg)
	if err != nil {
		return nil, err
	}
	s.inner, s.kms = inner, kms
	return s, nil
}

// Validate checks exactly the constructor's limits without I/O or credentials.
func (cfg EncryptedStorageConfig) Validate() error {
	_, err := newEncryptedStorageConfig(cfg)
	return err
}

func newEncryptedStorageConfig(cfg EncryptedStorageConfig) (*EncryptedStorage, error) {
	if !validKeyARN(cfg.WriteKeyARN) {
		return nil, errors.New("encrypted storage: immutable write key ARN required")
	}
	if cfg.MaxObjectBytes == 0 {
		cfg.MaxObjectBytes = 64 << 20
	}
	if cfg.MaxInFlight == 0 {
		cfg.MaxInFlight = 4
	}
	if cfg.MaxWaiters == 0 {
		cfg.MaxWaiters = 16
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 2 * time.Minute
	}
	if cfg.MaxObjectBytes < 1 || cfg.MaxObjectBytes > 1<<40 || cfg.MaxInFlight < 1 || cfg.MaxInFlight > 128 || cfg.MaxWaiters < 0 || cfg.MaxWaiters > 1024 || cfg.Timeout < time.Millisecond || cfg.Timeout > time.Hour {
		return nil, errors.New("encrypted storage: invalid resource limits")
	}
	encryptedSize, err := sio.EncryptedSize(uint64(cfg.MaxObjectBytes + int64(len(encryptedMarker))))
	if err != nil {
		return nil, err
	}
	maxCiphertext := int64(encryptedSize) + int64(len(encryptedMagic)+4+encryptedHeaderLimit)
	if cfg.MaxSpoolBytes == 0 {
		cfg.MaxSpoolBytes = (maxCiphertext + 1) * int64(cfg.MaxInFlight)
	}
	if cfg.MaxSpoolBytes < maxCiphertext+1 {
		return nil, errors.New("encrypted storage: spool budget cannot hold one maximum object")
	}
	allowed := map[string]bool{cfg.WriteKeyARN: true}
	for _, key := range cfg.AllowedReadKeyARNs {
		if !validKeyARN(key) {
			return nil, errors.New("encrypted storage: immutable read key ARN required")
		}
		allowed[key] = true
	}
	return &EncryptedStorage{cfg: cfg, allowed: allowed, maxCiphertext: maxCiphertext, writeKey: cfg.WriteKeyARN, changed: make(chan struct{})}, nil
}
func (s *EncryptedStorage) Stats() EncryptedStorageStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return EncryptedStorageStats{s.active, s.waiters, int64(s.active) * (s.maxCiphertext + 1)}
}
func (s *EncryptedStorage) acquire(ctx context.Context) (func(), error) {
	s.mu.Lock()
	waiting := false
	defer func() {
		if waiting {
			s.waiters--
		}
		s.mu.Unlock()
	}()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.active < s.cfg.MaxInFlight && int64(s.active+1)*(s.maxCiphertext+1) <= s.cfg.MaxSpoolBytes {
			s.active++
			var once sync.Once
			return func() {
				once.Do(func() { s.mu.Lock(); s.active--; close(s.changed); s.changed = make(chan struct{}); s.mu.Unlock() })
			}, nil
		}
		if !waiting {
			if s.waiters >= s.cfg.MaxWaiters {
				return nil, ErrEncryptedStorageSaturated
			}
			s.waiters++
			waiting = true
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
		s.mu.Lock()
	}
}
func encryptedPath(key string) error {
	if len(key) > 1024 || key == "" || key == "." || path.IsAbs(key) || path.Clean(key) != key || strings.HasPrefix(key, "../") || strings.ContainsAny(key, "\\\x00") || !utf8.ValidString(key) {
		return errors.New("encrypted storage: canonical relative object key required")
	}
	return nil
}
func (s *EncryptedStorage) RotateWriteKey(ctx context.Context, newKeyARN string) error {
	if !s.allowed[newKeyARN] {
		return errors.New("encrypted storage: rotation target is not configured in allowed key references")
	}
	s.mu.Lock()
	old := s.writeKey
	s.mu.Unlock()
	rotated, err := s.kms.RotateKeyRef(ctx, RotateKeyRefRequest{OldKeyRef: old, NewKeyRef: newKeyARN})
	if err != nil {
		return err
	}
	if rotated.KeyRef != newKeyARN {
		return errors.New("encrypted storage: KMS returned a different rotation key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeKey != old {
		return errors.New("encrypted storage: concurrent key rotation")
	}
	s.writeKey = newKeyARN
	return nil
}

type encryptedHeader struct {
	Version    int    `json:"v"`
	Algorithm  string `json:"algorithm"`
	KeyARN     string `json:"key_arn"`
	WrappedKey string `json:"wrapped_key"`
	PathDigest string `json:"path_sha256"`
}

func objectDigest(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
func objectKMSContext(key string) map[string]string {
	return map[string]string{"object_sha256": objectDigest(key), "format_sha256": objectDigest(encryptedMagic + encryptedAlgorithm)}
}
func encryptedStreamKey(dek, header []byte, key string) ([]byte, error) {
	sum := sha256.Sum256(header)
	return hkdf.Key(sha256.New, dek, sum[:], "levara-encrypted-object-v1:"+objectDigest(key), 32)
}
func dareConfig(key []byte) sio.Config {
	return sio.Config{MinVersion: sio.Version20, MaxVersion: sio.Version20, CipherSuites: []byte{sio.AES_GCM}, Key: key}
}
func (s *EncryptedStorage) spool() (*os.File, func(), error) {
	file, err := os.CreateTemp(s.cfg.SpoolDirectory, "levara-encrypted-*")
	if err != nil {
		return nil, nil, err
	}
	// Unlink immediately where supported: even process death leaves no named
	// spool files. The descriptor still holds ciphertext until it is closed.
	name := file.Name()
	_ = os.Remove(name)
	cleanup := func() { file.Close(); _ = os.Remove(name) }
	return file, cleanup, nil
}

type encryptedContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r encryptedContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (s *EncryptedStorage) Save(ctx context.Context, key string, data io.Reader) error {
	if err := encryptedPath(key); err != nil {
		return err
	}
	if data == nil {
		return errors.New("encrypted storage: reader required")
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	stopClose := context.AfterFunc(ctx, func() {
		if closer, ok := data.(io.Closer); ok {
			_ = closer.Close()
		}
	})
	defer stopClose()
	dek := make([]byte, 32)
	if _, err = rand.Read(dek); err != nil {
		return err
	}
	defer clear(dek)
	s.mu.Lock()
	writeKey := s.writeKey
	s.mu.Unlock()
	wrapped, err := s.kms.EncryptDataKey(ctx, EncryptDataKeyRequest{KeyRef: writeKey, Plaintext: dek, Context: objectKMSContext(key), Algorithm: "SYMMETRIC_DEFAULT"})
	if err != nil {
		return err
	}
	if wrapped.KeyRef != writeKey || wrapped.CiphertextKeyRef == "" || len(wrapped.CiphertextKeyRef) > 16<<10 {
		return errors.New("encrypted storage: invalid KMS wrapping response")
	}
	header, err := json.Marshal(encryptedHeader{1, encryptedAlgorithm, writeKey, wrapped.CiphertextKeyRef, objectDigest(key)})
	if err != nil || len(header) > encryptedHeaderLimit {
		return errors.New("encrypted storage: invalid envelope")
	}
	streamKey, err := encryptedStreamKey(dek, header, key)
	if err != nil {
		return err
	}
	defer clear(streamKey)
	f, cleanup, err := s.spool()
	if err != nil {
		return err
	}
	defer cleanup()
	if _, err = f.WriteString(encryptedMagic); err != nil {
		return err
	}
	if err = binary.Write(f, binary.BigEndian, uint32(len(header))); err != nil {
		return err
	}
	if _, err = f.Write(header); err != nil {
		return err
	}
	source := &io.LimitedReader{R: encryptedContextReader{ctx, data}, N: s.cfg.MaxObjectBytes + 1}
	encrypted, err := sio.EncryptReader(io.MultiReader(strings.NewReader(encryptedMarker), source), dareConfig(streamKey))
	if err != nil {
		return err
	}
	if _, err = io.Copy(f, encryptedContextReader{ctx, encrypted}); err != nil {
		return errors.Join(err, ctx.Err())
	}
	if source.N == 0 {
		return errors.New("encrypted storage: object exceeds size limit")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	// The underlying store only sees complete authenticated ciphertext.
	return s.inner.Save(ctx, key, f)
}

func (s *EncryptedStorage) Load(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := encryptedPath(key); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	release, err := s.acquire(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	var cleanup func()
	owned := true
	defer func() {
		if owned {
			cancel()
			if cleanup != nil {
				cleanup()
			}
			release()
		}
	}()
	raw, err := s.inner.Load(ctx, key)
	if err != nil {
		return nil, err
	}
	stopClose := context.AfterFunc(ctx, func() { _ = raw.Close() })
	f, remove, err := s.spool()
	if err != nil {
		stopClose()
		raw.Close()
		return nil, err
	}
	cleanup = remove
	n, copyErr := io.Copy(f, io.LimitReader(encryptedContextReader{ctx, raw}, s.maxCiphertext+1))
	stopClose()
	closeErr := raw.Close()
	if err = errors.Join(copyErr, closeErr); err != nil {
		return nil, err
	}
	if n > s.maxCiphertext {
		return nil, errors.New("encrypted storage: ciphertext exceeds size limit")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	prefix := make([]byte, len(encryptedMagic)+4)
	if _, err = io.ReadFull(f, prefix); err != nil {
		return nil, errors.New("encrypted storage: truncated envelope")
	}
	if string(prefix[:len(encryptedMagic)]) != encryptedMagic {
		return nil, errors.New("encrypted storage: plaintext or unsupported envelope rejected")
	}
	headerLen := binary.BigEndian.Uint32(prefix[len(encryptedMagic):])
	if headerLen == 0 || headerLen > encryptedHeaderLimit {
		return nil, errors.New("encrypted storage: header size invalid")
	}
	headerBytes := make([]byte, headerLen)
	if _, err = io.ReadFull(f, headerBytes); err != nil {
		return nil, errors.New("encrypted storage: truncated header")
	}
	var header encryptedHeader
	d := json.NewDecoder(bytes.NewReader(headerBytes))
	d.DisallowUnknownFields()
	if err = d.Decode(&header); err != nil {
		return nil, errors.New("encrypted storage: invalid header")
	}
	canonical, _ := json.Marshal(header)
	if !bytes.Equal(canonical, headerBytes) || header.Version != 1 || header.Algorithm != encryptedAlgorithm || !s.allowed[header.KeyARN] || header.PathDigest != objectDigest(key) || len(header.WrappedKey) > 16<<10 {
		return nil, errors.New("encrypted storage: header or key authority mismatch")
	}
	dek, err := s.kms.DecryptDataKey(ctx, DecryptDataKeyRequest{CiphertextKeyRef: header.WrappedKey, Context: objectKMSContext(key)})
	if err != nil {
		return nil, err
	}
	defer clear(dek.Plaintext)
	if len(dek.Plaintext) != 32 || dek.KeyRef != header.KeyARN {
		return nil, errors.New("encrypted storage: unwrapped key mismatch")
	}
	streamKey, err := encryptedStreamKey(dek.Plaintext, headerBytes, key)
	if err != nil {
		return nil, err
	}
	defer clear(streamKey)
	offset := int64(len(prefix) + len(headerBytes))
	openPlain := func() (io.Reader, error) {
		r, err := sio.DecryptReader(io.NewSectionReader(f, offset, n-offset), dareConfig(streamKey))
		if err != nil {
			return nil, err
		}
		marker := make([]byte, len(encryptedMarker))
		if _, err = io.ReadFull(r, marker); err != nil {
			return nil, err
		}
		if string(marker) != encryptedMarker {
			return nil, errors.New("encrypted storage: invalid payload framing")
		}
		return r, nil
	}
	verify, err := openPlain()
	if err != nil {
		return nil, err
	}
	size, err := io.Copy(io.Discard, encryptedContextReader{ctx, verify})
	if err != nil {
		return nil, err
	}
	if size > s.cfg.MaxObjectBytes {
		return nil, errors.New("encrypted storage: plaintext exceeds size limit")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	plain, err := openPlain()
	if err != nil {
		return nil, err
	}
	reader := &encryptedReadCloser{ctx: ctx, reader: plain, cleanup: func() { cancel(); cleanup(); release() }}
	reader.mu.Lock()
	reader.stop = context.AfterFunc(ctx, func() { _ = reader.Close() })
	reader.mu.Unlock()
	owned = false
	return reader, nil
}

type encryptedReadCloser struct {
	mu      sync.Mutex
	ctx     context.Context
	reader  io.Reader
	cleanup func()
	stop    func() bool
	closed  bool
}

func (r *encryptedReadCloser) closeLocked() {
	if !r.closed {
		r.closed = true
		if r.stop != nil {
			r.stop()
		}
		r.cleanup()
	}
}
func (r *encryptedReadCloser) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ctx.Err(); err != nil {
		r.closeLocked()
		return 0, err
	}
	if r.closed {
		return 0, io.EOF
	}
	n, err := r.reader.Read(p)
	if err != nil {
		r.closeLocked()
	}
	return n, err
}
func (r *encryptedReadCloser) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeLocked()
	return nil
}
func (s *EncryptedStorage) Delete(ctx context.Context, key string) error {
	if err := encryptedPath(key); err != nil {
		return err
	}
	return s.inner.Delete(ctx, key)
}
func (s *EncryptedStorage) Exists(ctx context.Context, key string) (bool, error) {
	if err := encryptedPath(key); err != nil {
		return false, err
	}
	return s.inner.Exists(ctx, key)
}
func (s *EncryptedStorage) List(ctx context.Context, prefix string) ([]string, error) {
	if prefix != "" {
		if err := encryptedPath(strings.TrimSuffix(prefix, "/")); err != nil {
			return nil, err
		}
	}
	return s.inner.List(ctx, prefix)
}
