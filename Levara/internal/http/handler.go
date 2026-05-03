package http

import (
	"fmt"

	"github.com/gofiber/fiber/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stek0v/levara/internal/metrics"
	"github.com/stek0v/levara/internal/store"
)

type Handler struct {
	cluster     *store.Cluster
	collections *store.CollectionManager
	dim         int
}

func NewHandler(cluster *store.Cluster, dim int) *Handler {
	return &Handler{cluster: cluster, dim: dim}
}

// SetCollections wires a CollectionManager so HTTP write/search/delete can
// route per-collection requests to the per-tenant HNSW+Arena+WAL stacks
// instead of the shared cluster store. Call after NewHandler when the
// CollectionManager has been constructed (it depends on data dir + node id
// which are resolved later in startup).
func (h *Handler) SetCollections(cm *store.CollectionManager) {
	h.collections = cm
}

func (h *Handler) Info(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"dimension": h.dim,
		"shards":    h.cluster.NumShards(),
		"status":    "ready",
	})
}

func (h *Handler) Insert(c *fiber.Ctx) error {
	metrics.InsertRequests.Inc()
	timer := prometheus.NewTimer(metrics.InsertDuration)
	defer timer.ObserveDuration()

	var req InsertRequest

	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "cannot parse json"})
	}

	if req.ID == "" || len(req.Vector) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "id and vector are required"})
	}

	var err error
	if req.Collection != "" && h.collections != nil {
		err = h.collections.Insert(req.Collection, req.ID, req.Vector, req.Data)
	} else {
		err = h.cluster.Insert(req.ID, req.Vector, req.Data)
	}
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	metrics.TotalVectors.Inc()
	return c.Status(fiber.StatusOK).JSON(fiber.Map{"message": "data inserted successfully"})
}

// BatchInsert handles POST /api/v1/batch_insert.
// All records are grouped by shard server-side; each shard applies its batch
// in a single Raft round-trip. For N records across S shards this means at
// most S Raft.Apply() calls instead of N, dramatically reducing write latency.
func (h *Handler) BatchInsert(c *fiber.Ctx) error {
	timer := prometheus.NewTimer(metrics.InsertDuration)
	defer timer.ObserveDuration()

	var req BatchInsertRequest

	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "cannot parse json"})
	}
	if len(req.Records) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "records array is required and must not be empty"})
	}

	metrics.InsertRequests.Add(float64(len(req.Records)))

	items := make([]store.BatchItem, 0, len(req.Records))
	for _, r := range req.Records {
		if r.ID == "" || len(r.Vector) == 0 {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("record id=%q: id and vector are required", r.ID),
			})
		}
		items = append(items, store.BatchItem{
			ID:     r.ID,
			Vector: r.Vector,
			Data:   r.Data,
		})
	}

	var errs []error
	if req.Collection != "" && h.collections != nil {
		errs = h.collections.BatchInsert(req.Collection, items)
	} else {
		errs = h.cluster.BatchInsert(items)
	}

	resp := BatchInsertResponse{
		Inserted: len(items) - len(errs),
		Failed:   len(errs),
	}
	for _, e := range errs {
		resp.Errors = append(resp.Errors, e.Error())
	}

	metrics.TotalVectors.Add(float64(resp.Inserted))

	if len(errs) > 0 {
		return c.Status(fiber.StatusMultiStatus).JSON(resp)
	}
	return c.Status(fiber.StatusOK).JSON(resp)
}

// Delete handles POST /api/v1/delete.
func (h *Handler) Delete(c *fiber.Ctx) error {
	var req DeleteRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "cannot parse json"})
	}
	if len(req.IDs) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "ids array is required"})
	}

	var errs []error
	if req.Collection != "" && h.collections != nil {
		errs = h.collections.BatchDelete(req.Collection, req.IDs)
	} else {
		errs = h.cluster.BatchDelete(req.IDs)
	}

	resp := DeleteResponse{
		Deleted: len(req.IDs) - len(errs),
		Failed:  len(errs),
	}
	for _, e := range errs {
		resp.Errors = append(resp.Errors, e.Error())
	}

	return c.JSON(resp)
}

func (h *Handler) Search(c *fiber.Ctx) error {
	metrics.SearchRequests.Inc()
	timer := prometheus.NewTimer(metrics.SearchDuration)
	defer timer.ObserveDuration()

	var req SearchRequest

	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "cannot parse json"})
	}

	if len(req.Vector) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "vector is required"})
	}

	if req.TopK <= 0 {
		req.TopK = 5 // Default TopK
	}

	var results []store.VectroRecord
	if req.Collection != "" && h.collections != nil {
		recs, err := h.collections.Search(req.Collection, req.Vector, req.TopK)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}
		results = recs
	} else {
		results = h.cluster.Search(req.Vector, req.TopK)
	}

	responseItems := make([]SearchResult, 0, len(results))
	for _, res := range results {
		responseItems = append(responseItems, SearchResult{
			ID:    res.ID,
			Score: res.Score,
			Data:  res.Data,
		})
	}

	return c.JSON(SearchResponse{Results: responseItems})
}
