// notebooks.go — Interactive notebooks CRUD + cell execution.
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

// DTOs match frontend types exactly: Notebook{id, name, cells[], deletable}, Cell{id, name, type, content}
type NotebookDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Cells     []CellDTO `json:"cells"`
	Deletable bool      `json:"deletable"`
}

type CellDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`    // "code" | "markdown"
	Content string `json:"content"`
	Result  any    `json:"result,omitempty"`
	Error   string `json:"error,omitempty"`
}

func notebooksListHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.JSON([]NotebookDTO{})
		}
		ownerID, _ := c.Locals("user_id").(string)
		rows, err := cfg.DB.QueryContext(c.Context(),
			`SELECT id, title, owner_id FROM notebooks WHERE owner_id = $1 OR owner_id = '' ORDER BY updated_at DESC`, ownerID)
		if err != nil {
			return c.JSON([]NotebookDTO{})
		}
		defer rows.Close()
		var nbs []NotebookDTO
		for rows.Next() {
			var id, name, owner string
			rows.Scan(&id, &name, &owner)
			nbs = append(nbs, NotebookDTO{ID: id, Name: name, Cells: []CellDTO{}, Deletable: true})
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
			Name  string `json:"name"`
			Title string `json:"title"` // accept both
		}
		c.BodyParser(&req)
		name := req.Name
		if name == "" {
			name = req.Title
		}
		if name == "" {
			name = "Untitled"
		}
		id := uuid.New().String()
		ownerID, _ := c.Locals("user_id").(string)
		now := time.Now().UTC()
		if cfg.DB != nil {
			cfg.DB.ExecContext(c.Context(),
				`INSERT INTO notebooks (id, title, owner_id, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)`,
				id, name, ownerID, now, now)
		}
		return c.Status(201).JSON(NotebookDTO{ID: id, Name: name, Cells: []CellDTO{}, Deletable: true})
	}
}

func notebookGetHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Params("id")
		if cfg.DB == nil {
			return c.Status(404).JSON(fiber.Map{"error": "not found"})
		}
		var name, owner string
		err := cfg.DB.QueryRowContext(c.Context(),
			`SELECT id, title, owner_id FROM notebooks WHERE id = $1`, id).Scan(&id, &name, &owner)
		if err != nil {
			return c.Status(404).JSON(fiber.Map{"error": "not found"})
		}
		nb := NotebookDTO{ID: id, Name: name, Cells: []CellDTO{}, Deletable: true}
		rows, err := cfg.DB.QueryContext(c.Context(),
			`SELECT id, cell_type, source, output, position FROM notebook_cells WHERE notebook_id = $1 ORDER BY position`, id)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var cid, ctype, content, output string
				var pos int
				rows.Scan(&cid, &ctype, &content, &output, &pos)
				nb.Cells = append(nb.Cells, CellDTO{ID: cid, Name: "", Type: ctype, Content: content})
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
			Name  string    `json:"name"`
			Title string    `json:"title"`
			Cells []CellDTO `json:"cells"`
		}
		c.BodyParser(&req)
		name := req.Name
		if name == "" {
			name = req.Title
		}
		if cfg.DB != nil {
			if name != "" {
				cfg.DB.ExecContext(c.Context(), "UPDATE notebooks SET title = $1, updated_at = NOW() WHERE id = $2", name, id)
			}
			// Save cells if provided
			if req.Cells != nil {
				for i, cell := range req.Cells {
					if cell.ID == "" {
						cell.ID = uuid.New().String()
					}
					cfg.DB.ExecContext(c.Context(),
						`INSERT INTO notebook_cells (id, notebook_id, cell_type, source, position, created_at, updated_at)
						 VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
						 ON CONFLICT (id) DO UPDATE SET cell_type = $3, source = $4, position = $5, updated_at = NOW()`,
						cell.ID, id, cell.Type, cell.Content, i)
				}
			}
		}
		return c.JSON(fiber.Map{"id": id, "name": name, "updated": true})
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
			Name    string `json:"name"`
			Type    string `json:"type"`
			Content string `json:"content"`
			// Also accept old format
			CellType string `json:"cell_type"`
			Source   string `json:"source"`
		}
		c.BodyParser(&req)
		cellType := req.Type
		if cellType == "" { cellType = req.CellType }
		if cellType == "" { cellType = "code" }
		content := req.Content
		if content == "" { content = req.Source }

		cellID := uuid.New().String()
		if cfg.DB != nil {
			cfg.DB.ExecContext(c.Context(),
				`INSERT INTO notebook_cells (id, notebook_id, cell_type, source, position, created_at, updated_at)
				 VALUES ($1, $2, $3, $4, 0, NOW(), NOW())`,
				cellID, nbID, cellType, content)
			cfg.DB.ExecContext(c.Context(), "UPDATE notebooks SET updated_at = NOW() WHERE id = $1", nbID)
		}
		return c.Status(201).JSON(CellDTO{ID: cellID, Name: req.Name, Type: cellType, Content: content})
	}
}

func cellUpdateHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		cellID := c.Params("cellId")
		var req struct {
			Content string `json:"content"`
			Source  string `json:"source"`
			Type    string `json:"type"`
		}
		c.BodyParser(&req)
		content := req.Content
		if content == "" { content = req.Source }
		if cfg.DB != nil {
			if content != "" {
				cfg.DB.ExecContext(c.Context(), "UPDATE notebook_cells SET source = $1, updated_at = NOW() WHERE id = $2", content, cellID)
			}
			if req.Type != "" {
				cfg.DB.ExecContext(c.Context(), "UPDATE notebook_cells SET cell_type = $1 WHERE id = $2", req.Type, cellID)
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

func cellRunHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		cellID := c.Params("cellId")
		var cellType, content string

		if cfg.DB != nil {
			cfg.DB.QueryRowContext(c.Context(),
				"SELECT cell_type, source FROM notebook_cells WHERE id = $1", cellID).
				Scan(&cellType, &content)
		}
		// Override from body if provided
		var req struct {
			Type    string `json:"type"`
			Content string `json:"content"`
			Source  string `json:"source"`
		}
		c.BodyParser(&req)
		if req.Content != "" { content = req.Content }
		if req.Source != "" { content = req.Source }
		if req.Type != "" { cellType = req.Type }

		if content == "" {
			return c.Status(400).JSON(fiber.Map{"error": "empty cell"})
		}

		var output string
		var err error
		switch cellType {
		case "search":
			output, err = runSearchCell(c.Context(), cfg, content)
		case "cognify":
			output = fmt.Sprintf("Cognify triggered for: %s", content)
		case "markdown":
			output = content
		default:
			output, err = runCodeCell(c.Context(), cfg, content)
		}
		if err != nil {
			return c.JSON(fiber.Map{"id": cellID, "result": nil, "error": err.Error()})
		}
		if cfg.DB != nil {
			cfg.DB.ExecContext(c.Context(), "UPDATE notebook_cells SET output = $1, updated_at = NOW() WHERE id = $2", output, cellID)
		}
		return c.JSON(fiber.Map{"id": cellID, "result": output, "error": nil})
	}
}

func runSearchCell(ctx context.Context, cfg APIConfig, query string) (string, error) {
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil { return "[]", nil }
	embedClient := embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, 16, 1)
	sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections)
	colls := cfg.Collections.List()
	var results []map[string]any
	for _, coll := range colls {
		res, err := sp.SearchByText(ctx, coll, query, 5)
		if err != nil { continue }
		for _, r := range res {
			results = append(results, map[string]any{"id": r.ID, "score": r.Score, "collection": coll, "metadata": json.RawMessage(r.Metadata)})
		}
		if len(results) >= 10 { break }
	}
	out, _ := json.MarshalIndent(results, "", "  ")
	return string(out), nil
}

func runCodeCell(ctx context.Context, cfg APIConfig, source string) (string, error) {
	source = strings.TrimSpace(source)
	switch {
	case strings.HasPrefix(source, "collections"):
		if cfg.Collections == nil { return "[]", nil }
		out, _ := json.MarshalIndent(cfg.Collections.List(), "", "  ")
		return string(out), nil
	case strings.HasPrefix(source, "stats"):
		stats := map[string]any{"collections": 0, "embed_model": cfg.EmbedModel, "neo4j": cfg.Neo4jCfg.Neo4jURL != "", "postgres": cfg.DB != nil}
		if cfg.Collections != nil { stats["collections"] = len(cfg.Collections.List()) }
		out, _ := json.MarshalIndent(stats, "", "  ")
		return string(out), nil
	case strings.HasPrefix(source, "env"):
		envs := map[string]string{"LLM_ENDPOINT": os.Getenv("LLM_ENDPOINT"), "LLM_MODEL": os.Getenv("LLM_MODEL"), "EMBEDDING_ENDPOINT": os.Getenv("EMBEDDING_ENDPOINT"), "EMBEDDING_MODEL": os.Getenv("EMBEDDING_MODEL")}
		out, _ := json.MarshalIndent(envs, "", "  ")
		return string(out), nil
	case strings.HasPrefix(source, "search "):
		return runSearchCell(ctx, cfg, strings.TrimPrefix(source, "search "))
	default:
		return fmt.Sprintf("Unknown command: %s\n\nAvailable: collections, stats, env, search <query>", source), nil
	}
}
