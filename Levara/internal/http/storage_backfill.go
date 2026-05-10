package http

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/stek0v/levara/pkg/storage"
)

type StorageBackfillReport struct {
	Scanned  int `json:"scanned"`
	Migrated int `json:"migrated"`
	Skipped  int `json:"skipped"`
	Missing  int `json:"missing"`
	Failed   int `json:"failed"`
}

// BackfillRawLocationsToStorage migrates data.raw_data_location values from
// file:// paths to storage:// keys for non-local storage backends.
//
// The function is safe to run multiple times; existing storage:// rows are ignored.
func BackfillRawLocationsToStorage(ctx context.Context, cfg APIConfig, limit int) (StorageBackfillReport, error) {
	var report StorageBackfillReport
	if limit <= 0 {
		limit = 5000
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg.DB == nil || cfg.FileStorage == nil {
		return report, nil
	}
	if _, isLocal := cfg.FileStorage.(*storage.LocalStorage); isLocal {
		return report, nil
	}

	rows, err := cfg.DB.QueryContext(ctx, Q(`
		SELECT id, COALESCE(extension,''), raw_data_location
		FROM data
		WHERE raw_data_location LIKE 'file://%'
		ORDER BY id
		LIMIT $1
	`), limit)
	if err != nil {
		return report, fmt.Errorf("backfill scan: %w", err)
	}
	type candidate struct {
		id       string
		ext      string
		location string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.ext, &c.location); err != nil {
			report.Failed++
			continue
		}
		candidates = append(candidates, c)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return report, fmt.Errorf("backfill iteration: %w", err)
	}

	for _, c := range candidates {
		report.Scanned++
		dataID, ext, location := c.id, c.ext, c.location
		if !strings.HasPrefix(location, "file://") {
			report.Skipped++
			continue
		}
		localPath := strings.TrimPrefix(location, "file://")
		key := storageKeyForData(dataID, ext, localPath)

		exists, err := cfg.FileStorage.Exists(ctx, key)
		if err != nil {
			report.Failed++
			continue
		}
		if !exists {
			f, err := os.Open(localPath)
			if err != nil {
				if os.IsNotExist(err) {
					report.Missing++
				} else {
					report.Failed++
				}
				continue
			}
			saveErr := cfg.FileStorage.Save(ctx, key, f)
			closeErr := f.Close()
			if saveErr != nil {
				report.Failed++
				continue
			}
			if closeErr != nil {
				report.Failed++
				continue
			}
		}

		newLocation := storageURIPrefix + key
		_, err = cfg.DB.ExecContext(ctx, Q(`
			UPDATE data
			SET raw_data_location = $1, updated_at = NOW()
			WHERE id = $2
		`), newLocation, dataID)
		if err != nil {
			// SQLite test DBs may not have updated_at in slim schemas.
			_, fallbackErr := cfg.DB.ExecContext(ctx, Q(`
				UPDATE data
				SET raw_data_location = $1
				WHERE id = $2
			`), newLocation, dataID)
			if fallbackErr != nil {
				report.Failed++
				continue
			}
		}
		report.Migrated++
	}
	return report, nil
}
