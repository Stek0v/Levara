package http

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	dcdVSAModeGlobalVSA  = "global_vsa_first"
	dcdVSAModeOracle     = "oracle_route_vsa"
	dcdVSAModeWrongRoute = "wrong_route_vsa"
	dcdVSAModeEmptyRoute = "empty_route_vsa"
)

type dcdVSARoute struct {
	DomainID     string  `json:"domain_id"`
	CollectionID string  `json:"collection_id"`
	DocumentID   string  `json:"document_id"`
	Confidence   float64 `json:"confidence"`
	Source       string  `json:"source"`
}

type dcdVSABaselineCase struct {
	ID            string      `json:"id"`
	Scenario      string      `json:"scenario"`
	Query         string      `json:"query"`
	Expected      dcdVSARoute `json:"expected_route"`
	Predicate     string      `json:"predicate"`
	TargetID      string      `json:"target_id"`
	Budget        int         `json:"budget"`
	Candidates    []dcdVSAHit `json:"candidates"`
	AllowFallback bool        `json:"allow_fallback"`
}

type dcdVSAHit struct {
	ID           string  `json:"id"`
	DomainID     string  `json:"domain_id"`
	CollectionID string  `json:"collection_id"`
	DocumentID   string  `json:"document_id"`
	Predicate    string  `json:"predicate"`
	TargetID     string  `json:"target_id"`
	DatasetID    string  `json:"dataset_id"`
	Score        float64 `json:"score"`
	Expired      bool    `json:"expired,omitempty"`
	TenantLeak   bool    `json:"tenant_leak,omitempty"`
}

type dcdVSAMetrics struct {
	Cases                 int     `json:"cases"`
	DomainAccuracyAt1     float64 `json:"domain_accuracy_at_1"`
	CollectionAccuracyAt1 float64 `json:"collection_accuracy_at_1"`
	DocumentRecallAtK     float64 `json:"document_recall_at_k"`
	FactRecallAtK         float64 `json:"fact_recall_at_k"`
	MRR                   float64 `json:"mrr"`
	NDCGAtK               float64 `json:"ndcg_at_k"`
	PredicatePrecisionAtK float64 `json:"predicate_precision_at_k"`
	DistractorRate        float64 `json:"distractor_rate"`
	TenantLeakRate        float64 `json:"tenant_leak_rate"`
	ExpiredLeakRate       float64 `json:"expired_leak_rate"`
	ContextFactsAvg       float64 `json:"context_facts_avg"`
	LatencyP50Micros      int64   `json:"latency_p50_micros"`
	LatencyP95Micros      int64   `json:"latency_p95_micros"`
}

type dcdVSABaselineReport struct {
	GeneratedAt string                              `json:"generated_at"`
	Cases       int                                 `json:"cases"`
	Modes       []string                            `json:"modes"`
	Scenarios   []string                            `json:"scenarios"`
	Summary     map[string]dcdVSAMetrics            `json:"summary"`
	ByScenario  map[string]map[string]dcdVSAMetrics `json:"by_scenario"`
	Lift        map[string]map[string]float64       `json:"lift"`
	Notes       []string                            `json:"notes"`
}

type dcdVSALoadReport struct {
	GeneratedAt string                    `json:"generated_at"`
	Cases       int                       `json:"cases"`
	Iterations  int                       `json:"iterations"`
	Modes       []string                  `json:"modes"`
	Summary     map[string]dcdVSALoadStat `json:"summary"`
}

type dcdVSALoadStat struct {
	Queries          int     `json:"queries"`
	LatencyP50Micros int64   `json:"latency_p50_micros"`
	LatencyP95Micros int64   `json:"latency_p95_micros"`
	LatencyP99Micros int64   `json:"latency_p99_micros"`
	MaxMicros        int64   `json:"max_micros"`
	ThroughputQPS    float64 `json:"throughput_qps"`
}

func TestDCDVSABaselineEval(t *testing.T) {
	cases := buildDCDVSABaselineCases()
	modes := []string{
		dcdVSAModeGlobalVSA,
		dcdVSAModeOracle,
		dcdVSAModeWrongRoute,
		dcdVSAModeEmptyRoute,
	}
	byMode := make(map[string]map[string]dcdVSAMetrics, len(modes))
	for _, mode := range modes {
		byMode[mode] = evaluateDCDVSAMode(cases, mode)
	}
	report := buildDCDVSABaselineReport(cases, modes, byMode)
	writeDCDVSABaselineReportIfRequested(t, report)

	global := report.Summary[dcdVSAModeGlobalVSA]
	oracle := report.Summary[dcdVSAModeOracle]
	wrong := report.Summary[dcdVSAModeWrongRoute]
	empty := report.Summary[dcdVSAModeEmptyRoute]
	t.Logf("DCD/VSA baseline: global recall=%.3f mrr=%.3f; oracle recall=%.3f mrr=%.3f; wrong recall=%.3f; empty recall=%.3f",
		global.FactRecallAtK, global.MRR, oracle.FactRecallAtK, oracle.MRR, wrong.FactRecallAtK, empty.FactRecallAtK)

	if len(cases) < 52 {
		t.Fatalf("baseline case count=%d, want at least 52", len(cases))
	}
	if oracle.FactRecallAtK < global.FactRecallAtK+0.20 {
		t.Fatalf("oracle route recall %.3f did not clear global %.3f by >=0.20", oracle.FactRecallAtK, global.FactRecallAtK)
	}
	if oracle.MRR < global.MRR+0.20 {
		t.Fatalf("oracle route MRR %.3f did not clear global %.3f by >=0.20", oracle.MRR, global.MRR)
	}
	if wrong.TenantLeakRate != 0 || oracle.TenantLeakRate != 0 || empty.TenantLeakRate != 0 {
		t.Fatalf("tenant leak rate must remain zero: oracle=%.3f wrong=%.3f empty=%.3f",
			oracle.TenantLeakRate, wrong.TenantLeakRate, empty.TenantLeakRate)
	}
	if empty.FactRecallAtK != global.FactRecallAtK {
		t.Fatalf("empty route should degrade to global VSA: empty=%.3f global=%.3f", empty.FactRecallAtK, global.FactRecallAtK)
	}
}

func TestDCDVSALoadBaseline(t *testing.T) {
	rawCases := os.Getenv("LEVARA_DCD_VSA_LOAD_CASES")
	if rawCases == "" {
		t.Skip("set LEVARA_DCD_VSA_LOAD_CASES to run DCD/VSA load baseline")
	}
	iterations, err := strconv.Atoi(rawCases)
	if err != nil || iterations <= 0 {
		t.Fatalf("invalid LEVARA_DCD_VSA_LOAD_CASES=%q", rawCases)
	}
	cases := buildDCDVSABaselineCases()
	modes := []string{dcdVSAModeGlobalVSA, dcdVSAModeOracle, dcdVSAModeWrongRoute, dcdVSAModeEmptyRoute}
	report := dcdVSALoadReport{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Cases:       len(cases),
		Iterations:  iterations,
		Modes:       modes,
		Summary:     map[string]dcdVSALoadStat{},
	}
	for _, mode := range modes {
		report.Summary[mode] = runDCDVSALoadMode(cases, mode, iterations)
	}
	writeDCDVSALoadReportIfRequested(t, report)
	for _, mode := range modes {
		stat := report.Summary[mode]
		t.Logf("DCD/VSA load baseline mode=%s queries=%d p95=%dus p99=%dus qps=%.0f",
			mode, stat.Queries, stat.LatencyP95Micros, stat.LatencyP99Micros, stat.ThroughputQPS)
		if enforcePerfBudgets() && !raceDetectorEnabled() && stat.LatencyP95Micros > 1000 {
			t.Fatalf("mode=%s p95 latency %dus above 1000us", mode, stat.LatencyP95Micros)
		}
	}
}

func buildDCDVSABaselineCases() []dcdVSABaselineCase {
	scenarios := []struct {
		name       string
		predicate  string
		budget     int
		distractor int
	}{
		{"context_budget", "VALIDATES", 5, 12},
		{"fanout", "CALLS", 8, 40},
		{"predicate_specific", "OWNED_BY", 8, 18},
		{"distractor_heavy", "DEPENDS_ON", 8, 30},
	}
	var cases []dcdVSABaselineCase
	for _, scenario := range scenarios {
		for i := 0; i < 13; i++ {
			domain := fmt.Sprintf("domain_%02d", i%5)
			collection := fmt.Sprintf("%s_%s", domain, scenario.name)
			document := fmt.Sprintf("%s_doc_%02d", collection, i)
			target := fmt.Sprintf("%s_gold_%02d", scenario.name, i)
			id := fmt.Sprintf("%s_%02d", scenario.name, i)
			expected := dcdVSARoute{
				DomainID:     domain,
				CollectionID: collection,
				DocumentID:   document,
				Confidence:   1,
				Source:       "oracle-test",
			}
			candidates := buildDCDVSACandidates(id, scenario.name, scenario.predicate, target, expected, scenario.distractor)
			cases = append(cases, dcdVSABaselineCase{
				ID:            id,
				Scenario:      scenario.name,
				Query:         fmt.Sprintf("route %s question %02d", scenario.name, i),
				Expected:      expected,
				Predicate:     scenario.predicate,
				TargetID:      target,
				Budget:        scenario.budget,
				Candidates:    candidates,
				AllowFallback: true,
			})
		}
	}
	return cases
}

func buildDCDVSACandidates(id, scenario, predicate, target string, route dcdVSARoute, distractors int) []dcdVSAHit {
	var out []dcdVSAHit
	for i := 0; i < distractors; i++ {
		domain := fmt.Sprintf("domain_%02d", (i+1)%7)
		collection := fmt.Sprintf("%s_%s_noise", domain, scenario)
		doc := fmt.Sprintf("%s_doc_%02d", collection, i)
		noisePredicate := predicate
		if i%3 == 0 {
			noisePredicate = "RELATED_TO"
		}
		out = append(out, dcdVSAHit{
			ID:           fmt.Sprintf("%s_noise_%02d", id, i),
			DomainID:     domain,
			CollectionID: collection,
			DocumentID:   doc,
			Predicate:    noisePredicate,
			TargetID:     fmt.Sprintf("%s_noise_target_%02d", id, i),
			DatasetID:    "dcd-ds",
			Score:        1 - float64(i)*0.001,
		})
	}
	out = append(out, dcdVSAHit{
		ID:           id + "_gold",
		DomainID:     route.DomainID,
		CollectionID: route.CollectionID,
		DocumentID:   route.DocumentID,
		Predicate:    predicate,
		TargetID:     target,
		DatasetID:    "dcd-ds",
		Score:        0.50,
	})
	out = append(out, dcdVSAHit{
		ID:           id + "_tenant_leak_candidate",
		DomainID:     route.DomainID,
		CollectionID: route.CollectionID,
		DocumentID:   route.DocumentID,
		Predicate:    predicate,
		TargetID:     id + "_foreign_target",
		DatasetID:    "foreign-ds",
		Score:        0.99,
		TenantLeak:   true,
	})
	out = append(out, dcdVSAHit{
		ID:           id + "_expired_candidate",
		DomainID:     route.DomainID,
		CollectionID: route.CollectionID,
		DocumentID:   route.DocumentID,
		Predicate:    predicate,
		TargetID:     id + "_expired_target",
		DatasetID:    "dcd-ds",
		Score:        0.98,
		Expired:      true,
	})
	return out
}

func evaluateDCDVSAMode(cases []dcdVSABaselineCase, mode string) map[string]dcdVSAMetrics {
	byScenarioHits := map[string][][]dcdVSAHit{}
	byScenarioCases := map[string][]dcdVSABaselineCase{}
	byScenarioLatency := map[string][]time.Duration{}
	for _, tc := range cases {
		start := time.Now()
		hits := retrieveDCDVSAHits(tc, mode)
		byScenarioLatency[tc.Scenario] = append(byScenarioLatency[tc.Scenario], time.Since(start))
		byScenarioHits[tc.Scenario] = append(byScenarioHits[tc.Scenario], hits)
		byScenarioCases[tc.Scenario] = append(byScenarioCases[tc.Scenario], tc)
	}
	out := make(map[string]dcdVSAMetrics, len(byScenarioCases)+1)
	for scenario, scCases := range byScenarioCases {
		out[scenario] = scoreDCDVSA(scCases, byScenarioHits[scenario], byScenarioLatency[scenario], mode)
	}
	out["all"] = scoreDCDVSA(cases, flattenDCDVSAHits(byScenarioHits, cases), flattenDCDVSADurations(byScenarioLatency, cases), mode)
	return out
}

func runDCDVSALoadMode(cases []dcdVSABaselineCase, mode string, iterations int) dcdVSALoadStat {
	latencies := make([]time.Duration, 0, iterations)
	startAll := time.Now()
	for i := 0; i < iterations; i++ {
		tc := cases[i%len(cases)]
		start := time.Now()
		_ = retrieveDCDVSAHits(tc, mode)
		latencies = append(latencies, time.Since(start))
	}
	elapsed := time.Since(startAll)
	if elapsed <= 0 {
		elapsed = time.Nanosecond
	}
	return dcdVSALoadStat{
		Queries:          iterations,
		LatencyP50Micros: percentileDurations(latencies, 0.50).Microseconds(),
		LatencyP95Micros: percentileDurations(latencies, 0.95).Microseconds(),
		LatencyP99Micros: percentileDurations(latencies, 0.99).Microseconds(),
		MaxMicros:        percentileDurations(latencies, 1.00).Microseconds(),
		ThroughputQPS:    float64(iterations) / elapsed.Seconds(),
	}
}

func writeDCDVSALoadReportIfRequested(t *testing.T, report dcdVSALoadReport) {
	t.Helper()
	if os.Getenv("LEVARA_WRITE_DCD_VSA_LOAD_REPORT") == "" {
		return
	}
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	resultsDir := filepath.Join(repoRoot, "benchmark", "results")
	if err := os.MkdirAll(resultsDir, 0755); err != nil {
		t.Fatalf("mkdir benchmark/results: %v", err)
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshal DCD VSA load report: %v", err)
	}
	if err := os.WriteFile(filepath.Join(resultsDir, "dcd_vsa_load_baseline_latest.json"), raw, 0644); err != nil {
		t.Fatalf("write DCD VSA load json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "docs", "dcd-vsa-load-baseline-report.md"), []byte(renderDCDVSALoadMarkdown(report)), 0644); err != nil {
		t.Fatalf("write DCD VSA load markdown: %v", err)
	}
}

func renderDCDVSALoadMarkdown(report dcdVSALoadReport) string {
	var b strings.Builder
	b.WriteString("# DCD VSA Load Baseline Report\n\n")
	b.WriteString("Generated: " + report.GeneratedAt + "\n\n")
	fmt.Fprintf(&b, "Cases: %d\n\n", report.Cases)
	fmt.Fprintf(&b, "Iterations: %d\n\n", report.Iterations)
	b.WriteString("| Mode | queries | p50 latency (us) | p95 latency (us) | p99 latency (us) | max latency (us) | qps |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|\n")
	for _, mode := range report.Modes {
		stat := report.Summary[mode]
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %.0f |\n",
			mode, stat.Queries, stat.LatencyP50Micros, stat.LatencyP95Micros,
			stat.LatencyP99Micros, stat.MaxMicros, stat.ThroughputQPS)
	}
	return b.String()
}

func retrieveDCDVSAHits(tc dcdVSABaselineCase, mode string) []dcdVSAHit {
	candidates := filterVisibleDCDVSAHits(tc.Candidates)
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})
	switch mode {
	case dcdVSAModeOracle:
		scoped := filterDCDVSAHitsByRoute(candidates, tc.Expected)
		return mergeDCDVSAHits(scoped, candidates, tc.Budget)
	case dcdVSAModeWrongRoute:
		wrong := dcdVSARoute{
			DomainID:     "wrong_domain",
			CollectionID: "wrong_collection",
			DocumentID:   "wrong_document",
			Confidence:   0.99,
			Source:       "wrong-route-test",
		}
		scoped := filterDCDVSAHitsByRoute(candidates, wrong)
		if len(scoped) == 0 && tc.AllowFallback {
			return takeDCDVSAHits(candidates, tc.Budget)
		}
		return mergeDCDVSAHits(scoped, candidates, tc.Budget)
	case dcdVSAModeEmptyRoute:
		if tc.AllowFallback {
			return takeDCDVSAHits(candidates, tc.Budget)
		}
		return nil
	default:
		return takeDCDVSAHits(candidates, tc.Budget)
	}
}

func filterVisibleDCDVSAHits(candidates []dcdVSAHit) []dcdVSAHit {
	out := make([]dcdVSAHit, 0, len(candidates))
	for _, c := range candidates {
		if c.TenantLeak || c.Expired || c.DatasetID != "dcd-ds" {
			continue
		}
		out = append(out, c)
	}
	return out
}

func filterDCDVSAHitsByRoute(candidates []dcdVSAHit, route dcdVSARoute) []dcdVSAHit {
	var out []dcdVSAHit
	for _, c := range candidates {
		if c.DomainID == route.DomainID &&
			c.CollectionID == route.CollectionID &&
			c.DocumentID == route.DocumentID {
			out = append(out, c)
		}
	}
	return out
}

func takeDCDVSAHits(candidates []dcdVSAHit, budget int) []dcdVSAHit {
	if budget > len(candidates) {
		budget = len(candidates)
	}
	return append([]dcdVSAHit(nil), candidates[:budget]...)
}

func mergeDCDVSAHits(primary, secondary []dcdVSAHit, budget int) []dcdVSAHit {
	out := make([]dcdVSAHit, 0, budget)
	seen := map[string]struct{}{}
	add := func(hit dcdVSAHit) {
		if len(out) >= budget {
			return
		}
		if _, ok := seen[hit.ID]; ok {
			return
		}
		seen[hit.ID] = struct{}{}
		out = append(out, hit)
	}
	for _, hit := range primary {
		add(hit)
	}
	for _, hit := range secondary {
		add(hit)
	}
	return out
}

func scoreDCDVSA(cases []dcdVSABaselineCase, hitsByCase [][]dcdVSAHit, latencies []time.Duration, mode string) dcdVSAMetrics {
	var domainHits, collectionHits, documentHits, factHits, predicateHits, retrieved, distractors, tenantLeaks, expiredLeaks int
	var mrr, ndcg, contextFacts float64
	for i, tc := range cases {
		hits := hitsByCase[i]
		contextFacts += float64(len(hits))
		if mode == dcdVSAModeOracle {
			domainHits++
			collectionHits++
		}
		rank := 0
		for j, hit := range hits {
			if hit.TenantLeak || hit.DatasetID != "dcd-ds" {
				tenantLeaks++
			}
			if hit.Expired {
				expiredLeaks++
			}
			if hit.Predicate == tc.Predicate {
				predicateHits++
			}
			if hit.DocumentID == tc.Expected.DocumentID {
				documentHits++
			}
			if hit.TargetID != tc.TargetID {
				distractors++
			}
			if hit.TargetID == tc.TargetID && hit.Predicate == tc.Predicate {
				factHits++
				if rank == 0 {
					rank = j + 1
				}
			}
		}
		retrieved += len(hits)
		if rank > 0 {
			mrr += 1 / float64(rank)
			ndcg += 1 / math.Log2(float64(rank+1))
		}
	}
	n := float64(len(cases))
	if n == 0 {
		return dcdVSAMetrics{}
	}
	denom := float64(retrieved)
	if denom == 0 {
		denom = 1
	}
	return dcdVSAMetrics{
		Cases:                 len(cases),
		DomainAccuracyAt1:     float64(domainHits) / n,
		CollectionAccuracyAt1: float64(collectionHits) / n,
		DocumentRecallAtK:     float64(documentHits) / n,
		FactRecallAtK:         float64(factHits) / n,
		MRR:                   mrr / n,
		NDCGAtK:               ndcg / n,
		PredicatePrecisionAtK: float64(predicateHits) / denom,
		DistractorRate:        float64(distractors) / denom,
		TenantLeakRate:        float64(tenantLeaks) / denom,
		ExpiredLeakRate:       float64(expiredLeaks) / denom,
		ContextFactsAvg:       contextFacts / n,
		LatencyP50Micros:      percentileDurations(latencies, 0.50).Microseconds(),
		LatencyP95Micros:      percentileDurations(latencies, 0.95).Microseconds(),
	}
}

func flattenDCDVSAHits(byScenario map[string][][]dcdVSAHit, cases []dcdVSABaselineCase) [][]dcdVSAHit {
	out := make([][]dcdVSAHit, 0, len(cases))
	offsets := map[string]int{}
	for _, tc := range cases {
		idx := offsets[tc.Scenario]
		out = append(out, byScenario[tc.Scenario][idx])
		offsets[tc.Scenario] = idx + 1
	}
	return out
}

func flattenDCDVSADurations(byScenario map[string][]time.Duration, cases []dcdVSABaselineCase) []time.Duration {
	out := make([]time.Duration, 0, len(cases))
	offsets := map[string]int{}
	for _, tc := range cases {
		idx := offsets[tc.Scenario]
		out = append(out, byScenario[tc.Scenario][idx])
		offsets[tc.Scenario] = idx + 1
	}
	return out
}

func buildDCDVSABaselineReport(cases []dcdVSABaselineCase, modes []string, byMode map[string]map[string]dcdVSAMetrics) dcdVSABaselineReport {
	byScenario := map[string]map[string]dcdVSAMetrics{}
	summary := map[string]dcdVSAMetrics{}
	scenarioSet := map[string]struct{}{}
	for _, tc := range cases {
		scenarioSet[tc.Scenario] = struct{}{}
	}
	for _, mode := range modes {
		summary[mode] = byMode[mode]["all"]
		for scenario, metrics := range byMode[mode] {
			if scenario == "all" {
				continue
			}
			if byScenario[scenario] == nil {
				byScenario[scenario] = map[string]dcdVSAMetrics{}
			}
			byScenario[scenario][mode] = metrics
		}
	}
	var scenarios []string
	for scenario := range scenarioSet {
		scenarios = append(scenarios, scenario)
	}
	sort.Strings(scenarios)
	lift := map[string]map[string]float64{}
	base := summary[dcdVSAModeGlobalVSA]
	for _, mode := range modes {
		if mode == dcdVSAModeGlobalVSA {
			continue
		}
		m := summary[mode]
		lift[mode] = map[string]float64{
			"fact_recall_at_k": m.FactRecallAtK - base.FactRecallAtK,
			"mrr":              m.MRR - base.MRR,
			"ndcg_at_k":        m.NDCGAtK - base.NDCGAtK,
			"distractor_rate":  m.DistractorRate - base.DistractorRate,
		}
	}
	return dcdVSABaselineReport{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Cases:       len(cases),
		Modes:       modes,
		Scenarios:   scenarios,
		Summary:     summary,
		ByScenario:  byScenario,
		Lift:        lift,
		Notes: []string{
			"This is a pre-implementation baseline. It uses synthetic route metadata and does not change production searchHandler behavior.",
			"global_vsa_first models current VSA-before-SQL without DCD route narrowing.",
			"oracle_route_vsa models the upper bound of perfect domain/collection/document routing before VSA candidate selection.",
			"wrong_route_vsa and empty_route_vsa validate graceful fallback behavior.",
		},
	}
}

func writeDCDVSABaselineReportIfRequested(t *testing.T, report dcdVSABaselineReport) {
	t.Helper()
	if os.Getenv("LEVARA_WRITE_DCD_VSA_BASELINE_REPORT") == "" {
		return
	}
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	resultsDir := filepath.Join(repoRoot, "benchmark", "results")
	if err := os.MkdirAll(resultsDir, 0755); err != nil {
		t.Fatalf("mkdir benchmark/results: %v", err)
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshal DCD VSA baseline report: %v", err)
	}
	if err := os.WriteFile(filepath.Join(resultsDir, "dcd_vsa_baseline_latest.json"), raw, 0644); err != nil {
		t.Fatalf("write DCD VSA baseline json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "docs", "dcd-vsa-baseline-report.md"), []byte(renderDCDVSABaselineMarkdown(report)), 0644); err != nil {
		t.Fatalf("write DCD VSA baseline markdown: %v", err)
	}
}

func renderDCDVSABaselineMarkdown(report dcdVSABaselineReport) string {
	var b strings.Builder
	b.WriteString("# DCD VSA Baseline Report\n\n")
	b.WriteString("Generated: " + report.GeneratedAt + "\n\n")
	fmt.Fprintf(&b, "Cases: %d\n\n", report.Cases)
	b.WriteString("Scenarios: " + strings.Join(report.Scenarios, ", ") + "\n\n")
	b.WriteString("## Summary\n\n")
	b.WriteString("| Mode | domain@1 | collection@1 | document recall@k | fact recall@k | MRR | nDCG@k | predicate precision | distractor rate | tenant leak | expired leak | p95 latency (us) |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, mode := range report.Modes {
		m := report.Summary[mode]
		fmt.Fprintf(&b, "| %s | %.3f | %.3f | %.3f | %.3f | %.3f | %.3f | %.3f | %.3f | %.3f | %.3f | %d |\n",
			mode, m.DomainAccuracyAt1, m.CollectionAccuracyAt1, m.DocumentRecallAtK,
			m.FactRecallAtK, m.MRR, m.NDCGAtK, m.PredicatePrecisionAtK,
			m.DistractorRate, m.TenantLeakRate, m.ExpiredLeakRate, m.LatencyP95Micros)
	}
	b.WriteString("\n## Lift vs global_vsa_first\n\n")
	b.WriteString("| Mode | fact recall lift | MRR lift | nDCG lift | distractor delta |\n")
	b.WriteString("|---|---:|---:|---:|---:|\n")
	for _, mode := range report.Modes {
		if mode == dcdVSAModeGlobalVSA {
			continue
		}
		l := report.Lift[mode]
		fmt.Fprintf(&b, "| %s | %.3f | %.3f | %.3f | %.3f |\n",
			mode, l["fact_recall_at_k"], l["mrr"], l["ndcg_at_k"], l["distractor_rate"])
	}
	b.WriteString("\n## By Scenario\n\n")
	for _, scenario := range report.Scenarios {
		b.WriteString("### " + scenario + "\n\n")
		b.WriteString("| Mode | fact recall@k | MRR | nDCG@k | distractor rate | context avg |\n")
		b.WriteString("|---|---:|---:|---:|---:|---:|\n")
		for _, mode := range report.Modes {
			m := report.ByScenario[scenario][mode]
			fmt.Fprintf(&b, "| %s | %.3f | %.3f | %.3f | %.3f | %.2f |\n",
				mode, m.FactRecallAtK, m.MRR, m.NDCGAtK, m.DistractorRate, m.ContextFactsAvg)
		}
		b.WriteString("\n")
	}
	b.WriteString("## Notes\n\n")
	for _, note := range report.Notes {
		b.WriteString("- " + note + "\n")
	}
	return b.String()
}
