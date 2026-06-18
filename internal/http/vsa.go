package http

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"unicode"

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
		if err := refreshPredicateSynonyms(c.Context(), cfg.DB, req.DatasetID); err != nil {
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
		return
	}
	if err := refreshPredicateSynonyms(ctx, cfg.DB, datasetID); err != nil {
		log.Printf("[vsa] refresh predicate synonyms after %s dataset=%q: %v", source, datasetID, err)
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
	items := vsaGraphContextItems(ctx, cfg, entityNames, allowedDatasetIDs, limit, "", nil)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.format())
	}
	return out
}

func vsaGraphContextItems(ctx context.Context, cfg APIConfig, entityNames []string, allowedDatasetIDs []string, limit int, queryText string, routeCandidates []dcdRouteCandidate) []graphContextItem {
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
	predicateSynonyms := loadPredicateSynonyms(ctx, cfg.DB, datasets, nil)

	var out []graphContextItem
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
			predicates = rankVSAPredicatesForQuery(predicates, queryText, predicateSynonyms)
			if len(predicates) > 8 {
				predicates = predicates[:8]
			}
			for _, predicate := range predicates {
				topK := 3
				if len(routeCandidates) > 0 {
					topK = 12
				}
				candidates, err := store.QueryObjectWithOptions(ctx, datasetID, source.ID, predicate, vsamemory.QueryOptions{
					QueryText: queryText,
					TopK:      topK,
					Rerank:    strings.TrimSpace(queryText) != "",
				})
				if err != nil {
					log.Printf("[vsa] query dataset=%q source=%q predicate=%q: %v", datasetID, source.ID, predicate, err)
					continue
				}
				candidates = rerankVSACandidatesByDCDRoute(candidates, routeCandidates)
				if len(candidates) > 3 {
					candidates = candidates[:3]
				}
				for _, candidate := range candidates {
					targetName := vsaNodeName(ctx, cfg.DB, candidate.TargetID, datasetID)
					if targetName == "" {
						targetName = candidate.TargetID
					}
					routeBoost := dcdRouteCandidateBoost(candidate, routeCandidates)
					key := source.Name + "\x00" + predicate + "\x00" + targetName
					if _, ok := seen[key]; ok {
						continue
					}
					seen[key] = struct{}{}
					out = append(out, graphContextItem{
						SourceName:   source.Name,
						Predicate:    predicate,
						TargetName:   targetName,
						DatasetID:    datasetID,
						DomainID:     candidate.DomainID,
						CollectionID: candidate.CollectionID,
						DocumentID:   candidate.DocumentID,
						Provider:     graphContextProviderVSA,
						Score:        candidateScoreWithDCDRoute(candidate, routeCandidates),
						RouteBoost:   routeBoost,
						HasRouteMeta: candidateHasDCDRouteMetadata(candidate),
					})
					if len(out) >= limit {
						return out
					}
				}
			}
		}
	}
	return out
}

func candidateHasDCDRouteMetadata(candidate vsamemory.Candidate) bool {
	return candidate.DomainID != "" || candidate.CollectionID != "" || candidate.DocumentID != ""
}

func rerankVSACandidatesByDCDRoute(candidates []vsamemory.Candidate, routes []dcdRouteCandidate) []vsamemory.Candidate {
	if len(candidates) == 0 || len(routes) == 0 {
		return candidates
	}
	out := append([]vsamemory.Candidate(nil), candidates...)
	sort.SliceStable(out, func(i, j int) bool {
		left := candidateScoreWithDCDRoute(out[i], routes)
		right := candidateScoreWithDCDRoute(out[j], routes)
		if left == right {
			if out[i].Similarity != out[j].Similarity {
				return out[i].Similarity > out[j].Similarity
			}
			return out[i].TargetID < out[j].TargetID
		}
		return left > right
	})
	return out
}

func candidateScoreWithDCDRoute(candidate vsamemory.Candidate, routes []dcdRouteCandidate) float64 {
	score := candidate.RerankScore
	if score == 0 {
		score = candidate.Similarity
	}
	return score + dcdRouteCandidateBoost(candidate, routes)
}

func dcdRouteCandidateBoost(candidate vsamemory.Candidate, routes []dcdRouteCandidate) float64 {
	var best float64
	for _, route := range routes {
		if route.DatasetID != "" && candidate.DatasetID != "" && route.DatasetID != candidate.DatasetID {
			continue
		}
		boost := 0.0
		if route.DomainID != "" && candidate.DomainID == route.DomainID {
			boost += 0.10
		}
		if route.CollectionID != "" && candidate.CollectionID == route.CollectionID {
			boost += 0.20
		}
		if route.DocumentID != "" && candidate.DocumentID == route.DocumentID {
			boost += 0.35
		}
		boost *= route.Confidence
		if boost > best {
			best = boost
		}
	}
	return best
}

func graphContextTokens(s string) map[string]struct{} {
	out := map[string]struct{}{}
	var b strings.Builder
	flush := func() {
		if b.Len() == 0 {
			return
		}
		token := b.String()
		b.Reset()
		if len(token) < 2 {
			return
		}
		for _, variant := range []string{
			token,
			strings.TrimSuffix(token, "s"),
			strings.TrimSuffix(token, "ed"),
			strings.TrimSuffix(token, "ing"),
		} {
			if len(variant) >= 2 {
				out[variant] = struct{}{}
			}
		}
	}
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
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
