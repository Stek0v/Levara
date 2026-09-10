package http

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/ingest"
)

const maxStructuredArtifactBytes = 16 << 20

func structuredArtifactHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		if cfg.DB == nil {
			return fiber.NewError(404, "not found")
		}
		ref := documentRefFromFiber(c)
		if err := authorizeDocumentFiber(c, cfg, ref, accesspkg.ActionRead); err != nil {
			return err
		}
		ctx, err := documentReadContext(c, cfg, ctx, ref)
		if err != nil {
			return err
		}
		var location, artifactHash string
		var size int64
		err = cfg.DB.QueryRowContext(ctx, Q(`SELECT a.storage_location,a.artifact_sha256,a.byte_size
			FROM document_structured_artifacts a
			JOIN data d ON d.id=a.data_id
			JOIN dataset_data dd ON dd.data_id=d.id
			WHERE a.id=$1 AND a.data_id=$2 AND dd.dataset_id=$3 AND a.state='active'
			AND a.source_revision=d.source_revision AND LOWER(a.raw_content_hash)=LOWER(d.raw_content_hash)`),
			c.Params("artifactId"), ref.DataID, ref.DatasetID).Scan(&location, &artifactHash, &size)
		if errors.Is(err, sql.ErrNoRows) {
			return fiber.NewError(404, "not found")
		}
		if err != nil {
			return documentHTTPError(err)
		}
		if size < 0 || size > maxStructuredArtifactBytes {
			return fiber.NewError(422, "invalid structured artifact")
		}
		raw, err := loadStructuredArtifact(ctx, cfg, location)
		if err != nil {
			if os.IsNotExist(err) {
				return fiber.NewError(404, "not found")
			}
			return fiber.NewError(500, "load structured artifact failed")
		}
		hash := sha256.Sum256(raw)
		if int64(len(raw)) != size || !strings.EqualFold(hex.EncodeToString(hash[:]), artifactHash) || !json.Valid(raw) {
			return fiber.NewError(422, "invalid structured artifact")
		}
		if err := authorizeDocumentFiber(c, cfg, ref, accesspkg.ActionRead); err != nil {
			return err
		}
		c.Set("Cache-Control", "private, no-store")
		c.Set("Content-Type", "application/json")
		c.Set("X-Content-Type-Options", "nosniff")
		if err := c.Send(raw); err != nil {
			return err
		}
		return sendProtectedResponseWithFence(c, ctx)
	}
}

func loadStructuredArtifact(ctx context.Context, cfg APIConfig, location string) ([]byte, error) {
	var reader io.ReadCloser
	var err error
	if strings.HasPrefix(location, storageURIPrefix) {
		if cfg.FileStorage == nil {
			return nil, errors.New("file storage backend is not configured")
		}
		reader, err = cfg.FileStorage.Load(ctx, strings.TrimPrefix(location, storageURIPrefix))
	} else {
		reader, err = openRawLocal(ctx, cfg, strings.TrimPrefix(location, "file://"))
	}
	if err != nil {
		return nil, err
	}
	if reader == nil {
		return nil, errors.New("file storage returned no reader")
	}
	raw, readErr := io.ReadAll(io.LimitReader(reader, maxStructuredArtifactBytes+1))
	return raw, errors.Join(readErr, reader.Close(), ctx.Err())
}

// cleanupRetiredStructuredArtifacts is idempotent. SQL retirement closes
// access first; a backend failure leaves the row for the next retry.
func cleanupRetiredStructuredArtifacts(ctx context.Context, cfg APIConfig, dataID string) bool {
	if cfg.DB == nil {
		return false
	}
	w, err := ingest.NewMetadataWriterForStorage(cfg.DB, cfg.FileStorage)
	if err != nil {
		return true
	}
	return w.CleanupRetiredStructuredArtifacts(ctx, dataID, cfg.StoragePath, cfg.FileStorage) != nil
}
