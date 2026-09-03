package memoryindex

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	Pending    Status = "pending"
	Running    Status = "running"
	Completed  Status = "completed"
	Failed     Status = "failed"
	DeadLetter Status = "dead_letter"
)

type Job struct {
	ID         string `json:"id"`
	MemoryID   string `json:"memory_id"`
	Operation  string `json:"operation"`
	Collection string `json:"collection"`
	OwnerID    string `json:"owner_id"`
	Digest     string `json:"digest"`
	Model      string `json:"model"`
	Dimension  int    `json:"dimension"`
	Status     Status `json:"status"`
	Attempts   int    `json:"attempts"`
	NextRunAt  string `json:"next_run_at"`
	LastError  string `json:"last_error"`
}

type Store struct {
	db       *sql.DB
	postgres bool
	mu       sync.Mutex
}

func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("memory index outbox: nil database")
	}
	s := &Store{db: db, postgres: isPostgres(db)}
	ddl := `CREATE TABLE IF NOT EXISTS memory_index_jobs (
		id TEXT PRIMARY KEY, memory_id TEXT NOT NULL, operation TEXT NOT NULL,
		collection_name TEXT NOT NULL DEFAULT '', owner_id TEXT NOT NULL DEFAULT '', digest TEXT NOT NULL,
		embed_model TEXT NOT NULL DEFAULT '', embed_dimension INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'pending', attempts INTEGER NOT NULL DEFAULT 0,
		next_run_at TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		UNIQUE(memory_id, operation, digest)
	)`
	if _, err := db.Exec(ddl); err != nil {
		return nil, err
	}
	return s, nil
}

// Enqueue adds a job outside any caller transaction.
func (s *Store) Enqueue(ctx context.Context, j Job) (Job, error) {
	j.Status = Pending
	now := time.Now().UTC().Format(time.RFC3339Nano)
	q := s.bind(`INSERT INTO memory_index_jobs (id,memory_id,operation,collection_name,owner_id,digest,embed_model,embed_dimension,status,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(memory_id,operation,digest) DO NOTHING`)
	if _, err := s.db.ExecContext(ctx, q, j.ID, j.MemoryID, j.Operation, j.Collection, j.OwnerID, j.Digest, j.Model, j.Dimension, string(j.Status), now, now); err != nil {
		return Job{}, err
	}
	var id string
	var status Status
	var attempts int
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id,status,attempts FROM memory_index_jobs WHERE memory_id=? AND operation=? AND digest=?`), j.MemoryID, j.Operation, j.Digest).Scan(&id, &status, &attempts)
	if err != nil {
		return Job{}, err
	}
	j.ID, j.Status, j.Attempts = id, status, attempts
	return j, nil
}

// EnqueueTx adds a job within the caller's transaction.
func (s *Store) EnqueueTx(ctx context.Context, tx *sql.Tx, j Job) (Job, error) {
	if j.ID == "" {
		j.ID = uuid.NewString()
	}
	j.Status = Pending
	now := time.Now().UTC().Format(time.RFC3339Nano)
	q := s.bind(`INSERT INTO memory_index_jobs (id,memory_id,operation,collection_name,owner_id,digest,embed_model,embed_dimension,status,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(memory_id,operation,digest) DO NOTHING`)
	if _, err := tx.ExecContext(ctx, q, j.ID, j.MemoryID, j.Operation, j.Collection, j.OwnerID, j.Digest, j.Model, j.Dimension, string(j.Status), now, now); err != nil {
		return Job{}, err
	}
	err := tx.QueryRowContext(ctx, s.bind(`SELECT id,status,attempts FROM memory_index_jobs WHERE memory_id=? AND operation=? AND digest=?`), j.MemoryID, j.Operation, j.Digest).Scan(&j.ID, &j.Status, &j.Attempts)
	return j, err
}

// Claim atomically moves the next due job to 'running'. Safe across
	// multiple server processes sharing one database: each claim is a single
	// conditional UPDATE guarded on the selectable statuses, so only the
	// winner's UPDATE affects the row; losers move on to the next candidate
	// (finding H9, 2026-09-03 review). On PostgreSQL candidates are selected
	// FOR UPDATE SKIP LOCKED to avoid lock contention between claimers.
func (s *Store) Claim(ctx context.Context) (Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	skipLocked := ""
	if s.postgres {
		skipLocked = " FOR UPDATE SKIP LOCKED"
	}
	candQ := s.bind(`SELECT id FROM memory_index_jobs WHERE status IN ('pending','failed') AND (next_run_at='' OR next_run_at<=?) ORDER BY created_at LIMIT 8` + skipLocked)
	rows, err := s.db.QueryContext(ctx, candQ, now)
	if err != nil {
		return Job{}, false, err
	}
	var candidates []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			candidates = append(candidates, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Job{}, false, err
	}
	updQ := s.bind(`UPDATE memory_index_jobs SET status='running',attempts=attempts+1,updated_at=? WHERE id=? AND status IN ('pending','failed')`)
	for _, id := range candidates {
		res, err := s.db.ExecContext(ctx, updQ, now, id)
		if err != nil {
			return Job{}, false, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return Job{}, false, err
		}
		if n == 0 {
			continue // another process claimed it first
		}
		var j Job
		err = s.db.QueryRowContext(ctx, s.bind(`SELECT id,memory_id,operation,collection_name,owner_id,digest,embed_model,embed_dimension,status,attempts,next_run_at,last_error
			FROM memory_index_jobs WHERE id=?`), id).
			Scan(&j.ID, &j.MemoryID, &j.Operation, &j.Collection, &j.OwnerID, &j.Digest, &j.Model, &j.Dimension, &j.Status, &j.Attempts, &j.NextRunAt, &j.LastError)
		if err != nil {
			return Job{}, false, err
		}
		return j, true, nil
	}
	return Job{}, false, nil
}

func (s *Store) Finish(ctx context.Context, j Job, runErr error, maxAttempts int, backoff time.Duration) error {
	status := Completed
	last := ""
	next := ""
	if runErr != nil {
		last = runErr.Error()
		status = Failed
		if j.Attempts >= maxAttempts {
			status = DeadLetter
		} else {
			next = time.Now().Add(backoff * time.Duration(1<<max(0, j.Attempts-1))).UTC().Format(time.RFC3339Nano)
		}
	}
	// Status predicate: a stale worker whose job was reclaimed after lease
	// loss must not clobber the new owner's state.
	_, err := s.db.ExecContext(ctx, s.bind(`UPDATE memory_index_jobs SET status=?,last_error=?,next_run_at=?,updated_at=? WHERE id=? AND status='running'`), string(status), last, next, time.Now().UTC().Format(time.RFC3339Nano), j.ID)
	return err
}

func (s *Store) Counts(ctx context.Context) (map[Status]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status,COUNT(*) FROM memory_index_jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[Status]int{}
	for rows.Next() {
		var st Status
		var n int
		if rows.Scan(&st, &n) == nil {
			out[st] = n
		}
	}
	return out, rows.Err()
}

func (s *Store) List(ctx context.Context, ownerID string, limit int) ([]Job, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	q := `SELECT id,memory_id,operation,collection_name,owner_id,digest,embed_model,embed_dimension,status,attempts,next_run_at,last_error FROM memory_index_jobs`
	args := []any{}
	if ownerID != "" {
		q += " WHERE owner_id=?"
		args = append(args, ownerID)
	}
	q += " ORDER BY updated_at DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.bind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if rows.Scan(&j.ID, &j.MemoryID, &j.Operation, &j.Collection, &j.OwnerID, &j.Digest, &j.Model, &j.Dimension, &j.Status, &j.Attempts, &j.NextRunAt, &j.LastError) == nil {
			out = append(out, j)
		}
	}
	return out, rows.Err()
}

func (s *Store) Retry(ctx context.Context, id, ownerID string) (bool, error) {
	q := `UPDATE memory_index_jobs SET status='pending',next_run_at='',last_error='',updated_at=? WHERE id=? AND status IN ('failed','dead_letter')`
	args := []any{time.Now().UTC().Format(time.RFC3339Nano), id}
	if ownerID != "" {
		q += " AND owner_id=?"
		args = append(args, ownerID)
	}
	res, err := s.db.ExecContext(ctx, s.bind(q), args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// RecoverRunning returns jobs left running by a crashed process to pending.
// The worker is idempotent, so replaying after a partial vector insert is safe.
func (s *Store) RecoverRunning(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, s.bind(`UPDATE memory_index_jobs SET status='pending',next_run_at='',last_error='recovered after restart',updated_at=? WHERE status='running'`), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) WaitReady(ctx context.Context, collection, ownerID string, maxWait time.Duration) bool {
	deadline := time.Now().Add(maxWait)
	for {
		var n int
		err := s.db.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM memory_index_jobs WHERE collection_name=? AND owner_id=? AND status IN ('pending','running','failed')`), collection, ownerID).Scan(&n)
		if err != nil || n == 0 {
			return err == nil
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (s *Store) bind(q string) string {
	if !s.postgres {
		return q
	}
	var b strings.Builder
	n := 1
	for _, r := range q {
		if r == '?' {
			fmt.Fprintf(&b, "$%d", n)
			n++
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func isPostgres(db *sql.DB) bool {
	n := strings.ToLower(fmt.Sprintf("%T", db.Driver()))
	return strings.Contains(n, "pgx") || strings.Contains(n, "pq") || strings.Contains(n, "stdlib")
}
