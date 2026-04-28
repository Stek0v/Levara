// notebooks.go — Interactive notebooks CRUD + cell execution.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stek0v/levara/pipeline"
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
		rows, err := cfg.DB.QueryContext(context.Background(),
			Q(`SELECT id, title, owner_id FROM notebooks WHERE owner_id = $1 OR owner_id = '' ORDER BY updated_at DESC`), ownerID)
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
			cfg.DB.ExecContext(context.Background(),
				Q(`INSERT INTO notebooks (id, title, owner_id, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)`),
				id, name, ownerID, now, now)
		}
		return c.Status(201).JSON(NotebookDTO{ID: id, Name: name, Cells: []CellDTO{}, Deletable: true})
	}
}

func notebookGetHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Params("id")
		if cfg.DB == nil {
			return c.Status(404).JSON(fiber.Map{"detail": "not found"})
		}
		var name, owner string
		err := cfg.DB.QueryRowContext(context.Background(),
			Q(`SELECT id, title, owner_id FROM notebooks WHERE id = $1`), id).Scan(&id, &name, &owner)
		if err != nil {
			return c.Status(404).JSON(fiber.Map{"detail": "not found"})
		}
		nb := NotebookDTO{ID: id, Name: name, Cells: []CellDTO{}, Deletable: true}
		rows, err := cfg.DB.QueryContext(context.Background(),
			Q(`SELECT id, cell_type, source, output, position FROM notebook_cells WHERE notebook_id = $1 ORDER BY position`), id)
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
				cfg.DB.ExecContext(context.Background(), Q("UPDATE notebooks SET title = $1, updated_at = NOW() WHERE id = $2"), name, id)
			}
			// Save cells if provided
			if req.Cells != nil {
				for i, cell := range req.Cells {
					if cell.ID == "" {
						cell.ID = uuid.New().String()
					}
					upsertSQL, upsertArgs := QArgs(`INSERT INTO notebook_cells (id, notebook_id, cell_type, source, position, created_at, updated_at)
						 VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
						 ON CONFLICT (id) DO UPDATE SET cell_type = $3, source = $4, position = $5, updated_at = NOW()`,
						cell.ID, id, cell.Type, cell.Content, i)
					cfg.DB.ExecContext(context.Background(), upsertSQL, upsertArgs...)
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
			cfg.DB.ExecContext(context.Background(), Q("DELETE FROM notebooks WHERE id = $1"), id)
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
			cfg.DB.ExecContext(context.Background(),
				Q(`INSERT INTO notebook_cells (id, notebook_id, cell_type, source, position, created_at, updated_at)
				 VALUES ($1, $2, $3, $4, 0, NOW(), NOW())`),
				cellID, nbID, cellType, content)
			cfg.DB.ExecContext(context.Background(), Q("UPDATE notebooks SET updated_at = NOW() WHERE id = $1"), nbID)
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
				cfg.DB.ExecContext(context.Background(), Q("UPDATE notebook_cells SET source = $1, updated_at = NOW() WHERE id = $2"), content, cellID)
			}
			if req.Type != "" {
				cfg.DB.ExecContext(context.Background(), Q("UPDATE notebook_cells SET cell_type = $1 WHERE id = $2"), req.Type, cellID)
			}
		}
		return c.JSON(fiber.Map{"id": cellID, "updated": true})
	}
}

func cellDeleteHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		cellID := c.Params("cellId")
		if cfg.DB != nil {
			cfg.DB.ExecContext(context.Background(), Q("DELETE FROM notebook_cells WHERE id = $1"), cellID)
		}
		return c.JSON(fiber.Map{"deleted": true})
	}
}

func cellRunHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		cellID := c.Params("cellId")
		var cellType, content string

		if cfg.DB != nil {
			cfg.DB.QueryRowContext(context.Background(),
				Q("SELECT cell_type, source FROM notebook_cells WHERE id = $1"), cellID).
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
			return c.Status(400).JSON(fiber.Map{"detail": "empty cell"})
		}

		var output string
		var err error
		switch cellType {
		case "search":
			output, err = runSearchCell(context.Background(), cfg, content)
		case "cognify":
			output = fmt.Sprintf("Cognify triggered for: %s", content)
		case "markdown":
			output = content
		default:
			output, err = runCodeCell(context.Background(), cfg, content)
		}
		if err != nil {
			return c.JSON(fiber.Map{"id": cellID, "result": nil, "error": err.Error()})
		}
		if cfg.DB != nil {
			cfg.DB.ExecContext(context.Background(), Q("UPDATE notebook_cells SET output = $1, updated_at = NOW() WHERE id = $2"), output, cellID)
		}
		return c.JSON(fiber.Map{"id": cellID, "result": output, "error": nil})
	}
}

func runSearchCell(ctx context.Context, cfg APIConfig, query string) (string, error) {
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil { return "[]", nil }
	embedClient := cfg.EmbedClient
	sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections, nil)
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
		if cfg.Collections == nil {
			return "[]", nil
		}
		out, _ := json.MarshalIndent(cfg.Collections.List(), "", "  ")
		return string(out), nil

	case strings.HasPrefix(source, "datasets"):
		if cfg.DB == nil {
			return "[]", nil
		}
		rows, err := cfg.DB.QueryContext(ctx, "SELECT id, name FROM datasets ORDER BY created_at DESC LIMIT 20")
		if err != nil {
			return "[]", nil
		}
		defer rows.Close()
		var items []map[string]string
		for rows.Next() {
			var id, name string
			rows.Scan(&id, &name)
			items = append(items, map[string]string{"id": id, "name": name})
		}
		if items == nil {
			items = []map[string]string{}
		}
		out, _ := json.MarshalIndent(items, "", "  ")
		return string(out), nil

	case strings.HasPrefix(source, "stats"):
		stats := map[string]any{"collections": 0, "embed_model": cfg.EmbedModel, "neo4j": cfg.Neo4jCfg.Neo4jURL != "", "postgres": cfg.DB != nil}
		if cfg.Collections != nil {
			stats["collections"] = len(cfg.Collections.List())
		}
		out, _ := json.MarshalIndent(stats, "", "  ")
		return string(out), nil

	case strings.HasPrefix(source, "env"):
		envs := map[string]string{"LLM_ENDPOINT": os.Getenv("LLM_ENDPOINT"), "LLM_MODEL": os.Getenv("LLM_MODEL"), "EMBEDDING_ENDPOINT": os.Getenv("EMBEDDING_ENDPOINT"), "EMBEDDING_MODEL": os.Getenv("EMBEDDING_MODEL")}
		out, _ := json.MarshalIndent(envs, "", "  ")
		return string(out), nil

	case strings.HasPrefix(source, "search "):
		return runSearchCell(ctx, cfg, strings.TrimPrefix(source, "search "))

	case strings.HasPrefix(source, "graph "):
		query := strings.TrimPrefix(source, "graph ")
		if cfg.DB == nil {
			return "[]", nil
		}
		graphSQL, graphArgs := QArgs("SELECT id, name, type, description FROM graph_nodes WHERE name ILIKE $1 OR description ILIKE $1 LIMIT 10",
			"%"+query+"%")
		rows, err := cfg.DB.QueryContext(ctx, graphSQL, graphArgs...)
		if err != nil {
			return "[]", nil
		}
		defer rows.Close()
		var items []map[string]string
		for rows.Next() {
			var id, name, nodeType, desc string
			rows.Scan(&id, &name, &nodeType, &desc)
			items = append(items, map[string]string{"id": id, "name": name, "type": nodeType, "description": desc})
		}
		if items == nil {
			items = []map[string]string{}
		}
		out, _ := json.MarshalIndent(items, "", "  ")
		return string(out), nil

	case strings.HasPrefix(source, "embed "):
		text := strings.TrimPrefix(source, "embed ")
		if cfg.EmbedEndpoint == "" {
			return "", fmt.Errorf("embed-server not configured (EMBEDDING_ENDPOINT not set)")
		}
		embedClient := cfg.EmbedClient
		vecs, err := embedClient.EmbedTexts(ctx, []string{text})
		if err != nil {
			return "", fmt.Errorf("embed error: %w", err)
		}
		if len(vecs) == 0 {
			return "[]", nil
		}
		result := map[string]any{"text": text, "dim": len(vecs[0]), "vector": vecs[0]}
		out, _ := json.MarshalIndent(result, "", "  ")
		return string(out), nil

	case strings.HasPrefix(source, "count "):
		collName := strings.TrimPrefix(source, "count ")
		if cfg.Collections == nil {
			return "0", nil
		}
		db, err := cfg.Collections.Get(collName)
		if err != nil {
			return "0", fmt.Errorf("collection %q not found", collName)
		}
		return fmt.Sprintf("%d", db.Count()), nil

	case source == "info":
		info := map[string]any{
			"version":        "levara/1.0",
			"uptime":         time.Since(serverStartTime).String(),
			"embed_endpoint": cfg.EmbedEndpoint,
			"embed_model":    cfg.EmbedModel,
			"neo4j":          cfg.Neo4jCfg.Neo4jURL != "",
			"postgres":       cfg.DB != nil,
		}
		if cfg.Collections != nil {
			info["collections_count"] = len(cfg.Collections.List())
		}
		// Check embed-server health
		if cfg.EmbedEndpoint != "" {
			resp, err := http.Get(cfg.EmbedEndpoint + "/health")
			if err == nil {
				resp.Body.Close()
				info["embed_status"] = resp.StatusCode == 200
			} else {
				info["embed_status"] = false
			}
		}
		out, _ := json.MarshalIndent(info, "", "  ")
		return string(out), nil

	case source == "help" || source == "?":
		return "Available commands:\n" +
			"  collections          — list vector collections\n" +
			"  datasets             — list datasets from PostgreSQL\n" +
			"  stats                — system statistics\n" +
			"  env                  — environment variables\n" +
			"  search <query>       — vector search across collections\n" +
			"  graph <query>        — search graph nodes by name/description\n" +
			"  embed <text>         — get embedding vector for text\n" +
			"  count <collection>   — record count in collection\n" +
			"  info                 — server info (version, uptime, services)\n" +
			"  help                 — this message", nil

	default:
		return fmt.Sprintf("Unknown command: %s\n\nType 'help' to see available commands.", source), nil
	}
}

// serverStartTime tracks when the server was initialized (used by info command).
var serverStartTime = time.Now()
