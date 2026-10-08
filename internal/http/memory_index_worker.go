package http

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"sync"

	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/memoryindex"
)

func StartMemoryIndexWorker(cfg APIConfig, interval time.Duration, workers int) func() {
	ctx, cancel := context.WithCancel(context.Background())
	if cfg.MemoryIndexOutbox == nil {
		return cancel
	}
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	if workers <= 0 {
		workers = 2
	}
	_, _ = cfg.MemoryIndexOutbox.RecoverRunning(context.Background())
	enqueueMissingMemoryVectors(cfg)
	// Claim remains atomic in the durable store, so workers cannot execute the
	// same job concurrently. Keep the pool bounded because each completed embed
	// also performs a CPU-heavy HNSW insertion.
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
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
	// Stop cancels and waits for the worker pool to exit so shutdown never
	// closes the database under an in-flight embedding write (finding H8,
	// 2026-09-03 review). Idempotent via sync.Once.
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			wg.Wait()
		})
	}
}

// enqueueMissingMemoryVectors is the startup incremental reconcile. SQL is
// authoritative; every live row missing from its sidecar becomes an outbox
// job instead of being synchronously re-embedded during startup.
func enqueueMissingMemoryVectors(cfg APIConfig) {
	if cfg.DB == nil || cfg.Collections == nil || cfg.MemoryIndexOutbox == nil {
		return
	}
	rows, err := cfg.DB.Query(Q(`SELECT id,key,value,type,owner_id,collection_name FROM memories WHERE superseded_by='' AND valid_until IS NULL`))
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
	if err := rows.Err(); err != nil {
		return
	}
	rows.Close()
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

// memoryIndexMaxAttempts bounds both real-failure retries and breaker
// deferrals: a deferral no longer refunds the attempt, so a job facing a
// permanently unavailable embedding service terminates in dead_letter
// (recoverable via memory_index_retry) instead of parking forever.
const memoryIndexMaxAttempts = 5

func runMemoryIndexJob(ctx context.Context, cfg APIConfig) bool {
	job, ok, err := cfg.MemoryIndexOutbox.Claim(ctx)
	if err != nil || !ok {
		return false
	}
	err = executeMemoryIndexJob(ctx, cfg, job)
	var blocked *embed.BreakerOpenError
	if errors.As(err, &blocked) {
		return persistMemoryIndexTransition(ctx, func(ctx context.Context) error {
			return cfg.MemoryIndexOutbox.Defer(ctx, job, blocked.RetryAt, memoryIndexMaxAttempts)
		})
	}
	return persistMemoryIndexTransition(ctx, func(ctx context.Context) error {
		return cfg.MemoryIndexOutbox.Finish(ctx, job, err, memoryIndexMaxAttempts, time.Second)
	})
}

func persistMemoryIndexTransition(ctx context.Context, transition func(context.Context) error) bool {
	backoff := 10 * time.Millisecond
	for {
		if err := transition(ctx); err == nil {
			return true
		} else {
			log.Printf("[memory-index] terminal transition failed: %v", err)
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
		if backoff < time.Second {
			backoff *= 2
		}
	}
}

// ponytail: fixed stripes bound lock memory; split further only if contention is measured.
// Embedding stays outside these locks. Vector publication/deletion for an ID is ordered.
var memoryVectorLocks [64]sync.Mutex

// beginMemoryVectorEffect holds SQL truth until the synchronous vector effect returns.
// Existing PostgreSQL sources need only a row fence; absent IDs retain the
// table fence to prevent recreation during a native delete. SQLite reserves its writer.
func beginMemoryVectorEffect(ctx context.Context, db *sql.DB, memoryID string) (*sql.Tx, func(), error) {
	ctx, timeout := context.WithTimeout(ctx, 30*time.Second)
	conn, err := db.Conn(ctx)
	if err != nil {
		timeout()
		return nil, nil, err
	}
	txCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, cancel)
	tx, err := conn.BeginTx(txCtx, nil)
	release := func() {
		stop()
		if tx != nil {
			_ = tx.Rollback()
		}
		cancel()
		_ = conn.Close()
		timeout()
	}
	fail := func(err error) (*sql.Tx, func(), error) { release(); return nil, nil, err }
	if err != nil {
		return fail(err)
	}
	if GetDBProvider() == DBSQLite {
		// A WAL read snapshot alone allows a concurrent writer to commit.
		_, err = tx.ExecContext(ctx, "UPDATE memories SET id=id WHERE 1=0")
	} else {
		var lockedID string
		err = tx.QueryRowContext(ctx, "SELECT id FROM memories WHERE id=$1 FOR UPDATE", memoryID).Scan(&lockedID)
		if errors.Is(err, sql.ErrNoRows) {
			// Row locks cannot protect absence. Recheck after acquiring the table
			// fence: a concurrent insertion may have committed while we waited.
			_, err = tx.ExecContext(ctx, "LOCK TABLE memories IN SHARE MODE")
			if err == nil {
				err = tx.QueryRowContext(ctx, "SELECT id FROM memories WHERE id=$1 FOR UPDATE", memoryID).Scan(&lockedID)
				if errors.Is(err, sql.ErrNoRows) {
					err = nil
				}
			}
		}
	}
	if err != nil {
		return fail(err)
	}
	if err = ctx.Err(); err != nil {
		return fail(err)
	}
	if !stop() {
		return fail(ctx.Err())
	}
	return tx, release, nil
}

func executeMemoryIndexJob(ctx context.Context, cfg APIConfig, job memoryindex.Job) error {
	if cfg.Collections == nil || cfg.DB == nil {
		return fmt.Errorf("index dependencies unavailable")
	}
	hash := sha256.Sum256([]byte(job.MemoryID))
	lock := &memoryVectorLocks[int(hash[0])%len(memoryVectorLocks)]
	if job.Operation == "delete_vector" {
		lock.Lock()
		defer lock.Unlock()
		tx, release, err := beginMemoryVectorEffect(ctx, cfg.DB, job.MemoryID)
		if err != nil {
			return err
		}
		defer release()
		var owner, collection, superseded string
		var validUntil sql.NullString
		err = tx.QueryRowContext(ctx, Q(`SELECT owner_id,collection_name,superseded_by,CAST(valid_until AS TEXT) FROM memories WHERE id=$1`), job.MemoryID).Scan(&owner, &collection, &superseded, &validUntil)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && (owner != job.OwnerID || collection != job.Collection || superseded == "" && !validUntil.Valid) {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !cfg.Collections.HasRecord(memoryCollectionNameHTTP(job.Collection), job.MemoryID) {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return cfg.Collections.Delete(memoryCollectionNameHTTP(job.Collection), job.MemoryID)
	}
	var key, value, typ, owner, collection string
	err := cfg.DB.QueryRowContext(ctx, Q(`SELECT key,value,type,owner_id,collection_name FROM memories WHERE id=$1 AND superseded_by='' AND valid_until IS NULL`), job.MemoryID).Scan(&key, &value, &typ, &owner, &collection)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
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
	hook, err := func() (func(), error) {
		lock.Lock()
		defer lock.Unlock()
		tx, release, err := beginMemoryVectorEffect(ctx, cfg.DB, job.MemoryID)
		if err != nil {
			return nil, err
		}
		defer release()
		// The embedding input is key/value. A metadata-only type update can
		// reuse the running digest job; publish its current SQL type.
		err = tx.QueryRowContext(ctx, Q(`SELECT type FROM memories WHERE id=$1 AND key=$2 AND value=$3 AND owner_id=$4 AND collection_name=$5 AND superseded_by='' AND valid_until IS NULL`), job.MemoryID, key, value, owner, collection).Scan(&typ)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		meta, _ := json.Marshal(map[string]string{"key": key, "value": value, "type": typ, "owner_id": owner, "collection": collection, "memory_id": job.MemoryID})
		hook, err := cfg.Collections.InsertDeferredHook(memoryCollectionNameHTTP(collection), job.MemoryID, vec, meta)
		if err != nil {
			return nil, err
		}
		if !cfg.Collections.HasRecord(memoryCollectionNameHTTP(collection), job.MemoryID) {
			return nil, fmt.Errorf("vector absent after insert")
		}
		return hook, nil
	}()
	if err != nil {
		return err
	}
	if hook != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		hook()
	}
	return nil
}

func memoryCollectionNameHTTP(collection string) string {
	if collection == "" {
		return "_memories"
	}
	return "_memories_" + collection
}
