// ontologies.go — Ontology upload and management endpoints.
package http

import (
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/ontology"
)

func ontologyUploadHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		file, err := c.FormFile("file")
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "file required (multipart form field 'file')"})
		}

		name := c.FormValue("name")
		if name == "" {
			name = file.Filename
		}

		// Save file
		ontDir := filepath.Join(cfg.StoragePath, "ontologies")
		os.MkdirAll(ontDir, 0755)
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
		}

		// Store in PostgreSQL
		id := uuid.New().String()
		format := "rdf/xml"
		if filepath.Ext(file.Filename) == ".ttl" {
			format = "turtle"
		}

		if cfg.DB != nil {
			upsertSQL, upsertArgs := QArgs(`INSERT INTO ontologies (id, name, file_path, format, classes_count, individuals_count, created_at)
				 VALUES ($1, $2, $3, $4, $5, $6, $7)
				 ON CONFLICT (name) DO UPDATE SET file_path = $3, format = $4, classes_count = $5, individuals_count = $6`,
				id, name, savePath, format, classesCount, individualsCount, time.Now().UTC())
			cfg.DB.ExecContext(c.Context(), upsertSQL, upsertArgs...)
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

func ontologyDeleteHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Params("id")
		if cfg.DB != nil {
			var fp string
			cfg.DB.QueryRowContext(c.Context(), Q("SELECT file_path FROM ontologies WHERE id = $1"), id).Scan(&fp)
			if fp != "" {
				os.Remove(fp)
			}
			cfg.DB.ExecContext(c.Context(), Q("DELETE FROM ontologies WHERE id = $1"), id)
		}
		return c.SendStatus(204)
	}
}

func ontologyListHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.JSON([]any{})
		}
		rows, err := cfg.DB.QueryContext(c.Context(),
			Q("SELECT id, name, file_path, format, COALESCE(classes_count,0), COALESCE(individuals_count,0), created_at FROM ontologies ORDER BY created_at"))
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
