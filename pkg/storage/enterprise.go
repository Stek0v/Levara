package storage

import (
	"context"
	"io"
	"time"
)

// RetentionClass describes the delete/retention behavior an enterprise object
// backend should preserve. The empty value means default backend behavior.
type RetentionClass string

const (
	RetentionDefault    RetentionClass = ""
	RetentionTemporary  RetentionClass = "temporary"
	RetentionGovernance RetentionClass = "governance"
	RetentionCompliance RetentionClass = "compliance"
)

// ObjectMetadata is the enterprise metadata envelope for raw objects. It is
// deliberately separate from the basic Storage interface so personal/local
// deployments stay simple while corporate adapters can preserve retention,
// tenant, digest, and encryption-key facts.
type ObjectMetadata struct {
	TenantID             string         `json:"tenant_id,omitempty"`
	ProjectID            string         `json:"project_id,omitempty"`
	ContentDigest        string         `json:"content_digest,omitempty"`
	RetentionClass       RetentionClass `json:"retention_class,omitempty"`
	RetainUntil          time.Time      `json:"retain_until,omitempty"`
	LegalHold            bool           `json:"legal_hold,omitempty"`
	EncryptionKeyRef     string         `json:"encryption_key_ref,omitempty"`
	EncryptionAlgorithm  string         `json:"encryption_algorithm,omitempty"`
	EncryptedDataKeyRef  string         `json:"encrypted_data_key_ref,omitempty"`
	PlaintextKeyMaterial bool           `json:"-"`
}

// ObjectInfo is returned by metadata-aware backends.
type ObjectInfo struct {
	Path     string         `json:"path"`
	Size     int64          `json:"size,omitempty"`
	Modified time.Time      `json:"modified,omitempty"`
	Metadata ObjectMetadata `json:"metadata"`
}

// EnterpriseStorage is the optional object-storage adapter contract for Team
// and Enterprise deployments. It extends Storage instead of replacing it:
// existing Personal/Solo code can keep using Save/Load/Delete/List/Exists, while
// corporate adapters can opt into metadata, legal hold, retention, and direct
// reads.
type EnterpriseStorage interface {
	Storage
	SaveWithMetadata(ctx context.Context, path string, data io.Reader, metadata ObjectMetadata) error
	Stat(ctx context.Context, path string) (ObjectInfo, error)
	DeleteWithOptions(ctx context.Context, path string, opts DeleteOptions) error
}

type DeleteOptions struct {
	BypassGovernance bool
}

// DirectReader is implemented by enterprise backends that can issue
// time-limited direct reads while still preserving access control at the caller.
// This is intentionally separate from Presigner so local storage and existing
// S3 code do not need to grow metadata semantics immediately.
type DirectReader interface {
	PresignRead(ctx context.Context, path string, ttl time.Duration) (string, error)
}

// KMS defines the BYOK/KMS hook shape for wrapping data keys. Implementations
// must never return or log long-lived master key material. Storage adapters use
// the returned CiphertextKeyRef/KeyRef metadata, not plaintext keys.
type KMS interface {
	EncryptDataKey(ctx context.Context, req EncryptDataKeyRequest) (EncryptDataKeyResponse, error)
	DecryptDataKey(ctx context.Context, req DecryptDataKeyRequest) (DecryptDataKeyResponse, error)
	RotateKeyRef(ctx context.Context, req RotateKeyRefRequest) (RotateKeyRefResponse, error)
	KeyMetadata(ctx context.Context, keyRef string) (KeyMetadata, error)
}

type EncryptDataKeyRequest struct {
	KeyRef      string            `json:"key_ref"`
	Plaintext   []byte            `json:"-"`
	Context     map[string]string `json:"context,omitempty"`
	Algorithm   string            `json:"algorithm,omitempty"`
	RequestedBy string            `json:"requested_by,omitempty"`
}

type EncryptDataKeyResponse struct {
	CiphertextKeyRef string `json:"ciphertext_key_ref"`
	KeyRef           string `json:"key_ref"`
	Algorithm        string `json:"algorithm,omitempty"`
}

type DecryptDataKeyRequest struct {
	CiphertextKeyRef string            `json:"ciphertext_key_ref"`
	Context          map[string]string `json:"context,omitempty"`
	RequestedBy      string            `json:"requested_by,omitempty"`
}

type DecryptDataKeyResponse struct {
	Plaintext []byte `json:"-"`
	KeyRef    string `json:"key_ref"`
}

type RotateKeyRefRequest struct {
	OldKeyRef  string `json:"old_key_ref"`
	NewKeyRef  string `json:"new_key_ref"`
	ObjectPath string `json:"object_path,omitempty"`
}

type RotateKeyRefResponse struct {
	KeyRef string `json:"key_ref"`
}

type KeyMetadata struct {
	KeyRef    string    `json:"key_ref"`
	Provider  string    `json:"provider,omitempty"`
	Algorithm string    `json:"algorithm,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	RotatedAt time.Time `json:"rotated_at,omitempty"`
}
