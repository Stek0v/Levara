package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stek0v/levara/pkg/vsamemory"
)

type vsaEvalCase struct {
	ID            string `json:"id"`
	Scenario      string `json:"scenario"`
	DatasetID     string `json:"dataset_id"`
	SourceID      string `json:"source_id"`
	SourceName    string `json:"source_name"`
	Predicate     string `json:"predicate"`
	TargetID      string `json:"target_id"`
	TargetName    string `json:"target_name"`
	Budget        int    `json:"budget"`
	Distractors   int    `json:"distractors"`
	SamePredicate int    `json:"same_predicate_distractors"`
}

type vsaEvalFact struct {
	SourceID  string `json:"source_id"`
	Predicate string `json:"predicate"`
	TargetID  string `json:"target_id"`
	DatasetID string `json:"dataset_id"`
	Expired   bool   `json:"expired,omitempty"`
}

type vsaEvalModeMetrics struct {
	FactRecallAtK         float64 `json:"fact_recall_at_k"`
	TargetRecallAtK       float64 `json:"target_recall_at_k"`
	PredicatePrecisionAtK float64 `json:"predicate_precision_at_k"`
	MRR                   float64 `json:"mrr"`
	NDCGAtK               float64 `json:"ndcg_at_k"`
	ContextFactsAvg       float64 `json:"context_facts_avg"`
	LatencyP50Micros      int64   `json:"latency_p50_micros"`
	LatencyP95Micros      int64   `json:"latency_p95_micros"`
	TenantLeakRate        float64 `json:"tenant_leak_rate"`
	ExpiredLeakRate       float64 `json:"expired_fact_leak_rate"`
}

type vsaEvalReport struct {
	GeneratedAt string                                   `json:"generated_at"`
	Cases       int                                      `json:"cases"`
	Budgets     map[string]int                           `json:"budgets"`
	Modes       []string                                 `json:"modes"`
	ByScenario  map[string]map[string]vsaEvalModeMetrics `json:"by_scenario"`
	Summary     map[string]vsaEvalModeMetrics            `json:"summary"`
	Lift        map[string]map[string]float64            `json:"lift"`
	Notes       []string                                 `json:"notes"`
}

func TestVSAQuantitativeEval(t *testing.T) {
	ctx := context.Background()
	cases := buildVSAEvalCases()
	db := newVSAQuantEvalDB(t, cases)
	defer db.Close()

	prev := GetDBProvider()
	SetDBProvider(DBSQLite)
	defer SetDBProvider(prev)

	store := vsaStoreForDB(db, 1024, 1)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure VSA schema: %v", err)
	}

	modes := []string{"baseline_sql_graph", "vsa_empty_index", "current_append", "vsa_first", "vsa_only"}
	emptyStore := vsaStoreForDB(db, 1024, 1)
	if err := emptyStore.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure empty VSA schema: %v", err)
	}
	emptyMetrics := evaluateVSAMode(ctx, db, emptyStore, cases, "vsa_empty_index")

	if err := store.RebuildFromGraph(ctx, "ds-vsa"); err != nil {
		t.Fatalf("rebuild VSA: %v", err)
	}

	metrics := map[string]map[string]vsaEvalModeMetrics{}
	metrics["baseline_sql_graph"] = evaluateVSAMode(ctx, db, store, cases, "baseline_sql_graph")
	metrics["vsa_empty_index"] = emptyMetrics
	metrics["current_append"] = evaluateVSAMode(ctx, db, store, cases, "current_append")
	metrics["vsa_first"] = evaluateVSAMode(ctx, db, store, cases, "vsa_first")
	metrics["vsa_only"] = evaluateVSAMode(ctx, db, store, cases, "vsa_only")

	report := buildVSAEvalReport(cases, modes, metrics)
	writeVSAEvalReportIfRequested(t, report)

	base := report.Summary["baseline_sql_graph"]
	vsaFirst := report.Summary["vsa_first"]
	currentAppend := report.Summary["current_append"]
	t.Logf("VSA quantitative eval: baseline recall=%.3f mrr=%.3f; current_append recall=%.3f mrr=%.3f; vsa_first recall=%.3f mrr=%.3f",
		base.FactRecallAtK, base.MRR, currentAppend.FactRecallAtK, currentAppend.MRR, vsaFirst.FactRecallAtK, vsaFirst.MRR)

	if vsaFirst.FactRecallAtK < base.FactRecallAtK+0.20 {
		t.Fatalf("vsa_first fact recall %.3f did not clear baseline %.3f by >=0.20", vsaFirst.FactRecallAtK, base.FactRecallAtK)
	}
	if vsaFirst.MRR < base.MRR+0.15 {
		t.Fatalf("vsa_first MRR %.3f did not clear baseline %.3f by >=0.15", vsaFirst.MRR, base.MRR)
	}
	if vsaFirst.TenantLeakRate != 0 || vsaFirst.ExpiredLeakRate != 0 {
		t.Fatalf("vsa_first leak rates tenant=%.3f expired=%.3f, want zero", vsaFirst.TenantLeakRate, vsaFirst.ExpiredLeakRate)
	}
}

func TestVSABeforeSQLGraphPreservesTargetUnderBudget(t *testing.T) {
	ctx := context.Background()
	cases := buildVSAEvalCases()
	db := newVSAQuantEvalDB(t, cases)
	defer db.Close()

	prev := GetDBProvider()
	SetDBProvider(DBSQLite)
	defer SetDBProvider(prev)

	store := vsaStoreForDB(db, 1024, 1)
	if err := store.RebuildFromGraph(ctx, "ds-vsa"); err != nil {
		t.Fatalf("rebuild VSA: %v", err)
	}

	var sqlFirstHits, vsaFirstHits int
	for _, tc := range cases {
		sqlFirst := retrieveVSAEvalFacts(ctx, db, store, tc, "current_append")
		vsaFirst := retrieveVSAEvalFacts(ctx, db, store, tc, "vsa_first")
		if containsVSAEvalFact(sqlFirst, tc) {
			sqlFirstHits++
		}
		if containsVSAEvalFact(vsaFirst, tc) {
			vsaFirstHits++
		} else {
			t.Fatalf("vsa-before-sql missed target for case %s scenario=%s budget=%d", tc.ID, tc.Scenario, tc.Budget)
		}
	}
	t.Logf("VSA before SQL graph target hits=%d/%d; SQL before VSA target hits=%d/%d",
		vsaFirstHits, len(cases), sqlFirstHits, len(cases))
	if sqlFirstHits >= vsaFirstHits {
		t.Fatalf("VSA before SQL graph did not improve target hits: vsa_first=%d sql_first=%d", vsaFirstHits, sqlFirstHits)
	}
}

func buildVSAEvalCases() []vsaEvalCase {
	var out []vsaEvalCase
	add := func(scenario string, names []string, predicate string, budget, distractors, samePredicate int) {
		for i, name := range names {
			id := fmt.Sprintf("%s_%03d", scenario, i+1)
			src := strings.ToLower(strings.ReplaceAll(name, " ", "_"))
			out = append(out, vsaEvalCase{
				ID:            id,
				Scenario:      scenario,
				DatasetID:     "ds-vsa",
				SourceID:      id + "_" + src,
				SourceName:    name,
				Predicate:     predicate,
				TargetID:      id + "_aa_gold",
				TargetName:    name + " Gold",
				Budget:        budget,
				Distractors:   distractors,
				SamePredicate: samePredicate,
			})
		}
	}
	add("context_budget", []string{
		"Checkout", "Orders", "BillingAPI", "AuthService", "SearchAPI", "SyncWorker", "WorkspaceIndexer",
		"ReembedJob", "MCPServer", "GraphBuilder", "TenantMiddleware", "UploadHandler", "LLMProxy",
	}, "CALLS", 5, 24, 4)
	add("fanout", []string{
		"CheckoutFanout", "AuthFanout", "SearchFanout", "WorkspaceFanout", "SyncFanout", "ReembedFanout", "GraphFanout",
		"MCPFanout", "UploadFanout", "LLMFanout", "RerankFanout", "BackupFanout", "AuditFanout",
	}, "CALLS", 10, 80, 50)
	add("predicate_specific", []string{
		"CheckoutPredicate", "OrdersPredicate", "AuthPredicate", "SearchPredicate", "IndexPredicate", "SyncPredicate", "MCPPredicate",
		"UploadPredicate", "GraphPredicate", "TenantPredicate", "RerankPredicate", "CachePredicate", "AuditPredicate",
	}, "VALIDATES", 10, 36, 8)
	add("distractor_heavy", []string{
		"CheckoutDistractor", "AuthDistractor", "OrdersDistractor", "SearchDistractor", "WorkspaceDistractor", "SyncDistractor", "ReembedDistractor",
		"MCPDistractor", "GraphDistractor", "TenantDistractor", "UploadDistractor", "LLMDistractor", "RerankDistractor",
	}, "DEPENDS_ON", 10, 45, 15)
	return out
}

func containsVSAEvalFact(facts []vsaEvalFact, tc vsaEvalCase) bool {
	for _, fact := range facts {
		if fact.SourceID == tc.SourceID &&
			fact.Predicate == tc.Predicate &&
			fact.TargetID == tc.TargetID &&
			fact.DatasetID == tc.DatasetID &&
			!fact.Expired {
			return true
		}
	}
	return false
}

func newVSAQuantEvalDB(t *testing.T, cases []vsaEvalCase) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+t.TempDir()+"/vsa-quant-eval.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	schema := `
CREATE TABLE graph_nodes (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '',
	type TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	properties TEXT NOT NULL DEFAULT '{}',
	dataset_id TEXT NOT NULL DEFAULT ''
);
CREATE TABLE graph_edges (
	id TEXT PRIMARY KEY,
	source_id TEXT NOT NULL,
	target_id TEXT NOT NULL,
	relationship_name TEXT NOT NULL DEFAULT '',
	properties TEXT NOT NULL DEFAULT '{}',
	valid_until TEXT,
	dataset_id TEXT NOT NULL DEFAULT ''
);`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	for _, tc := range cases {
		mustExecVSAEval(t, db, `INSERT INTO graph_nodes(id, name, type, dataset_id) VALUES (?, ?, 'Service', ?)`, tc.SourceID, tc.SourceName, tc.DatasetID)
		mustExecVSAEval(t, db, `INSERT INTO graph_nodes(id, name, type, dataset_id) VALUES (?, ?, 'Target', ?)`, tc.TargetID, tc.TargetName, tc.DatasetID)
		for i := 0; i < tc.Distractors; i++ {
			targetID := fmt.Sprintf("%s_zz_%03d", tc.ID, i)
			targetName := fmt.Sprintf("%s Distractor %03d", tc.SourceName, i)
			predicate := fmt.Sprintf("NOISE_%02d", i%7)
			if i < tc.SamePredicate {
				predicate = tc.Predicate
			}
			mustExecVSAEval(t, db, `INSERT INTO graph_nodes(id, name, type, dataset_id) VALUES (?, ?, 'Distractor', ?)`, targetID, targetName, tc.DatasetID)
			mustExecVSAEval(t, db, `INSERT INTO graph_edges(id, source_id, target_id, relationship_name, dataset_id) VALUES (?, ?, ?, ?, ?)`,
				fmt.Sprintf("%s_a_%03d", tc.ID, i), tc.SourceID, targetID, predicate, tc.DatasetID)
		}
		otherID := tc.ID + "_tenant_b"
		expiredID := tc.ID + "_expired"
		mustExecVSAEval(t, db, `INSERT INTO graph_nodes(id, name, type, dataset_id) VALUES (?, ?, 'Foreign', 'ds-other')`, otherID, tc.SourceName+" TenantB")
		mustExecVSAEval(t, db, `INSERT INTO graph_edges(id, source_id, target_id, relationship_name, dataset_id) VALUES (?, ?, ?, ?, 'ds-other')`,
			tc.ID+"_b_tenant", tc.SourceID, otherID, tc.Predicate)
		mustExecVSAEval(t, db, `INSERT INTO graph_nodes(id, name, type, dataset_id) VALUES (?, ?, 'Expired', ?)`, expiredID, tc.SourceName+" Expired", tc.DatasetID)
		mustExecVSAEval(t, db, `INSERT INTO graph_edges(id, source_id, target_id, relationship_name, valid_until, dataset_id) VALUES (?, ?, ?, ?, '2025-01-01T00:00:00Z', ?)`,
			tc.ID+"_c_expired", tc.SourceID, expiredID, tc.Predicate, tc.DatasetID)
		mustExecVSAEval(t, db, `INSERT INTO graph_edges(id, source_id, target_id, relationship_name, dataset_id) VALUES (?, ?, ?, ?, ?)`,
			tc.ID+"_z_gold", tc.SourceID, tc.TargetID, tc.Predicate, tc.DatasetID)
	}
	return db
}

func mustExecVSAEval(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q args=%v: %v", query, args, err)
	}
}

func evaluateVSAMode(ctx context.Context, db *sql.DB, store *vsamemory.Store, cases []vsaEvalCase, mode string) map[string]vsaEvalModeMetrics {
	byScenarioFacts := map[string][][]vsaEvalFact{}
	byScenarioCases := map[string][]vsaEvalCase{}
	byScenarioLatency := map[string][]time.Duration{}
	for _, tc := range cases {
		start := time.Now()
		facts := retrieveVSAEvalFacts(ctx, db, store, tc, mode)
		byScenarioLatency[tc.Scenario] = append(byScenarioLatency[tc.Scenario], time.Since(start))
		byScenarioFacts[tc.Scenario] = append(byScenarioFacts[tc.Scenario], facts)
		byScenarioCases[tc.Scenario] = append(byScenarioCases[tc.Scenario], tc)
	}
	out := make(map[string]vsaEvalModeMetrics, len(byScenarioCases)+1)
	for scenario, scCases := range byScenarioCases {
		out[scenario] = scoreVSAEval(scCases, byScenarioFacts[scenario], byScenarioLatency[scenario])
	}
	out["all"] = scoreVSAEval(cases, flattenVSAEvalFacts(byScenarioFacts, cases), flattenVSAEvalDurations(byScenarioLatency, cases))
	return out
}

func retrieveVSAEvalFacts(ctx context.Context, db *sql.DB, store *vsamemory.Store, tc vsaEvalCase, mode string) []vsaEvalFact {
	switch mode {
	case "baseline_sql_graph":
		return retrieveSQLSourceFacts(ctx, db, tc)
	case "vsa_empty_index", "vsa_only":
		return retrieveVSAFacts(ctx, store, tc, tc.Budget)
	case "current_append":
		base := retrieveSQLSourceFacts(ctx, db, tc)
		if len(base) >= tc.Budget {
			return base
		}
		return mergeVSAEvalFacts(base, retrieveVSAFacts(ctx, store, tc, tc.Budget-len(base)), tc.Budget)
	case "vsa_first":
		first := retrieveVSAFacts(ctx, store, tc, tc.Budget)
		return mergeVSAEvalFacts(first, retrieveSQLSourceFacts(ctx, db, tc), tc.Budget)
	default:
		return nil
	}
}

func retrieveSQLSourceFacts(ctx context.Context, db *sql.DB, tc vsaEvalCase) []vsaEvalFact {
	rows, err := db.QueryContext(ctx, `
		SELECT source_id, relationship_name, target_id, dataset_id, COALESCE(valid_until, '')
		FROM graph_edges
		WHERE source_id = ? AND dataset_id = ?
		ORDER BY id
		LIMIT ?`, tc.SourceID, tc.DatasetID, tc.Budget)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []vsaEvalFact
	for rows.Next() {
		var fact vsaEvalFact
		var validUntil string
		if rows.Scan(&fact.SourceID, &fact.Predicate, &fact.TargetID, &fact.DatasetID, &validUntil) == nil {
			fact.Expired = validUntil != ""
			out = append(out, fact)
		}
	}
	return out
}

func retrieveVSAFacts(ctx context.Context, store *vsamemory.Store, tc vsaEvalCase, budget int) []vsaEvalFact {
	if budget <= 0 {
		return nil
	}
	candidates, err := store.QueryObject(ctx, tc.DatasetID, tc.SourceID, tc.Predicate, budget)
	if err != nil {
		return nil
	}
	out := make([]vsaEvalFact, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, vsaEvalFact{
			SourceID:  tc.SourceID,
			Predicate: c.Predicate,
			TargetID:  c.TargetID,
			DatasetID: c.DatasetID,
		})
	}
	return out
}

func mergeVSAEvalFacts(primary, secondary []vsaEvalFact, budget int) []vsaEvalFact {
	out := make([]vsaEvalFact, 0, budget)
	seen := map[string]struct{}{}
	add := func(f vsaEvalFact) {
		if len(out) >= budget {
			return
		}
		key := f.SourceID + "\x00" + f.Predicate + "\x00" + f.TargetID + "\x00" + f.DatasetID
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, f)
	}
	for _, f := range primary {
		add(f)
	}
	for _, f := range secondary {
		add(f)
	}
	return out
}

func scoreVSAEval(cases []vsaEvalCase, factsByCase [][]vsaEvalFact, latencies []time.Duration) vsaEvalModeMetrics {
	var factHits, targetHits, predicateHits, retrieved, leaks, expiredLeaks int
	var mrr, ndcg, contextFacts float64
	for i, tc := range cases {
		facts := factsByCase[i]
		contextFacts += float64(len(facts))
		rank := 0
		for j, fact := range facts {
			if fact.Predicate == tc.Predicate {
				predicateHits++
			}
			if fact.DatasetID != tc.DatasetID {
				leaks++
			}
			if fact.Expired {
				expiredLeaks++
			}
			if fact.TargetID == tc.TargetID {
				targetHits++
				if fact.Predicate == tc.Predicate && fact.SourceID == tc.SourceID {
					factHits++
					if rank == 0 {
						rank = j + 1
					}
				}
			}
		}
		retrieved += len(facts)
		if rank > 0 {
			mrr += 1 / float64(rank)
			ndcg += 1 / math.Log2(float64(rank+1))
		}
	}
	n := float64(len(cases))
	if n == 0 {
		return vsaEvalModeMetrics{}
	}
	precisionDenom := float64(retrieved)
	if precisionDenom == 0 {
		precisionDenom = 1
	}
	latencyP50, latencyP95 := percentileDurations(latencies, 0.50), percentileDurations(latencies, 0.95)
	return vsaEvalModeMetrics{
		FactRecallAtK:         float64(factHits) / n,
		TargetRecallAtK:       float64(targetHits) / n,
		PredicatePrecisionAtK: float64(predicateHits) / precisionDenom,
		MRR:                   mrr / n,
		NDCGAtK:               ndcg / n,
		ContextFactsAvg:       contextFacts / n,
		LatencyP50Micros:      latencyP50.Microseconds(),
		LatencyP95Micros:      latencyP95.Microseconds(),
		TenantLeakRate:        float64(leaks) / precisionDenom,
		ExpiredLeakRate:       float64(expiredLeaks) / precisionDenom,
	}
}

func percentileDurations(values []time.Duration, p float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
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

func flattenVSAEvalFacts(byScenario map[string][][]vsaEvalFact, cases []vsaEvalCase) [][]vsaEvalFact {
	out := make([][]vsaEvalFact, 0, len(cases))
	offsets := map[string]int{}
	for _, tc := range cases {
		idx := offsets[tc.Scenario]
		out = append(out, byScenario[tc.Scenario][idx])
		offsets[tc.Scenario] = idx + 1
	}
	return out
}

func flattenVSAEvalDurations(byScenario map[string][]time.Duration, cases []vsaEvalCase) []time.Duration {
	out := make([]time.Duration, 0, len(cases))
	offsets := map[string]int{}
	for _, tc := range cases {
		idx := offsets[tc.Scenario]
		out = append(out, byScenario[tc.Scenario][idx])
		offsets[tc.Scenario] = idx + 1
	}
	return out
}

func buildVSAEvalReport(cases []vsaEvalCase, modes []string, byMode map[string]map[string]vsaEvalModeMetrics) vsaEvalReport {
	byScenario := map[string]map[string]vsaEvalModeMetrics{}
	summary := map[string]vsaEvalModeMetrics{}
	for _, mode := range modes {
		summary[mode] = byMode[mode]["all"]
		for scenario, metrics := range byMode[mode] {
			if scenario == "all" {
				continue
			}
			if byScenario[scenario] == nil {
				byScenario[scenario] = map[string]vsaEvalModeMetrics{}
			}
			byScenario[scenario][mode] = metrics
		}
	}
	lift := map[string]map[string]float64{}
	for _, mode := range modes {
		if mode == "baseline_sql_graph" {
			continue
		}
		m := summary[mode]
		b := summary["baseline_sql_graph"]
		lift[mode] = map[string]float64{
			"fact_recall_at_k":         m.FactRecallAtK - b.FactRecallAtK,
			"target_recall_at_k":       m.TargetRecallAtK - b.TargetRecallAtK,
			"predicate_precision_at_k": m.PredicatePrecisionAtK - b.PredicatePrecisionAtK,
			"mrr":                      m.MRR - b.MRR,
			"ndcg_at_k":                m.NDCGAtK - b.NDCGAtK,
			"latency_p95_micros":       float64(m.LatencyP95Micros - b.LatencyP95Micros),
		}
	}
	return vsaEvalReport{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Cases:       len(cases),
		Budgets: map[string]int{
			"context_budget":     5,
			"fanout":             10,
			"predicate_specific": 10,
			"distractor_heavy":   10,
		},
		Modes:      modes,
		ByScenario: byScenario,
		Summary:    summary,
		Lift:       lift,
		Notes: []string{
			"current_append models the current graph-search integration: SQL context consumes budget before VSA is appended.",
			"vsa_first models a VSA-aware budget policy: predicate VSA facts are retrieved before SQL graph filler.",
			"TestVSABeforeSQLGraphPreservesTargetUnderBudget checks the order directly: VSA-before-SQL finds the target in all 52 cases, SQL-before-VSA finds none.",
			"Positive vsa_first lift quantifies VSA signal; flat current_append lift indicates budget allocation can hide that signal.",
		},
	}
}

func writeVSAEvalReportIfRequested(t *testing.T, report vsaEvalReport) {
	t.Helper()
	if os.Getenv("LEVARA_WRITE_VSA_EVAL_REPORT") == "" {
		return
	}
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	resultsDir := filepath.Join(repoRoot, "benchmark", "results")
	jsonPath := filepath.Join(resultsDir, "vsa_quantitative_eval_latest.json")
	mdPath := filepath.Join(repoRoot, "docs", "vsa-quantitative-eval-report.md")
	if err := os.MkdirAll(resultsDir, 0755); err != nil {
		t.Fatalf("mkdir benchmark/results: %v", err)
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if err := os.WriteFile(jsonPath, raw, 0644); err != nil {
		t.Fatalf("write json report: %v", err)
	}
	md := renderVSAEvalMarkdown(report)
	if err := os.WriteFile(mdPath, []byte(md), 0644); err != nil {
		t.Fatalf("write markdown report: %v", err)
	}
	absJSON, _ := filepath.Abs(jsonPath)
	absMD, _ := filepath.Abs(mdPath)
	t.Logf("wrote VSA eval reports: %s %s", absJSON, absMD)
}

func renderVSAEvalMarkdown(report vsaEvalReport) string {
	var b strings.Builder
	b.WriteString("# VSA Quantitative Evaluation Report\n\n")
	b.WriteString("Generated: " + report.GeneratedAt + "\n\n")
	fmt.Fprintf(&b, "Cases: %d\n\n", report.Cases)
	b.WriteString("## Summary\n\n")
	b.WriteString("| Mode | fact_recall@k | MRR | nDCG@k | predicate_precision@k | p95 latency (us) |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|\n")
	for _, mode := range report.Modes {
		m := report.Summary[mode]
		fmt.Fprintf(&b, "| %s | %.3f | %.3f | %.3f | %.3f | %d |\n",
			mode, m.FactRecallAtK, m.MRR, m.NDCGAtK, m.PredicatePrecisionAtK, m.LatencyP95Micros)
	}
	b.WriteString("\n## Lift vs Baseline\n\n")
	b.WriteString("| Mode | fact_recall lift | MRR lift | nDCG lift | precision lift | p95 latency delta (us) |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|\n")
	for _, mode := range report.Modes {
		if mode == "baseline_sql_graph" {
			continue
		}
		l := report.Lift[mode]
		fmt.Fprintf(&b, "| %s | %.3f | %.3f | %.3f | %.3f | %.0f |\n",
			mode, l["fact_recall_at_k"], l["mrr"], l["ndcg_at_k"], l["predicate_precision_at_k"], l["latency_p95_micros"])
	}
	b.WriteString("\n## By Scenario\n\n")
	scenarios := make([]string, 0, len(report.ByScenario))
	for scenario := range report.ByScenario {
		scenarios = append(scenarios, scenario)
	}
	sort.Strings(scenarios)
	for _, scenario := range scenarios {
		b.WriteString("### " + scenario + "\n\n")
		b.WriteString("| Mode | fact_recall@k | MRR | nDCG@k | predicate_precision@k | context facts avg |\n")
		b.WriteString("|---|---:|---:|---:|---:|---:|\n")
		for _, mode := range report.Modes {
			m := report.ByScenario[scenario][mode]
			fmt.Fprintf(&b, "| %s | %.3f | %.3f | %.3f | %.3f | %.2f |\n",
				mode, m.FactRecallAtK, m.MRR, m.NDCGAtK, m.PredicatePrecisionAtK, m.ContextFactsAvg)
		}
		b.WriteString("\n")
	}
	b.WriteString("## Notes\n\n")
	for _, note := range report.Notes {
		b.WriteString("- " + note + "\n")
	}
	return b.String()
}
