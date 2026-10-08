package memoryindex

import (
	"context"
	"database/sql"
	"errors"
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
	for _, ddl := range []string{
		`CREATE INDEX IF NOT EXISTS memory_index_jobs_ready ON memory_index_jobs(collection_name,owner_id) WHERE status IN ('pending','running','failed')`,
		`CREATE INDEX IF NOT EXISTS memory_index_jobs_claim ON memory_index_jobs(created_at,next_run_at) WHERE status IN ('pending','failed')`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Enqueue adds a job outside any caller transaction. Completed duplicates reopen
// with a fresh ID so an old Finish/Defer cannot match a reset attempt counter.
func (s *Store) Enqueue(ctx context.Context, j Job) (Job, error) {
	if j.ID == "" {
		j.ID = uuid.NewString()
	}
	j.Status = Pending
	now := time.Now().UTC().Format(time.RFC3339Nano)
	q := s.bind(`INSERT INTO memory_index_jobs (id,memory_id,operation,collection_name,owner_id,digest,embed_model,embed_dimension,status,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(memory_id,operation,digest) DO UPDATE SET
		id=?,embed_model=excluded.embed_model,embed_dimension=excluded.embed_dimension,status='pending',attempts=0,next_run_at='',last_error='',updated_at=excluded.updated_at
		WHERE memory_index_jobs.status='completed' AND memory_index_jobs.collection_name=excluded.collection_name AND memory_index_jobs.owner_id=excluded.owner_id`)
	if _, err := s.db.ExecContext(ctx, q, j.ID, j.MemoryID, j.Operation, j.Collection, j.OwnerID, j.Digest, j.Model, j.Dimension, string(j.Status), now, now, uuid.NewString()); err != nil {
		return Job{}, err
	}
	owner, collection := j.OwnerID, j.Collection
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id,status,attempts,collection_name,owner_id,embed_model,embed_dimension,next_run_at,last_error FROM memory_index_jobs WHERE memory_id=? AND operation=? AND digest=?`), j.MemoryID, j.Operation, j.Digest).
		Scan(&j.ID, &j.Status, &j.Attempts, &j.Collection, &j.OwnerID, &j.Model, &j.Dimension, &j.NextRunAt, &j.LastError)
	if err != nil {
		return Job{}, err
	}
	if j.Status == Completed && (j.OwnerID != owner || j.Collection != collection) {
		return Job{}, fmt.Errorf("memory index outbox: completed publication namespace mismatch")
	}
	return j, nil
}

// EnqueueTx adds or reopens a job within the caller's transaction. Rotation and
// retry-budget reset roll back together with the caller's authoritative writes.
func (s *Store) EnqueueTx(ctx context.Context, tx *sql.Tx, j Job) (Job, error) {
	if j.ID == "" {
		j.ID = uuid.NewString()
	}
	j.Status = Pending
	now := time.Now().UTC().Format(time.RFC3339Nano)
	q := s.bind(`INSERT INTO memory_index_jobs (id,memory_id,operation,collection_name,owner_id,digest,embed_model,embed_dimension,status,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(memory_id,operation,digest) DO UPDATE SET
		id=?,embed_model=excluded.embed_model,embed_dimension=excluded.embed_dimension,status='pending',attempts=0,next_run_at='',last_error='',updated_at=excluded.updated_at
		WHERE memory_index_jobs.status='completed' AND memory_index_jobs.collection_name=excluded.collection_name AND memory_index_jobs.owner_id=excluded.owner_id`)
	if _, err := tx.ExecContext(ctx, q, j.ID, j.MemoryID, j.Operation, j.Collection, j.OwnerID, j.Digest, j.Model, j.Dimension, string(j.Status), now, now, uuid.NewString()); err != nil {
		return Job{}, err
	}
	owner, collection := j.OwnerID, j.Collection
	err := tx.QueryRowContext(ctx, s.bind(`SELECT id,status,attempts,collection_name,owner_id,embed_model,embed_dimension,next_run_at,last_error FROM memory_index_jobs WHERE memory_id=? AND operation=? AND digest=?`), j.MemoryID, j.Operation, j.Digest).
		Scan(&j.ID, &j.Status, &j.Attempts, &j.Collection, &j.OwnerID, &j.Model, &j.Dimension, &j.NextRunAt, &j.LastError)
	if err != nil {
		return Job{}, err
	}
	if j.Status == Completed && (j.OwnerID != owner || j.Collection != collection) {
		return Job{}, fmt.Errorf("memory index outbox: completed publication namespace mismatch")
	}
	return j, nil
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
	updQ := s.bind(`UPDATE memory_index_jobs SET status='running',attempts=attempts+1,updated_at=? WHERE id=? AND status IN ('pending','failed') AND (next_run_at='' OR next_run_at<=?)`)
	for _, id := range candidates {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return Job{}, false, err
		}
		var j Job
		// Decode before committing: a failed read must not leave a durable
		// running claim with no worker holding its full identity.
		err = tx.QueryRowContext(ctx, updQ+` RETURNING id,memory_id,operation,collection_name,owner_id,digest,embed_model,embed_dimension,status,attempts,next_run_at,last_error`, now, id, now).
			Scan(&j.ID, &j.MemoryID, &j.Operation, &j.Collection, &j.OwnerID, &j.Digest, &j.Model, &j.Dimension, &j.Status, &j.Attempts, &j.NextRunAt, &j.LastError)
		if err != nil {
			_ = tx.Rollback()
			if errors.Is(err, sql.ErrNoRows) {
				continue // another process claimed it first
			}
			return Job{}, false, err
		}
		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			return Job{}, false, err
		}
		return j, true, nil
	}
	return Job{}, false, nil
}

// Defer parks a claim that never reached the provider until the breaker's
// retry time. The claim's attempt stays spent: refunding it would let a
// permanently unavailable provider park a job forever (load-gate S3 drain,
// 2026-09-29), so once attempts reaches maxAttempts the job goes to
// dead_letter — the preceding failure is retained for diagnosis, and
// memory_index_retry can requeue it after recovery.
func (s *Store) Defer(ctx context.Context, j Job, until time.Time, maxAttempts int) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if maxAttempts > 0 && j.Attempts >= maxAttempts {
		_, err := s.db.ExecContext(ctx, s.bind(`UPDATE memory_index_jobs SET status='dead_letter',last_error=CASE WHEN last_error='' THEN 'defer budget exhausted: embedding service unavailable' ELSE last_error END,next_run_at='',updated_at=?
			WHERE id=? AND status='running' AND attempts=? AND next_run_at=? AND memory_id=? AND operation=? AND digest=? AND collection_name=? AND owner_id=?`),
			now, j.ID, j.Attempts, j.NextRunAt, j.MemoryID, j.Operation, j.Digest, j.Collection, j.OwnerID)
		return err
	}
	_, err := s.db.ExecContext(ctx, s.bind(`UPDATE memory_index_jobs SET status='pending',next_run_at=?,updated_at=?
		WHERE id=? AND status='running' AND attempts=? AND next_run_at=? AND memory_id=? AND operation=? AND digest=? AND collection_name=? AND owner_id=?`),
		until.UTC().Format(time.RFC3339Nano), now, j.ID, j.Attempts, j.NextRunAt, j.MemoryID, j.Operation, j.Digest, j.Collection, j.OwnerID)
	return err
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
	// Match the full claim identity: caller-supplied IDs can be reused by another
	// tuple after rotation, and stale workers must not change that publication.
	_, err := s.db.ExecContext(ctx, s.bind(`UPDATE memory_index_jobs SET status=?,last_error=?,next_run_at=?,updated_at=? WHERE id=? AND status='running' AND attempts=? AND next_run_at=? AND memory_id=? AND operation=? AND digest=? AND collection_name=? AND owner_id=?`), string(status), last, next, time.Now().UTC().Format(time.RFC3339Nano), j.ID, j.Attempts, j.NextRunAt, j.MemoryID, j.Operation, j.Digest, j.Collection, j.OwnerID)
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
	query := `SELECT EXISTS(SELECT 1 FROM memory_index_jobs WHERE collection_name=? AND owner_id=? AND status IN ('pending','running','failed'))`
	if s.postgres {
		// COUNT uses the partial index reliably when pending rows sit after a
		// large completed history; PostgreSQL can choose a slow seq scan for EXISTS.
		query = `SELECT COUNT(*) > 0 FROM memory_index_jobs WHERE collection_name=? AND owner_id=? AND status IN ('pending','running','failed')`
	}
	for {
		var pending bool
		err := s.db.QueryRowContext(ctx, s.bind(query), collection, ownerID).Scan(&pending)
		if err != nil || !pending {
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
