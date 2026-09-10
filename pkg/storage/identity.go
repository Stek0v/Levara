package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
)

// DestinationIdentity binds recovery records to the constructed destination.
// Credentials and write-key rotation do not change where an object is stored.
// Custom backends must supply their identity explicitly to the ingest writer.
func DestinationIdentity(backend Storage) (string, error) {
	var parts []string
	switch s := backend.(type) {
	case *LocalStorage:
		if s == nil {
			return "", errors.New("storage destination unavailable")
		}
		root, err := filepath.Abs(s.basePath)
		if err != nil {
			return "", err
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			return "", err
		}
		parts = []string{"local", root}
	case *S3Storage:
		if s == nil {
			return "", errors.New("storage destination unavailable")
		}
		parts = []string{"s3", s.endpoint, s.region, s.bucket}
	case *EncryptedStorage:
		if s == nil {
			return "", errors.New("storage destination unavailable")
		}
		inner, err := DestinationIdentity(s.inner)
		if err != nil {
			return "", err
		}
		parts = []string{"envelope-v1", inner}
	default:
		if custom, ok := backend.(interface{ DestinationIdentity() string }); ok {
			identity := custom.DestinationIdentity()
			if identity != "" && len(identity) <= 1024 {
				parts = []string{"custom", identity}
				break
			}
		}
		return "", errors.New("storage backend requires an explicit destination identity")
	}
	raw, _ := json.Marshal(parts)
	hash := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}
