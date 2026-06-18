package http

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

type graphContextArchCase struct {
	ID         string `json:"id"`
	Scenario   string `json:"scenario"`
	Collection string `json:"collection"`
	SourceName string `json:"source_name"`
	SourceID   string `json:"source_id"`
	Query      string `json:"query"`
	Predicate  string `json:"predicate"`
	TargetName string `json:"target_name"`
	DatasetID  string `json:"dataset_id"`
}

type graphContextArchMetrics struct {
	Cases                 int     `json:"cases"`
	TargetRecallAtK       float64 `json:"target_recall_at_k"`
	MRR                   float64 `json:"mrr"`
	NDCGAtK               float64 `json:"ndcg_at_k"`
	PredicatePrecisionAtK float64 `json:"predicate_precision_at_k"`
	TenantLeakRate        float64 `json:"tenant_leak_rate"`
	ExpiredLeakRate       float64 `json:"expired_leak_rate"`
	VSAContextAvg         float64 `json:"vsa_context_avg"`
	SQLContextAvg         float64 `json:"sql_context_avg"`
	LatencyP50Micros      int64   `json:"latency_p50_micros"`
	LatencyP95Micros      int64   `json:"latency_p95_micros"`
	LatencyMaxMicros      int64   `json:"latency_max_micros"`
	ThroughputQPS         float64 `json:"throughput_qps"`
}

type graphContextArchReport struct {
	GeneratedAt string                             `json:"generated_at"`
	Modes       []string                           `json:"modes"`
	Scenarios   []string                           `json:"scenarios"`
	Summary     map[string]graphContextArchMetrics `json:"summary"`
	Lift        map[string]map[string]float64      `json:"lift"`
}

func TestSearchHandlerGraphContextArchitectureEval(t *testing.T) {
	cases := buildGraphContextArchCases()
	modes := []string{graphContextOrderSQLOnly, graphContextOrderSQLFirst, graphContextOrderVSAFirst, graphContextOrderVSAOnly}
	summary := map[string]graphContextArchMetrics{}
	for _, mode := range modes {
		summary[mode] = runGraphContextArchMode(t, mode, cases)
	}
	report := buildGraphContextArchReport(modes, cases, summary)
	writeGraphContextArchReportIfRequested(t, report)

	sqlFirst := report.Summary[graphContextOrderSQLFirst]
	vsaFirst := report.Summary[graphContextOrderVSAFirst]
	t.Logf("searchHandler architecture eval: sql_first recall=%.3f mrr=%.3f p95=%dus; vsa_first recall=%.3f mrr=%.3f p95=%dus",
		sqlFirst.TargetRecallAtK, sqlFirst.MRR, sqlFirst.LatencyP95Micros,
		vsaFirst.TargetRecallAtK, vsaFirst.MRR, vsaFirst.LatencyP95Micros)

	if vsaFirst.TargetRecallAtK < 0.90 {
		t.Fatalf("vsa_first recall %.3f below 0.90", vsaFirst.TargetRecallAtK)
	}
	if vsaFirst.MRR < 0.90 {
		t.Fatalf("vsa_first MRR %.3f below 0.90", vsaFirst.MRR)
	}
	if vsaFirst.TargetRecallAtK-sqlFirst.TargetRecallAtK < 0.40 {
		t.Fatalf("recall lift %.3f below 0.40", vsaFirst.TargetRecallAtK-sqlFirst.TargetRecallAtK)
	}
	if vsaFirst.TenantLeakRate != 0 || vsaFirst.ExpiredLeakRate != 0 {
		t.Fatalf("vsa_first leak rates tenant=%.3f expired=%.3f, want zero", vsaFirst.TenantLeakRate, vsaFirst.ExpiredLeakRate)
	}
	if vsaFirst.LatencyP95Micros > 150000 {
		t.Fatalf("vsa_first p95 latency %dus above 150000us", vsaFirst.LatencyP95Micros)
	}
}

func buildGraphContextArchCases() []graphContextArchCase {
	scenarios := []struct {
		name      string
		predicate string
		queryFmt  string
		targetFmt string
	}{
		{"budget_pressure", "VALIDATES", "what validates %s", "%s Validator"},
		{"predicate_synonym", "OWNED_BY", "who maintains %s", "%s Owner"},
		{"fanout", "DEPENDS_ON", "what does %s require", "%s Dependency"},
		{"distractor_heavy", "SECURED_BY", "what protects %s", "%s Guard"},
		{"mixed_production", "EMITS", "what publishes from %s", "%s Topic"},
	}
	var cases []graphContextArchCase
	perScenario := 1
	if os.Getenv("LEVARA_WRITE_GRAPH_CONTEXT_ARCH_REPORT") != "" {
		perScenario = 5
	}
	if os.Getenv("LEVARA_HEAVY_GRAPH_CONTEXT_ARCH_EVAL") != "" {
		perScenario = 20
	}
	for _, scenario := range scenarios {
		for i := 0; i < perScenario; i++ {
			source := fmt.Sprintf("%s Service %02d", strings.Title(strings.ReplaceAll(scenario.name, "_", " ")), i)
			sourceID := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(source, " ", "-"), "_", "-"))
			target := fmt.Sprintf(scenario.targetFmt, source)
			id := fmt.Sprintf("%s-%02d", scenario.name, i)
			cases = append(cases, graphContextArchCase{
				ID:         id,
				Scenario:   scenario.name,
				Collection: "entities_" + strings.ReplaceAll(id, "-", "_"),
				SourceName: source,
				SourceID:   sourceID,
				Query:      fmt.Sprintf(scenario.queryFmt, source),
				Predicate:  scenario.predicate,
				TargetName: target,
				DatasetID:  "arch-ds",
			})
		}
	}
	return cases
}

func runGraphContextArchMode(t *testing.T, mode string, cases []graphContextArchCase) graphContextArchMetrics {
	t.Helper()
	t.Setenv("LEVARA_GRAPH_CONTEXT_ORDER", mode)
	t.Setenv("LEVARA_GRAPH_CONTEXT_LIMIT", "8")
	t.Setenv("LEVARA_GRAPH_CONTEXT_VSA_RESERVE", "4")
	t.Setenv("LLM_ENDPOINT", "")
	t.Setenv("LLM_MODEL", "")

	env := newSearchTestEnv(t)
	env.cfg.LLMProvider = nil
	seedGraphContextArchFixture(t, env, cases)
	env.start()

	var hits, predicateHits, retrieved, tenantLeaks, expiredLeaks int
	var mrr, ndcg, vsaCount, sqlCount float64
	latencies := make([]time.Duration, 0, len(cases))
	startAll := time.Now()
	for _, tc := range cases {
		start := time.Now()
		_, body := env.postSearch(map[string]any{
			"query_text": tc.Query,
			"query_type": "GRAPH_COMPLETION",
			"collection": tc.Collection,
			"top_k":      1,
		})
		latencies = append(latencies, time.Since(start))
		ctxArr, _ := body["context"].([]any)
		vsaCount += numberFromBody(body, "graph_context_vsa_count")
		sqlCount += numberFromBody(body, "graph_context_sql_count")
		for i, raw := range ctxArr {
			line, _ := raw.(string)
			if line == "" {
				continue
			}
			retrieved++
			if strings.Contains(line, tc.Predicate) {
				predicateHits++
			}
			if strings.Contains(line, "Foreign Tenant Target") {
				tenantLeaks++
			}
			if strings.Contains(line, "Expired Target") {
				expiredLeaks++
			}
			if strings.Contains(line, tc.TargetName) && strings.Contains(line, tc.Predicate) {
				hits++
				mrr += 1.0 / float64(i+1)
				ndcg += 1.0 / math.Log2(float64(i)+2)
				break
			}
		}
	}
	elapsed := time.Since(startAll)
	denomRetrieved := float64(retrieved)
	if denomRetrieved == 0 {
		denomRetrieved = 1
	}
	n := float64(len(cases))
	return graphContextArchMetrics{
		Cases:                 len(cases),
		TargetRecallAtK:       float64(hits) / n,
		MRR:                   mrr / n,
		NDCGAtK:               ndcg / n,
		PredicatePrecisionAtK: float64(predicateHits) / denomRetrieved,
		TenantLeakRate:        float64(tenantLeaks) / denomRetrieved,
		ExpiredLeakRate:       float64(expiredLeaks) / denomRetrieved,
		VSAContextAvg:         vsaCount / n,
		SQLContextAvg:         sqlCount / n,
		LatencyP50Micros:      percentileArchLatency(latencies, 0.50).Microseconds(),
		LatencyP95Micros:      percentileArchLatency(latencies, 0.95).Microseconds(),
		LatencyMaxMicros:      percentileArchLatency(latencies, 1.00).Microseconds(),
		ThroughputQPS:         n / elapsed.Seconds(),
	}
}

func seedGraphContextArchFixture(t *testing.T, env *searchTestEnv, cases []graphContextArchCase) {
	t.Helper()
	vec := []float32{1, 0, 0, 0}
	for _, tc := range cases {
		env.insertVector(tc.Collection, "vec-"+tc.ID, vec, map[string]any{
			"name":       tc.SourceName,
			"dataset_id": tc.DatasetID,
		})
		env.insertNode(tc.SourceID, tc.SourceName, "Service", tc.DatasetID)
		for i := 0; i < 24; i++ {
			targetID := fmt.Sprintf("%s-noise-%02d", tc.SourceID, i)
			env.insertNode(targetID, fmt.Sprintf("%s Noise %02d", tc.SourceName, i), "Service", tc.DatasetID)
			env.insertEdgeInDataset(fmt.Sprintf("%s-a-noise-%02d", tc.ID, i), tc.SourceID, targetID, "CALLS", tc.DatasetID)
		}
		targetID := tc.SourceID + "-target"
		env.insertNode(targetID, tc.TargetName, "Service", tc.DatasetID)
		env.insertEdgeInDataset(tc.ID+"-z-target", tc.SourceID, targetID, tc.Predicate, tc.DatasetID)

		foreignID := tc.SourceID + "-foreign"
		env.insertNode(foreignID, "Foreign Tenant Target", "Service", "foreign-ds")
		env.insertEdgeInDataset(tc.ID+"-foreign", tc.SourceID, foreignID, tc.Predicate, "foreign-ds")

		expiredID := tc.SourceID + "-expired"
		env.insertNode(expiredID, "Expired Target", "Service", tc.DatasetID)
		if _, err := env.db.Exec(`INSERT INTO graph_edges(id, source_id, target_id, relationship_name, dataset_id, valid_until) VALUES (?, ?, ?, ?, ?, ?)`,
			tc.ID+"-expired", tc.SourceID, expiredID, tc.Predicate, tc.DatasetID, "2025-01-01T00:00:00Z"); err != nil {
			t.Fatalf("insert expired edge: %v", err)
		}
	}
	if err := vsaStoreForDB(env.db, 1024, 1).RebuildFromGraph(context.Background(), "arch-ds"); err != nil {
		t.Fatalf("rebuild VSA arch fixture: %v", err)
	}
	if err := refreshPredicateSynonyms(context.Background(), env.db, "arch-ds"); err != nil {
		t.Fatalf("refresh predicate synonyms: %v", err)
	}
}

func numberFromBody(body map[string]any, key string) float64 {
	switch v := body[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	default:
		return 0
	}
}

func percentileArchLatency(values []time.Duration, p float64) time.Duration {
	cp := append([]time.Duration(nil), values...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	idx := int(math.Ceil(p*float64(len(cp)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}

func buildGraphContextArchReport(modes []string, cases []graphContextArchCase, summary map[string]graphContextArchMetrics) graphContextArchReport {
	scenarioSet := map[string]struct{}{}
	for _, tc := range cases {
		scenarioSet[tc.Scenario] = struct{}{}
	}
	var scenarios []string
	for scenario := range scenarioSet {
		scenarios = append(scenarios, scenario)
	}
	sort.Strings(scenarios)
	lift := map[string]map[string]float64{}
	base := summary[graphContextOrderSQLFirst]
	for _, mode := range modes {
		if mode == graphContextOrderSQLFirst {
			continue
		}
		m := summary[mode]
		lift[mode] = map[string]float64{
			"target_recall_at_k": m.TargetRecallAtK - base.TargetRecallAtK,
			"mrr":                m.MRR - base.MRR,
			"ndcg_at_k":          m.NDCGAtK - base.NDCGAtK,
			"latency_p95_micros": float64(m.LatencyP95Micros - base.LatencyP95Micros),
		}
	}
	return graphContextArchReport{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Modes:       modes,
		Scenarios:   scenarios,
		Summary:     summary,
		Lift:        lift,
	}
}

func writeGraphContextArchReportIfRequested(t *testing.T, report graphContextArchReport) {
	t.Helper()
	if os.Getenv("LEVARA_WRITE_GRAPH_CONTEXT_ARCH_REPORT") == "" {
		return
	}
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	resultsDir := filepath.Join(repoRoot, "benchmark", "results")
	if err := os.MkdirAll(resultsDir, 0755); err != nil {
		t.Fatalf("mkdir benchmark/results: %v", err)
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshal graph context architecture report: %v", err)
	}
	if err := os.WriteFile(filepath.Join(resultsDir, "graph_context_architecture_eval_latest.json"), raw, 0644); err != nil {
		t.Fatalf("write graph context architecture json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "docs", "graph-context-architecture-eval-report.md"), []byte(renderGraphContextArchMarkdown(report)), 0644); err != nil {
		t.Fatalf("write graph context architecture markdown: %v", err)
	}
}

func renderGraphContextArchMarkdown(report graphContextArchReport) string {
	var b strings.Builder
	b.WriteString("# Graph Context Architecture Eval Report\n\n")
	b.WriteString("Generated: " + report.GeneratedAt + "\n\n")
	b.WriteString("Scenarios: " + strings.Join(report.Scenarios, ", ") + "\n\n")
	b.WriteString("| Mode | recall@k | MRR | nDCG@k | predicate precision | tenant leak | expired leak | VSA avg | SQL avg | p95 latency (us) | qps |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, mode := range report.Modes {
		m := report.Summary[mode]
		b.WriteString(fmt.Sprintf("| %s | %.3f | %.3f | %.3f | %.3f | %.3f | %.3f | %.2f | %.2f | %d | %.0f |\n",
			mode, m.TargetRecallAtK, m.MRR, m.NDCGAtK, m.PredicatePrecisionAtK,
			m.TenantLeakRate, m.ExpiredLeakRate, m.VSAContextAvg, m.SQLContextAvg,
			m.LatencyP95Micros, m.ThroughputQPS))
	}
	b.WriteString("\n## Lift vs sql_first\n\n")
	b.WriteString("| Mode | recall lift | MRR lift | nDCG lift | p95 delta (us) |\n")
	b.WriteString("|---|---:|---:|---:|---:|\n")
	for _, mode := range report.Modes {
		if mode == graphContextOrderSQLFirst {
			continue
		}
		l := report.Lift[mode]
		b.WriteString(fmt.Sprintf("| %s | %.3f | %.3f | %.3f | %.0f |\n",
			mode, l["target_recall_at_k"], l["mrr"], l["ndcg_at_k"], l["latency_p95_micros"]))
	}
	return b.String()
}
