package audit

import (
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// MultiSink mirrors an entry to independent sinks. A nil sink is ignored.
type MultiSink []Sink

func (s MultiSink) Log(e Entry) {
	for _, sink := range s {
		if sink != nil {
			sink.Log(e)
		}
	}
}

// ReadModel is an asynchronous, rebuildable SQL projection of the durable
// JSONL MCP audit stream. Queue saturation drops only the projection copy;
// callers retain the primary JSONL event and never block on the database.
type ReadModel struct {
	db        *sql.DB
	q         chan Entry
	dropped   atomic.Uint64
	inserted  atomic.Uint64
	errors    atomic.Uint64
	queued    atomic.Int64
	importing atomic.Bool
	imported  atomic.Uint64
	closed    chan struct{}
	postgres  bool
}

type ProjectionHealth struct {
	QueueDepth int    `json:"queue_depth"`
	Dropped    uint64 `json:"dropped"`
	Inserted   uint64 `json:"inserted"`
	Errors     uint64 `json:"errors"`
	Importing  bool   `json:"importing"`
	Imported   uint64 `json:"imported"`
}

type Summary struct {
	Total          int64            `json:"total"`
	Errors         int64            `json:"errors"`
	ZeroResults    int64            `json:"zero_results"`
	ErrorRate      float64          `json:"error_rate"`
	ZeroResultRate float64          `json:"zero_result_rate"`
	P50MS          int64            `json:"p50_ms"`
	P95MS          int64            `json:"p95_ms"`
	P99MS          int64            `json:"p99_ms"`
	ByTool         map[string]int64 `json:"by_tool"`
	ByOutcome      map[string]int64 `json:"by_outcome"`
	Dropped        uint64           `json:"projection_dropped"`
}

type EventRow struct {
	ID           string          `json:"id"`
	TS           string          `json:"ts"`
	SessionID    string          `json:"session_id,omitempty"`
	AgentID      string          `json:"agent_id,omitempty"`
	ClientName   string          `json:"client_name,omitempty"`
	Toolset      string          `json:"toolset,omitempty"`
	Tool         string          `json:"tool"`
	Collection   string          `json:"collection,omitempty"`
	LatencyMS    int64           `json:"latency_ms"`
	Outcome      string          `json:"outcome"`
	ResultCount  int             `json:"result_count"`
	ZeroResult   bool            `json:"zero_result"`
	TraceID      string          `json:"trace_id,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
	Args         json.RawMessage `json:"args,omitempty"`
}

type EventFilter struct {
	Since                             time.Time
	Tool, Outcome, Client, Collection string
	Limit, Offset                     int
	IncludeArgs                       bool
}

func NewReadModel(db *sql.DB, queueSize int) (*ReadModel, error) {
	if db == nil {
		return nil, fmt.Errorf("audit read model: nil database")
	}
	if queueSize <= 0 {
		queueSize = 4096
	}
	ddl := `CREATE TABLE IF NOT EXISTS mcp_audit_events (
		id TEXT PRIMARY KEY, ts TEXT NOT NULL, session_id TEXT, agent_id TEXT,
		client_name TEXT, client_version TEXT, toolset TEXT, tool TEXT NOT NULL,
		collection_name TEXT, args_json TEXT, latency_ms INTEGER NOT NULL,
		outcome TEXT NOT NULL, result_count INTEGER NOT NULL DEFAULT 0,
		zero_result INTEGER NOT NULL DEFAULT 0, request_bytes INTEGER NOT NULL DEFAULT 0,
		response_bytes INTEGER NOT NULL DEFAULT 0, trace_id TEXT, error_message TEXT
	)`
	if _, err := db.Exec(ddl); err != nil {
		return nil, err
	}
	driverName := strings.ToLower(fmt.Sprintf("%T", db.Driver()))
	r := &ReadModel{db: db, q: make(chan Entry, queueSize), closed: make(chan struct{}), postgres: strings.Contains(driverName, "pgx") || strings.Contains(driverName, "pq") || strings.Contains(driverName, "stdlib")}
	go r.run()
	return r, nil
}

func (r *ReadModel) Log(e Entry) {
	if r == nil {
		return
	}
	select {
	case r.q <- e:
		r.queued.Add(1)
	default:
		r.dropped.Add(1)
	}
}

func (r *ReadModel) run() {
	defer close(r.closed)
	const batchSize = 256
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	batch := make([]Entry, 0, batchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := r.insertBatch(context.Background(), batch); err != nil {
			r.errors.Add(1)
		} else {
			r.inserted.Add(uint64(len(batch)))
		}
		batch = batch[:0]
	}
	for {
		select {
		case e, ok := <-r.q:
			if !ok {
				flush()
				return
			}
			r.queued.Add(-1)
			batch = append(batch, e)
			if len(batch) == batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (r *ReadModel) insert(ctx context.Context, e Entry) error {
	return r.insertBatch(ctx, []Entry{e})
}

func (r *ReadModel) insertBatch(ctx context.Context, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	const columns = 18
	var query strings.Builder
	query.WriteString(`INSERT INTO mcp_audit_events (id,ts,session_id,agent_id,client_name,client_version,toolset,tool,collection_name,args_json,latency_ms,outcome,result_count,zero_result,request_bytes,response_bytes,trace_id,error_message) VALUES `)
	values := make([]any, 0, len(entries)*columns)
	for i, e := range entries {
		if i > 0 {
			query.WriteByte(',')
		}
		query.WriteString("(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
		args, _ := json.Marshal(e.Args)
		id := e.RequestID
		if id == "" {
			id = fmt.Sprintf("%s:%s:%s:%d", e.TS, e.SessionID, e.Tool, e.LatencyMS)
		}
		values = append(values, id, e.TS, e.SessionID, e.AgentID, e.ClientName, e.ClientVersion, e.Toolset, e.Tool, e.Collection, string(args), e.LatencyMS, string(e.Outcome), e.ResultCount, boolInt(e.ZeroResult), e.RequestBytes, e.ResponseBytes, e.TraceID, e.ErrorMessage)
	}
	query.WriteString(" ON CONFLICT(id) DO NOTHING")
	_, err := r.db.ExecContext(ctx, r.bind(query.String()), values...)
	return err
}

func (r *ReadModel) ImportDir(ctx context.Context, dir string) (int, error) {
	r.importing.Store(true)
	defer r.importing.Store(false)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	imported := 0
	batch := make([]Entry, 0, 256)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := r.insertBatch(ctx, batch); err != nil {
			return err
		}
		imported += len(batch)
		r.imported.Add(uint64(len(batch)))
		batch = batch[:0]
		return nil
	}
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".log") && !strings.HasSuffix(entry.Name(), ".log.gz")) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		var reader io.Reader = file
		var gz *gzip.Reader
		if strings.HasSuffix(path, ".gz") {
			gz, err = gzip.NewReader(file)
			if err != nil {
				file.Close()
				continue
			}
			reader = gz
		}
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
		for scanner.Scan() {
			var e Entry
			if json.Unmarshal(scanner.Bytes(), &e) == nil && e.Tool != "" {
				batch = append(batch, e)
				if len(batch) == cap(batch) {
					if err := flush(); err != nil {
						file.Close()
						return imported, err
					}
				}
			}
		}
		if gz != nil {
			gz.Close()
		}
		file.Close()
	}
	if err := flush(); err != nil {
		return imported, err
	}
	return imported, nil
}

func (r *ReadModel) Health() ProjectionHealth {
	if r == nil {
		return ProjectionHealth{}
	}
	return ProjectionHealth{QueueDepth: int(r.queued.Load()), Dropped: r.dropped.Load(), Inserted: r.inserted.Load(), Errors: r.errors.Load(), Importing: r.importing.Load(), Imported: r.imported.Load()}
}

func (r *ReadModel) Prune(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, r.bind(`DELETE FROM mcp_audit_events WHERE ts < ?`), olderThan.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *ReadModel) StartRetention(days int) func() {
	if days <= 0 {
		days = 90
	}
	done := make(chan struct{})
	prune := func() { _, _ = r.Prune(context.Background(), time.Now().AddDate(0, 0, -days)) }
	prune()
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				prune()
			}
		}
	}()
	return func() { close(done) }
}

func (r *ReadModel) Events(ctx context.Context, f EventFilter) ([]EventRow, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	selectArgs := "''"
	if f.IncludeArgs {
		selectArgs = "args_json"
	}
	q := `SELECT id,ts,session_id,agent_id,client_name,toolset,tool,collection_name,latency_ms,outcome,result_count,zero_result,trace_id,error_message,` + selectArgs + ` FROM mcp_audit_events WHERE ts>=?`
	args := []any{f.Since.UTC().Format(time.RFC3339Nano)}
	for _, x := range []struct{ column, value string }{{"tool", f.Tool}, {"outcome", f.Outcome}, {"client_name", f.Client}, {"collection_name", f.Collection}} {
		if x.value != "" {
			q += " AND " + x.column + "=?"
			args = append(args, x.value)
		}
	}
	q += " ORDER BY ts DESC LIMIT ? OFFSET ?"
	args = append(args, f.Limit, f.Offset)
	rows, err := r.db.QueryContext(ctx, r.bind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventRow{}
	for rows.Next() {
		var e EventRow
		var zero int
		var raw string
		if rows.Scan(&e.ID, &e.TS, &e.SessionID, &e.AgentID, &e.ClientName, &e.Toolset, &e.Tool, &e.Collection, &e.LatencyMS, &e.Outcome, &e.ResultCount, &zero, &e.TraceID, &e.ErrorMessage, &raw) == nil {
			e.ZeroResult = zero != 0
			if f.IncludeArgs && raw != "" {
				e.Args = json.RawMessage(raw)
			}
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

func (r *ReadModel) Close() {
	if r != nil {
		close(r.q)
		<-r.closed
	}
}

func (r *ReadModel) Summary(ctx context.Context, since time.Time) (Summary, error) {
	out := Summary{ByTool: map[string]int64{}, ByOutcome: map[string]int64{}, Dropped: r.dropped.Load()}
	rows, err := r.db.QueryContext(ctx, r.bind(`SELECT tool,outcome,zero_result,latency_ms FROM mcp_audit_events WHERE ts >= ?`), since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return out, err
	}
	defer rows.Close()
	var latencies []int64
	for rows.Next() {
		var tool, outcome string
		var zero int
		var latency int64
		if rows.Scan(&tool, &outcome, &zero, &latency) != nil {
			continue
		}
		out.Total++
		out.ByTool[tool]++
		out.ByOutcome[outcome]++
		latencies = append(latencies, latency)
		if outcome != string(OutcomeOK) {
			out.Errors++
		}
		if zero != 0 {
			out.ZeroResults++
		}
	}
	if out.Total > 0 {
		out.ErrorRate = float64(out.Errors) / float64(out.Total)
		out.ZeroResultRate = float64(out.ZeroResults) / float64(out.Total)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	out.P50MS = percentile(latencies, .50)
	out.P95MS = percentile(latencies, .95)
	out.P99MS = percentile(latencies, .99)
	return out, rows.Err()
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (r *ReadModel) bind(query string) string {
	if !r.postgres {
		return query
	}
	var b strings.Builder
	n := 1
	for _, ch := range query {
		if ch == '?' {
			fmt.Fprintf(&b, "$%d", n)
			n++
		} else {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

func percentile(values []int64, p float64) int64 {
	if len(values) == 0 {
		return 0
	}
	idx := int(float64(len(values)-1) * p)
	return values[idx]
}
