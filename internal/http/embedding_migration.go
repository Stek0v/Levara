package http

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/embcontract"
	"github.com/stek0v/levara/pkg/embed"
)

type embeddingMigrationRequest struct {
	SourceCollection string `json:"source_collection"`
	TargetCollection string `json:"target_collection"`
	TargetEndpoint   string `json:"target_endpoint"`
	TargetModel      string `json:"target_model"`
	TargetDim        int    `json:"target_dim"`
	TargetTokenizer  string `json:"target_tokenizer"`
	TargetPooling    string `json:"target_pooling"`
	TargetNormalize  string `json:"target_normalization"`
	TargetMetric     string `json:"target_metric"`
	BatchSize        int    `json:"batch_size"`
	MaxAttempts      int    `json:"max_attempts"`
	DryRun           bool   `json:"dry_run"`
}

type embeddingMigrationStatus struct {
	mu                 sync.Mutex `json:"-"`
	RunID              string     `json:"run_id"`
	Status             string     `json:"status"`
	SourceCollection   string     `json:"source_collection"`
	TargetCollection   string     `json:"target_collection"`
	TargetModel        string     `json:"target_model"`
	TargetDim          int        `json:"target_dim"`
	TargetVersion      string     `json:"target_version"`
	TotalRecords       int        `json:"total_records"`
	Processed          int        `json:"processed"`
	Failed             int        `json:"failed"`
	LastProcessedIndex int        `json:"last_processed_index"`
	CheckpointID       string     `json:"checkpoint_id,omitempty"`
	FailedIDs          []string   `json:"failed_ids,omitempty"`
	Attempts           int        `json:"attempts"`
	MaxAttempts        int        `json:"max_attempts"`
	ElapsedMs          int64      `json:"elapsed_ms"`
	Message            string     `json:"message,omitempty"`

	request embeddingMigrationRequest
	units   []embeddingMigrationUnit
}

type embeddingMigrationStatusSnapshot struct {
	RunID              string   `json:"run_id"`
	Status             string   `json:"status"`
	SourceCollection   string   `json:"source_collection"`
	TargetCollection   string   `json:"target_collection"`
	TargetModel        string   `json:"target_model"`
	TargetDim          int      `json:"target_dim"`
	TargetVersion      string   `json:"target_version"`
	TotalRecords       int      `json:"total_records"`
	Processed          int      `json:"processed"`
	Failed             int      `json:"failed"`
	LastProcessedIndex int      `json:"last_processed_index"`
	CheckpointID       string   `json:"checkpoint_id,omitempty"`
	FailedIDs          []string `json:"failed_ids,omitempty"`
	Attempts           int      `json:"attempts"`
	MaxAttempts        int      `json:"max_attempts"`
	ElapsedMs          int64    `json:"elapsed_ms"`
	Message            string   `json:"message,omitempty"`
}

type embeddingMigrationUnit struct {
	ID   string
	Text string
	Meta []byte
}

var embeddingMigrationRuns sync.Map

type embeddingShadowReadRequest struct {
	SourceCollection string   `json:"source_collection"`
	ShadowCollection string   `json:"shadow_collection"`
	Queries          []string `json:"queries"`
	TopK             int      `json:"top_k"`
	SourceEndpoint   string   `json:"source_endpoint"`
	ShadowEndpoint   string   `json:"shadow_endpoint"`
	SourceModel      string   `json:"source_model"`
	ShadowModel      string   `json:"shadow_model"`
}

type embeddingShadowReadRow struct {
	Query           string   `json:"query"`
	SourceIDs       []string `json:"source_ids"`
	ShadowIDs       []string `json:"shadow_ids"`
	JaccardAtK      float64  `json:"jaccard_at_k"`
	Top1Match       bool     `json:"top1_match"`
	SourceEmpty     bool     `json:"source_empty"`
	ShadowEmpty     bool     `json:"shadow_empty"`
	SourceLatencyMs int64    `json:"source_latency_ms"`
	ShadowLatencyMs int64    `json:"shadow_latency_ms"`
}

type embeddingShadowReadReport struct {
	SourceCollection string                   `json:"source_collection"`
	ShadowCollection string                   `json:"shadow_collection"`
	TopK             int                      `json:"top_k"`
	QueryCount       int                      `json:"query_count"`
	MeanJaccardAtK   float64                  `json:"mean_jaccard_at_k"`
	Top1Stability    float64                  `json:"top1_stability"`
	SourceEmptyRate  float64                  `json:"source_empty_rate"`
	ShadowEmptyRate  float64                  `json:"shadow_empty_rate"`
	SourceP50Ms      int64                    `json:"source_p50_ms"`
	ShadowP50Ms      int64                    `json:"shadow_p50_ms"`
	Rows             []embeddingShadowReadRow `json:"rows"`
}

func RegisterEmbeddingMigrationAPI(app fiber.Router, cfg APIConfig) {
	app.Post("/embedding-migrations", embeddingMigrationStartHandler(cfg))
	app.Get("/embedding-migrations/:runId/status", embeddingMigrationStatusHandler())
	app.Post("/embedding-migrations/:runId/retry", embeddingMigrationRetryHandler(cfg))
	app.Post("/embedding-migrations/shadow-read", embeddingShadowReadHandler(cfg))
}

func embeddingMigrationStartHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req embeddingMigrationRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid request"})
		}
		if err := validateEmbeddingMigrationRequest(cfg, &req); err != nil {
			return c.Status(err.Code).JSON(fiber.Map{"detail": err.Message})
		}
		run := newEmbeddingMigrationRun(req)
		embeddingMigrationRuns.Store(run.RunID, run)
		if req.DryRun {
			run.Status = "DRY_RUN"
			run.Message = "validated migration request; no records written"
			return c.JSON(run.snapshot())
		}
		go runEmbeddingMigration(context.Background(), cfg, run, nil)
		return c.JSON(run.snapshot())
	}
}

func embeddingMigrationStatusHandler() fiber.Handler {
	return func(c *fiber.Ctx) error {
		val, ok := embeddingMigrationRuns.Load(c.Params("runId"))
		if !ok {
			return c.Status(404).JSON(fiber.Map{"detail": "run not found"})
		}
		return c.JSON(val.(*embeddingMigrationStatus).snapshot())
	}
}

func embeddingMigrationRetryHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		val, ok := embeddingMigrationRuns.Load(c.Params("runId"))
		if !ok {
			return c.Status(404).JSON(fiber.Map{"detail": "run not found"})
		}
		run := val.(*embeddingMigrationStatus)
		run.mu.Lock()
		if len(run.FailedIDs) == 0 {
			run.mu.Unlock()
			return c.JSON(run.snapshot())
		}
		if run.MaxAttempts > 0 && run.Attempts >= run.MaxAttempts {
			run.mu.Unlock()
			return c.Status(409).JSON(fiber.Map{"detail": "max attempts reached"})
		}
		failed := append([]string(nil), run.FailedIDs...)
		run.Status = "RUNNING"
		run.FailedIDs = nil
		run.Failed = 0
		run.mu.Unlock()

		go runEmbeddingMigration(context.Background(), cfg, run, failed)
		return c.JSON(run.snapshot())
	}
}

func validateEmbeddingMigrationRequest(cfg APIConfig, req *embeddingMigrationRequest) *fiber.Error {
	if req.SourceCollection == "" || req.TargetCollection == "" {
		return fiber.NewError(400, "source_collection and target_collection required")
	}
	if req.SourceCollection == req.TargetCollection {
		return fiber.NewError(400, "source and target must be different")
	}
	if cfg.Collections == nil {
		return fiber.NewError(503, "collections not configured")
	}
	if !cfg.Collections.Has(req.SourceCollection) {
		return fiber.NewError(404, fmt.Sprintf("source collection %q not found", req.SourceCollection))
	}
	req.TargetEndpoint = firstNonEmpty(req.TargetEndpoint, cfg.EmbedEndpoint)
	req.TargetModel = firstNonEmpty(req.TargetModel, cfg.EmbedModel)
	if req.TargetEndpoint == "" || req.TargetModel == "" {
		return fiber.NewError(400, "target_model and embedding endpoint required")
	}
	if req.BatchSize <= 0 {
		req.BatchSize = 50
	}
	if req.MaxAttempts <= 0 {
		req.MaxAttempts = 3
	}
	return nil
}

func newEmbeddingMigrationRun(req embeddingMigrationRequest) *embeddingMigrationStatus {
	return &embeddingMigrationStatus{
		RunID:            uuid.NewString(),
		Status:           "QUEUED",
		SourceCollection: req.SourceCollection,
		TargetCollection: req.TargetCollection,
		TargetModel:      req.TargetModel,
		TargetDim:        req.TargetDim,
		Attempts:         0,
		MaxAttempts:      req.MaxAttempts,
		request:          req,
	}
}

func (s *embeddingMigrationStatus) snapshot() embeddingMigrationStatusSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return embeddingMigrationStatusSnapshot{
		RunID:              s.RunID,
		Status:             s.Status,
		SourceCollection:   s.SourceCollection,
		TargetCollection:   s.TargetCollection,
		TargetModel:        s.TargetModel,
		TargetDim:          s.TargetDim,
		TargetVersion:      s.TargetVersion,
		TotalRecords:       s.TotalRecords,
		Processed:          s.Processed,
		Failed:             s.Failed,
		LastProcessedIndex: s.LastProcessedIndex,
		CheckpointID:       s.CheckpointID,
		FailedIDs:          append([]string(nil), s.FailedIDs...),
		Attempts:           s.Attempts,
		MaxAttempts:        s.MaxAttempts,
		ElapsedMs:          s.ElapsedMs,
		Message:            s.Message,
	}
}

func runEmbeddingMigration(ctx context.Context, cfg APIConfig, run *embeddingMigrationStatus, retryIDs []string) {
	start := time.Now()
	run.mu.Lock()
	run.Status = "RUNNING"
	run.Attempts++
	run.mu.Unlock()

	units, err := loadEmbeddingMigrationUnits(cfg, run)
	if err != nil {
		run.fail(start, fmt.Sprintf("load source: %v", err))
		return
	}
	if len(retryIDs) > 0 {
		units = filterMigrationUnits(units, retryIDs)
	}
	run.mu.Lock()
	run.units = units
	run.TotalRecords = len(units)
	run.mu.Unlock()
	if len(units) == 0 {
		run.complete(start, "no records to migrate")
		return
	}

	client := embed.NewClient(run.request.TargetEndpoint, run.request.TargetModel, run.request.BatchSize, 3)
	targetDim := run.request.TargetDim
	if targetDim <= 0 {
		vecs, err := client.EmbedTexts(ctx, []string{units[0].Text})
		if err != nil || len(vecs) == 0 {
			run.fail(start, fmt.Sprintf("detect dim: %v", err))
			return
		}
		targetDim = len(vecs[0])
	}
	metric := firstNonEmpty(run.request.TargetMetric, "cosine")
	contract := embcontract.Contract{
		Encoder:       run.request.TargetModel,
		Tokenizer:     run.request.TargetTokenizer,
		Pooling:       run.request.TargetPooling,
		Normalization: run.request.TargetNormalize,
		Dim:           targetDim,
		Metric:        metric,
	}.Normalized()

	if err := ensureMigrationTargetCollection(cfg, run.request.TargetCollection, targetDim, metric, contract); err != nil {
		run.fail(start, fmt.Sprintf("prepare target: %v", err))
		return
	}
	run.mu.Lock()
	run.TargetDim = targetDim
	run.TargetVersion = contract.Fingerprint()
	run.mu.Unlock()

	for i := 0; i < len(units); i += run.request.BatchSize {
		end := i + run.request.BatchSize
		if end > len(units) {
			end = len(units)
		}
		batch := units[i:end]
		texts := make([]string, len(batch))
		for j, u := range batch {
			texts[j] = u.Text
		}
		vecs, err := client.EmbedTexts(ctx, texts)
		if err != nil {
			run.markBatchFailed(start, batch, i, fmt.Sprintf("embed batch: %v", err))
			continue
		}
		for j, vec := range vecs {
			u := batch[j]
			meta := embcontract.StampMetadata(json.RawMessage(u.Meta), contract)
			if err := cfg.Collections.Insert(run.request.TargetCollection, u.ID, vec, meta); err != nil {
				run.markFailedID(start, u.ID, i+j, fmt.Sprintf("insert: %v", err))
				continue
			}
			run.markProcessed(start, u.ID, i+j)
		}
	}

	run.mu.Lock()
	failed := run.Failed
	run.ElapsedMs = time.Since(start).Milliseconds()
	if failed > 0 {
		run.Status = "DEAD_LETTER"
		run.Message = fmt.Sprintf("%d record(s) failed; retry available until max_attempts=%d", failed, run.MaxAttempts)
	} else {
		run.Status = "COMPLETED"
		run.Message = fmt.Sprintf("migrated %d/%d records", run.Processed, run.TotalRecords)
	}
	run.mu.Unlock()
}

func loadEmbeddingMigrationUnits(cfg APIConfig, run *embeddingMigrationStatus) ([]embeddingMigrationUnit, error) {
	ids, _, metas, err := cfg.Collections.AllRecords(run.request.SourceCollection)
	if err != nil {
		return nil, err
	}
	units := make([]embeddingMigrationUnit, 0, len(ids))
	for i, id := range ids {
		text := textFromMigrationMetadata(metas[i])
		if text == "" {
			text = string(metas[i])
		}
		units = append(units, embeddingMigrationUnit{ID: id, Text: text, Meta: metas[i]})
	}
	return units, nil
}

func textFromMigrationMetadata(meta []byte) string {
	if v, ok := unwrapMem0Envelope(meta); ok {
		return v
	}
	var m map[string]any
	if json.Unmarshal(meta, &m) != nil {
		return ""
	}
	for _, key := range []string{"text", "name", "description", "content"} {
		if v, ok := m[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func filterMigrationUnits(units []embeddingMigrationUnit, ids []string) []embeddingMigrationUnit {
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	out := make([]embeddingMigrationUnit, 0, len(ids))
	for _, u := range units {
		if _, ok := want[u.ID]; ok {
			out = append(out, u)
		}
	}
	return out
}

func ensureMigrationTargetCollection(cfg APIConfig, name string, dim int, metric string, contract embcontract.Contract) error {
	if cfg.Collections.Has(name) {
		if got := cfg.Collections.Dim(name); got != dim {
			return fmt.Errorf("target collection %q dim=%d, want %d", name, got, dim)
		}
		return cfg.Collections.UpdateEmbeddingContract(name, contract)
	}
	if err := cfg.Collections.CreateWithDim(name, dim, contract.Encoder, metric); err != nil {
		return err
	}
	return cfg.Collections.UpdateEmbeddingContract(name, contract)
}

func (s *embeddingMigrationStatus) markProcessed(start time.Time, id string, idx int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Processed++
	s.LastProcessedIndex = idx
	s.CheckpointID = id
	s.ElapsedMs = time.Since(start).Milliseconds()
}

func (s *embeddingMigrationStatus) markFailedID(start time.Time, id string, idx int, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Failed++
	s.FailedIDs = append(s.FailedIDs, id)
	s.LastProcessedIndex = idx
	s.CheckpointID = id
	s.ElapsedMs = time.Since(start).Milliseconds()
	s.Message = msg
}

func (s *embeddingMigrationStatus) markBatchFailed(start time.Time, batch []embeddingMigrationUnit, idx int, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range batch {
		s.Failed++
		s.FailedIDs = append(s.FailedIDs, u.ID)
	}
	s.LastProcessedIndex = idx
	if len(batch) > 0 {
		s.CheckpointID = batch[len(batch)-1].ID
	}
	s.ElapsedMs = time.Since(start).Milliseconds()
	s.Message = msg
}

func (s *embeddingMigrationStatus) fail(start time.Time, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = "FAILED"
	s.ElapsedMs = time.Since(start).Milliseconds()
	s.Message = msg
}

func (s *embeddingMigrationStatus) complete(start time.Time, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = "COMPLETED"
	s.ElapsedMs = time.Since(start).Milliseconds()
	s.Message = msg
}

func embeddingShadowReadHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req embeddingShadowReadRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid request"})
		}
		if req.SourceCollection == "" || req.ShadowCollection == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "source_collection and shadow_collection required"})
		}
		if len(req.Queries) == 0 {
			return c.Status(400).JSON(fiber.Map{"detail": "queries required"})
		}
		if req.TopK <= 0 {
			req.TopK = 10
		}
		if cfg.Collections == nil {
			return c.Status(503).JSON(fiber.Map{"detail": "collections not configured"})
		}
		if !cfg.Collections.Has(req.SourceCollection) || !cfg.Collections.Has(req.ShadowCollection) {
			return c.Status(404).JSON(fiber.Map{"detail": "source or shadow collection not found"})
		}

		report, err := runEmbeddingShadowRead(context.Background(), cfg, req)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": err.Error()})
		}
		return c.JSON(report)
	}
}

func runEmbeddingShadowRead(ctx context.Context, cfg APIConfig, req embeddingShadowReadRequest) (embeddingShadowReadReport, error) {
	sourceMeta := cfg.Collections.GetMeta(req.SourceCollection)
	shadowMeta := cfg.Collections.GetMeta(req.ShadowCollection)
	sourceModel := firstNonEmpty(req.SourceModel, modelFromMeta(sourceMeta), cfg.EmbedModel)
	shadowModel := firstNonEmpty(req.ShadowModel, modelFromMeta(shadowMeta), cfg.EmbedModel)
	sourceEndpoint := firstNonEmpty(req.SourceEndpoint, cfg.EmbedEndpoint)
	shadowEndpoint := firstNonEmpty(req.ShadowEndpoint, cfg.EmbedEndpoint)
	if sourceEndpoint == "" || shadowEndpoint == "" || sourceModel == "" || shadowModel == "" {
		return embeddingShadowReadReport{}, errBadShadowReadConfig()
	}

	sourceClient := embed.NewClient(sourceEndpoint, sourceModel, 1, 1)
	shadowClient := embed.NewClient(shadowEndpoint, shadowModel, 1, 1)
	rows := make([]embeddingShadowReadRow, 0, len(req.Queries))
	var sourceLatencies, shadowLatencies []int64
	var jaccardSum float64
	var top1Matches, sourceEmpty, shadowEmpty int

	for _, q := range req.Queries {
		row := embeddingShadowReadRow{Query: q}

		sourceStart := time.Now()
		sourceIDs, err := embedAndSearchIDs(ctx, cfg, sourceClient, req.SourceCollection, q, req.TopK)
		row.SourceLatencyMs = time.Since(sourceStart).Milliseconds()
		if err != nil {
			sourceIDs = nil
		}

		shadowStart := time.Now()
		shadowIDs, err := embedAndSearchIDs(ctx, cfg, shadowClient, req.ShadowCollection, q, req.TopK)
		row.ShadowLatencyMs = time.Since(shadowStart).Milliseconds()
		if err != nil {
			shadowIDs = nil
		}

		row.SourceIDs = sourceIDs
		row.ShadowIDs = shadowIDs
		row.SourceEmpty = len(sourceIDs) == 0
		row.ShadowEmpty = len(shadowIDs) == 0
		row.Top1Match = len(sourceIDs) > 0 && len(shadowIDs) > 0 && sourceIDs[0] == shadowIDs[0]
		row.JaccardAtK = jaccardStrings(sourceIDs, shadowIDs)

		jaccardSum += row.JaccardAtK
		if row.Top1Match {
			top1Matches++
		}
		if row.SourceEmpty {
			sourceEmpty++
		}
		if row.ShadowEmpty {
			shadowEmpty++
		}
		sourceLatencies = append(sourceLatencies, row.SourceLatencyMs)
		shadowLatencies = append(shadowLatencies, row.ShadowLatencyMs)
		rows = append(rows, row)
	}

	n := float64(len(rows))
	return embeddingShadowReadReport{
		SourceCollection: req.SourceCollection,
		ShadowCollection: req.ShadowCollection,
		TopK:             req.TopK,
		QueryCount:       len(rows),
		MeanJaccardAtK:   jaccardSum / n,
		Top1Stability:    float64(top1Matches) / n,
		SourceEmptyRate:  float64(sourceEmpty) / n,
		ShadowEmptyRate:  float64(shadowEmpty) / n,
		SourceP50Ms:      p50Int64(sourceLatencies),
		ShadowP50Ms:      p50Int64(shadowLatencies),
		Rows:             rows,
	}, nil
}

func embedAndSearchIDs(ctx context.Context, cfg APIConfig, client *embed.Client, collection, query string, topK int) ([]string, error) {
	vec, err := client.EmbedSingle(ctx, query)
	if err != nil {
		return nil, err
	}
	recs, err := cfg.Collections.Search(collection, vec, topK)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(recs))
	for _, r := range recs {
		ids = append(ids, r.ID)
	}
	return ids, nil
}

func jaccardStrings(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	seen := make(map[string]struct{}, len(a))
	for _, id := range a {
		seen[id] = struct{}{}
	}
	intersection := 0
	union := len(seen)
	for _, id := range b {
		if _, ok := seen[id]; ok {
			intersection++
			continue
		}
		union++
	}
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func p50Int64(vals []int64) int64 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]int64(nil), vals...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return cp[len(cp)/2]
}

func modelFromMeta(meta *store.CollectionMeta) string {
	if meta == nil {
		return ""
	}
	return meta.EmbeddingModel
}

func errBadShadowReadConfig() error {
	return fiber.NewError(400, "embedding endpoint and models required for source and shadow")
}
