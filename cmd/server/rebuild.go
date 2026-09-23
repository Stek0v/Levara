package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stek0v/levara/internal/store"
)

// runCollectionRebuild is the offline maintenance path behind
// -rebuild-collection. It MUST run with the main server stopped (exclusive
// data-dir ownership). Classification is vector-preserving: records are
// removed only when their document is gone (orphan) or superseded by a newer
// published generation; everything else keeps its exact vector.
func runCollectionRebuild(collection, pgURL, dataDir, nodeID string, dim int, apply bool, manifestPath string) error {
	if conn, err := net.DialTimeout("tcp", "127.0.0.1:8081", time.Second); err == nil {
		conn.Close()
		return fmt.Errorf("a levera server appears to be serving on :8081 — stop it before rebuilding (exclusive data-dir ownership is required)")
	}

	hnswCfg := store.HNSWConfig{M: 16, M0: 32, EfSearchMult: 8, EfSearchMin: 64, LevelMult: 1.0 / 0.69}
	fmt.Printf("[rebuild] opening collection manager (WAL replay; may take minutes)...\n")
	start := time.Now()
	cm, err := store.NewCollectionManager(dim, filepath.Join(dataDir, nodeID), hnswCfg)
	if err != nil {
		return fmt.Errorf("open collection manager: %w", err)
	}
	defer func() { _ = cm.Close() }()
	db, err := cm.Get(collection)
	if err != nil {
		return fmt.Errorf("collection %q: %w", collection, err)
	}

	// Zero-cost rollback snapshot: hardlinks keep the pre-rebuild inodes
	// alive after Checkpoint's atomic replace — no bytes copied.
	snapDir := filepath.Join(dataDir, "rebuild-snapshots")
	_ = os.MkdirAll(snapDir, 0o755)
	stamp := time.Now().Format("20060102-150405")
	for _, p := range []string{db.WALPath(), db.DiskPath()} {
		if p == "" {
			continue
		}
		link := filepath.Join(snapDir, filepath.Base(p)+"."+stamp)
		if err := os.Link(p, link); err != nil && !os.IsExist(err) {
			return fmt.Errorf("hardlink snapshot %s: %w", p, err)
		}
	}

	pg, err := sql.Open("pgx", pgURL)
	if err != nil {
		return err
	}
	defer pg.Close()
	currentDocs := map[string]struct{}{}
	rows, err := pg.Query(`SELECT dd.data_id FROM dataset_data dd
		JOIN datasets ds ON ds.id = dd.dataset_id WHERE ds.name = $1`, collection)
	if err != nil {
		return fmt.Errorf("load dataset docs: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		currentDocs[id] = struct{}{}
	}
	rows.Close()
	currentGens := map[string]string{}
	rows, err = pg.Query(`SELECT p.data_id, p.generation FROM document_index_publications p
		JOIN datasets ds ON ds.id = p.dataset_id WHERE ds.name = $1`, collection)
	if err != nil {
		return fmt.Errorf("load publications: %w", err)
	}
	for rows.Next() {
		var id, gen string
		if err := rows.Scan(&id, &gen); err != nil {
			rows.Close()
			return err
		}
		currentGens[id] = gen
	}
	rows.Close()
	fmt.Printf("[rebuild] dataset %q: %d live documents, %d published generations (loaded in %s)\n",
		collection, len(currentDocs), len(currentGens), time.Since(start).Round(time.Second))

	records := db.AllRecords()
	keep, remove := store.ClassifyRebuildRecords(records, currentDocs, currentGens)
	stats := map[string]int{}
	for _, reason := range remove {
		stats[reason]++
	}
	fmt.Printf("[rebuild] records: %d total | keep %d | remove %d (orphan %d, superseded %d)\n",
		len(records), len(keep), len(remove), stats[store.RebuildOrphan], stats[store.RebuildSuperseded])

	if manifestPath == "" {
		manifestPath = filepath.Join(snapDir, "rebuild-manifest-"+stamp+".json")
	}
	mf, err := os.Create(manifestPath)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(mf)
	enc.SetIndent("", " ")
	_ = enc.Encode(map[string]any{"collection": collection, "generated": time.Now().UTC().Format(time.RFC3339), "remove": remove})
	mf.Close()
	fmt.Printf("[rebuild] manifest: %s\n", manifestPath)
	if !apply {
		fmt.Printf("[rebuild] dry-run complete — nothing changed. Re-run with -rebuild-apply to execute.\n")
		return nil
	}

	for id := range remove {
		if err := db.Delete(id); err != nil {
			return fmt.Errorf("delete %s: %w", id, err)
		}
	}
	if err := db.Checkpoint(); err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}
	after := db.AllRecords()
	if len(after) != len(keep) {
		return fmt.Errorf("post-verify failed: %d records remain, want %d — rollback via snapshot %s before booting", len(after), len(keep), snapDir)
	}
	fmt.Printf("[rebuild] applied: %d removed, %d live records, WAL+meta compacted (snapshot: %s)\n",
		len(remove), len(after), snapDir)
	return nil
}
