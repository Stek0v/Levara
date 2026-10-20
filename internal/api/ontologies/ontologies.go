// ontologies.go — Ontology upload and management endpoints.
//
// Extracted verbatim from internal/http/ontologies.go (Phase-2 route-group
// slice, code-modernization brief §3). The in-memory ontology cache moves
// with the file: uploads populate it and PromptSuffix reads it, so the
// cognify prompt-injection behaviour is preserved bit-for-bit.
package ontologies

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/stek0v/levara/pkg/ontology"
)

// Config carries the ontology plane's dependencies: the storage root for
// uploaded files, the shared SQL pool, and the query rewriter pair.
type Config struct {
	StoragePath string
	DB          *sql.DB

	Q    func(query string) string
	QArgs func(query string, args ...any) (string, []any)
}

// In-memory ontology cache for cognify prompt injection
var (
	ontologyCache   = map[string]*ontology.Ontology{}
	ontologyCacheMu sync.RWMutex
)

// Register wires the ontology management endpoints.
func Register(app fiber.Router, cfg Config) {
	app.Post("/ontologies", ontologyUploadHandler(cfg))
	app.Get("/ontologies", ontologyListHandler(cfg))
	app.Delete("/ontologies/:id", ontologyDeleteHandler(cfg))
}

func ontologyUploadHandler(cfg Config) fiber.Handler {
	return func(c *fiber.Ctx) error {
		file, err := c.FormFile("file")
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "file required (multipart form field 'file')"})
		}

		name := c.FormValue("name")
		if name == "" {
			name = file.Filename
		}

		// Save file. BL-2: surface MkdirAll error instead of silently
		// continuing; the SaveFile below would fail with a far less useful
		// message.
		ontDir := filepath.Join(cfg.StoragePath, "ontologies")
		if err := os.MkdirAll(ontDir, 0755); err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "mkdir: " + err.Error()})
		}
		savePath := filepath.Join(ontDir, file.Filename)

		src, err := file.Open()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "open file: " + err.Error()})
		}
		defer src.Close()

		dst, err := os.Create(savePath)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "save file: " + err.Error()})
		}
		defer dst.Close()
		io.Copy(dst, src)

		// Parse ontology to extract class/individual counts
		var classesCount, individualsCount int
		parsed, parseErr := ontology.LoadFromFile(savePath)
		if parseErr == nil && parsed != nil {
			classesCount = len(parsed.Classes)
			individualsCount = len(parsed.Individuals)
			// Cache for cognify prompt injection
			ontologyCacheMu.Lock()
			ontologyCache[name] = parsed
			ontologyCacheMu.Unlock()
		}

		// Store in PostgreSQL
		id := uuid.New().String()
		format := "rdf/xml"
		if filepath.Ext(file.Filename) == ".ttl" {
			format = "turtle"
		}

		if cfg.DB != nil {
			upsertSQL, upsertArgs := cfg.QArgs(`INSERT INTO ontologies (id, name, file_path, format, classes_count, individuals_count, created_at)
				 VALUES ($1, $2, $3, $4, $5, $6, $7)
				 ON CONFLICT (name) DO UPDATE SET file_path = $3, format = $4, classes_count = $5, individuals_count = $6`,
				id, name, savePath, format, classesCount, individualsCount, time.Now().UTC())
			cfg.DB.ExecContext(context.Background(), upsertSQL, upsertArgs...)
		}

		return c.Status(201).JSON(fiber.Map{
			"id":                id,
			"name":              name,
			"format":            format,
			"path":              savePath,
			"classes_count":     classesCount,
			"individuals_count": individualsCount,
		})
	}
}

func ontologyDeleteHandler(cfg Config) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Params("id")
		if cfg.DB != nil {
			var fp string
			cfg.DB.QueryRowContext(context.Background(), cfg.Q("SELECT file_path FROM ontologies WHERE id = $1"), id).Scan(&fp)
			if fp != "" {
				os.Remove(fp)
			}
			cfg.DB.ExecContext(context.Background(), cfg.Q("DELETE FROM ontologies WHERE id = $1"), id)
		}
		return c.SendStatus(204)
	}
}

func ontologyListHandler(cfg Config) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.JSON([]any{})
		}
		rows, err := cfg.DB.QueryContext(context.Background(),
			cfg.Q("SELECT id, name, file_path, format, COALESCE(classes_count,0), COALESCE(individuals_count,0), created_at FROM ontologies ORDER BY created_at"))
		if err != nil {
			return c.JSON([]any{})
		}
		defer rows.Close()
		var items []fiber.Map
		for rows.Next() {
			var id, name, fp, format string
			var classesCount, individualsCount int
			var ca time.Time
			rows.Scan(&id, &name, &fp, &format, &classesCount, &individualsCount, &ca)
			items = append(items, fiber.Map{
				"id": id, "name": name, "file_path": fp, "format": format,
				"classes_count": classesCount, "individuals_count": individualsCount,
				"created_at": ca.Format(time.RFC3339),
			})
		}
		if items == nil {
			items = []fiber.Map{}
		}
		return c.JSON(items)
	}
}

// PromptSuffix returns an LLM prompt addition for ontology-guided
// extraction. If ontology loaded for this collection → constrains entity
// types. If not → returns "" (no constraint).
func PromptSuffix(collection string) string {
	ontologyCacheMu.RLock()
	ont := ontologyCache[collection]
	ontologyCacheMu.RUnlock()

	if ont == nil || len(ont.Classes) == 0 {
		return ""
	}
	return "\n\nIMPORTANT: Only extract entities of these types: " +
		strings.Join(ont.ListClasses(), ", ") +
		". Ignore all other entity types."
}
