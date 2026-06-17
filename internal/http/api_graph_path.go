// api_graph_path.go — Stage-4 HTTP API for path/traversal queries on the
// knowledge graph. Returns shortest-path edges as a flat list with cursor
// pagination and as_of temporal filtering.
package http

import (
	"context"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/stek0v/levara/pkg/graphdb"
	"github.com/stek0v/levara/pkg/graphstore"
)

// graphPathHandler exposes graphdb.PathBetween over HTTP.
//
// GET /graph/path?from=ID1&to=ID2&max_hops=N&as_of=UNIX&limit=N&cursor=...
//
//	from, to    required node IDs
//	max_hops    1..8, default 4
//	as_of       unix-seconds; 0/missing = no temporal filter
//	limit       1..1000, default 100
//	cursor      opaque continuation from previous response.next_cursor
func graphPathHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		from := c.Query("from")
		to := c.Query("to")
		if from == "" || to == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "from and to query params required",
			})
		}

		q := graphdb.PathQuery{
			From:    from,
			To:      to,
			MaxHops: parseIntDefault(c.Query("max_hops"), 0),
			AsOf:    parseInt64Default(c.Query("as_of"), 0),
			Limit:   parseIntDefault(c.Query("limit"), 0),
			Cursor:  c.Query("cursor"),
		}

		queryCtx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()

		if cfg.DB != nil {
			result, err := graphstore.NewSQLGraphStore(cfg.DB).PathBetween(queryCtx, q)
			if err != nil {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"error": err.Error(),
				})
			}
			return c.JSON(result)
		}

		if cfg.Neo4jCfg.Neo4jURL != "" {
			writer, err := graphdb.NewWriter(queryCtx, cfg.Neo4jCfg.Neo4jURL,
				cfg.Neo4jCfg.Neo4jUser, cfg.Neo4jCfg.Neo4jPassword, cfg.Neo4jCfg.Neo4jDatabase)
			if err != nil {
				return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
					"error": "neo4j connect: " + err.Error(),
				})
			}
			defer writer.Close(queryCtx)

			result, err := writer.PathBetween(queryCtx, q)
			if err != nil {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"error": err.Error(),
				})
			}
			return c.JSON(result)
		}

		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "graph store not configured",
		})
	}
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func parseInt64Default(s string, def int64) int64 {
	if s == "" {
		return def
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return n
}
