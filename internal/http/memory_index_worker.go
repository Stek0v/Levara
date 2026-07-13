package http

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/stek0v/levara/pkg/memoryindex"
)

func StartMemoryIndexWorker(cfg APIConfig, interval time.Duration) func() {
	ctx, cancel := context.WithCancel(context.Background())
	if cfg.MemoryIndexOutbox == nil {
		return cancel
	}
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	_, _ = cfg.MemoryIndexOutbox.RecoverRunning(context.Background())
	enqueueMissingMemoryVectors(cfg)
	// Embedding is network-bound. A small fixed pool prevents bursts of saves
	// from turning into one interval plus one embedding round trip per memory.
	// Claim remains atomic in the durable store, so workers cannot execute the
	// same job concurrently.
	const workers = 8
	for range workers {
		go func() {
			t := time.NewTicker(interval)
			defer t.Stop()
			for {
				if runMemoryIndexJob(ctx, cfg) {
					continue // drain backlog without waiting for the next tick
				}
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
	return cancel
}

// enqueueMissingMemoryVectors is the startup incremental reconcile. SQL is
// authoritative; every live row missing from its sidecar becomes an outbox
// job instead of being synchronously re-embedded during startup.
func enqueueMissingMemoryVectors(cfg APIConfig) {
	if cfg.DB == nil || cfg.Collections == nil || cfg.MemoryIndexOutbox == nil {
		return
	}
	rows, err := cfg.DB.Query(Q(`SELECT id,key,value,type,owner_id,collection_name FROM memories WHERE superseded_by=''`))
	if err != nil {
		return
	}
	defer rows.Close()
	type row struct{ id, key, value, typ, owner, collection string }
	var missing []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.key, &r.value, &r.typ, &r.owner, &r.collection) != nil {
			continue
		}
		if !cfg.Collections.HasRecord(memoryCollectionNameHTTP(r.collection), r.id) {
			missing = append(missing, r)
		}
	}
	for _, r := range missing {
		tx, err := cfg.DB.BeginTx(context.Background(), nil)
		if err != nil {
			continue
		}
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(r.key+"\x00"+r.value)))
		_, err = cfg.MemoryIndexOutbox.EnqueueTx(context.Background(), tx, memoryindex.Job{MemoryID: r.id, Operation: "upsert_vector", Collection: r.collection, OwnerID: r.owner, Digest: digest, Model: cfg.EmbedModel})
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}
}

func runMemoryIndexJob(ctx context.Context, cfg APIConfig) bool {
	job, ok, err := cfg.MemoryIndexOutbox.Claim(ctx)
	if err != nil || !ok {
		return false
	}
	err = executeMemoryIndexJob(ctx, cfg, job)
	_ = cfg.MemoryIndexOutbox.Finish(context.Background(), job, err, 5, time.Second)
	return true
}

func executeMemoryIndexJob(ctx context.Context, cfg APIConfig, job memoryindex.Job) error {
	if job.Operation == "delete_vector" {
		return cfg.Collections.Delete(memoryCollectionNameHTTP(job.Collection), job.MemoryID)
	}
	var key, value, typ, owner, collection string
	err := cfg.DB.QueryRowContext(ctx, Q(`SELECT key,value,type,owner_id,collection_name FROM memories WHERE id=$1`), job.MemoryID).Scan(&key, &value, &typ, &owner, &collection)
	if err != nil {
		return err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(key+"\x00"+value)))
	if digest != job.Digest || owner != job.OwnerID || collection != job.Collection {
		return nil
	}
	embedCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if cfg.EmbedClient == nil || cfg.Collections == nil {
		return fmt.Errorf("embedding/index dependencies unavailable")
	}
	vec, err := cfg.EmbedClient.EmbedSingle(embedCtx, key+" "+value)
	if err != nil {
		return err
	}
	if job.Dimension > 0 && len(vec) != job.Dimension {
		return fmt.Errorf("embedding dimension %d, want %d", len(vec), job.Dimension)
	}
	meta, _ := json.Marshal(map[string]string{"key": key, "value": value, "type": typ, "collection": collection, "memory_id": job.MemoryID})
	if err = cfg.Collections.Insert(memoryCollectionNameHTTP(collection), job.MemoryID, vec, meta); err != nil {
		return err
	}
	if !cfg.Collections.HasRecord(memoryCollectionNameHTTP(collection), job.MemoryID) {
		return fmt.Errorf("vector absent after insert")
	}
	return nil
}

func memoryCollectionNameHTTP(collection string) string {
	if collection == "" {
		return "_memories"
	}
	return "_memories_" + collection
}
