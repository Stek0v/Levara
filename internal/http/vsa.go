package http

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/vsamemory"
)

type vsaRebuildRequest struct {
	DatasetID string `json:"dataset_id"`
	Dim       int    `json:"dim"`
	ShardSize int    `json:"shard_size"`
}

type vsaQueryResponse struct {
	TargetID   string  `json:"target_id"`
	TargetName string  `json:"target_name,omitempty"`
	EdgeID     string  `json:"edge_id"`
	Predicate  string  `json:"predicate"`
	DatasetID  string  `json:"dataset_id"`
	ShardID    string  `json:"shard_id"`
	Similarity float64 `json:"similarity"`
}

func RegisterVSAAPI(app fiber.Router, cfg APIConfig) {
	app.Get("/vsa/status", vsaStatusHandler(cfg))
	app.Post("/vsa/rebuild", vsaRebuildHandler(cfg))
	app.Get("/vsa/query", vsaQueryHandler(cfg))
}

func vsaStatusHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.JSON(fiber.Map{
				"available": false,
				"reason":    "sql graph store unavailable",
			})
		}
		stats, err := vsaStoreForDB(cfg.DB, 0, 0).Stats(c.Context())
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(stats)
	}
}

func vsaRebuildHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "sql graph store unavailable"})
		}
		var req vsaRebuildRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		store := vsaStoreForDB(cfg.DB, req.Dim, req.ShardSize)
		if err := store.RebuildFromGraph(c.Context(), req.DatasetID); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{
			"status":     "rebuilt",
			"dataset_id": req.DatasetID,
		})
	}
}

func vsaQueryHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "sql graph store unavailable"})
		}
		sourceID := c.Query("source_id")
		predicate := c.Query("predicate")
		if sourceID == "" || predicate == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "source_id and predicate are required"})
		}
		topK := 10
		if raw := c.Query("top_k"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "top_k must be a positive integer"})
			}
			topK = n
		}
		store := vsaStoreForDB(cfg.DB, 0, 0)
		if err := store.EnsureSchema(c.Context()); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}
		candidates, err := store.QueryObject(c.Context(), c.Query("dataset_id"), sourceID, predicate, topK)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}
		out := make([]vsaQueryResponse, 0, len(candidates))
		for _, candidate := range candidates {
			out = append(out, vsaQueryResponse{
				TargetID:   candidate.TargetID,
				TargetName: vsaNodeName(c.Context(), cfg.DB, candidate.TargetID, candidate.DatasetID),
				EdgeID:     candidate.EdgeID,
				Predicate:  candidate.Predicate,
				DatasetID:  candidate.DatasetID,
				ShardID:    candidate.ShardID,
				Similarity: candidate.Similarity,
			})
		}
		return c.JSON(fiber.Map{"candidates": out})
	}
}

func rebuildVSAMemory(ctx context.Context, cfg APIConfig, datasetID, source string) {
	if cfg.DB == nil {
		return
	}
	if err := vsaStoreForDB(cfg.DB, 0, 0).RebuildFromGraph(ctx, datasetID); err != nil {
		log.Printf("[vsa] rebuild after %s dataset=%q: %v", source, datasetID, err)
	}
}

func vsaStoreForDB(db *sql.DB, dim, shardSize int) *vsamemory.Store {
	dialect := vsamemory.DialectPostgres
	if GetDBProvider() == DBSQLite || looksLikeSQLite(db) {
		dialect = vsamemory.DialectSQLite
	}
	return vsamemory.NewStore(db, vsamemory.Config{
		Dim:       dim,
		ShardSize: shardSize,
		Dialect:   dialect,
	})
}

func looksLikeSQLite(db *sql.DB) bool {
	if db == nil {
		return false
	}
	var version string
	return db.QueryRow(`SELECT sqlite_version()`).Scan(&version) == nil
}

type vsaSourceNode struct {
	ID        string
	Name      string
	DatasetID string
}

func vsaGraphContext(ctx context.Context, cfg APIConfig, entityNames []string, allowedDatasetIDs []string, limit int) []string {
	if cfg.DB == nil || len(entityNames) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = 20
	}
	store := vsaStoreForDB(cfg.DB, 0, 0)
	if err := store.EnsureSchema(ctx); err != nil {
		log.Printf("[vsa] ensure schema: %v", err)
		return nil
	}

	datasets := allowedDatasetIDs
	if datasets == nil {
		ids, err := store.DatasetIDs(ctx)
		if err != nil {
			log.Printf("[vsa] list datasets: %v", err)
			return nil
		}
		datasets = ids
	}
	if len(datasets) == 0 {
		return nil
	}

	sources, err := vsaResolveSources(ctx, cfg.DB, entityNames, allowedDatasetIDs)
	if err != nil {
		log.Printf("[vsa] resolve sources: %v", err)
		return nil
	}
	if len(sources) == 0 {
		return nil
	}
	if len(sources) > 5 {
		sources = sources[:5]
	}

	var out []string
	seen := make(map[string]struct{})
	for _, source := range sources {
		for _, datasetID := range datasets {
			if allowedDatasetIDs != nil && source.DatasetID != "" && source.DatasetID != datasetID {
				continue
			}
			predicates, err := store.Predicates(ctx, datasetID)
			if err != nil {
				log.Printf("[vsa] list predicates dataset=%q: %v", datasetID, err)
				continue
			}
			if len(predicates) > 8 {
				predicates = predicates[:8]
			}
			for _, predicate := range predicates {
				candidates, err := store.QueryObject(ctx, datasetID, source.ID, predicate, 3)
				if err != nil {
					log.Printf("[vsa] query dataset=%q source=%q predicate=%q: %v", datasetID, source.ID, predicate, err)
					continue
				}
				for _, candidate := range candidates {
					targetName := vsaNodeName(ctx, cfg.DB, candidate.TargetID, datasetID)
					if targetName == "" {
						targetName = candidate.TargetID
					}
					key := source.Name + "\x00" + predicate + "\x00" + targetName
					if _, ok := seen[key]; ok {
						continue
					}
					seen[key] = struct{}{}
					out = append(out, fmt.Sprintf("%s is related to %s via %s (VSA score %.3f)", source.Name, targetName, predicate, candidate.Similarity))
					if len(out) >= limit {
						return out
					}
				}
			}
		}
	}
	return out
}

func vsaResolveSources(ctx context.Context, db *sql.DB, names []string, allowedDatasetIDs []string) ([]vsaSourceNode, error) {
	if db == nil || len(names) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(names)+len(allowedDatasetIDs))
	for _, name := range names {
		args = append(args, name)
	}
	filter := ""
	if allowedDatasetIDs != nil {
		if len(allowedDatasetIDs) == 0 {
			return nil, nil
		}
		start := len(args) + 1
		for _, id := range allowedDatasetIDs {
			args = append(args, id)
		}
		filter = " AND (dataset_id IS NULL OR dataset_id = '' OR dataset_id " + InPlaceholders(len(allowedDatasetIDs), start) + ")"
	}
	query := Q(fmt.Sprintf(`
		SELECT id, name, COALESCE(dataset_id, '')
		FROM graph_nodes
		WHERE name %s%s
		ORDER BY name, id`, InPlaceholders(len(names), 1), filter))
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		if isMissingVSADependency(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var out []vsaSourceNode
	for rows.Next() {
		var n vsaSourceNode
		if err := rows.Scan(&n.ID, &n.Name, &n.DatasetID); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func vsaNodeName(ctx context.Context, db *sql.DB, id, datasetID string) string {
	if db == nil || id == "" {
		return ""
	}
	query := Q(`
		SELECT name
		FROM graph_nodes
		WHERE id = $1 AND ($2 = '' OR dataset_id IS NULL OR dataset_id = '' OR dataset_id = $3)
		LIMIT 1`)
	var name string
	if err := db.QueryRowContext(ctx, query, id, datasetID, datasetID).Scan(&name); err != nil {
		if !errors.Is(err, sql.ErrNoRows) && !isMissingVSADependency(err) {
			log.Printf("[vsa] resolve node name id=%q: %v", id, err)
		}
		return ""
	}
	return name
}

func isMissingVSADependency(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "no such column")
}
