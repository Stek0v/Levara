// ontologies.go — Ontology upload and management endpoints.
package http

import (
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
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

		// Store in PostgreSQL
		id := uuid.New().String()
		format := "rdf/xml"
		if filepath.Ext(file.Filename) == ".ttl" {
			format = "turtle"
		}

		if cfg.DB != nil {
			cfg.DB.ExecContext(c.Context(),
				`INSERT INTO ontologies (id, name, file_path, format, created_at) VALUES ($1, $2, $3, $4, $5)
				 ON CONFLICT (name) DO UPDATE SET file_path = $3, format = $4`,
				id, name, savePath, format, time.Now().UTC())
		}

		return c.Status(201).JSON(fiber.Map{
			"id":     id,
			"name":   name,
			"format": format,
			"path":   savePath,
		})
	}
}

func ontologyListHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.JSON([]any{})
		}
		rows, err := cfg.DB.QueryContext(c.Context(),
			"SELECT id, name, file_path, format, created_at FROM ontologies ORDER BY created_at")
		if err != nil {
			return c.JSON([]any{})
		}
		defer rows.Close()
		var items []fiber.Map
		for rows.Next() {
			var id, name, fp, format string
			var ca time.Time
			rows.Scan(&id, &name, &fp, &format, &ca)
			items = append(items, fiber.Map{
				"id": id, "name": name, "file_path": fp, "format": format,
				"created_at": ca.Format(time.RFC3339),
			})
		}
		if items == nil {
			items = []fiber.Map{}
		}
		return c.JSON(items)
	}
}
