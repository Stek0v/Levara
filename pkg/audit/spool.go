package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrSpoolFull      = errors.New("audit spool capacity exhausted")
	ErrSpoolEvent     = errors.New("invalid audit envelope")
	ErrSpoolOversize  = errors.New("audit envelope exceeds byte limit")
	ErrSpoolLease     = errors.New("audit delivery lease lost")
	ErrSpoolAdmission = errors.New("audit event was not admitted")
)

// VerifiedScope comes from server authentication, never event metadata. It is
// audit attribution, not authority to access a resource or another tenant.
type VerifiedScope struct {
	ActorID, TenantID string
	Verified          bool
}

// SpoolEnvelope is the entire export schema. Content, arguments, error text,
// request headers and arbitrary metadata cannot enter the payload. Resource and
// target are populated only for the closed document.rest operation vocabulary.
type SpoolEnvelope struct {
	Version       int    `json:"version"`
	EventID       string `json:"event_id"`
	TS            string `json:"ts"`
	Source        string `json:"source"`
	Operation     string `json:"operation"`
	Outcome       string `json:"outcome"`
	ActorID       string `json:"actor_id,omitempty"`
	TenantID      string `json:"tenant_id,omitempty"`
	ScopeVerified bool   `json:"scope_verified"`
	Resource      string `json:"resource,omitempty"`
	Target        string `json:"target,omitempty"`
	LatencyMS     int64  `json:"latency_ms"`
	ResultCount   int64  `json:"result_count"`
	RequestBytes  int64  `json:"request_bytes"`
	ResponseBytes int64  `json:"response_bytes"`
}

func spoolResource(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == "" || part == "." || part == ".." || !spoolIdentifier(part, 128) {
			return false
		}
	}
	return true
}

func spoolIdentifier(s string, max int) bool {
	if len(s) > max {
		return false
	}
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("_.:@-", c) {
			continue
		}
		return false
	}
	return true
}

func spoolOutcome(s string) string {
	// Workspace has its own closed outcome vocabulary; preserve it exactly.
	switch s {
	case "success", "failure", "denied":
		return s
	}
	for _, outcome := range AllOutcomes() {
		if string(outcome) == s {
			return s
		}
	}
	return "unknown"
}

func EnvelopeFromEntry(e Entry) SpoolEnvelope {
	return envelopeFrom(e.TS, "mcp", e.Tool, string(e.Outcome), VerifiedScope{ActorID: e.AgentID, TenantID: e.TenantID, Verified: e.ScopeVerified}, e.LatencyMS, int64(e.ResultCount), int64(e.RequestBytes), int64(e.ResponseBytes))
}

func EnvelopeFromEvent(e Event, scope VerifiedScope) SpoolEnvelope {
	// Only the fixed numeric fields are copied; even well-formed strings in
	// metadata cannot supply actor, tenant, operation or source proof.
	number := func(key string) int64 {
		switch v := e.Metadata[key].(type) {
		case int:
			if v >= 0 {
				return int64(v)
			}
		case int64:
			if v >= 0 {
				return v
			}
		case float64:
			if v >= 0 && v < 1<<53 {
				return int64(v)
			}
		}
		return 0
	}
	envelope := envelopeFrom(e.TS, e.Source, e.Type, e.Outcome, scope, number("latency_ms"), number("result_count"), number("request_bytes"), number("response_bytes"))
	if e.Source == "document.rest" {
		switch e.Type {
		case "register", "set_mode", "set_hold", "grant", "revoke", "group_create", "group_members_replace":
			if spoolResource(e.Subject, 257) {
				envelope.Resource = e.Subject
			}
		}
		if e.Type == "grant" || e.Type == "revoke" {
			kind, kindOK := e.Metadata["principal_kind"].(string)
			id, idOK := e.Metadata["principal_id"].(string)
			target := kind + "/" + id
			if kindOK && idOK && (kind == "user" || kind == "group") && spoolResource(target, 257) {
				envelope.Target = target
			}
		}
	}
	return envelope
}

func envelopeFrom(ts, source, operation, outcome string, scope VerifiedScope, latency, count, request, response int64) SpoolEnvelope {
	if ts == "" {
		ts = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if !scope.Verified {
		scope.ActorID, scope.TenantID = "", ""
	}
	return SpoolEnvelope{Version: 1, EventID: uuid.NewString(), TS: ts, Source: source, Operation: operation, Outcome: spoolOutcome(outcome), ActorID: scope.ActorID, TenantID: scope.TenantID, ScopeVerified: scope.Verified, LatencyMS: max(latency, 0), ResultCount: max(count, 0), RequestBytes: max(request, 0), ResponseBytes: max(response, 0)}
}

func (e SpoolEnvelope) validate() error {
	if e.Version != 1 || !spoolIdentifier(e.EventID, 128) || e.EventID == "" || !spoolIdentifier(e.Source, 64) || e.Source == "" || !spoolIdentifier(e.Operation, 128) || e.Operation == "" || !spoolIdentifier(e.ActorID, 128) || !spoolIdentifier(e.TenantID, 128) || (e.Resource != "" && !spoolResource(e.Resource, 257)) || (e.Target != "" && !spoolResource(e.Target, 257)) || (e.Outcome != "unknown" && spoolOutcome(e.Outcome) != e.Outcome) {
		return ErrSpoolEvent
	}
	if len(e.TS) > 64 {
		return ErrSpoolEvent
	}
	if _, err := time.Parse(time.RFC3339Nano, e.TS); err != nil {
		return ErrSpoolEvent
	}
	if (!e.ScopeVerified && (e.ActorID != "" || e.TenantID != "")) || (e.ScopeVerified && e.ActorID == "") || e.LatencyMS < 0 || e.ResultCount < 0 || e.RequestBytes < 0 || e.ResponseBytes < 0 {
		return ErrSpoolEvent
	}
	return nil
}

type SpoolConfig struct {
	Dialect             string // sqlite or postgres; chosen by server configuration
	DestinationID       string
	MaxEvents           int
	MaxBytes            int64
	MaxEventBytes       int
	MaxAttempts         int // includes first attempt; exhausted leases enter DLQ
	LeaseDuration       time.Duration
	RetryBase, RetryMax time.Duration
}

type SQLSpool struct {
	db  *sql.DB
	cfg SpoolConfig
	now func() time.Time
}

func NewSQLSpool(db *sql.DB, cfg SpoolConfig) (*SQLSpool, error) {
	if db == nil {
		return nil, errors.New("audit spool database required")
	}
	var err error
	cfg, err = cfg.normalized()
	if err != nil {
		return nil, err
	}
	return &SQLSpool{db: db, cfg: cfg, now: time.Now}, nil
}

func (cfg SpoolConfig) normalized() (SpoolConfig, error) {
	if cfg.MaxEvents == 0 {
		cfg.MaxEvents = 10000
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = 16 << 20
	}
	if cfg.MaxEventBytes == 0 {
		cfg.MaxEventBytes = 2048
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.LeaseDuration == 0 {
		cfg.LeaseDuration = 30 * time.Second
	}
	if cfg.RetryBase == 0 {
		cfg.RetryBase = time.Second
	}
	if cfg.RetryMax == 0 {
		cfg.RetryMax = time.Minute
	}
	if (cfg.Dialect != "sqlite" && cfg.Dialect != "postgres") || cfg.DestinationID == "" || !spoolIdentifier(cfg.DestinationID, 128) || cfg.MaxEvents < 1 || cfg.MaxEvents > 1000000 || cfg.MaxBytes < 1 || cfg.MaxBytes > 1<<40 || cfg.MaxEventBytes < 128 || cfg.MaxEventBytes > 65536 || int64(cfg.MaxEventBytes) > cfg.MaxBytes || cfg.MaxAttempts < 1 || cfg.MaxAttempts > 20 || cfg.LeaseDuration < time.Millisecond || cfg.LeaseDuration > time.Hour || cfg.RetryBase < time.Millisecond || cfg.RetryMax < cfg.RetryBase || cfg.RetryMax > 24*time.Hour {
		return cfg, errors.New("invalid audit spool configuration")
	}
	return cfg, nil
}

func (s *SQLSpool) q(query string) string {
	if s.cfg.Dialect == "sqlite" {
		return query
	}
	i := 0
	var out strings.Builder
	for _, r := range query {
		if r == '?' {
			i++
			fmt.Fprintf(&out, "$%d", i)
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

// EnsureSchema initializes a separate durable spool, never the lossy analytics
// projection. One destination row serializes SQL admission and worker metadata.
func (s *SQLSpool) EnsureSchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS audit_spool_destinations (id TEXT PRIMARY KEY,max_events BIGINT NOT NULL CHECK(max_events>0),max_bytes BIGINT NOT NULL CHECK(max_bytes>0),max_event_bytes BIGINT NOT NULL CHECK(max_event_bytes>0),max_attempts BIGINT NOT NULL CHECK(max_attempts>0),lease_ms BIGINT NOT NULL CHECK(lease_ms>0),retry_base_ms BIGINT NOT NULL CHECK(retry_base_ms>0),retry_max_ms BIGINT NOT NULL CHECK(retry_max_ms>=retry_base_ms),delivered BIGINT NOT NULL DEFAULT 0 CHECK(delivered>=0),retried BIGINT NOT NULL DEFAULT 0 CHECK(retried>=0),rejected BIGINT NOT NULL DEFAULT 0 CHECK(rejected>=0))`,
		`CREATE TABLE IF NOT EXISTS audit_spool_events (destination_id TEXT NOT NULL REFERENCES audit_spool_destinations(id),event_id TEXT NOT NULL,payload TEXT NOT NULL,payload_bytes BIGINT NOT NULL CHECK(payload_bytes>0),created_ms BIGINT NOT NULL,state TEXT NOT NULL CHECK(state IN ('pending','leased','dead')),attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts>=0),next_ms BIGINT NOT NULL,lease_token TEXT NOT NULL DEFAULT '',lease_until_ms BIGINT NOT NULL DEFAULT 0,last_status INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(destination_id,event_id),CHECK((state='leased' AND lease_token<>'' AND lease_until_ms>0) OR (state<>'leased' AND lease_token='' AND lease_until_ms=0)))`,
		`CREATE INDEX IF NOT EXISTS idx_audit_spool_ready ON audit_spool_events(destination_id,state,next_ms,created_ms)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_spool_expiry ON audit_spool_events(destination_id,state,lease_until_ms)`,
	} {
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	c := s.cfg
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO audit_spool_destinations(id,max_events,max_bytes,max_event_bytes,max_attempts,lease_ms,retry_base_ms,retry_max_ms) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`), c.DestinationID, c.MaxEvents, c.MaxBytes, c.MaxEventBytes, c.MaxAttempts, c.LeaseDuration.Milliseconds(), c.RetryBase.Milliseconds(), c.RetryMax.Milliseconds())
	if err != nil {
		return err
	}
	var events, eventBytes, attempts int
	var bytes, lease, base, cap int64
	if err = tx.QueryRowContext(ctx, s.q(`SELECT max_events,max_bytes,max_event_bytes,max_attempts,lease_ms,retry_base_ms,retry_max_ms FROM audit_spool_destinations WHERE id=?`), c.DestinationID).Scan(&events, &bytes, &eventBytes, &attempts, &lease, &base, &cap); err != nil {
		return err
	}
	if events != c.MaxEvents || bytes != c.MaxBytes || eventBytes != c.MaxEventBytes || attempts != c.MaxAttempts || lease != c.LeaseDuration.Milliseconds() || base != c.RetryBase.Milliseconds() || cap != c.RetryMax.Milliseconds() {
		return errors.New("audit spool destination configuration differs from persisted bounds")
	}
	return tx.Commit()
}

func (s *SQLSpool) begin(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	// This is the first statement: SQLite obtains its writer lock before any
	// read snapshot; PostgreSQL takes the destination row lock.
	res, err := tx.ExecContext(ctx, s.q(`UPDATE audit_spool_destinations SET id=id WHERE id=?`), s.cfg.DestinationID)
	if err == nil {
		var n int64
		n, err = res.RowsAffected()
		if err == nil && n != 1 {
			err = errors.New("audit spool destination is not initialized")
		}
	}
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func (s *SQLSpool) rejected(ctx context.Context, reason error) error {
	res, err := s.db.ExecContext(ctx, s.q(`UPDATE audit_spool_destinations SET rejected=rejected+1 WHERE id=?`), s.cfg.DestinationID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrSpoolAdmission
	}
	return reason
}

// Admit returns only after commit. Retrying the same envelope before delivery
// is idempotent; reuse the envelope's ID after an uncertain commit. Delivered
// rows are deleted, so consumers must also deduplicate event_id (at least once).
// This SQL transaction does not include the upstream business mutation.
func (s *SQLSpool) Admit(ctx context.Context, e SpoolEnvelope) error {
	if err := e.validate(); err != nil {
		return s.rejected(ctx, err)
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return s.rejected(ctx, ErrSpoolEvent)
	}
	if len(raw) > s.cfg.MaxEventBytes {
		return s.rejected(ctx, ErrSpoolOversize)
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var prior string
	err = tx.QueryRowContext(ctx, s.q(`SELECT payload FROM audit_spool_events WHERE destination_id=? AND event_id=?`), s.cfg.DestinationID, e.EventID).Scan(&prior)
	if err == nil {
		if prior != string(raw) {
			if _, err = tx.ExecContext(ctx, s.q(`UPDATE audit_spool_destinations SET rejected=rejected+1 WHERE id=?`), s.cfg.DestinationID); err != nil {
				return err
			}
			if err = tx.Commit(); err != nil {
				return err
			}
			return ErrSpoolEvent
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var count, bytes int64
	if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*),COALESCE(SUM(payload_bytes),0) FROM audit_spool_events WHERE destination_id=?`), s.cfg.DestinationID).Scan(&count, &bytes); err != nil {
		return err
	}
	if count >= int64(s.cfg.MaxEvents) || bytes+int64(len(raw)) > s.cfg.MaxBytes {
		if _, err = tx.ExecContext(ctx, s.q(`UPDATE audit_spool_destinations SET rejected=rejected+1 WHERE id=?`), s.cfg.DestinationID); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return ErrSpoolFull
	}
	now := s.now().UnixMilli()
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO audit_spool_events(destination_id,event_id,payload,payload_bytes,created_ms,state,next_ms) VALUES(?,?,?,?,?,'pending',?)`), s.cfg.DestinationID, e.EventID, string(raw), len(raw), now, now)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLSpool) AdmitEntry(ctx context.Context, e Entry) (string, error) {
	envelope := EnvelopeFromEntry(e)
	return envelope.EventID, s.Admit(ctx, envelope)
}
func (s *SQLSpool) AdmitEvent(ctx context.Context, e Event, scope VerifiedScope) (string, error) {
	envelope := EnvelopeFromEvent(e, scope)
	return envelope.EventID, s.Admit(ctx, envelope)
}

type spoolClaim struct {
	id, payload, token string
	bytes, attempts    int
	until              int64
}

func (s *SQLSpool) claim(ctx context.Context, count, bytes int) ([]spoolClaim, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := s.now().UnixMilli()
	// A crash during the final attempt must not make a permanently leased row.
	_, err = tx.ExecContext(ctx, s.q(`UPDATE audit_spool_events SET state=CASE WHEN attempts>=? THEN 'dead' ELSE 'pending' END,lease_token='',lease_until_ms=0,next_ms=? WHERE destination_id=? AND state='leased' AND lease_until_ms<=?`), s.cfg.MaxAttempts, now, s.cfg.DestinationID, now)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, s.q(`SELECT event_id,payload,payload_bytes,attempts FROM audit_spool_events WHERE destination_id=? AND state='pending' AND next_ms<=? ORDER BY created_ms,event_id LIMIT ?`), s.cfg.DestinationID, now, count)
	if err != nil {
		return nil, err
	}
	batch := []spoolClaim{}
	used := len(`{"events":[]}`)
	for rows.Next() {
		var c spoolClaim
		if err = rows.Scan(&c.id, &c.payload, &c.bytes, &c.attempts); err != nil {
			rows.Close()
			return nil, err
		}
		extra := c.bytes
		if len(batch) > 0 {
			extra++
		}
		if used+extra > bytes {
			break
		}
		used += extra
		batch = append(batch, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	retries := 0
	for i := range batch {
		c := &batch[i]
		c.token = uuid.NewString()
		c.until = now + s.cfg.LeaseDuration.Milliseconds()
		if c.attempts > 0 {
			retries++
		}
		c.attempts++
		res, err := tx.ExecContext(ctx, s.q(`UPDATE audit_spool_events SET state='leased',attempts=attempts+1,lease_token=?,lease_until_ms=? WHERE destination_id=? AND event_id=? AND state='pending' AND attempts=?`), c.token, c.until, s.cfg.DestinationID, c.id, c.attempts-1)
		if err != nil {
			return nil, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, ErrSpoolLease
		}
	}
	if retries > 0 {
		if _, err = tx.ExecContext(ctx, s.q(`UPDATE audit_spool_destinations SET retried=retried+? WHERE id=?`), retries, s.cfg.DestinationID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return batch, nil
}

func (s *SQLSpool) settle(ctx context.Context, batch []spoolClaim, status int, success bool) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UnixMilli()
	for _, c := range batch {
		var result sql.Result
		if success {
			result, err = tx.ExecContext(ctx, s.q(`DELETE FROM audit_spool_events WHERE destination_id=? AND event_id=? AND state='leased' AND lease_token=? AND lease_until_ms>?`), s.cfg.DestinationID, c.id, c.token, now)
		} else {
			state := "pending"
			if c.attempts >= s.cfg.MaxAttempts {
				state = "dead"
			}
			backoff := s.cfg.RetryBase
			for i := 1; i < c.attempts && backoff < s.cfg.RetryMax; i++ {
				backoff = min(backoff*2, s.cfg.RetryMax)
			}
			result, err = tx.ExecContext(ctx, s.q(`UPDATE audit_spool_events SET state=?,next_ms=?,lease_token='',lease_until_ms=0,last_status=? WHERE destination_id=? AND event_id=? AND state='leased' AND lease_token=? AND lease_until_ms>?`), state, now+backoff.Milliseconds(), status, s.cfg.DestinationID, c.id, c.token, now)
		}
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrSpoolLease
		}
	}
	if success {
		if _, err = tx.ExecContext(ctx, s.q(`UPDATE audit_spool_destinations SET delivered=delivered+? WHERE id=?`), len(batch), s.cfg.DestinationID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type SpoolStats struct {
	Pending, Dead, PendingBytes, DeadBytes int64
	OldestPendingAge, OldestDeadAge        time.Duration
	Delivered, Retried, Rejected           int64
}

func (s *SQLSpool) Stats(ctx context.Context) (SpoolStats, error) {
	var out SpoolStats
	var pendingSince, deadSince int64
	// One statement provides a coherent snapshot including in-flight leases.
	err := s.db.QueryRowContext(ctx, s.q(`SELECT d.delivered,d.retried,d.rejected,COALESCE(SUM(CASE WHEN e.state<>'dead' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN e.state='dead' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN e.state<>'dead' THEN e.payload_bytes ELSE 0 END),0),COALESCE(SUM(CASE WHEN e.state='dead' THEN e.payload_bytes ELSE 0 END),0),COALESCE(MIN(CASE WHEN e.state<>'dead' THEN e.created_ms END),0),COALESCE(MIN(CASE WHEN e.state='dead' THEN e.created_ms END),0) FROM audit_spool_destinations d LEFT JOIN audit_spool_events e ON e.destination_id=d.id WHERE d.id=? GROUP BY d.id,d.delivered,d.retried,d.rejected`), s.cfg.DestinationID).Scan(&out.Delivered, &out.Retried, &out.Rejected, &out.Pending, &out.Dead, &out.PendingBytes, &out.DeadBytes, &pendingSince, &deadSince)
	if err != nil {
		return out, err
	}
	now := s.now().UnixMilli()
	if pendingSince > 0 {
		out.OldestPendingAge = time.Duration(max(now-pendingSince, 0)) * time.Millisecond
	}
	if deadSince > 0 {
		out.OldestDeadAge = time.Duration(max(now-deadSince, 0)) * time.Millisecond
	}
	return out, nil
}

// DeadLetters returns only the sanitized export envelopes, with stable pagination.
func (s *SQLSpool) DeadLetters(ctx context.Context, after string, limit int) ([]SpoolEnvelope, error) {
	if limit < 1 || limit > 100 || !spoolIdentifier(after, 128) {
		return nil, ErrSpoolEvent
	}
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT payload FROM audit_spool_events WHERE destination_id=? AND state='dead' AND event_id>? ORDER BY event_id LIMIT ?`), s.cfg.DestinationID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SpoolEnvelope, 0)
	for rows.Next() {
		var raw string
		var event SpoolEnvelope
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, err
		}
		if err := event.validate(); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

// ResolveDeadLetters preserves event IDs on replay. A repeated request can only
// affect rows still in dead-letter state; it cannot steal a delivery lease.
func (s *SQLSpool) ResolveDeadLetters(ctx context.Context, ids []string, discard bool) (int64, error) {
	if len(ids) < 1 || len(ids) > 100 {
		return 0, ErrSpoolEvent
	}
	for _, id := range ids {
		if id == "" || !spoolIdentifier(id, 128) {
			return 0, ErrSpoolEvent
		}
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var total int64
	for _, id := range ids {
		query := `UPDATE audit_spool_events SET state='pending',attempts=0,next_ms=?,lease_token='',lease_until_ms=0 WHERE destination_id=? AND event_id=? AND state='dead'`
		args := []any{s.now().UnixMilli(), s.cfg.DestinationID, id}
		if discard {
			query = `DELETE FROM audit_spool_events WHERE destination_id=? AND event_id=? AND state='dead'`
			args = args[1:]
		}
		r, err := tx.ExecContext(ctx, s.q(query), args...)
		if err != nil {
			return 0, err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return 0, err
		}
		total += n
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return total, nil
}

// SpoolSink adapts void legacy interfaces. Admission can block only until its
// deadline; errors are surfaced through the mandatory generic callback. Use
// Admit directly where the caller must receive a persistence error.
// The callback runs inline and must be bounded (for example, a counter/log).
type SpoolSink struct {
	spool   *SQLSpool
	timeout time.Duration
	onError func(error)
}

func NewSpoolSink(spool *SQLSpool, timeout time.Duration, onError func(error)) (*SpoolSink, error) {
	if spool == nil || timeout <= 0 || timeout > time.Minute || onError == nil {
		return nil, errors.New("audit spool adapter requires bounded admission and an error callback")
	}
	return &SpoolSink{spool, timeout, onError}, nil
}

func (s *SpoolSink) Log(e Entry) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	if _, err := s.spool.AdmitEntry(ctx, e); err != nil {
		s.onError(ErrSpoolAdmission)
	}
}
func (s *SpoolSink) WriteEvent(e Event) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	_, err := s.spool.AdmitEvent(ctx, e, e.VerifiedScope)
	return err
}

// Legacy events without server-supplied VerifiedScope omit actor and tenant.
func (s *SpoolSink) LogEvent(e Event) {
	if err := s.WriteEvent(e); err != nil {
		s.onError(ErrSpoolAdmission)
	}
}

var _ Sink = (*SpoolSink)(nil)
var _ EventSink = (*SpoolSink)(nil)
