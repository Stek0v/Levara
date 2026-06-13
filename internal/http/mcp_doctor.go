// mcp_doctor.go — System health diagnostics MCP tool ("doctor").
// Aggregates service connectivity, embedding coverage, BM25 coverage,
// graph connectivity, and memory staleness into a structured report
// that agents can act on programmatically.
package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/stek0v/levara/pkg/graphdb"
)

// ── Doctor check types ──

type doctorCheck struct {
	Name        string `json:"name"`
	Status      string `json:"status"` // "ok", "warn", "fail"
	Message     string `json:"message"`
	Remediation string `json:"remediation,omitempty"`
}

type doctorReport struct {
	Status  string        `json:"status"` // worst of all checks
	Checks  []doctorCheck `json:"checks"`
	Summary string        `json:"summary"`
}

// toolDoctor runs all health checks and returns a structured report.
func (h *mcpHandler) toolDoctor(ctx context.Context, args map[string]any) mcpToolResult {
	verbose, _ := args["verbose"].(bool)

	var checks []doctorCheck

	// 1. PostgreSQL
	checks = append(checks, h.checkPostgres(ctx))

	// 2. Embedding service
	checks = append(checks, h.checkEmbedService())

	// 2b. Reranker service (optional — skip check when not configured).
	if h.cfg.RerankEndpoint != "" {
		checks = append(checks, h.checkRerankService())
	}

	// 3. LLM service
	checks = append(checks, h.checkLLM())

	// 4. Neo4j (optional)
	if h.cfg.Neo4jCfg.Neo4jURL != "" {
		checks = append(checks, h.checkNeo4j(ctx))
		checks = append(checks, h.checkNeo4jSchema(ctx))
	}

	// 5. Embedding coverage
	checks = append(checks, h.checkEmbeddingCoverage(ctx, verbose)...)

	// 5b. Embedding drift — are any collections on a stale model/dim?
	//     Turns check_drift tool logic into a doctor-level assertion so
	//     a mid-migration state surfaces without the operator having to
	//     ask specifically.
	checks = append(checks, h.checkEmbeddingDriftAssertion(ctx))

	// 6. BM25 coverage
	checks = append(checks, h.checkBM25Coverage(verbose))

	// 7. Graph connectivity
	checks = append(checks, h.checkGraphConnectivity(ctx))

	// 8. Memory staleness
	checks = append(checks, h.checkMemoryStaleness(ctx))

	// Compute overall status
	okCount, warnCount, failCount := 0, 0, 0
	for _, c := range checks {
		switch c.Status {
		case "fail":
			failCount++
		case "warn":
			warnCount++
		default:
			okCount++
		}
	}

	overall := "ok"
	if failCount > 0 {
		overall = "fail"
	} else if warnCount > 0 {
		overall = "warn"
	}

	report := doctorReport{
		Status:  overall,
		Checks:  checks,
		Summary: fmt.Sprintf("%d/%d ok, %d warn, %d fail", okCount, len(checks), warnCount, failCount),
	}

	// Log heartbeat if DB available
	h.logHeartbeat("doctor", report)

	return mcpJSONResult(report)
}

// ── Individual checks ──

func (h *mcpHandler) checkPostgres(ctx context.Context) doctorCheck {
	if h.cfg.DB == nil {
		return doctorCheck{
			Name:        "postgres",
			Status:      "warn",
			Message:     "Not configured (no DB_DSN)",
			Remediation: "Set DB_DSN environment variable to enable PostgreSQL",
		}
	}
	if err := h.cfg.DB.PingContext(ctx); err != nil {
		return doctorCheck{
			Name:        "postgres",
			Status:      "fail",
			Message:     fmt.Sprintf("Connection error: %v", err),
			Remediation: "Check DB_DSN and PostgreSQL service status",
		}
	}
	return doctorCheck{Name: "postgres", Status: "ok", Message: "Connected"}
}

func (h *mcpHandler) checkEmbedService() doctorCheck {
	ep := h.cfg.EmbedEndpoint
	if ep == "" {
		return doctorCheck{
			Name:        "embed_service",
			Status:      "warn",
			Message:     "Not configured (no EMBED_ENDPOINT)",
			Remediation: "Set EMBED_ENDPOINT to enable vector embeddings",
		}
	}

	// Derive base URL
	base := ep
	for _, suffix := range []string{"/v1/embeddings", "/v1/embed", "/api/embed", "/api/embeddings", "/embeddings"} {
		if strings.HasSuffix(base, suffix) {
			base = strings.TrimSuffix(base, suffix)
			break
		}
	}

	client := &http.Client{Timeout: 3 * time.Second}
	for _, path := range []string{"/api/tags", "/health", "/v1/models", ""} {
		resp, err := client.Get(base + path)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return doctorCheck{
					Name:    "embed_service",
					Status:  "ok",
					Message: fmt.Sprintf("Connected (%s, model: %s)", ep, h.cfg.EmbedModel),
				}
			}
		}
	}
	return doctorCheck{
		Name:        "embed_service",
		Status:      "fail",
		Message:     fmt.Sprintf("Unreachable: %s", ep),
		Remediation: "Start embedding server or verify EMBED_ENDPOINT URL",
	}
}

func (h *mcpHandler) checkLLM() doctorCheck {
	ep := os.Getenv("LLM_ENDPOINT")
	model := os.Getenv("LLM_MODEL")
	if ep == "" {
		return doctorCheck{
			Name:        "llm",
			Status:      "warn",
			Message:     "Not configured (no LLM_ENDPOINT)",
			Remediation: "Set LLM_ENDPOINT and LLM_MODEL for cognify and RAG features",
		}
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(ep + "/models")
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode == 200 {
			return doctorCheck{
				Name:    "llm",
				Status:  "ok",
				Message: fmt.Sprintf("Connected (%s, model: %s)", ep, model),
			}
		}
	}
	return doctorCheck{
		Name:        "llm",
		Status:      "fail",
		Message:     fmt.Sprintf("Unreachable: %s", ep),
		Remediation: "Start LLM server (Ollama/DeepSeek) or verify LLM_ENDPOINT",
	}
}

func (h *mcpHandler) checkNeo4j(ctx context.Context) doctorCheck {
	url := h.cfg.Neo4jCfg.Neo4jURL
	// Simple TCP check — full driver connection is expensive
	client := &http.Client{Timeout: 3 * time.Second}
	// Neo4j HTTP API is typically on port 7474
	httpURL := strings.Replace(url, "bolt://", "http://", 1)
	httpURL = strings.Replace(httpURL, "neo4j://", "http://", 1)
	httpURL = strings.Replace(httpURL, ":7687", ":7474", 1)
	resp, err := client.Get(httpURL)
	if err == nil {
		resp.Body.Close()
		return doctorCheck{Name: "neo4j", Status: "ok", Message: fmt.Sprintf("Connected (%s)", url)}
	}
	return doctorCheck{
		Name:        "neo4j",
		Status:      "warn",
		Message:     fmt.Sprintf("Unreachable: %s", url),
		Remediation: "Check Neo4j service or remove NEO4J_URL if not needed",
	}
}

func (h *mcpHandler) checkNeo4jSchema(ctx context.Context) doctorCheck {
	writer, err := graphdb.NewWriter(ctx, h.cfg.Neo4jCfg.Neo4jURL, h.cfg.Neo4jCfg.Neo4jUser,
		h.cfg.Neo4jCfg.Neo4jPassword, h.cfg.Neo4jCfg.Neo4jDatabase)
	if err != nil {
		return doctorCheck{
			Name:        "neo4j_schema",
			Status:      "warn",
			Message:     fmt.Sprintf("Unable to verify schema (connect error): %v", err),
			Remediation: "Ensure Neo4j is reachable to run schema checks and auto-create required indexes/constraints",
		}
	}
	defer writer.Close(ctx)

	constraintRows, err := writer.Query(ctx, "SHOW CONSTRAINTS YIELD type, labelsOrTypes, properties RETURN type, labelsOrTypes, properties", nil)
	if err != nil {
		return doctorCheck{
			Name:        "neo4j_schema",
			Status:      "warn",
			Message:     fmt.Sprintf("Constraint introspection failed: %v", err),
			Remediation: "Verify Neo4j version/permissions; SHOW CONSTRAINTS must be accessible",
		}
	}
	indexRows, err := writer.Query(ctx, "SHOW INDEXES YIELD labelsOrTypes, properties RETURN labelsOrTypes, properties", nil)
	if err != nil {
		return doctorCheck{
			Name:        "neo4j_schema",
			Status:      "warn",
			Message:     fmt.Sprintf("Index introspection failed: %v", err),
			Remediation: "Verify Neo4j version/permissions; SHOW INDEXES must be accessible",
		}
	}

	obs := neo4jObservedSchema{
		constraints: normalizeNeo4jConstraintRows(constraintRows),
		indexes:     normalizeNeo4jIndexRows(indexRows),
	}
	status, message, remediation := evaluateNeo4jSchema(obs)
	return doctorCheck{
		Name:        "neo4j_schema",
		Status:      status,
		Message:     message,
		Remediation: remediation,
	}
}

type neo4jObservedSchema struct {
	constraints map[string]bool
	indexes     map[string]bool
}

func requiredNeo4jConstraintKeys() []string {
	return []string{"__Node__:id:UNIQUE"}
}

func requiredNeo4jIndexKeys() []string {
	return []string{
		"__Node__:name",
		"__Node__:dataset_id",
		"__Node__:type",
	}
}

func evaluateNeo4jSchema(obs neo4jObservedSchema) (status, message, remediation string) {
	missing := missingNeo4jSchemaKeys(obs)
	if len(missing) == 0 {
		return "ok", "Required Neo4j constraints/indexes are present", ""
	}
	sort.Strings(missing)
	return "warn",
		fmt.Sprintf("Missing %d schema objects: %s", len(missing), strings.Join(missing, ", ")),
		"Run startup schema bootstrap (Writer.EnsureSchema) or manually create the missing Neo4j indexes/constraints"
}

func missingNeo4jSchemaKeys(obs neo4jObservedSchema) []string {
	var missing []string
	for _, key := range requiredNeo4jConstraintKeys() {
		if !obs.constraints[key] {
			missing = append(missing, "constraint:"+key)
		}
	}
	for _, key := range requiredNeo4jIndexKeys() {
		if !obs.indexes[key] {
			missing = append(missing, "index:"+key)
		}
	}
	return missing
}

func normalizeNeo4jConstraintRows(rows []map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, row := range rows {
		typ, _ := row["type"].(string)
		props := rowStringSlice(row["properties"])
		labels := rowStringSlice(row["labelsOrTypes"])
		for _, label := range labels {
			for _, prop := range props {
				key := fmt.Sprintf("%s:%s:%s", label, prop, strings.ToUpper(strings.TrimSpace(typ)))
				out[key] = true
			}
		}
	}
	return out
}

func normalizeNeo4jIndexRows(rows []map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, row := range rows {
		props := rowStringSlice(row["properties"])
		labels := rowStringSlice(row["labelsOrTypes"])
		for _, label := range labels {
			for _, prop := range props {
				out[fmt.Sprintf("%s:%s", label, prop)] = true
			}
		}
	}
	return out
}

func rowStringSlice(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, it := range x {
			if s, ok := it.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return x
	default:
		return nil
	}
}

func (h *mcpHandler) checkEmbeddingCoverage(ctx context.Context, verbose bool) []doctorCheck {
	if h.cfg.Collections == nil {
		return []doctorCheck{{Name: "embedding_coverage", Status: "warn", Message: "No collection manager"}}
	}

	collections := h.cfg.Collections.List()
	if len(collections) == 0 {
		return []doctorCheck{{Name: "embedding_coverage", Status: "ok", Message: "No collections yet"}}
	}

	totalRecords := 0
	emptyCollections := 0
	var details []string

	for _, name := range collections {
		meta := h.cfg.Collections.GetMeta(name)
		if meta == nil {
			continue
		}
		totalRecords += meta.RecordCount
		if meta.RecordCount == 0 {
			emptyCollections++
			if verbose {
				details = append(details, fmt.Sprintf("  %s: 0 records", name))
			}
		} else if verbose {
			details = append(details, fmt.Sprintf("  %s: %d records", name, meta.RecordCount))
		}
	}

	if verbose && len(details) > 0 {
		checks := []doctorCheck{}
		status := "ok"
		msg := fmt.Sprintf("%d collections, %d records total, %d empty", len(collections), totalRecords, emptyCollections)
		if emptyCollections > 0 && float64(emptyCollections)/float64(len(collections)) > 0.5 {
			status = "warn"
		}
		checks = append(checks, doctorCheck{
			Name:        "embedding_coverage",
			Status:      status,
			Message:     msg + "\n" + strings.Join(details, "\n"),
			Remediation: condStr(status == "warn", "Run cognify on datasets to populate empty collections", ""),
		})
		return checks
	}

	status := "ok"
	msg := fmt.Sprintf("%d collections, %d records total", len(collections), totalRecords)
	if emptyCollections > len(collections)/2 && len(collections) > 1 {
		status = "warn"
		msg = fmt.Sprintf("%d collections (%d empty), %d records total", len(collections), emptyCollections, totalRecords)
	}
	return []doctorCheck{{
		Name:        "embedding_coverage",
		Status:      status,
		Message:     msg,
		Remediation: condStr(status == "warn", "Run cognify on datasets to populate empty collections", ""),
	}}
}

func (h *mcpHandler) checkBM25Coverage(verbose bool) doctorCheck {
	if h.cfg.Collections == nil {
		return doctorCheck{Name: "bm25_coverage", Status: "warn", Message: "No collection manager"}
	}

	collections := h.cfg.Collections.List()
	indexed := 0
	missing := []string{}
	for _, name := range collections {
		// Skip internal collections
		if strings.HasPrefix(name, "Triplet_") || strings.HasSuffix(name, "_community_summaries") {
			continue
		}
		if h.cfg.BM25Indexes != nil {
			if _, ok := h.cfg.BM25Indexes[name]; ok {
				indexed++
				continue
			}
		}
		missing = append(missing, name)
	}

	if len(missing) == 0 {
		return doctorCheck{Name: "bm25_coverage", Status: "ok", Message: fmt.Sprintf("All %d user collections indexed", indexed)}
	}

	msg := fmt.Sprintf("%d/%d user collections have BM25 index", indexed, indexed+len(missing))
	if verbose {
		msg += "\n  Missing: " + strings.Join(missing, ", ")
	}
	return doctorCheck{
		Name:        "bm25_coverage",
		Status:      "warn",
		Message:     msg,
		Remediation: fmt.Sprintf("Re-cognify collections to build BM25: %s", strings.Join(missing, ", ")),
	}
}

func (h *mcpHandler) checkGraphConnectivity(ctx context.Context) doctorCheck {
	if h.cfg.DB == nil {
		return doctorCheck{Name: "graph_connectivity", Status: "warn", Message: "No database configured"}
	}

	var totalNodes, orphanNodes int
	err := h.cfg.DB.QueryRowContext(ctx, Q(`SELECT COUNT(*) FROM graph_nodes`)).Scan(&totalNodes)
	if err != nil {
		return doctorCheck{Name: "graph_connectivity", Status: "warn", Message: fmt.Sprintf("Query error: %v", err)}
	}

	if totalNodes == 0 {
		return doctorCheck{Name: "graph_connectivity", Status: "ok", Message: "No graph nodes yet"}
	}

	err = h.cfg.DB.QueryRowContext(ctx, Q(`
		SELECT COUNT(*) FROM graph_nodes gn
		WHERE NOT EXISTS (SELECT 1 FROM graph_edges ge WHERE ge.source_id = gn.id OR ge.target_id = gn.id)
	`)).Scan(&orphanNodes)
	if err != nil {
		return doctorCheck{Name: "graph_connectivity", Status: "warn", Message: fmt.Sprintf("Orphan query error: %v", err)}
	}

	orphanPct := float64(orphanNodes) / float64(totalNodes) * 100
	msg := fmt.Sprintf("%d nodes, %d orphans (%.0f%%)", totalNodes, orphanNodes, orphanPct)

	if orphanPct > 20 {
		return doctorCheck{
			Name:        "graph_connectivity",
			Status:      "warn",
			Message:     msg,
			Remediation: "Run prune_graph with include_orphan_nodes=true to clean up",
		}
	}
	return doctorCheck{Name: "graph_connectivity", Status: "ok", Message: msg}
}

func (h *mcpHandler) checkMemoryStaleness(ctx context.Context) doctorCheck {
	if h.cfg.DB == nil {
		return doctorCheck{Name: "memory_staleness", Status: "warn", Message: "No database configured"}
	}

	rows, err := h.cfg.DB.QueryContext(ctx, Q(`
		SELECT COALESCE(room, 'unset') as room, MIN(updated_at) as oldest, COUNT(*) as cnt
		FROM memories
		GROUP BY room
		ORDER BY oldest ASC
		LIMIT 10
	`))
	if err != nil {
		return doctorCheck{Name: "memory_staleness", Status: "warn", Message: fmt.Sprintf("Query error: %v", err)}
	}
	defer rows.Close()

	var staleRooms []string
	totalMemories := 0
	threshold := time.Now().AddDate(0, 0, -30) // 30 days

	for rows.Next() {
		var room, oldest string
		var cnt int
		if err := rows.Scan(&room, &oldest, &cnt); err != nil {
			continue
		}
		totalMemories += cnt

		// Parse timestamp
		t, parseErr := time.Parse(time.RFC3339, oldest)
		if parseErr != nil {
			t, parseErr = time.Parse("2006-01-02T15:04:05Z", oldest)
		}
		if parseErr != nil {
			t, parseErr = time.Parse("2006-01-02 15:04:05", oldest)
		}
		if parseErr == nil && t.Before(threshold) {
			staleRooms = append(staleRooms, fmt.Sprintf("%s (since %s)", room, t.Format("2006-01-02")))
		}
	}

	if totalMemories == 0 {
		return doctorCheck{Name: "memory_staleness", Status: "ok", Message: "No memories stored yet"}
	}

	if len(staleRooms) > 0 {
		return doctorCheck{
			Name:        "memory_staleness",
			Status:      "warn",
			Message:     fmt.Sprintf("%d memories total; stale rooms: %s", totalMemories, strings.Join(staleRooms, ", ")),
			Remediation: "Review and update memories in stale rooms with recall_memory + save_memory",
		}
	}

	return doctorCheck{
		Name:    "memory_staleness",
		Status:  "ok",
		Message: fmt.Sprintf("%d memories total, all updated within 30 days", totalMemories),
	}
}

// ── Heartbeat logger ──

// logHeartbeat records an event to the heartbeats table (if DB is available).
func (h *mcpHandler) logHeartbeat(eventType string, payload any) {
	if h.cfg.DB == nil {
		return
	}
	data, _ := json.Marshal(payload)
	id := fmt.Sprintf("hb-%s", time.Now().UTC().Format("20060102T150405.000"))
	_, _ = h.cfg.DB.ExecContext(context.Background(),
		Q(`INSERT INTO heartbeats (id, event_type, payload, created_at) VALUES ($1, $2, $3, $4)`),
		id, eventType, string(data), time.Now().UTC().Format(time.RFC3339))
}

// toolHeartbeat queries recent heartbeat events.
func (h *mcpHandler) toolHeartbeat(ctx context.Context, args map[string]any) mcpToolResult {
	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: `{"error":"no database configured"}`}}, IsError: true}
	}

	eventType, _ := args["event_type"].(string)
	limit := 10
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}
	if limit > 100 {
		limit = 100
	}

	var rows *sql.Rows
	var err error
	if eventType != "" {
		q, a := QArgs(`SELECT id, event_type, payload, created_at FROM heartbeats WHERE event_type = $1 ORDER BY created_at DESC LIMIT $2`, eventType, limit)
		rows, err = h.cfg.DB.QueryContext(ctx, q, a...)
	} else {
		rows, err = h.cfg.DB.QueryContext(ctx, Q(`SELECT id, event_type, payload, created_at FROM heartbeats ORDER BY created_at DESC LIMIT $1`), limit)
	}
	if err != nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: fmt.Sprintf(`{"error":"%s"}`, err)}}, IsError: true}
	}
	defer rows.Close()

	type hbEntry struct {
		ID        string          `json:"id"`
		EventType string          `json:"event_type"`
		Payload   json.RawMessage `json:"payload"`
		CreatedAt string          `json:"created_at"`
	}

	var events []hbEntry
	for rows.Next() {
		var e hbEntry
		var payload string
		if err := rows.Scan(&e.ID, &e.EventType, &payload, &e.CreatedAt); err != nil {
			continue
		}
		e.Payload = json.RawMessage(payload)
		events = append(events, e)
	}

	return mcpJSONResult(map[string]any{
		"count":  len(events),
		"events": events,
	})
}

// ── Helpers ──

func condStr(cond bool, t, f string) string {
	if cond {
		return t
	}
	return f
}

// checkRerankService verifies the Cohere-compat reranker endpoint is
// reachable. We probe /health when available, otherwise fall back to a
// HEAD on the rerank URL root.
func (h *mcpHandler) checkRerankService() doctorCheck {
	ep := h.cfg.RerankEndpoint
	// Strip the /rerank suffix so we can hit /health on the same host.
	base := strings.TrimSuffix(ep, "/rerank")

	client := &http.Client{Timeout: 3 * time.Second}
	for _, path := range []string{"/health", "/", ""} {
		resp, err := client.Get(base + path)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return doctorCheck{
					Name:    "rerank_service",
					Status:  "ok",
					Message: fmt.Sprintf("Connected (%s, model: %s)", ep, h.cfg.RerankModel),
				}
			}
		}
	}
	return doctorCheck{
		Name:        "rerank_service",
		Status:      "fail",
		Message:     fmt.Sprintf("Unreachable: %s", ep),
		Remediation: "Start reranker (e.g. qwen3-rerank-front sidecar) or unset RERANK_ENDPOINT to disable",
	}
}

// checkEmbeddingDriftAssertion surfaces any collections whose stored
// embed_model ≠ the currently configured one. Lifts the `check_drift`
// MCP tool logic into doctor's check list so a mid-migration state
// (some collections on old nomic-embed, some on new Qwen3) shows up in
// the default health report.
func (h *mcpHandler) checkEmbeddingDriftAssertion(ctx context.Context) doctorCheck {
	if !h.HasCollections() {
		return doctorCheck{
			Name:    "embedding_drift",
			Status:  "ok",
			Message: "No collections configured",
		}
	}
	currentModel := h.cfg.EmbedModel
	if currentModel == "" {
		return doctorCheck{
			Name:    "embedding_drift",
			Status:  "warn",
			Message: "Cannot check drift — EMBED_MODEL is empty",
		}
	}

	drifted := []string{}
	for _, name := range h.ListCollections() {
		// Skip internal collections — they're managed by the pipeline
		// and match the current model by construction.
		if strings.HasPrefix(name, "_") || strings.HasPrefix(name, "Triplet_") {
			continue
		}
		meta := h.CollectionMeta(name)
		if meta.EmbedModel != "" && meta.EmbedModel != currentModel {
			drifted = append(drifted, fmt.Sprintf("%s (was %s)", name, meta.EmbedModel))
		}
	}

	if len(drifted) == 0 {
		return doctorCheck{
			Name:    "embedding_drift",
			Status:  "ok",
			Message: fmt.Sprintf("All collections on %s", currentModel),
		}
	}
	return doctorCheck{
		Name:        "embedding_drift",
		Status:      "warn",
		Message:     fmt.Sprintf("%d collection(s) on stale model: %s", len(drifted), strings.Join(drifted, ", ")),
		Remediation: fmt.Sprintf("Run POST /api/v1/reembed or MCP `check_drift` then re-embed those collections to %s", currentModel),
	}
}
