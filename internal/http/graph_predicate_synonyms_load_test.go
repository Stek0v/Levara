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
)

type predicateSynonymLoadCase struct {
	Query    string `json:"query"`
	Expected string `json:"expected"`
}

type predicateSynonymLoadMetrics struct {
	Cases              int     `json:"cases"`
	Predicates         int     `json:"predicates"`
	Synonyms           int     `json:"synonyms"`
	Top1Accuracy       float64 `json:"top1_accuracy"`
	MRR                float64 `json:"mrr"`
	LatencyP50Micros   int64   `json:"latency_p50_micros"`
	LatencyP95Micros   int64   `json:"latency_p95_micros"`
	LatencyMaxMicros   int64   `json:"latency_max_micros"`
	ThroughputQPS      float64 `json:"throughput_qps"`
	RefreshMillis      int64   `json:"refresh_millis"`
	LoadSynonymsMillis int64   `json:"load_synonyms_millis"`
}

func TestPredicateSynonymMapLoadQualityAndSpeed(t *testing.T) {
	ctx := context.Background()
	db := newPredicateSynonymLoadDB(t)
	defer db.Close()

	prev := GetDBProvider()
	SetDBProvider(DBSQLite)
	defer SetDBProvider(prev)

	predicates, cases := seedPredicateSynonymLoadFixture(t, db)
	startRefresh := time.Now()
	if err := refreshPredicateSynonyms(ctx, db, "load-ds"); err != nil {
		t.Fatalf("refresh predicate synonyms: %v", err)
	}
	refreshLatency := time.Since(startRefresh)

	startLoad := time.Now()
	synonyms := loadPredicateSynonyms(ctx, db, []string{"load-ds"}, predicates)
	loadLatency := time.Since(startLoad)
	synonymCount := 0
	for _, values := range synonyms {
		synonymCount += len(values)
	}

	metrics := evaluatePredicateSynonymLoad(predicates, synonyms, cases, refreshLatency, loadLatency)
	writePredicateSynonymLoadReportIfRequested(t, metrics)
	t.Logf("predicate synonym load: top1=%.3f mrr=%.3f p50=%dus p95=%dus max=%dus qps=%.0f refresh=%dms load=%dms predicates=%d synonyms=%d",
		metrics.Top1Accuracy, metrics.MRR, metrics.LatencyP50Micros, metrics.LatencyP95Micros,
		metrics.LatencyMaxMicros, metrics.ThroughputQPS, metrics.RefreshMillis, metrics.LoadSynonymsMillis,
		metrics.Predicates, metrics.Synonyms)

	if metrics.Top1Accuracy < 0.98 {
		t.Fatalf("top1 accuracy %.3f below 0.98", metrics.Top1Accuracy)
	}
	if metrics.MRR < 0.99 {
		t.Fatalf("MRR %.3f below 0.99", metrics.MRR)
	}
	if enforcePerfBudgets() && !raceDetectorEnabled() {
		if metrics.LatencyP95Micros > 5000 {
			t.Fatalf("p95 latency %dus above 5000us", metrics.LatencyP95Micros)
		}
		if metrics.ThroughputQPS < 200 {
			t.Fatalf("throughput %.0fqps below 200qps", metrics.ThroughputQPS)
		}
		if metrics.RefreshMillis > 1500 {
			t.Fatalf("refresh latency %dms above 1500ms", metrics.RefreshMillis)
		}
		if metrics.LoadSynonymsMillis > 500 {
			t.Fatalf("load latency %dms above 500ms", metrics.LoadSynonymsMillis)
		}
	}
}

func newPredicateSynonymLoadDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+t.TempDir()+"/predicate-synonyms-load.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	schema := `
CREATE TABLE graph_edges (
	id TEXT PRIMARY KEY,
	source_id TEXT NOT NULL DEFAULT '',
	target_id TEXT NOT NULL DEFAULT '',
	relationship_name TEXT NOT NULL DEFAULT '',
	valid_until TEXT,
	dataset_id TEXT NOT NULL DEFAULT ''
);`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create load schema: %v", err)
	}
	return db
}

func seedPredicateSynonymLoadFixture(t *testing.T, db *sql.DB) ([]string, []predicateSynonymLoadCase) {
	t.Helper()
	if err := ensureGraphPredicateSynonymSchema(context.Background(), db); err != nil {
		t.Fatalf("ensure predicate synonym schema: %v", err)
	}
	base := []struct {
		predicate string
		synonyms  []string
		queries   []string
	}{
		{"OWNED_BY", []string{"maintains", "maintainer", "owner"}, []string{"who maintains service %03d", "owner for service %03d"}},
		{"DEPENDS_ON", []string{"requires", "needs", "dependency"}, []string{"what requires service %03d", "dependency for service %03d"}},
		{"VALIDATES", []string{"checks", "verifies", "ensures"}, []string{"what checks service %03d", "who verifies service %03d"}},
		{"CALLS", []string{"invokes", "requests", "talks"}, []string{"what invokes service %03d", "who requests service %03d"}},
		{"REPORTS_TO", []string{"reports", "manager", "lead"}, []string{"who reports for service %03d", "manager for service %03d"}},
		{"RUNS_ON", []string{"runtime", "hosted", "platform"}, []string{"runtime for service %03d", "where hosted service %03d"}},
		{"SECURED_BY", []string{"protected", "authenticates", "guards"}, []string{"what protects service %03d", "who guards service %03d"}},
		{"EMITS", []string{"publishes", "sends", "produces"}, []string{"what publishes service %03d", "what sends service %03d"}},
	}
	var predicates []string
	var cases []predicateSynonymLoadCase
	for i := 0; i < 100; i++ {
		for _, b := range base {
			predicate := fmt.Sprintf("%s_%03d", b.predicate, i)
			predicates = append(predicates, predicate)
			if _, err := db.Exec(`INSERT INTO graph_edges(id, source_id, target_id, relationship_name, dataset_id) VALUES (?, ?, ?, ?, ?)`,
				fmt.Sprintf("edge-%s", predicate), fmt.Sprintf("src-%03d", i), fmt.Sprintf("target-%03d", i), predicate, "load-ds"); err != nil {
				t.Fatalf("insert graph edge: %v", err)
			}
			for _, synonym := range b.synonyms {
				if err := upsertGraphPredicateSynonym(context.Background(), db, "load-ds", predicate, fmt.Sprintf("%s%d", synonym, i), predicateSynonymSourceManual, predicateSynonymWeightManual); err != nil {
					t.Fatalf("insert manual synonym: %v", err)
				}
			}
			for _, queryFmt := range b.queries {
				cases = append(cases, predicateSynonymLoadCase{
					Query:    fmt.Sprintf(queryFmt, i),
					Expected: predicate,
				})
			}
		}
	}
	sort.Strings(predicates)
	return predicates, cases
}

func evaluatePredicateSynonymLoad(predicates []string, synonyms map[string][]graphPredicateSynonym, cases []predicateSynonymLoadCase, refreshLatency, loadLatency time.Duration) predicateSynonymLoadMetrics {
	var top1 int
	var rr float64
	latencies := make([]time.Duration, 0, len(cases))
	startAll := time.Now()
	for _, tc := range cases {
		start := time.Now()
		ranked := rankVSAPredicatesForQuery(predicates, tc.Query, synonyms)
		latencies = append(latencies, time.Since(start))
		if len(ranked) > 0 && ranked[0] == tc.Expected {
			top1++
		}
		for i, predicate := range ranked {
			if predicate == tc.Expected {
				rr += 1.0 / float64(i+1)
				break
			}
		}
	}
	elapsed := time.Since(startAll)
	synonymCount := 0
	for _, values := range synonyms {
		synonymCount += len(values)
	}
	return predicateSynonymLoadMetrics{
		Cases:              len(cases),
		Predicates:         len(predicates),
		Synonyms:           synonymCount,
		Top1Accuracy:       float64(top1) / float64(len(cases)),
		MRR:                rr / float64(len(cases)),
		LatencyP50Micros:   percentilePredicateSynonymLatency(latencies, 0.50).Microseconds(),
		LatencyP95Micros:   percentilePredicateSynonymLatency(latencies, 0.95).Microseconds(),
		LatencyMaxMicros:   percentilePredicateSynonymLatency(latencies, 1.00).Microseconds(),
		ThroughputQPS:      float64(len(cases)) / elapsed.Seconds(),
		RefreshMillis:      refreshLatency.Milliseconds(),
		LoadSynonymsMillis: loadLatency.Milliseconds(),
	}
}

func percentilePredicateSynonymLatency(values []time.Duration, p float64) time.Duration {
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

func writePredicateSynonymLoadReportIfRequested(t *testing.T, metrics predicateSynonymLoadMetrics) {
	t.Helper()
	if os.Getenv("LEVARA_WRITE_PREDICATE_SYNONYM_LOAD_REPORT") == "" {
		return
	}
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	resultsDir := filepath.Join(repoRoot, "benchmark", "results")
	if err := os.MkdirAll(resultsDir, 0755); err != nil {
		t.Fatalf("mkdir benchmark/results: %v", err)
	}
	raw, err := json.MarshalIndent(metrics, "", "  ")
	if err != nil {
		t.Fatalf("marshal predicate synonym load metrics: %v", err)
	}
	jsonPath := filepath.Join(resultsDir, "predicate_synonym_load_latest.json")
	if err := os.WriteFile(jsonPath, raw, 0644); err != nil {
		t.Fatalf("write predicate synonym load json: %v", err)
	}
	mdPath := filepath.Join(repoRoot, "docs", "predicate-synonym-load-report.md")
	if err := os.WriteFile(mdPath, []byte(renderPredicateSynonymLoadMarkdown(metrics)), 0644); err != nil {
		t.Fatalf("write predicate synonym load markdown: %v", err)
	}
}

func renderPredicateSynonymLoadMarkdown(metrics predicateSynonymLoadMetrics) string {
	var b strings.Builder
	b.WriteString("# Predicate Synonym Map Load Report\n\n")
	fmt.Fprintf(&b, "Cases: %d\n\n", metrics.Cases)
	fmt.Fprintf(&b, "Predicates: %d\n\n", metrics.Predicates)
	fmt.Fprintf(&b, "Synonyms: %d\n\n", metrics.Synonyms)
	b.WriteString("| Metric | Value |\n")
	b.WriteString("|---|---:|\n")
	fmt.Fprintf(&b, "| top1 accuracy | %.3f |\n", metrics.Top1Accuracy)
	fmt.Fprintf(&b, "| MRR | %.3f |\n", metrics.MRR)
	fmt.Fprintf(&b, "| p50 latency (us) | %d |\n", metrics.LatencyP50Micros)
	fmt.Fprintf(&b, "| p95 latency (us) | %d |\n", metrics.LatencyP95Micros)
	fmt.Fprintf(&b, "| max latency (us) | %d |\n", metrics.LatencyMaxMicros)
	fmt.Fprintf(&b, "| throughput (qps) | %.0f |\n", metrics.ThroughputQPS)
	fmt.Fprintf(&b, "| refresh latency (ms) | %d |\n", metrics.RefreshMillis)
	fmt.Fprintf(&b, "| synonym load latency (ms) | %d |\n", metrics.LoadSynonymsMillis)
	return b.String()
}
