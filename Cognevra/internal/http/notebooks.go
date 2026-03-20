// notebooks.go — Interactive notebooks CRUD + cell execution.
// Cognee advanced feature: Jupyter-like notebooks for knowledge graph exploration.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stek0v/cognevra/pkg/embed"
	"github.com/stek0v/cognevra/pipeline"
)

type NotebookDTO struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	OwnerID   string    `json:"owner_id"`
	Cells     []CellDTO `json:"cells,omitempty"`
	CreatedAt string    `json:"created_at"`
	UpdatedAt string    `json:"updated_at"`
}

type CellDTO struct {
	ID       string `json:"id"`
	Type     string `json:"cell_type"` // code, markdown, search, cognify
	Source   string `json:"source"`
	Output   string `json:"output"`
	Position int    `json:"position"`
}

func notebooksListHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.JSON([]NotebookDTO{})
		}
		ownerID, _ := c.Locals("user_id").(string)

		rows, err := cfg.DB.QueryContext(c.Context(),
			`SELECT id, title, owner_id, created_at, updated_at FROM notebooks
			 WHERE owner_id = $1 OR owner_id = '' ORDER BY updated_at DESC`, ownerID)
		if err != nil {
			return c.JSON([]NotebookDTO{})
		}
		defer rows.Close()

		var nbs []NotebookDTO
		for rows.Next() {
			var nb NotebookDTO
			var ca, ua time.Time
			rows.Scan(&nb.ID, &nb.Title, &nb.OwnerID, &ca, &ua)
			nb.CreatedAt = ca.Format(time.RFC3339)
			nb.UpdatedAt = ua.Format(time.RFC3339)
			nbs = append(nbs, nb)
		}
		if nbs == nil {
			nbs = []NotebookDTO{}
		}
		return c.JSON(nbs)
	}
}

func notebookCreateHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req struct {
			Title string `json:"title"`
		}
		c.BodyParser(&req)
		if req.Title == "" {
			req.Title = "Untitled"
		}

		id := uuid.New().String()
		ownerID, _ := c.Locals("user_id").(string)
		now := time.Now().UTC()

		if cfg.DB != nil {
			cfg.DB.ExecContext(c.Context(),
				`INSERT INTO notebooks (id, title, owner_id, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)`,
				id, req.Title, ownerID, now, now)
		}

		return c.Status(201).JSON(NotebookDTO{
			ID: id, Title: req.Title, OwnerID: ownerID,
			CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339),
		})
	}
}

func notebookGetHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Params("id")
		if cfg.DB == nil {
			return c.Status(404).JSON(fiber.Map{"error": "not found"})
		}

		var nb NotebookDTO
		var ca, ua time.Time
		err := cfg.DB.QueryRowContext(c.Context(),
			`SELECT id, title, owner_id, created_at, updated_at FROM notebooks WHERE id = $1`, id).
			Scan(&nb.ID, &nb.Title, &nb.OwnerID, &ca, &ua)
		if err != nil {
			return c.Status(404).JSON(fiber.Map{"error": "not found"})
		}
		nb.CreatedAt = ca.Format(time.RFC3339)
		nb.UpdatedAt = ua.Format(time.RFC3339)

		// Load cells
		rows, err := cfg.DB.QueryContext(c.Context(),
			`SELECT id, cell_type, source, output, position FROM notebook_cells
			 WHERE notebook_id = $1 ORDER BY position`, id)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var cell CellDTO
				rows.Scan(&cell.ID, &cell.Type, &cell.Source, &cell.Output, &cell.Position)
				nb.Cells = append(nb.Cells, cell)
			}
		}
		if nb.Cells == nil {
			nb.Cells = []CellDTO{}
		}

		return c.JSON(nb)
	}
}

func notebookUpdateHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Params("id")
		var req struct {
			Title string `json:"title"`
		}
		c.BodyParser(&req)

		if cfg.DB != nil && req.Title != "" {
			cfg.DB.ExecContext(c.Context(),
				"UPDATE notebooks SET title = $1, updated_at = NOW() WHERE id = $2", req.Title, id)
		}
		return c.JSON(fiber.Map{"id": id, "title": req.Title, "updated": true})
	}
}

func notebookDeleteHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Params("id")
		if cfg.DB != nil {
			cfg.DB.ExecContext(c.Context(), "DELETE FROM notebooks WHERE id = $1", id)
		}
		return c.JSON(fiber.Map{"deleted": true})
	}
}

func cellAddHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		nbID := c.Params("id")
		var req struct {
			Type     string `json:"cell_type"`
			Source   string `json:"source"`
			Position int    `json:"position"`
		}
		c.BodyParser(&req)
		if req.Type == "" {
			req.Type = "code"
		}

		cellID := uuid.New().String()

		if cfg.DB != nil {
			cfg.DB.ExecContext(c.Context(),
				`INSERT INTO notebook_cells (id, notebook_id, cell_type, source, position, created_at, updated_at)
				 VALUES ($1, $2, $3, $4, $5, NOW(), NOW())`,
				cellID, nbID, req.Type, req.Source, req.Position)
			cfg.DB.ExecContext(c.Context(), "UPDATE notebooks SET updated_at = NOW() WHERE id = $1", nbID)
		}

		return c.Status(201).JSON(CellDTO{
			ID: cellID, Type: req.Type, Source: req.Source, Position: req.Position,
		})
	}
}

func cellUpdateHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		cellID := c.Params("cellId")
		var req struct {
			Source   string `json:"source"`
			Type     string `json:"cell_type"`
			Position *int   `json:"position"`
		}
		c.BodyParser(&req)

		if cfg.DB != nil {
			if req.Source != "" {
				cfg.DB.ExecContext(c.Context(),
					"UPDATE notebook_cells SET source = $1, updated_at = NOW() WHERE id = $2", req.Source, cellID)
			}
			if req.Type != "" {
				cfg.DB.ExecContext(c.Context(),
					"UPDATE notebook_cells SET cell_type = $1 WHERE id = $2", req.Type, cellID)
			}
			if req.Position != nil {
				cfg.DB.ExecContext(c.Context(),
					"UPDATE notebook_cells SET position = $1 WHERE id = $2", *req.Position, cellID)
			}
		}
		return c.JSON(fiber.Map{"id": cellID, "updated": true})
	}
}

func cellDeleteHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		cellID := c.Params("cellId")
		if cfg.DB != nil {
			cfg.DB.ExecContext(c.Context(), "DELETE FROM notebook_cells WHERE id = $1", cellID)
		}
		return c.JSON(fiber.Map{"deleted": true})
	}
}

// cellRunHandler executes a notebook cell.
// Supports cell types: search, cognify, code (Python-like expressions on graph data).
func cellRunHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		cellID := c.Params("cellId")

		var cell CellDTO
		if cfg.DB != nil {
			cfg.DB.QueryRowContext(c.Context(),
				"SELECT id, cell_type, source FROM notebook_cells WHERE id = $1", cellID).
				Scan(&cell.ID, &cell.Type, &cell.Source)
		}

		if cell.Source == "" {
			var req struct {
				Source string `json:"source"`
				Type   string `json:"cell_type"`
			}
			c.BodyParser(&req)
			cell.Source = req.Source
			cell.Type = req.Type
		}

		if cell.Source == "" {
			return c.Status(400).JSON(fiber.Map{"error": "empty cell"})
		}

		var output string
		var err error

		switch cell.Type {
		case "search":
			output, err = runSearchCell(c.Context(), cfg, cell.Source)
		case "cognify":
			output = fmt.Sprintf("Cognify triggered for: %s (use POST /cognify for full pipeline)", cell.Source)
		case "markdown":
			output = cell.Source // passthrough
		default: // "code" — evaluate as graph query
			output, err = runCodeCell(c.Context(), cfg, cell.Source)
		}

		if err != nil {
			output = fmt.Sprintf("Error: %v", err)
		}

		// Save output
		if cfg.DB != nil {
			cfg.DB.ExecContext(c.Context(),
				"UPDATE notebook_cells SET output = $1, updated_at = NOW() WHERE id = $2", output, cellID)
		}

		return c.JSON(fiber.Map{
			"id":     cellID,
			"output": output,
		})
	}
}

func runSearchCell(ctx context.Context, cfg APIConfig, query string) (string, error) {
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return "[]", nil
	}

	embedClient := embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, 16, 1)
	sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections)

	colls := cfg.Collections.List()
	var results []map[string]any

	for _, coll := range colls {
		res, err := sp.SearchByText(ctx, coll, query, 5)
		if err != nil {
			continue
		}
		for _, r := range res {
			results = append(results, map[string]any{
				"id": r.ID, "score": r.Score, "collection": coll,
				"metadata": json.RawMessage(r.Metadata),
			})
		}
		if len(results) >= 10 {
			break
		}
	}

	out, _ := json.MarshalIndent(results, "", "  ")
	return string(out), nil
}

func runCodeCell(ctx context.Context, cfg APIConfig, source string) (string, error) {
	source = strings.TrimSpace(source)

	// Simple built-in commands
	switch {
	case strings.HasPrefix(source, "collections"):
		if cfg.Collections == nil {
			return "[]", nil
		}
		colls := cfg.Collections.List()
		out, _ := json.MarshalIndent(colls, "", "  ")
		return string(out), nil

	case strings.HasPrefix(source, "stats"):
		stats := map[string]any{
			"collections": 0,
			"embed_model": cfg.EmbedModel,
			"neo4j":       cfg.Neo4jCfg.Neo4jURL != "",
			"postgres":    cfg.DB != nil,
		}
		if cfg.Collections != nil {
			stats["collections"] = len(cfg.Collections.List())
		}
		out, _ := json.MarshalIndent(stats, "", "  ")
		return string(out), nil

	case strings.HasPrefix(source, "env"):
		envs := map[string]string{
			"LLM_ENDPOINT":       os.Getenv("LLM_ENDPOINT"),
			"LLM_MODEL":          os.Getenv("LLM_MODEL"),
			"EMBEDDING_ENDPOINT": os.Getenv("EMBEDDING_ENDPOINT"),
			"EMBEDDING_MODEL":    os.Getenv("EMBEDDING_MODEL"),
		}
		out, _ := json.MarshalIndent(envs, "", "  ")
		return string(out), nil

	case strings.HasPrefix(source, "search "):
		query := strings.TrimPrefix(source, "search ")
		return runSearchCell(ctx, cfg, query)

	default:
		return fmt.Sprintf("Unknown command: %s\n\nAvailable: collections, stats, env, search <query>", source), nil
	}
}
