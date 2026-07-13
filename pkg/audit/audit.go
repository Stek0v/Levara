// Package audit emits one JSON line per MCP tool call so we have a
// permanent record of what every agent asked for, with what arguments,
// and how it ended.
//
// The Logger writes to an io.Writer (typically stderr, or a daily-rolled
// gzipped file). Args are sanitized before serialization: secrets are
// dropped, embedding vectors are collapsed to a short descriptor, and
// every string is capped at maxFieldChars to keep lines bounded.
package audit

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxFieldChars = 256
const maxSanitizeDepth = 6

// Outcome classifies how a tool call ended. The set is closed; new
// values should be added to outcomeValues for metric label allow-listing.
type Outcome string

const (
	OutcomeOK           Outcome = "ok"
	OutcomeClientError  Outcome = "client_error"
	OutcomeServerError  Outcome = "server_error"
	OutcomeTimeout      Outcome = "timeout"
	OutcomeUnauthorized Outcome = "unauthorized"
	OutcomeRateLimited  Outcome = "rate_limited"
)

// AllOutcomes returns every defined outcome. Useful for metric init or
// allow-list checks.
func AllOutcomes() []Outcome {
	return []Outcome{
		OutcomeOK, OutcomeClientError, OutcomeServerError,
		OutcomeTimeout, OutcomeUnauthorized, OutcomeRateLimited,
	}
}

// Entry is the on-wire schema. Field order is stable; new fields must
// be appended so log consumers can rely on positional readers if needed.
type Entry struct {
	TS            string         `json:"ts"`
	RequestID     string         `json:"request_id,omitempty"`
	SessionID     string         `json:"session_id,omitempty"`
	AgentID       string         `json:"agent_id,omitempty"`
	Tool          string         `json:"tool"`
	Args          map[string]any `json:"args,omitempty"`
	LatencyMS     int64          `json:"latency_ms"`
	Outcome       Outcome        `json:"outcome"`
	ResultSize    int            `json:"result_size,omitempty"`
	ErrorCode     string         `json:"error_code,omitempty"`
	ErrorMessage  string         `json:"error_message,omitempty"`
	ClientName    string         `json:"client_name,omitempty"`
	ClientVersion string         `json:"client_version,omitempty"`
	Toolset       string         `json:"toolset,omitempty"`
	Collection    string         `json:"collection,omitempty"`
	ResultCount   int            `json:"result_count,omitempty"`
	ZeroResult    bool           `json:"zero_result,omitempty"`
	RequestBytes  int            `json:"request_bytes,omitempty"`
	ResponseBytes int            `json:"response_bytes,omitempty"`
	TraceID       string         `json:"trace_id,omitempty"`
}

// Sink is the abstract write-side of an audit log. Both Logger (plain
// io.Writer) and FileLogger (daily-rolled file tree) satisfy it.
type Sink interface {
	Log(Entry)
}

// Event is a generic audit event for non-MCP surfaces such as workspace,
// profile validation, and future enterprise export sinks.
type Event struct {
	TS       string         `json:"ts"`
	Source   string         `json:"source"`
	Type     string         `json:"type"`
	Subject  string         `json:"subject,omitempty"`
	ActorID  string         `json:"actor_id,omitempty"`
	Outcome  string         `json:"outcome,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// EventSink is the generic audit-export boundary. Implementations may write
// JSONL, forward to a SIEM/log pipeline, or fan out to multiple destinations.
type EventSink interface {
	LogEvent(Event)
}

type EventSinkFunc func(Event)

func (f EventSinkFunc) LogEvent(e Event) {
	if f != nil {
		f(e)
	}
}

// Logger serializes Entry values to JSON lines on the configured writer.
// All exported methods are safe for concurrent use.
type Logger struct {
	mu  sync.Mutex
	w   io.Writer
	enc *json.Encoder
}

// NewLogger returns a Logger that writes to w. If w is nil, entries are
// emitted to os.Stderr — useful as a no-config default.
func NewLogger(w io.Writer) *Logger {
	if w == nil {
		w = os.Stderr
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &Logger{w: w, enc: enc}
}

// Log writes one Entry as a JSON line. Errors writing the line are
// swallowed — audit must never break a request.
func (l *Logger) Log(e Entry) {
	if l == nil {
		return
	}
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.enc.Encode(e)
}

// SanitizeArgs returns a copy of args with secrets dropped, vectors
// collapsed, and long strings truncated. The input map is not modified.
// Only top-level keys are inspected — nested maps are JSON-marshaled
// and truncated as strings, which is enough for current MCP arg shapes
// where nesting is rare.
func SanitizeArgs(args map[string]any) map[string]any {
	if len(args) == 0 {
		return nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		if sanitized, ok := sanitizeValue(k, v, 0); ok {
			out[k] = sanitized
		}
	}
	return out
}

func sanitizeValue(key string, value any, depth int) (any, bool) {
	if isSecretKey(key) {
		return nil, false
	}
	if isVectorKey(key) {
		return vectorDescriptor(value), true
	}
	if depth >= maxSanitizeDepth {
		return "<max-depth>", true
	}
	switch v := value.(type) {
	case string:
		return truncate(v), true
	case map[string]any:
		out := make(map[string]any, len(v))
		for nestedKey, nestedValue := range v {
			if sanitized, ok := sanitizeValue(nestedKey, nestedValue, depth+1); ok {
				out[nestedKey] = sanitized
			}
		}
		return out, true
	case []any:
		out := make([]any, 0, len(v))
		for _, nestedValue := range v {
			if sanitized, ok := sanitizeValue("", nestedValue, depth+1); ok {
				out = append(out, sanitized)
			}
		}
		return out, true
	default:
		return v, true
	}
}

func isSecretKey(k string) bool {
	low := strings.ToLower(k)
	if low == "password" || low == "secret" || low == "api_key" || low == "authorization" || low == "cookie" || low == "set-cookie" {
		return true
	}
	return strings.HasSuffix(low, "_token") || low == "token"
}

func isVectorKey(k string) bool {
	low := strings.ToLower(k)
	return low == "vector" || low == "embedding" || low == "embeddings"
}

func vectorDescriptor(v any) string {
	switch vv := v.(type) {
	case []float64:
		return fmt.Sprintf("<vector len=%d norm=%.4f>", len(vv), normFloat64(vv))
	case []float32:
		f := make([]float64, len(vv))
		for i, x := range vv {
			f[i] = float64(x)
		}
		return fmt.Sprintf("<vector len=%d norm=%.4f>", len(vv), normFloat64(f))
	case []any:
		nums := make([]float64, 0, len(vv))
		for _, x := range vv {
			if n, ok := x.(float64); ok {
				nums = append(nums, n)
			}
		}
		return fmt.Sprintf("<vector len=%d norm=%.4f>", len(vv), normFloat64(nums))
	default:
		return "<vector>"
	}
}

func normFloat64(v []float64) float64 {
	var s float64
	for _, x := range v {
		s += x * x
	}
	return math.Sqrt(s)
}

func truncate(s string) string {
	if len(s) <= maxFieldChars {
		return s
	}
	return s[:maxFieldChars] + "…"
}

// ── File logger with daily rotation ─────────────────────────────────

// FileLogger writes JSON lines to a daily file under dir, named
// mcp-YYYY-MM-DD.log. On UTC date change the current file is closed,
// gzipped to .log.gz, and a new file is opened. Files older than
// retentionDays are pruned at rotation time.
type FileLogger struct {
	dir           string
	retentionDays int
	mu            sync.Mutex
	currentDay    string
	currentFile   *os.File
	currentBuffer *bufio.Writer
	currentLogger *Logger
	bufferedLines int
	lastFlush     time.Time
}

// NewFileLogger opens (or creates) a daily-rolling audit log under dir.
// If dir cannot be created the error is returned and no logger is built.
func NewFileLogger(dir string, retentionDays int) (*FileLogger, error) {
	if retentionDays <= 0 {
		retentionDays = 30
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("audit: mkdir %s: %w", dir, err)
	}
	fl := &FileLogger{dir: dir, retentionDays: retentionDays}
	if err := fl.rotate(time.Now().UTC()); err != nil {
		return nil, err
	}
	return fl, nil
}

// Log writes the entry to the current file, rotating first if the UTC
// date has changed.
func (fl *FileLogger) Log(e Entry) {
	if fl == nil {
		return
	}
	now := time.Now().UTC()
	day := now.Format("2006-01-02")
	fl.mu.Lock()
	defer fl.mu.Unlock()
	if day != fl.currentDay {
		_ = fl.rotateLocked(now)
	}
	if fl.currentLogger != nil {
		fl.currentLogger.Log(e)
		fl.bufferedLines++
		// Amortize filesystem writes while keeping the unflushed crash window
		// bounded to a small number of events.
		if fl.currentBuffer != nil && (fl.bufferedLines >= 256 || now.Sub(fl.lastFlush) >= time.Second) {
			_ = fl.currentBuffer.Flush()
			fl.bufferedLines = 0
			fl.lastFlush = now
		}
	}
}

// Close flushes and closes the current file. Subsequent Log calls
// become no-ops.
func (fl *FileLogger) Close() error {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	if fl.currentFile == nil {
		return nil
	}
	if fl.currentBuffer != nil {
		_ = fl.currentBuffer.Flush()
	}
	err := fl.currentFile.Close()
	fl.currentFile = nil
	fl.currentBuffer = nil
	fl.currentLogger = nil
	return err
}

func (fl *FileLogger) rotate(now time.Time) error {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	return fl.rotateLocked(now)
}

func (fl *FileLogger) rotateLocked(now time.Time) error {
	if fl.currentFile != nil {
		prevPath := fl.currentFile.Name()
		if fl.currentBuffer != nil {
			_ = fl.currentBuffer.Flush()
		}
		_ = fl.currentFile.Close()
		fl.currentFile = nil
		fl.currentBuffer = nil
		fl.currentLogger = nil
		if err := gzipFile(prevPath); err != nil {
			fmt.Fprintf(os.Stderr, "audit: gzip %s failed: %v\n", prevPath, err)
		}
	}
	day := now.Format("2006-01-02")
	path := filepath.Join(fl.dir, "mcp-"+day+".log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("audit: open %s: %w", path, err)
	}
	fl.currentFile = f
	fl.currentDay = day
	fl.currentBuffer = bufio.NewWriterSize(f, 256*1024)
	fl.currentLogger = NewLogger(fl.currentBuffer)
	fl.bufferedLines = 0
	fl.lastFlush = now
	fl.pruneOldLocked(now)
	return nil
}

func (fl *FileLogger) pruneOldLocked(now time.Time) {
	cutoff := now.AddDate(0, 0, -fl.retentionDays)
	entries, err := os.ReadDir(fl.dir)
	if err != nil {
		return
	}
	type aged struct {
		name string
		mod  time.Time
	}
	var olds []aged
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "mcp-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			olds = append(olds, aged{name: name, mod: info.ModTime()})
		}
	}
	sort.Slice(olds, func(i, j int) bool { return olds[i].mod.Before(olds[j].mod) })
	for _, o := range olds {
		_ = os.Remove(filepath.Join(fl.dir, o.name))
	}
}

func gzipFile(path string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	outPath := path + ".gz"
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(out)
	if _, err := io.Copy(gz, in); err != nil {
		_ = gz.Close()
		_ = out.Close()
		_ = os.Remove(outPath)
		return err
	}
	if err := gz.Close(); err != nil {
		_ = out.Close()
		_ = os.Remove(outPath)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(path)
}
