//go:build v3bench

package store

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	v3BenchSeed        = int64(20261008)
	v3BenchMetadataLen = 4096
	v3BenchQueries     = 10
	v3BenchTopK        = 10
	v3BenchMaxDisk     = uint64(4 << 30)
	v3BenchMinFree     = uint64(12 << 30)
)

type v3BenchConfig struct {
	Mode           string `json:"mode"`
	Seed           int64  `json:"seed"`
	Dimension      int    `json:"dimension"`
	Records        int    `json:"records"`
	Fanout         int    `json:"directory_fanout"`
	Populated      int    `json:"populated_collections"`
	Queries        int    `json:"queries"`
	TopK           int    `json:"top_k"`
	Metadata       int    `json:"metadata_bytes"`
	M              int    `json:"hnsw_m"`
	M0             int    `json:"hnsw_m0"`
	EfConstruction int    `json:"hnsw_ef_construction"`
	EfMult         int    `json:"hnsw_ef_search_mult"`
	EfMin          int    `json:"hnsw_ef_search_min"`
	TimeoutSec     int    `json:"child_timeout_seconds"`
}

type v3BenchOracle struct {
	Queries  [][]float32 `json:"queries"`
	Expected [][]string  `json:"expected_top_10"`
}

type v3BenchReceipt struct {
	Mode                     string            `json:"mode"`
	ColdKind                 string            `json:"cold_kind"`
	Run                      int               `json:"run"`
	Revision                 string            `json:"revision"`
	WorkspaceRevision        string            `json:"workspace_revision"`
	BenchmarkSourceSHA256    string            `json:"benchmark_source_sha256"`
	SourceTreeSHA256         string            `json:"source_tree_sha256"`
	TestExecutableSHA256     string            `json:"test_executable_sha256"`
	BuildInfoSHA256          string            `json:"build_info_sha256"`
	ConfigSHA256             string            `json:"config_sha256"`
	GoVersion                string            `json:"go_version"`
	GOOS                     string            `json:"goos"`
	GOARCH                   string            `json:"goarch"`
	GOMAXPROCS               int               `json:"gomaxprocs"`
	Config                   v3BenchConfig     `json:"config"`
	FixtureSHA256            map[string]string `json:"fixture_sha256"`
	QuerySHA256              string            `json:"query_sha256"`
	WallMilliseconds         float64           `json:"wall_ms"`
	CPUMilliseconds          float64           `json:"cpu_ms"`
	PeakRSSBytes             uint64            `json:"peak_rss_bytes"`
	InputWALBytes            uint64            `json:"input_wal_bytes"`
	OutputMetaBytes          uint64            `json:"output_meta_bytes"`
	PersistentDiskBytes      uint64            `json:"persistent_disk_bytes"`
	RecordCount              int               `json:"record_count"`
	ExpectedRecordCount      int               `json:"expected_record_count"`
	SearchP50Milliseconds    float64           `json:"search_p50_ms"`
	SearchP95Milliseconds    float64           `json:"search_p95_ms"`
	SearchP99Milliseconds    float64           `json:"search_p99_ms"`
	SearchQueriesPerSecond   float64           `json:"search_queries_per_second"`
	StartupRecordsPerSecond  *float64          `json:"startup_records_per_second,omitempty"`
	ListenerReadinessMS      float64           `json:"listener_readiness_ms"`
	ExecutedQueryCount       int               `json:"executed_query_count"`
	QualityApplicable        bool              `json:"quality_applicable"`
	QualitySampleCount       int               `json:"quality_sample_count"`
	ReturnedResultCount      int               `json:"returned_result_count"`
	SnapshotBytes            int64             `json:"snapshot_bytes,omitempty"`
	PeakTemporaryDiskBytes   int64             `json:"peak_temporary_disk_bytes,omitempty"`
	LazyLoadMilliseconds     float64           `json:"lazy_load_ms,omitempty"`
	LazyLoadRecordsPerSecond float64           `json:"lazy_load_records_per_second,omitempty"`
	RecallAt10               *float64          `json:"recall_at_10,omitempty"`
	ResultSHA256             string            `json:"result_sha256"`
	Errors                   []string          `json:"errors"`
}

// TestV3StartupBenchmark measures S0: process-cold native WAL replay plus the
// current synchronous HNSW rebuild. It never accepts a production data path.
func TestV3StartupBenchmark(t *testing.T) {
	if os.Getenv("LEV_V3_BENCH_CHILD") == "1" {
		runV3BenchChild(t)
		return
	}

	cfg := v3BenchConfig{
		Mode:       envString("LEV_V3_BENCH_MODE", "S0"),
		Seed:       envInt64(t, "LEV_V3_BENCH_SEED", v3BenchSeed),
		Dimension:  envInt(t, "LEV_V3_BENCH_DIM", 768),
		Records:    envInt(t, "LEV_V3_BENCH_RECORDS", 1000),
		Fanout:     envInt(t, "LEV_V3_BENCH_FANOUT", 1),
		Populated:  1,
		Queries:    v3BenchQueries,
		TopK:       v3BenchTopK,
		Metadata:   v3BenchMetadataLen,
		TimeoutSec: envInt(t, "LEV_V3_BENCH_TIMEOUT_SEC", 120),
	}
	if cfg.Fanout == 326 {
		cfg.Populated = 2
	}
	hnsw := DefaultHNSWConfig()
	cfg.M = envInt(t, "LEV_V3_BENCH_HNSW_M", hnsw.M)
	cfg.M0 = envInt(t, "LEV_V3_BENCH_HNSW_M0", hnsw.M0)
	cfg.EfConstruction = envInt(t, "LEV_V3_BENCH_HNSW_EF_CONSTRUCTION", hnsw.EfConstruction)
	cfg.EfMult = envInt(t, "LEV_V3_BENCH_HNSW_EF_SEARCH_MULT", hnsw.EfSearchMult)
	cfg.EfMin = envInt(t, "LEV_V3_BENCH_HNSW_EF_SEARCH_MIN", hnsw.EfSearchMin)
	validateV3BenchConfig(t, cfg)

	prefix := t.TempDir()
	root := filepath.Join(prefix, "work")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	ownerTokenBytes := make([]byte, 32)
	if _, err := cryptorand.Read(ownerTokenBytes); err != nil {
		t.Fatal(err)
	}
	ownerToken := hex.EncodeToString(ownerTokenBytes)
	capabilityPath := filepath.Join(prefix, ".v3-bench-capability")
	if err := os.WriteFile(capabilityPath, []byte(ownerToken), 0o600); err != nil {
		t.Fatal(err)
	}
	estimated := estimateV3BenchDisk(cfg)
	if estimated > v3BenchMaxDisk {
		t.Skipf("disk preflight: estimate %d exceeds 4 GiB task budget", estimated)
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(root, &stat); err != nil {
		t.Fatalf("disk preflight: %v", err)
	}
	free := uint64(stat.Bavail) * uint64(stat.Bsize)
	if free < estimated+v3BenchMinFree {
		t.Skipf("disk preflight: free=%d estimate=%d would leave less than 12 GiB", free, estimated)
	}

	oracle, fixtureHashes, snapshotStats := buildV3BenchFixture(t, root, cfg)
	oraclePath := filepath.Join(root, "oracle.json")
	writeJSON(t, oraclePath, oracle)
	queryHash := fileSHA256(t, oraclePath)
	revision := v3BenchRevision(t)
	sourceHash := v3BenchSourceHash(t)
	sourceTreeHash := v3BenchSourceTreeHash(t)
	configJSON := mustJSON(t, cfg)
	configHash := sha256Hex([]byte(configJSON))
	workspaceRevision := revision + "+startup-source-tree.sha256:" + sourceTreeHash
	testExecutableHash := v3BenchExecutableHash(t)
	buildInfoHash := v3BenchBuildInfoHash(t)
	fixtureTreeHash := directoryContentSHA256(t, filepath.Join(root, "collections"))
	fixtureHashes["collections_tree"] = fixtureTreeHash
	if uint64(snapshotStats.PeakTempBytes)+directorySize(t, root) > v3BenchMaxDisk {
		t.Fatal("snapshot publication peak exceeds 4 GiB task budget")
	}
	checkV3BenchDiskBudget(t, root)

	var resultHash string
	for run := 1; run <= 3; run++ {
		if got := directoryContentSHA256(t, filepath.Join(root, "collections")); got != fixtureTreeHash {
			t.Fatalf("run %d fixture changed before child: got %s want %s", run, got, fixtureTreeHash)
		}
		receiptPath := filepath.Join(root, fmt.Sprintf("receipt-%d.json", run))
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSec)*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestV3StartupBenchmark$", "-test.count=1")
		capability, err := os.Open(capabilityPath)
		if err != nil {
			t.Fatal(err)
		}
		cmd.ExtraFiles = []*os.File{capability}
		cmd.Env = append(os.Environ(),
			"LEV_V3_BENCH_CHILD=1",
			"LEV_V3_BENCH_PREFIX="+prefix,
			"LEV_V3_BENCH_ROOT="+root,
			"LEV_V3_BENCH_CAPABILITY_FD=3",
			"LEV_V3_BENCH_CONFIG="+configJSON,
			"LEV_V3_BENCH_ORACLE="+oraclePath,
			"LEV_V3_BENCH_RECEIPT="+receiptPath,
			"LEV_V3_BENCH_REVISION="+revision,
			"LEV_V3_BENCH_WORKSPACE_REVISION="+workspaceRevision,
			"LEV_V3_BENCH_SOURCE_SHA="+sourceHash,
			"LEV_V3_BENCH_SOURCE_TREE_SHA="+sourceTreeHash,
			"LEV_V3_BENCH_EXECUTABLE_SHA="+testExecutableHash,
			"LEV_V3_BENCH_BUILD_INFO_SHA="+buildInfoHash,
			"LEV_V3_BENCH_CONFIG_SHA="+configHash,
			"LEV_V3_BENCH_FIXTURE_SHA="+mustJSON(t, fixtureHashes),
			"LEV_V3_BENCH_RUN="+strconv.Itoa(run),
			"LEV_V3_BENCH_SNAPSHOT_STATS="+mustJSON(t, snapshotStats),
			"GODEBUG="+appendGODEBUG(os.Getenv("GODEBUG"), "randseednop=0"),
		)
		output, err := cmd.CombinedOutput()
		_ = capability.Close()
		cancel()
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("run %d timed out after %ds; child was killed", run, cfg.TimeoutSec)
		}
		if err != nil {
			t.Fatalf("run %d child failed: %v\n%s", run, err, output)
		}
		if got := directoryContentSHA256(t, filepath.Join(root, "collections")); got != fixtureTreeHash {
			t.Fatalf("run %d fixture changed after child: got %s want %s", run, got, fixtureTreeHash)
		}
		checkV3BenchDiskBudget(t, root)
		data, err := os.ReadFile(receiptPath)
		if err != nil {
			t.Fatalf("run %d receipt: %v\n%s", run, err, output)
		}
		var receipt v3BenchReceipt
		if err := json.Unmarshal(data, &receipt); err != nil {
			t.Fatalf("run %d receipt JSON: %v", run, err)
		}
		expectedRecords := cfg.Records
		if cfg.Mode == "S2" {
			expectedRecords++
		}
		if len(receipt.Errors) != 0 || receipt.RecordCount != expectedRecords {
			t.Fatalf("run %d invalid receipt: %s", run, data)
		}
		if receipt.Revision != revision || receipt.BenchmarkSourceSHA256 != sourceHash || receipt.SourceTreeSHA256 != sourceTreeHash || receipt.WorkspaceRevision != workspaceRevision || receipt.TestExecutableSHA256 != testExecutableHash || receipt.BuildInfoSHA256 != buildInfoHash || receipt.ConfigSHA256 != configHash || sha256Hex([]byte(mustJSON(t, receipt.Config))) != configHash {
			t.Fatalf("run %d source/config identity mismatch: %s", run, data)
		}
		if receipt.QuerySHA256 != queryHash {
			t.Fatalf("run %d query digest changed: got %s want %s", run, receipt.QuerySHA256, queryHash)
		}
		if receipt.SnapshotBytes != snapshotStats.SnapshotBytes || receipt.PeakTemporaryDiskBytes != snapshotStats.PeakTempBytes {
			t.Fatalf("run %d snapshot size evidence mismatch: %s", run, data)
		}
		if cfg.Mode == "S3" {
			if receipt.StartupRecordsPerSecond != nil || receipt.ListenerReadinessMS <= 0 || receipt.LazyLoadMilliseconds <= 0 || receipt.LazyLoadRecordsPerSecond <= 0 {
				t.Fatalf("run %d lazy timing evidence invalid: %s", run, data)
			}
		} else if receipt.StartupRecordsPerSecond == nil || receipt.ListenerReadinessMS != receipt.WallMilliseconds {
			t.Fatalf("run %d eager timing evidence invalid: %s", run, data)
		}
		if cfg.Records == 0 {
			if receipt.QualityApplicable || receipt.QualitySampleCount != 0 || receipt.ExecutedQueryCount != 1 || receipt.ReturnedResultCount != 0 || receipt.RecallAt10 != nil {
				t.Fatalf("run %d empty quality must be not applicable: %s", run, data)
			}
		} else if !receipt.QualityApplicable || receipt.QualitySampleCount == 0 || receipt.ExecutedQueryCount == 0 || receipt.RecallAt10 == nil {
			t.Fatalf("run %d nonempty quality evidence missing: %s", run, data)
		}
		if run == 1 {
			resultHash = receipt.ResultSHA256
		} else if receipt.ResultSHA256 != resultHash {
			t.Fatalf("run %d result digest changed: got %s want %s", run, receipt.ResultSHA256, resultHash)
		}
		t.Logf("V3_STARTUP_RECEIPT_JSON=%s", data)
	}
}

func runV3BenchChild(t *testing.T) {
	prefix := os.Getenv("LEV_V3_BENCH_PREFIX")
	root := os.Getenv("LEV_V3_BENCH_ROOT")
	capabilityFD := envInt(t, "LEV_V3_BENCH_CAPABILITY_FD", -1)
	capability := os.NewFile(uintptr(capabilityFD), "v3-bench-capability")
	if capability == nil {
		t.Fatal("missing inherited benchmark capability")
	}
	defer capability.Close()
	if err := validateV3BenchRoot(root, prefix, capability); err != nil {
		t.Fatalf("unsafe child root: %v", err)
	}
	if os.Getenv("LEV_V3_BENCH_ORACLE") != filepath.Join(root, "oracle.json") || os.Getenv("LEV_V3_BENCH_RECEIPT") == "" || filepath.Dir(os.Getenv("LEV_V3_BENCH_RECEIPT")) != root {
		t.Fatal("child artifacts must be direct files in the owned root")
	}
	var cfg v3BenchConfig
	mustUnmarshal(t, []byte(os.Getenv("LEV_V3_BENCH_CONFIG")), &cfg)
	validateV3BenchConfig(t, cfg)
	var oracle v3BenchOracle
	oracleBytes, err := os.ReadFile(os.Getenv("LEV_V3_BENCH_ORACLE"))
	if err != nil {
		t.Fatal(err)
	}
	mustUnmarshal(t, oracleBytes, &oracle)
	var fixtureHashes map[string]string
	mustUnmarshal(t, []byte(os.Getenv("LEV_V3_BENCH_FIXTURE_SHA")), &fixtureHashes)
	if got := directoryContentSHA256(t, filepath.Join(root, "collections")); got != fixtureHashes["collections_tree"] {
		t.Fatalf("fixture digest mismatch: got %s want %s", got, fixtureHashes["collections_tree"])
	}

	// NewLevara uses the package RNG for graph levels. randseednop=0 is placed
	// in the child environment before process start so the baseline is repeatable.
	rand.Seed(cfg.Seed)
	beforeCPU := processCPU(t)
	started := time.Now()
	var queryDB *Levara
	var openedDBs []*Levara
	var closeStore func() error
	var lazyLoad time.Duration
	storagePath := filepath.Join(root, "collections", collectionName(0), "meta.bin")
	snapshotPath := filepath.Join(root, "collections", collectionName(0), "hnsw.snapshot")
	switch cfg.Mode {
	case "S0":
		cm, err := NewCollectionManager(cfg.Dimension, root, v3BenchHNSWConfig(cfg))
		if err != nil {
			t.Fatalf("open fixture: %v", err)
		}
		for i := 0; i < cfg.Fanout; i++ {
			db, getErr := cm.Get(collectionName(i))
			if getErr != nil {
				t.Fatal(getErr)
			}
			openedDBs = append(openedDBs, db)
		}
		queryDB = openedDBs[0]
		closeStore = cm.Close
	case "S1", "S2":
		queryDB, err = openV3SnapshotLevara(cfg.Dimension, storagePath, snapshotPath, cfg.Mode == "S2", v3BenchHNSWConfig(cfg))
		if err != nil {
			t.Fatalf("open snapshot fixture: %v", err)
		}
		closeStore = queryDB.Close
		openedDBs = []*Levara{queryDB}
	case "S3":
		lazy := newV3LazyLevara(func() (*Levara, error) {
			return openV3SnapshotLevara(cfg.Dimension, storagePath, snapshotPath, false, v3BenchHNSWConfig(cfg))
		})
		results, state, err := lazy.Search(make([]float32, cfg.Dimension), 1)
		if err != nil || state != v3LazyLoading || results != nil {
			t.Fatalf("lazy first read: state=%s results=%v err=%v", state, results, err)
		}
		loadStarted := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSec)*time.Second)
		err = lazy.Wait(ctx)
		cancel()
		lazyLoad = time.Since(loadStarted)
		if err != nil {
			t.Fatalf("lazy load: %v", err)
		}
		lazy.mu.Lock()
		queryDB = lazy.db
		lazy.mu.Unlock()
		closeStore = lazy.close
		openedDBs = []*Levara{queryDB}
	}
	wall := time.Since(started)
	if cfg.Mode == "S3" {
		wall -= lazyLoad
	}
	defer closeStore()
	var snapshotStats v3SnapshotWriteStats
	mustUnmarshal(t, []byte(os.Getenv("LEV_V3_BENCH_SNAPSHOT_STATS")), &snapshotStats)
	expectedRecords := cfg.Records
	if cfg.Mode == "S2" {
		expectedRecords++
	}
	listenerReadiness := milliseconds(wall)
	var startupRate *float64
	var lazyRate float64
	if cfg.Mode == "S3" {
		lazyRate = rate(expectedRecords, lazyLoad)
	} else {
		value := rate(expectedRecords, wall)
		startupRate = &value
	}

	receipt := v3BenchReceipt{
		Mode:                     cfg.Mode,
		ColdKind:                 "process-cold",
		Run:                      envInt(t, "LEV_V3_BENCH_RUN", 0),
		Revision:                 os.Getenv("LEV_V3_BENCH_REVISION"),
		WorkspaceRevision:        os.Getenv("LEV_V3_BENCH_WORKSPACE_REVISION"),
		BenchmarkSourceSHA256:    os.Getenv("LEV_V3_BENCH_SOURCE_SHA"),
		SourceTreeSHA256:         os.Getenv("LEV_V3_BENCH_SOURCE_TREE_SHA"),
		TestExecutableSHA256:     v3BenchExecutableHash(t),
		BuildInfoSHA256:          v3BenchBuildInfoHash(t),
		ConfigSHA256:             os.Getenv("LEV_V3_BENCH_CONFIG_SHA"),
		GoVersion:                runtime.Version(),
		GOOS:                     runtime.GOOS,
		GOARCH:                   runtime.GOARCH,
		GOMAXPROCS:               runtime.GOMAXPROCS(0),
		Config:                   cfg,
		FixtureSHA256:            fixtureHashes,
		QuerySHA256:              sha256Hex(oracleBytes),
		WallMilliseconds:         milliseconds(wall),
		CPUMilliseconds:          milliseconds(processCPU(t) - beforeCPU),
		PeakRSSBytes:             peakRSS(t),
		InputWALBytes:            sumSuffixSize(t, filepath.Join(root, "collections"), ".wal"),
		OutputMetaBytes:          sumNamedSize(t, filepath.Join(root, "collections"), "meta.bin"),
		PersistentDiskBytes:      directorySize(t, filepath.Join(root, "collections")),
		ExpectedRecordCount:      expectedRecords,
		StartupRecordsPerSecond:  startupRate,
		ListenerReadinessMS:      listenerReadiness,
		SnapshotBytes:            snapshotStats.SnapshotBytes,
		PeakTemporaryDiskBytes:   snapshotStats.PeakTempBytes,
		LazyLoadMilliseconds:     milliseconds(lazyLoad),
		LazyLoadRecordsPerSecond: lazyRate,
		Errors:                   []string{},
	}

	for _, db := range openedDBs {
		receipt.RecordCount += db.Count()
	}
	if queryDB != nil && len(oracle.Queries) > 0 {
		latencies := make([]time.Duration, 0, len(oracle.Queries))
		digest := sha256.New()
		hits, possible := 0, 0
		searchStarted := time.Now()
		for i, query := range oracle.Queries {
			start := time.Now()
			results := queryDB.Search(query, cfg.TopK)
			receipt.ReturnedResultCount += len(results)
			latencies = append(latencies, time.Since(start))
			expected := make(map[string]struct{}, len(oracle.Expected[i]))
			for _, id := range oracle.Expected[i] {
				expected[id] = struct{}{}
			}
			possible += len(expected)
			for _, result := range results {
				fmt.Fprintf(digest, "%s\x00%.9g\n", result.ID, result.Score)
				if _, ok := expected[result.ID]; ok {
					hits++
				}
			}
		}
		searchWall := time.Since(searchStarted)
		receipt.ExecutedQueryCount = len(latencies)
		receipt.QualitySampleCount = possible
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		receipt.SearchP50Milliseconds = milliseconds(percentile(latencies, 0.50))
		receipt.SearchP95Milliseconds = milliseconds(percentile(latencies, 0.95))
		receipt.SearchP99Milliseconds = milliseconds(percentile(latencies, 0.99))
		receipt.SearchQueriesPerSecond = rate(len(latencies), searchWall)
		receipt.ResultSHA256 = hex.EncodeToString(digest.Sum(nil))
		if possible > 0 {
			recall := float64(hits) / float64(possible)
			receipt.RecallAt10 = &recall
			receipt.QualityApplicable = true
			if recall < 0.90 {
				receipt.Errors = append(receipt.Errors, fmt.Sprintf("recall@10 %.3f < 0.90", recall))
			}
		} else if cfg.Records > 0 {
			receipt.Errors = append(receipt.Errors, "nonempty fixture produced no quality samples")
		} else if len(latencies) != 1 || receipt.ReturnedResultCount != 0 {
			receipt.Errors = append(receipt.Errors, fmt.Sprintf("empty fixture search evidence invalid: queries=%d results=%d", len(latencies), receipt.ReturnedResultCount))
		}
	} else {
		receipt.ResultSHA256 = hex.EncodeToString(sha256.New().Sum(nil))
		if cfg.Records > 0 {
			receipt.Errors = append(receipt.Errors, "nonempty fixture executed zero queries")
		}
	}
	if receipt.RecordCount != expectedRecords {
		receipt.Errors = append(receipt.Errors, fmt.Sprintf("record count %d != %d", receipt.RecordCount, expectedRecords))
	}
	writeJSON(t, os.Getenv("LEV_V3_BENCH_RECEIPT"), receipt)
}

func buildV3BenchFixture(t *testing.T, root string, cfg v3BenchConfig) (v3BenchOracle, map[string]string, v3SnapshotWriteStats) {
	t.Helper()
	collectionsRoot := filepath.Join(root, "collections")
	if err := os.MkdirAll(collectionsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cfg.Fanout; i++ {
		collectionDir := filepath.Join(collectionsRoot, collectionName(i))
		if err := os.Mkdir(collectionDir, 0o755); err != nil {
			t.Fatal(err)
		}
		count := 0
		if i < cfg.Populated && cfg.Records > i {
			count = (cfg.Records-1-i)/cfg.Populated + 1
		}
		writeJSON(t, filepath.Join(collectionDir, collectionMetaFile), CollectionMeta{
			Name:           collectionName(i),
			EmbeddingDim:   cfg.Dimension,
			DistanceMetric: "cosine",
			RecordCount:    count,
			CreatedAt:      "2026-10-08T00:00:00Z",
			UpdatedAt:      "2026-10-08T00:00:00Z",
		})
		if err := os.WriteFile(filepath.Join(collectionDir, "meta.bin"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(collectionDir, "meta.bin.wal"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	rng := rand.New(rand.NewSource(cfg.Seed))
	vectors := make([][]float32, cfg.Records)
	var snapshotStats v3SnapshotWriteStats
	payload := json.RawMessage(`{"padding":"` + strings.Repeat("x", cfg.Metadata-14) + `"}`)
	for i := range vectors {
		vectors[i] = gaussianVector(rng, cfg.Dimension)
	}
	for collection := 0; collection < cfg.Populated; collection++ {
		path := filepath.Join(collectionsRoot, collectionName(collection), "meta.bin")
		db, err := NewLevara(cfg.Dimension, path, v3BenchHNSWConfig(cfg))
		if err != nil {
			t.Fatal(err)
		}
		db.hnsw.randFloat64 = rand.New(rand.NewSource(cfg.Seed + int64(collection) + 1)).Float64
		var batch []BatchItem
		for i, vector := range vectors {
			if i%cfg.Populated != collection {
				continue
			}
			batch = append(batch, BatchItem{ID: recordID(i), Vector: vector, Data: payload})
			if len(batch) == 100 {
				if errs := db.BatchInsert(batch); len(errs) > 0 {
					t.Fatalf("fixture batch: %v", errs)
				}
				batch = batch[:0]
			}
		}
		if len(batch) > 0 {
			if errs := db.BatchInsert(batch); len(errs) > 0 {
				t.Fatalf("fixture batch: %v", errs)
			}
		}
		waitV3IndexReady(t, db, time.Duration(cfg.TimeoutSec)*time.Second)
		if collection == 0 && cfg.Mode != "S0" {
			walBytes, err := os.ReadFile(path + ".wal")
			if err != nil {
				t.Fatal(err)
			}
			snapshotStats, err = writeV3HNSWSnapshot(db, filepath.Join(filepath.Dir(path), "hnsw.snapshot"), walBytes)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Mode == "S2" {
				tailVector := make([]float32, cfg.Dimension)
				tailVector[0] = -1
				if err := db.Insert("record-tail", tailVector, json.RawMessage(`{"tail":true}`)); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}

	oracle := v3BenchOracle{}
	queryCount := cfg.Queries
	firstCollectionCount := (cfg.Records + cfg.Populated - 1) / cfg.Populated
	if cfg.Records == 0 {
		oracle.Queries = append(oracle.Queries, make([]float32, cfg.Dimension))
		oracle.Expected = append(oracle.Expected, []string{})
		return oracle, hashesForPopulatedWALs(t, collectionsRoot, cfg.Populated), snapshotStats
	}
	if queryCount > firstCollectionCount {
		queryCount = firstCollectionCount
	}
	queryRNG := rand.New(rand.NewSource(cfg.Seed + 99))
	for q := 0; q < queryCount; q++ {
		query := gaussianVector(queryRNG, cfg.Dimension)
		oracle.Queries = append(oracle.Queries, query)
		type scored struct {
			id string
			d  float32
		}
		all := make([]scored, 0, firstCollectionCount)
		for i, vector := range vectors {
			if i%cfg.Populated == 0 {
				all = append(all, scored{id: recordID(i), d: dist(normalizeVec(query), vector)})
			}
		}
		sort.Slice(all, func(i, j int) bool {
			if all[i].d == all[j].d {
				return all[i].id < all[j].id
			}
			return all[i].d < all[j].d
		})
		limit := cfg.TopK
		if limit > len(all) {
			limit = len(all)
		}
		expected := make([]string, limit)
		for i := 0; i < limit; i++ {
			expected[i] = all[i].id
		}
		oracle.Expected = append(oracle.Expected, expected)
	}

	return oracle, hashesForPopulatedWALs(t, collectionsRoot, cfg.Populated), snapshotStats
}

func hashesForPopulatedWALs(t *testing.T, root string, populated int) map[string]string {
	t.Helper()
	hashes := make(map[string]string, populated)
	for i := 0; i < populated; i++ {
		path := filepath.Join(root, collectionName(i), "meta.bin.wal")
		hashes[collectionName(i)+"/meta.bin.wal"] = fileSHA256(t, path)
	}
	return hashes
}

func waitV3IndexReady(t *testing.T, db *Levara, timeout time.Duration) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	ticker := time.NewTicker(time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		db.pendingMu.RLock()
		pending := len(db.pendingVecs)
		db.pendingMu.RUnlock()
		db.hnsw.RLock()
		indexed := len(db.hnsw.Nodes)
		db.hnsw.RUnlock()
		db.mu.RLock()
		records := len(db.index)
		db.mu.RUnlock()
		if pending == 0 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("fixture index readiness timed out: pending=%d indexed=%d records=%d", pending, indexed, records)
		case <-ticker.C:
		}
	}
}

func validateV3BenchConfig(t *testing.T, cfg v3BenchConfig) {
	t.Helper()
	if cfg.Mode != "S0" && cfg.Mode != "S1" && cfg.Mode != "S2" && cfg.Mode != "S3" {
		t.Fatalf("mode must be S0, S1, S2, or S3, got %q", cfg.Mode)
	}
	if cfg.Dimension != 256 && cfg.Dimension != 768 {
		t.Fatalf("dimension must be 256 or 768, got %d", cfg.Dimension)
	}
	if cfg.Records < 0 || cfg.Records > 250000 {
		t.Fatalf("records must be 0..250000, got %d", cfg.Records)
	}
	if cfg.Fanout != 1 && cfg.Fanout != 326 {
		t.Fatalf("fanout must be 1 or 326, got %d", cfg.Fanout)
	}
	if cfg.Mode != "S0" && cfg.Fanout != 1 {
		t.Fatalf("experimental snapshot modes currently require fanout=1")
	}
	if cfg.Mode == "S2" && cfg.Records == 0 {
		t.Fatal("S2 tail benchmark requires a nonempty prefix fixture")
	}
	if cfg.Populated < 1 || cfg.Populated > cfg.Fanout || cfg.TimeoutSec < 1 {
		t.Fatalf("invalid populated/timeout configuration: %+v", cfg)
	}
	wantPopulated := 1
	if cfg.Fanout == 326 {
		wantPopulated = 2
	}
	if cfg.Populated != wantPopulated || cfg.Queries != v3BenchQueries || cfg.TopK != v3BenchTopK || cfg.Metadata != v3BenchMetadataLen {
		t.Fatalf("benchmark fixture constants changed: %+v", cfg)
	}
	if cfg.M < 2 || cfg.M > 128 || cfg.M0 < cfg.M || cfg.M0 > 256 || cfg.EfConstruction < 0 || cfg.EfConstruction > 8192 || cfg.EfConstruction > 0 && cfg.EfConstruction < cfg.M || cfg.EfMult < 1 || cfg.EfMult > 256 || cfg.EfMin < cfg.TopK || cfg.EfMin > 4096 {
		t.Fatalf("invalid benchmark HNSW override: M=%d M0=%d efConstruction=%d efMult=%d efMin=%d", cfg.M, cfg.M0, cfg.EfConstruction, cfg.EfMult, cfg.EfMin)
	}
}

func v3BenchHNSWConfig(cfg v3BenchConfig) HNSWConfig {
	return HNSWConfig{M: cfg.M, M0: cfg.M0, EfConstruction: cfg.EfConstruction, EfSearchMult: cfg.EfMult, EfSearchMin: cfg.EfMin, LevelMult: DefaultHNSWConfig().LevelMult}
}

func TestV3BenchmarkRejectsUnsafeRoot(t *testing.T) {
	token := strings.Repeat("a", 64)
	prefix := t.TempDir()
	capabilityPath := filepath.Join(prefix, ".v3-bench-capability")
	if err := os.WriteFile(capabilityPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	capability, err := os.Open(capabilityPath)
	if err != nil {
		t.Fatal(err)
	}
	defer capability.Close()
	if err := validateV3BenchRoot(t.TempDir(), prefix, capability); err == nil {
		t.Fatal("accepted arbitrary directory outside benchmark prefix")
	}
	realRoot := filepath.Join(prefix, "work")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkRoot := filepath.Join(prefix, "linked-root")
	if err := os.Symlink(realRoot, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	if err := validateV3BenchRoot(symlinkRoot, prefix, capability); err == nil {
		t.Fatal("accepted symlinked root")
	}
	forgedPrefix := t.TempDir()
	forgedRoot := filepath.Join(forgedPrefix, "work")
	if err := os.Mkdir(forgedRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(forgedPrefix, ".v3-bench-capability"), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateV3BenchRoot(forgedRoot, forgedPrefix, capability); err == nil {
		t.Fatal("accepted forged marker without matching inherited capability file")
	}
}

func validateV3BenchRoot(root, prefix string, capability *os.File) error {
	if root == "" || prefix == "" || !filepath.IsAbs(root) || !filepath.IsAbs(prefix) || filepath.Clean(root) != root || filepath.Clean(prefix) != prefix {
		return fmt.Errorf("root and prefix must be clean absolute paths")
	}
	if filepath.Dir(root) != prefix || filepath.Base(root) != "work" {
		return fmt.Errorf("root must be the expected direct child of the benchmark prefix")
	}
	for _, path := range []string{prefix, root} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("benchmark path must be a real directory: %s", path)
		}
	}
	capabilityPath := filepath.Join(prefix, ".v3-bench-capability")
	pathInfo, err := os.Lstat(capabilityPath)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("capability path must be a regular file")
	}
	fdInfo, err := capability.Stat()
	if err != nil || !os.SameFile(pathInfo, fdInfo) {
		return fmt.Errorf("inherited capability does not name the parent-created file")
	}
	if _, err := capability.Seek(0, 0); err != nil {
		return fmt.Errorf("capability is not seekable")
	}
	token := make([]byte, 65)
	n, err := capability.Read(token)
	if err != nil || n != 64 {
		return fmt.Errorf("invalid inherited capability")
	}
	decoded, err := hex.DecodeString(string(token[:n]))
	if err != nil || len(decoded) != 32 {
		return fmt.Errorf("invalid capability token")
	}
	return nil
}

func checkV3BenchDiskBudget(t *testing.T, root string) {
	t.Helper()
	used := directorySize(t, root)
	if used > v3BenchMaxDisk {
		t.Fatalf("actual task root %d exceeds 4 GiB budget", used)
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(root, &stat); err != nil {
		t.Fatal(err)
	}
	free := uint64(stat.Bavail) * uint64(stat.Bsize)
	if free < v3BenchMinFree {
		t.Fatalf("actual free space %d is below 12 GiB floor", free)
	}
}

func estimateV3BenchDisk(cfg v3BenchConfig) uint64 {
	perRecord := uint64(cfg.Dimension*4 + cfg.Metadata + 1024)
	return uint64(cfg.Records)*perRecord*2 + uint64(cfg.Fanout)*(1<<20)
}

func gaussianVector(rng *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32(rng.NormFloat64())
	}
	return normalizeVec(v)
}

func collectionName(i int) string { return fmt.Sprintf("collection-%03d", i) }
func recordID(i int) string       { return fmt.Sprintf("record-%09d", i) }

func percentile(values []time.Duration, quantile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	idx := int(quantile*float64(len(values)-1) + 0.5)
	return values[idx]
}

func rate(count int, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(count) / elapsed.Seconds()
}

func milliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func processCPU(t *testing.T) time.Duration {
	t.Helper()
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	return time.Duration(usage.Utime.Sec+usage.Stime.Sec)*time.Second +
		time.Duration(usage.Utime.Usec+usage.Stime.Usec)*time.Microsecond
}

func peakRSS(t *testing.T) uint64 {
	t.Helper()
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	bytes := uint64(usage.Maxrss)
	if runtime.GOOS != "darwin" {
		bytes *= 1024
	}
	return bytes
}

func directorySize(t *testing.T, root string) uint64 {
	t.Helper()
	var total uint64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += uint64(info.Size())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return total
}

func sumSuffixSize(t *testing.T, root, suffix string) uint64 {
	t.Helper()
	return sumMatchingSize(t, root, func(name string) bool { return strings.HasSuffix(name, suffix) })
}

func sumNamedSize(t *testing.T, root, name string) uint64 {
	t.Helper()
	return sumMatchingSize(t, root, func(candidate string) bool { return candidate == name })
}

func sumMatchingSize(t *testing.T, root string, match func(string) bool) uint64 {
	t.Helper()
	var total uint64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !match(entry.Name()) {
			return err
		}
		info, err := entry.Info()
		if err == nil {
			total += uint64(info.Size())
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return total
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256Hex(data)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func v3BenchSourceHash(t *testing.T) string {
	t.Helper()
	_, path, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate benchmark source")
	}
	return fileSHA256(t, path)
}

func v3BenchSourceTreeHash(t *testing.T) string {
	t.Helper()
	_, harnessPath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate benchmark source")
	}
	dir := filepath.Dir(harnessPath)
	names := []string{"collections.go", "db.go", "hnsw.go", "hnsw_snapshot.go", "startup_benchmark_test.go", "wal.go"}
	hash := sha256.New()
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(hash, "%s\x00%d\x00", name, len(data))
		hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func v3BenchExecutableHash(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return fileSHA256(t, path)
}

func v3BenchBuildInfoHash(t *testing.T) string {
	t.Helper()
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("read Go build identity")
	}
	return sha256Hex([]byte(info.String()))
}

func directoryContentSHA256(t *testing.T, root string) string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("fixture contains symlink: %s", path)
		}
		if entry.Type().IsRegular() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(hash, "%s\x00%d\x00", filepath.ToSlash(rel), len(data))
		hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func v3BenchRevision(t *testing.T) string {
	t.Helper()
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && setting.Value != "" {
				return setting.Value
			}
		}
	}
	output, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("revision: %v", err)
	}
	return strings.TrimSpace(string(output))
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mustUnmarshal(t *testing.T, data []byte, value any) {
	t.Helper()
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatal(err)
	}
}

func envInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return parsed
}

func envInt64(t *testing.T, name string, fallback int64) int64 {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return parsed
}

func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func appendGODEBUG(current, setting string) string {
	if current == "" {
		return setting
	}
	return current + "," + setting
}
