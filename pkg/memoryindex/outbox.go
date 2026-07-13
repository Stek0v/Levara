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

func (s *Store) Claim(ctx context.Context) (Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var j Job
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id,memory_id,operation,collection_name,owner_id,digest,embed_model,embed_dimension,status,attempts,next_run_at,last_error
		FROM memory_index_jobs WHERE status IN ('pending','failed') AND (next_run_at='' OR next_run_at<=?) ORDER BY created_at LIMIT 1`), now).
		Scan(&j.ID, &j.MemoryID, &j.Operation, &j.Collection, &j.OwnerID, &j.Digest, &j.Model, &j.Dimension, &j.Status, &j.Attempts, &j.NextRunAt, &j.LastError)
	if err == sql.ErrNoRows {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	j.Attempts++
	j.Status = Running
	if _, err = tx.ExecContext(ctx, s.bind(`UPDATE memory_index_jobs SET status='running',attempts=?,updated_at=? WHERE id=?`), j.Attempts, now, j.ID); err != nil {
		return Job{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return Job{}, false, err
	}
	return j, true, nil
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
	_, err := s.db.ExecContext(ctx, s.bind(`UPDATE memory_index_jobs SET status=?,last_error=?,next_run_at=?,updated_at=? WHERE id=?`), string(status), last, next, time.Now().UTC().Format(time.RFC3339Nano), j.ID)
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
