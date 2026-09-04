package http

import (
	"log"
	"time"

	"github.com/gofiber/fiber/v2"

	accesspkg "github.com/stek0v/levara/pkg/access"
)

// Project context & activity endpoints (block ③ of the WebUI plan).
//
// Context = active memories bound to the dataset's collection (memories
// are keyed by collection_name = dataset name). Activity = a merged feed
// of uploads and share grants. Share revocations are intentionally not
// reconstructible (no revocation log) and are omitted; chat sessions are
// omitted because interactions carry no collection linkage.

type ContextItem struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Value     string `json:"value"`
	Type      string `json:"type"`
	CreatedAt string `json:"created_at"`
}

type ActivityItem struct {
	Type      string `json:"type"` // upload | share_granted | context_add
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	CreatedAt string `json:"created_at"`
}

// timestampString normalizes a scanned timestamp (PG returns time.Time,
// SQLite returns string) into an RFC3339-ish string.
func timestampString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	case time.Time:
		return t.Format(time.RFC3339)
	}
	return ""
}

func datasetContextHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()

		dsID := c.Params("id")
		if cfg.DB == nil {
			return c.JSON([]ContextItem{})
		}
		if err := authorizeDatasetFiber(c, cfg, dsID, accesspkg.ActionRead); err != nil {
			return err
		}

		// Resolve the dataset name — memories link via collection_name.
		var name string
		if err := cfg.DB.QueryRowContext(ctx, Q(`SELECT name FROM datasets WHERE id = $1`), dsID).Scan(&name); err != nil {
			log.Printf("[datasets] context name lookup ds=%s: %v", dsID, err)
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"detail": "dataset not found"})
		}

		rows, err := cfg.DB.QueryContext(ctx,
			Q(`SELECT id, key, value, type, created_at
			   FROM memories
			   WHERE collection_name = $1 AND superseded_by = '' AND valid_until IS NULL
			   ORDER BY created_at DESC LIMIT 100`), name)
		if err != nil {
			log.Printf("[datasets] context query ds=%s: %v", dsID, err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "load context: " + err.Error()})
		}
		defer rows.Close()

		items := []ContextItem{}
		for rows.Next() {
			var it ContextItem
			var created any
			if err := rows.Scan(&it.ID, &it.Key, &it.Value, &it.Type, &created); err != nil {
				log.Printf("[datasets] context scan ds=%s: %v", dsID, err)
				continue
			}
			it.CreatedAt = timestampString(created)
			items = append(items, it)
		}
		return c.JSON(items)
	}
}

func datasetActivityHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()

		dsID := c.Params("id")
		if cfg.DB == nil {
			return c.JSON([]ActivityItem{})
		}
		if err := authorizeDatasetFiber(c, cfg, dsID, accesspkg.ActionRead); err != nil {
			return err
		}

		items := []ActivityItem{}

		// Uploads.
		rows, err := cfg.DB.QueryContext(ctx,
			Q(`SELECT d.name, COALESCE(d.pipeline_status, ''), d.created_at
			   FROM data d JOIN dataset_data dd ON d.id = dd.data_id
			   WHERE dd.dataset_id = $1 ORDER BY d.created_at DESC LIMIT 50`), dsID)
		if err != nil {
			log.Printf("[datasets] activity uploads ds=%s: %v", dsID, err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "load activity: " + err.Error()})
		}
		for rows.Next() {
			var name, status string
			var at any
			if err := rows.Scan(&name, &status, &at); err != nil {
				continue
			}
			items = append(items, ActivityItem{Type: "upload", Title: name, Detail: status, CreatedAt: timestampString(at)})
		}
		rows.Close()

		// Share grants — via the rbac.go helper (policy boundary).
		grants, err := datasetShareGrants(Q, cfg.DB, ctx, dsID)
		if err != nil {
			log.Printf("[datasets] activity shares ds=%s: %v", dsID, err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "load activity: " + err.Error()})
		}
		for _, g := range grants {
			items = append(items, ActivityItem{Type: "share_granted", Title: g.Email, Detail: g.Role, CreatedAt: timestampString(g.Created)})
		}

		// Context additions.
		var name string
		if err := cfg.DB.QueryRowContext(ctx, Q(`SELECT name FROM datasets WHERE id = $1`), dsID).Scan(&name); err == nil {
			rows, err = cfg.DB.QueryContext(ctx,
				Q(`SELECT key, type, created_at FROM memories
				   WHERE collection_name = $1 AND superseded_by = '' AND valid_until IS NULL
				   ORDER BY created_at DESC LIMIT 50`), name)
			if err == nil {
				for rows.Next() {
					var key, typ string
					var at any
					if err := rows.Scan(&key, &typ, &at); err != nil {
						continue
					}
					items = append(items, ActivityItem{Type: "context_add", Title: key, Detail: typ, CreatedAt: timestampString(at)})
				}
			}
		}
		if rows != nil {
			rows.Close()
		}

		// Merge sort desc, cap 50.
		for i := 1; i < len(items); i++ {
			for j := i; j > 0 && items[j].CreatedAt > items[j-1].CreatedAt; j-- {
				items[j], items[j-1] = items[j-1], items[j]
			}
		}
		if len(items) > 50 {
			items = items[:50]
		}
		return c.JSON(items)
	}
}
