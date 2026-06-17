package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
	EnableDualWrite  bool   `json:"enable_dual_write"`
}

type embeddingMigrationCutoverRequest struct {
	ArchiveCollection string `json:"archive_collection"`
	ArchiveSuffix     string `json:"archive_suffix"`
	RetentionDays     int    `json:"retention_days"`
}

type embeddingMigrationCutoverResponse struct {
	RunID              string `json:"run_id"`
	SourceCollection   string `json:"source_collection"`
	PromotedCollection string `json:"promoted_collection"`
	ArchiveCollection  string `json:"archive_collection"`
	RetentionUntil     string `json:"retention_until"`
	Status             string `json:"status"`
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

	request  embeddingMigrationRequest
	units    []embeddingMigrationUnit
	storeDir string
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

type embeddingMigrationPersisted struct {
	Request embeddingMigrationRequest        `json:"request"`
	Status  embeddingMigrationStatusSnapshot `json:"status"`
}

type embeddingDualWriteRule struct {
	SourceCollection string               `json:"source_collection"`
	TargetCollection string               `json:"target_collection"`
	TargetEndpoint   string               `json:"target_endpoint"`
	TargetModel      string               `json:"target_model"`
	TargetContract   embcontract.Contract `json:"target_contract"`
	Enabled          bool                 `json:"enabled"`
	UpdatedAt        string               `json:"updated_at"`
}

var (
	embeddingMigrationRuns       sync.Map
	embeddingMigrationDualWriteM sync.Mutex
)

type embeddingShadowReadRequest struct {
	SourceCollection       string   `json:"source_collection"`
	ShadowCollection       string   `json:"shadow_collection"`
	Queries                []string `json:"queries"`
	TopK                   int      `json:"top_k"`
	SourceEndpoint         string   `json:"source_endpoint"`
	ShadowEndpoint         string   `json:"shadow_endpoint"`
	SourceModel            string   `json:"source_model"`
	ShadowModel            string   `json:"shadow_model"`
	MinMeanJaccardAtK      float64  `json:"min_mean_jaccard_at_k"`
	MinTop1Stability       float64  `json:"min_top1_stability"`
	MaxShadowEmptyRate     float64  `json:"max_shadow_empty_rate"`
	MaxShadowP95LatencyMs  int64    `json:"max_shadow_p95_latency_ms"`
	MaxLatencyRatioP95     float64  `json:"max_latency_ratio_p95"`
	MaxMeanTopScoreDelta   float64  `json:"max_mean_top_score_delta"`
	RequireCutoverGatePass bool     `json:"require_cutover_gate_pass"`
}

type embeddingShadowReadRow struct {
	Query           string   `json:"query"`
	SourceIDs       []string `json:"source_ids"`
	ShadowIDs       []string `json:"shadow_ids"`
	JaccardAtK      float64  `json:"jaccard_at_k"`
	Top1Match       bool     `json:"top1_match"`
	SourceEmpty     bool     `json:"source_empty"`
	ShadowEmpty     bool     `json:"shadow_empty"`
	SourceTopScore  float32  `json:"source_top_score"`
	ShadowTopScore  float32  `json:"shadow_top_score"`
	TopScoreDelta   float64  `json:"top_score_delta"`
	SourceLatencyMs int64    `json:"source_latency_ms"`
	ShadowLatencyMs int64    `json:"shadow_latency_ms"`
}

type embeddingShadowReadReport struct {
	SourceCollection  string                   `json:"source_collection"`
	ShadowCollection  string                   `json:"shadow_collection"`
	TopK              int                      `json:"top_k"`
	QueryCount        int                      `json:"query_count"`
	MeanJaccardAtK    float64                  `json:"mean_jaccard_at_k"`
	Top1Stability     float64                  `json:"top1_stability"`
	SourceEmptyRate   float64                  `json:"source_empty_rate"`
	ShadowEmptyRate   float64                  `json:"shadow_empty_rate"`
	SourceP50Ms       int64                    `json:"source_p50_ms"`
	SourceP95Ms       int64                    `json:"source_p95_ms"`
	SourceP99Ms       int64                    `json:"source_p99_ms"`
	ShadowP50Ms       int64                    `json:"shadow_p50_ms"`
	ShadowP95Ms       int64                    `json:"shadow_p95_ms"`
	ShadowP99Ms       int64                    `json:"shadow_p99_ms"`
	MeanTopScoreDelta float64                  `json:"mean_top_score_delta"`
	CutoverReady      bool                     `json:"cutover_ready"`
	GateFailures      []string                 `json:"gate_failures,omitempty"`
	Rows              []embeddingShadowReadRow `json:"rows"`
}

func RegisterEmbeddingMigrationAPI(app fiber.Router, cfg APIConfig) {
	installEmbeddingMigrationDualWriteHook(cfg)
	app.Post("/embedding-migrations", embeddingMigrationStartHandler(cfg))
	app.Get("/embedding-migrations/:runId/status", embeddingMigrationStatusHandler(cfg))
	app.Post("/embedding-migrations/:runId/retry", embeddingMigrationRetryHandler(cfg))
	app.Post("/embedding-migrations/:runId/cutover", embeddingMigrationCutoverHandler(cfg))
	app.Get("/embedding-migrations/dual-write", embeddingMigrationDualWriteListHandler(cfg))
	app.Delete("/embedding-migrations/dual-write/:source", embeddingMigrationDualWriteDisableHandler(cfg))
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
		run := newEmbeddingMigrationRun(req, embeddingMigrationStoreDir(cfg))
		embeddingMigrationRuns.Store(run.RunID, run)
		if req.DryRun {
			run.Status = "DRY_RUN"
			run.Message = "validated migration request; no records written"
			run.persist()
			return c.JSON(run.snapshot())
		}
		run.persist()
		go runEmbeddingMigration(context.Background(), cfg, run, nil)
		return c.JSON(run.snapshot())
	}
}

func embeddingMigrationStatusHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		run, ok := loadEmbeddingMigrationRun(cfg, c.Params("runId"))
		if !ok {
			return c.Status(404).JSON(fiber.Map{"detail": "run not found"})
		}
		return c.JSON(run.snapshot())
	}
}

func embeddingMigrationRetryHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		run, ok := loadEmbeddingMigrationRun(cfg, c.Params("runId"))
		if !ok {
			return c.Status(404).JSON(fiber.Map{"detail": "run not found"})
		}
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
		run.persist()

		go runEmbeddingMigration(context.Background(), cfg, run, failed)
		return c.JSON(run.snapshot())
	}
}

func embeddingMigrationCutoverHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.Collections == nil {
			return c.Status(503).JSON(fiber.Map{"detail": "collections not configured"})
		}
		run, ok := loadEmbeddingMigrationRun(cfg, c.Params("runId"))
		if !ok {
			return c.Status(404).JSON(fiber.Map{"detail": "run not found"})
		}
		var req embeddingMigrationCutoverRequest
		_ = c.BodyParser(&req)
		run.mu.Lock()
		status := run.Status
		source := run.SourceCollection
		target := run.TargetCollection
		run.mu.Unlock()
		if status != "COMPLETED" {
			return c.Status(409).JSON(fiber.Map{"detail": "migration must be COMPLETED before cutover", "status": status})
		}
		if !cfg.Collections.Has(source) || !cfg.Collections.Has(target) {
			return c.Status(404).JSON(fiber.Map{"detail": "source or target collection not found"})
		}
		archive := firstNonEmpty(req.ArchiveCollection, source+firstNonEmpty(req.ArchiveSuffix, "__archive_"+time.Now().UTC().Format("20060102T150405Z")))
		retentionDays := req.RetentionDays
		if retentionDays <= 0 {
			retentionDays = 7
		}
		retentionUntil := time.Now().UTC().AddDate(0, 0, retentionDays)

		if err := cfg.Collections.Rename(source, archive); err != nil {
			return c.Status(409).JSON(fiber.Map{"detail": fmt.Sprintf("archive live collection: %v", err)})
		}
		if err := cfg.Collections.MarkArchive(archive, source, "embedding_migration_cutover", retentionUntil); err != nil {
			_ = cfg.Collections.Rename(archive, source)
			return c.Status(500).JSON(fiber.Map{"detail": fmt.Sprintf("mark archive: %v", err)})
		}
		if err := cfg.Collections.Rename(target, source); err != nil {
			_ = cfg.Collections.Rename(archive, source)
			return c.Status(500).JSON(fiber.Map{"detail": fmt.Sprintf("promote shadow collection: %v", err)})
		}
		removeEmbeddingDualWriteRule(cfg, source)
		run.mu.Lock()
		run.Status = "CUTOVER_COMPLETED"
		run.Message = fmt.Sprintf("promoted %s to %s; archived previous live as %s", target, source, archive)
		run.mu.Unlock()
		run.persist()
		return c.JSON(embeddingMigrationCutoverResponse{
			RunID:              run.RunID,
			SourceCollection:   source,
			PromotedCollection: target,
			ArchiveCollection:  archive,
			RetentionUntil:     retentionUntil.Format(time.RFC3339),
			Status:             "CUTOVER_COMPLETED",
		})
	}
}

func embeddingMigrationDualWriteListHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		rules := readEmbeddingDualWriteRules(embeddingMigrationStoreDir(cfg))
		out := make([]embeddingDualWriteRule, 0, len(rules))
		for _, rule := range rules {
			out = append(out, rule)
		}
		sort.Slice(out, func(i, j int) bool {
			return out[i].SourceCollection < out[j].SourceCollection
		})
		return c.JSON(fiber.Map{"rules": out})
	}
}

func embeddingMigrationDualWriteDisableHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		source := c.Params("source")
		if source == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "source required"})
		}
		removeEmbeddingDualWriteRule(cfg, source)
		return c.JSON(fiber.Map{"source_collection": source, "status": "disabled"})
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

func newEmbeddingMigrationRun(req embeddingMigrationRequest, storeDir string) *embeddingMigrationStatus {
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
		storeDir:         storeDir,
	}
}

func loadEmbeddingMigrationRun(cfg APIConfig, runID string) (*embeddingMigrationStatus, bool) {
	if val, ok := embeddingMigrationRuns.Load(runID); ok {
		return val.(*embeddingMigrationStatus), true
	}
	run, err := readEmbeddingMigrationRun(embeddingMigrationStoreDir(cfg), runID)
	if err != nil {
		return nil, false
	}
	embeddingMigrationRuns.Store(run.RunID, run)
	return run, true
}

func embeddingMigrationStoreDir(cfg APIConfig) string {
	if cfg.StoragePath == "" {
		return ""
	}
	return filepath.Join(cfg.StoragePath, "embedding_migrations")
}

func readEmbeddingMigrationRun(storeDir, runID string) (*embeddingMigrationStatus, error) {
	if storeDir == "" || runID == "" {
		return nil, os.ErrNotExist
	}
	raw, err := os.ReadFile(filepath.Join(storeDir, runID+".json"))
	if err != nil {
		return nil, err
	}
	var persisted embeddingMigrationPersisted
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return nil, err
	}
	st := persisted.Status
	return &embeddingMigrationStatus{
		RunID:              st.RunID,
		Status:             st.Status,
		SourceCollection:   st.SourceCollection,
		TargetCollection:   st.TargetCollection,
		TargetModel:        st.TargetModel,
		TargetDim:          st.TargetDim,
		TargetVersion:      st.TargetVersion,
		TotalRecords:       st.TotalRecords,
		Processed:          st.Processed,
		Failed:             st.Failed,
		LastProcessedIndex: st.LastProcessedIndex,
		CheckpointID:       st.CheckpointID,
		FailedIDs:          append([]string(nil), st.FailedIDs...),
		Attempts:           st.Attempts,
		MaxAttempts:        st.MaxAttempts,
		ElapsedMs:          st.ElapsedMs,
		Message:            st.Message,
		request:            persisted.Request,
		storeDir:           storeDir,
	}, nil
}

func (s *embeddingMigrationStatus) persist() {
	if s.storeDir == "" {
		return
	}
	payload := embeddingMigrationPersisted{
		Request: s.request,
		Status:  s.snapshot(),
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		log.Printf("[embedding-migrations] marshal run %s: %v", s.RunID, err)
		return
	}
	if err := os.MkdirAll(s.storeDir, 0755); err != nil {
		log.Printf("[embedding-migrations] mkdir %q: %v", s.storeDir, err)
		return
	}
	path := filepath.Join(s.storeDir, s.RunID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		log.Printf("[embedding-migrations] write %q: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("[embedding-migrations] rename %q -> %q: %v", tmp, path, err)
	}
}

func installEmbeddingMigrationDualWriteHook(cfg APIConfig) {
	if cfg.Collections == nil || cfg.StoragePath == "" {
		return
	}
	storeDir := embeddingMigrationStoreDir(cfg)
	cfg.Collections.SetAfterInsertHook(func(collection, id string, meta any) {
		rules := readEmbeddingDualWriteRules(storeDir)
		rule, ok := rules[collection]
		if !ok || !rule.Enabled || rule.TargetCollection == "" || rule.TargetCollection == collection {
			return
		}
		raw := rawEmbeddingMigrationMetadata(meta)
		text := textFromMigrationMetadata(raw)
		if text == "" {
			text = string(raw)
		}
		if text == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		client := embed.NewClient(rule.TargetEndpoint, rule.TargetModel, 1, 2)
		vec, err := client.EmbedSingle(ctx, text)
		if err != nil {
			log.Printf("[embedding-migrations] dual-write embed source=%s target=%s id=%s: %v", collection, rule.TargetCollection, id, err)
			return
		}
		stamped := embcontract.StampMetadata(json.RawMessage(raw), rule.TargetContract)
		if err := cfg.Collections.Insert(rule.TargetCollection, id, vec, stamped); err != nil {
			log.Printf("[embedding-migrations] dual-write insert source=%s target=%s id=%s: %v", collection, rule.TargetCollection, id, err)
		}
	})
}

func persistEmbeddingDualWriteRule(cfg APIConfig, rule embeddingDualWriteRule) {
	storeDir := embeddingMigrationStoreDir(cfg)
	if storeDir == "" {
		return
	}
	embeddingMigrationDualWriteM.Lock()
	defer embeddingMigrationDualWriteM.Unlock()
	rules := readEmbeddingDualWriteRules(storeDir)
	rules[rule.SourceCollection] = rule
	writeEmbeddingDualWriteRules(storeDir, rules)
}

func removeEmbeddingDualWriteRule(cfg APIConfig, sourceCollection string) {
	storeDir := embeddingMigrationStoreDir(cfg)
	if storeDir == "" || sourceCollection == "" {
		return
	}
	embeddingMigrationDualWriteM.Lock()
	defer embeddingMigrationDualWriteM.Unlock()
	rules := readEmbeddingDualWriteRules(storeDir)
	delete(rules, sourceCollection)
	writeEmbeddingDualWriteRules(storeDir, rules)
}

func readEmbeddingDualWriteRules(storeDir string) map[string]embeddingDualWriteRule {
	rules := map[string]embeddingDualWriteRule{}
	if storeDir == "" {
		return rules
	}
	raw, err := os.ReadFile(filepath.Join(storeDir, "dual_write_rules.json"))
	if err != nil {
		return rules
	}
	if err := json.Unmarshal(raw, &rules); err != nil {
		log.Printf("[embedding-migrations] read dual-write rules: %v", err)
		return map[string]embeddingDualWriteRule{}
	}
	return rules
}

func writeEmbeddingDualWriteRules(storeDir string, rules map[string]embeddingDualWriteRule) {
	if err := os.MkdirAll(storeDir, 0755); err != nil {
		log.Printf("[embedding-migrations] mkdir %q: %v", storeDir, err)
		return
	}
	raw, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		log.Printf("[embedding-migrations] marshal dual-write rules: %v", err)
		return
	}
	path := filepath.Join(storeDir, "dual_write_rules.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		log.Printf("[embedding-migrations] write %q: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("[embedding-migrations] rename %q -> %q: %v", tmp, path, err)
	}
}

func rawEmbeddingMigrationMetadata(meta any) []byte {
	switch v := meta.(type) {
	case nil:
		return nil
	case json.RawMessage:
		return []byte(v)
	case []byte:
		return v
	case string:
		return []byte(v)
	default:
		raw, _ := json.Marshal(v)
		return raw
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
	run.persist()

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
	run.persist()
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
	run.persist()
	if run.request.EnableDualWrite {
		persistEmbeddingDualWriteRule(cfg, embeddingDualWriteRule{
			SourceCollection: run.request.SourceCollection,
			TargetCollection: run.request.TargetCollection,
			TargetEndpoint:   run.request.TargetEndpoint,
			TargetModel:      run.request.TargetModel,
			TargetContract:   contract,
			Enabled:          true,
			UpdatedAt:        time.Now().UTC().Format(time.RFC3339),
		})
	}

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
	run.persist()
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
	s.Processed++
	s.LastProcessedIndex = idx
	s.CheckpointID = id
	s.ElapsedMs = time.Since(start).Milliseconds()
	s.mu.Unlock()
	s.persist()
}

func (s *embeddingMigrationStatus) markFailedID(start time.Time, id string, idx int, msg string) {
	s.mu.Lock()
	s.Failed++
	s.FailedIDs = append(s.FailedIDs, id)
	s.LastProcessedIndex = idx
	s.CheckpointID = id
	s.ElapsedMs = time.Since(start).Milliseconds()
	s.Message = msg
	s.mu.Unlock()
	s.persist()
}

func (s *embeddingMigrationStatus) markBatchFailed(start time.Time, batch []embeddingMigrationUnit, idx int, msg string) {
	s.mu.Lock()
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
	s.mu.Unlock()
	s.persist()
}

func (s *embeddingMigrationStatus) fail(start time.Time, msg string) {
	s.mu.Lock()
	s.Status = "FAILED"
	s.ElapsedMs = time.Since(start).Milliseconds()
	s.Message = msg
	s.mu.Unlock()
	s.persist()
}

func (s *embeddingMigrationStatus) complete(start time.Time, msg string) {
	s.mu.Lock()
	s.Status = "COMPLETED"
	s.ElapsedMs = time.Since(start).Milliseconds()
	s.Message = msg
	s.mu.Unlock()
	s.persist()
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
		if req.RequireCutoverGatePass && !report.CutoverReady {
			return c.Status(409).JSON(report)
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
	var jaccardSum, topScoreDeltaSum float64
	var top1Matches, sourceEmpty, shadowEmpty int

	for _, q := range req.Queries {
		row := embeddingShadowReadRow{Query: q}

		sourceStart := time.Now()
		sourceResult, err := embedAndSearchIDs(ctx, cfg, sourceClient, req.SourceCollection, q, req.TopK)
		row.SourceLatencyMs = time.Since(sourceStart).Milliseconds()
		if err != nil {
			sourceResult.IDs = nil
		}

		shadowStart := time.Now()
		shadowResult, err := embedAndSearchIDs(ctx, cfg, shadowClient, req.ShadowCollection, q, req.TopK)
		row.ShadowLatencyMs = time.Since(shadowStart).Milliseconds()
		if err != nil {
			shadowResult.IDs = nil
		}

		row.SourceIDs = sourceResult.IDs
		row.ShadowIDs = shadowResult.IDs
		row.SourceTopScore = sourceResult.TopScore
		row.ShadowTopScore = shadowResult.TopScore
		row.SourceEmpty = len(sourceResult.IDs) == 0
		row.ShadowEmpty = len(shadowResult.IDs) == 0
		row.Top1Match = len(sourceResult.IDs) > 0 && len(shadowResult.IDs) > 0 && sourceResult.IDs[0] == shadowResult.IDs[0]
		row.JaccardAtK = jaccardStrings(sourceResult.IDs, shadowResult.IDs)
		row.TopScoreDelta = absFloat64(float64(row.SourceTopScore - row.ShadowTopScore))

		jaccardSum += row.JaccardAtK
		topScoreDeltaSum += row.TopScoreDelta
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
	report := embeddingShadowReadReport{
		SourceCollection:  req.SourceCollection,
		ShadowCollection:  req.ShadowCollection,
		TopK:              req.TopK,
		QueryCount:        len(rows),
		MeanJaccardAtK:    jaccardSum / n,
		Top1Stability:     float64(top1Matches) / n,
		SourceEmptyRate:   float64(sourceEmpty) / n,
		ShadowEmptyRate:   float64(shadowEmpty) / n,
		SourceP50Ms:       p50Int64(sourceLatencies),
		SourceP95Ms:       percentileInt64(sourceLatencies, 0.95),
		SourceP99Ms:       percentileInt64(sourceLatencies, 0.99),
		ShadowP50Ms:       p50Int64(shadowLatencies),
		ShadowP95Ms:       percentileInt64(shadowLatencies, 0.95),
		ShadowP99Ms:       percentileInt64(shadowLatencies, 0.99),
		MeanTopScoreDelta: topScoreDeltaSum / n,
		Rows:              rows,
	}
	report.CutoverReady, report.GateFailures = evaluateShadowReadGate(req, report)
	return report, nil
}

type embeddingShadowSearchResult struct {
	IDs      []string
	TopScore float32
}

func embedAndSearchIDs(ctx context.Context, cfg APIConfig, client *embed.Client, collection, query string, topK int) (embeddingShadowSearchResult, error) {
	vec, err := client.EmbedSingle(ctx, query)
	if err != nil {
		return embeddingShadowSearchResult{}, err
	}
	recs, err := cfg.Collections.Search(collection, vec, topK)
	if err != nil {
		return embeddingShadowSearchResult{}, err
	}
	ids := make([]string, 0, len(recs))
	for _, r := range recs {
		ids = append(ids, r.ID)
	}
	out := embeddingShadowSearchResult{IDs: ids}
	if len(recs) > 0 {
		out.TopScore = recs[0].Score
	}
	return out, nil
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
	return percentileInt64(vals, 0.50)
}

func percentileInt64(vals []int64, pct float64) int64 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]int64(nil), vals...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	if pct <= 0 {
		return cp[0]
	}
	if pct >= 1 {
		return cp[len(cp)-1]
	}
	idx := int(float64(len(cp)-1) * pct)
	return cp[idx]
}

func evaluateShadowReadGate(req embeddingShadowReadRequest, report embeddingShadowReadReport) (bool, []string) {
	var failures []string
	if req.MinMeanJaccardAtK > 0 && report.MeanJaccardAtK < req.MinMeanJaccardAtK {
		failures = append(failures, fmt.Sprintf("mean_jaccard_at_k %.4f < %.4f", report.MeanJaccardAtK, req.MinMeanJaccardAtK))
	}
	if req.MinTop1Stability > 0 && report.Top1Stability < req.MinTop1Stability {
		failures = append(failures, fmt.Sprintf("top1_stability %.4f < %.4f", report.Top1Stability, req.MinTop1Stability))
	}
	if req.MaxShadowEmptyRate > 0 && report.ShadowEmptyRate > req.MaxShadowEmptyRate {
		failures = append(failures, fmt.Sprintf("shadow_empty_rate %.4f > %.4f", report.ShadowEmptyRate, req.MaxShadowEmptyRate))
	}
	if req.MaxShadowP95LatencyMs > 0 && report.ShadowP95Ms > req.MaxShadowP95LatencyMs {
		failures = append(failures, fmt.Sprintf("shadow_p95_ms %d > %d", report.ShadowP95Ms, req.MaxShadowP95LatencyMs))
	}
	if req.MaxLatencyRatioP95 > 0 && report.SourceP95Ms > 0 {
		ratio := float64(report.ShadowP95Ms) / float64(report.SourceP95Ms)
		if ratio > req.MaxLatencyRatioP95 {
			failures = append(failures, fmt.Sprintf("p95_latency_ratio %.4f > %.4f", ratio, req.MaxLatencyRatioP95))
		}
	}
	if req.MaxMeanTopScoreDelta > 0 && report.MeanTopScoreDelta > req.MaxMeanTopScoreDelta {
		failures = append(failures, fmt.Sprintf("mean_top_score_delta %.4f > %.4f", report.MeanTopScoreDelta, req.MaxMeanTopScoreDelta))
	}
	return len(failures) == 0, failures
}

func absFloat64(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
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
