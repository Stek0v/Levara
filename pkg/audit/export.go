// Audit export boundary. Where audit.go records MCP tool calls to a local
// JSON-line log, this file defines the *adapter* seam an enterprise deployment
// plugs a SIEM/log pipeline into: a generic Exporter that accepts sanitized
// audit.Event values, never blocks the calling request, retries transient
// delivery failures with bounded backoff, and sheds load by dropping (with a
// counter) rather than growing unbounded. The first concrete adapter is the
// local JSONL sink in export_jsonl.go.
//
// Two invariants hold regardless of adapter:
//   - LogEvent must never block the caller. Delivery happens on a background
//     worker; a full buffer drops the event and increments Dropped.
//   - Events are sanitized at the boundary (SanitizeEvent) as defense in depth,
//     so a careless upstream caller still cannot export markdown bodies,
//     private file paths, raw snippets, secrets, or raw tokens.
package audit

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// maxMetaValueChars bounds an exported metadata string. Anything longer is
// treated as a potential content/snippet leak and redacted rather than
// truncated — truncation could still emit the first 120 chars of a private
// document, redaction cannot.
const maxMetaValueChars = 120

// ExportStats is a point-in-time snapshot of an exporter's lifetime counters.
// All fields are monotonic. Delivered+Dropped+Failed need not equal Enqueued
// at any instant because in-flight events sit between the channel and the sink.
type ExportStats struct {
	Enqueued  uint64 `json:"enqueued"`  // accepted into the buffer
	Delivered uint64 `json:"delivered"` // written to the sink (eventually) successfully
	Dropped   uint64 `json:"dropped"`   // shed because the buffer was full (backpressure)
	Retried   uint64 `json:"retried"`   // individual delivery attempts that failed and were retried
	Failed    uint64 `json:"failed"`    // events abandoned after exhausting retries
}

// Exporter is the audit-export adapter contract. It is an EventSink (so it
// drops into the existing WorkspaceAuditSink slot without handler changes),
// plus lifecycle and observability: Stats for monitoring, Close for graceful
// drain on shutdown.
type Exporter interface {
	EventSink
	Stats() ExportStats
	Close() error
}

// ExportConfig tunes an AsyncExporter. The zero value is usable — withDefaults
// fills sane bounds — so callers can pass ExportConfig{} and override only what
// they care about.
type ExportConfig struct {
	// BufferSize bounds the in-memory queue. When full, LogEvent drops rather
	// than blocks. Default 1024.
	BufferSize int
	// MaxRetries is the number of *additional* delivery attempts after the
	// first failure. Default 3 (so up to 4 attempts total).
	MaxRetries int
	// RetryBackoff is the initial sleep between retries; it doubles each retry,
	// capped at retryBackoffCap. Default 50ms.
	RetryBackoff time.Duration
	// OnDrop, if set, is called for each event shed due to a full buffer. It
	// runs on the caller's goroutine and must not block.
	OnDrop func(Event)
	// OnClose, if set, runs after the worker has drained on Close — used to
	// close the underlying sink (e.g. the JSONL file).
	OnClose func() error
}

const retryBackoffCap = 2 * time.Second

func (c ExportConfig) withDefaults() ExportConfig {
	if c.BufferSize <= 0 {
		c.BufferSize = 1024
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	} else if c.MaxRetries == 0 {
		c.MaxRetries = 3
	}
	if c.RetryBackoff <= 0 {
		c.RetryBackoff = 50 * time.Millisecond
	}
	return c
}

// deliverFunc writes one already-sanitized event to the underlying sink,
// returning an error on transient failure so the exporter can retry. A nil
// return means delivered.
type deliverFunc func(Event) error

// AsyncExporter is a non-blocking, bounded, retrying EventSink. It owns a
// background worker that pulls events off a buffered channel and hands each to
// a deliverFunc with bounded retry. Concurrent LogEvent calls are safe; Close
// is safe to call once and makes subsequent LogEvent calls no-ops.
type AsyncExporter struct {
	cfg     ExportConfig
	deliver deliverFunc

	ch chan Event
	wg sync.WaitGroup

	// mu guards closed and gates send-vs-close on ch: LogEvent holds RLock
	// while doing its non-blocking send, Close holds Lock before close(ch), so
	// a send can never race the close into a send-on-closed-channel panic.
	mu     sync.RWMutex
	closed bool

	enqueued  atomic.Uint64
	delivered atomic.Uint64
	dropped   atomic.Uint64
	retried   atomic.Uint64
	failed    atomic.Uint64
}

// NewAsyncExporter starts an exporter that delivers via deliver. The worker
// runs until Close. deliver must be safe to call from a single goroutine; it is
// never called concurrently with itself.
func NewAsyncExporter(deliver deliverFunc, cfg ExportConfig) *AsyncExporter {
	cfg = cfg.withDefaults()
	e := &AsyncExporter{
		cfg:     cfg,
		deliver: deliver,
		ch:      make(chan Event, cfg.BufferSize),
	}
	e.wg.Add(1)
	go e.run()
	return e
}

// LogEvent sanitizes and enqueues e for background delivery. It never blocks:
// if the buffer is full the event is dropped and Dropped is incremented. After
// Close it is a no-op. Safe for concurrent use.
func (e *AsyncExporter) LogEvent(ev Event) {
	if e == nil {
		return
	}
	clean := SanitizeEvent(ev)
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return
	}
	select {
	case e.ch <- clean:
		e.enqueued.Add(1)
	default:
		e.dropped.Add(1)
		if e.cfg.OnDrop != nil {
			e.cfg.OnDrop(clean)
		}
	}
}

func (e *AsyncExporter) run() {
	defer e.wg.Done()
	for ev := range e.ch {
		e.deliverWithRetry(ev)
	}
}

// deliverWithRetry attempts delivery up to 1+MaxRetries times with doubling
// backoff (capped). Events are pre-sanitized on enqueue, so this only owns
// transport. A final failure increments Failed and drops the event — delivery
// must never wedge the worker forever.
func (e *AsyncExporter) deliverWithRetry(ev Event) {
	backoff := e.cfg.RetryBackoff
	for attempt := 0; ; attempt++ {
		if err := e.deliver(ev); err == nil {
			e.delivered.Add(1)
			return
		}
		if attempt >= e.cfg.MaxRetries {
			e.failed.Add(1)
			return
		}
		e.retried.Add(1)
		time.Sleep(backoff)
		if backoff *= 2; backoff > retryBackoffCap {
			backoff = retryBackoffCap
		}
	}
}

// Stats returns a snapshot of lifetime counters.
func (e *AsyncExporter) Stats() ExportStats {
	return ExportStats{
		Enqueued:  e.enqueued.Load(),
		Delivered: e.delivered.Load(),
		Dropped:   e.dropped.Load(),
		Retried:   e.retried.Load(),
		Failed:    e.failed.Load(),
	}
}

// Close stops accepting events, drains the buffer through the worker, then runs
// OnClose (e.g. to close the file sink). Idempotent. Safe to call once after
// which LogEvent is a no-op.
func (e *AsyncExporter) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	close(e.ch)
	e.mu.Unlock()

	e.wg.Wait()
	if e.cfg.OnClose != nil {
		return e.cfg.OnClose()
	}
	return nil
}

// SanitizeEvent returns a copy of e safe to export off-box: secret-keyed
// metadata is dropped, vectors are collapsed to a descriptor, and any string
// value that looks like content (long, multiline, a file path, or markdown) is
// replaced with "<redacted>". Controlled top-level fields are truncated. This
// is defense in depth — workspace events are already narrow and sanitized at
// their source — so an export adapter can never become a content exfiltration
// path even if a future caller mirrors a careless event.
func SanitizeEvent(e Event) Event {
	out := e
	out.Source = truncate(e.Source)
	out.Type = truncate(e.Type)
	out.Subject = truncate(e.Subject)
	out.ActorID = truncate(e.ActorID)
	out.Outcome = truncate(e.Outcome)
	if len(e.Metadata) > 0 {
		md := make(map[string]any, len(e.Metadata))
		for k, v := range e.Metadata {
			if sv, keep := sanitizeMetaValue(k, v); keep {
				md[k] = sv
			}
		}
		out.Metadata = md
	}
	return out
}

func sanitizeMetaValue(k string, v any) (any, bool) {
	if isSecretKey(k) {
		return nil, false
	}
	if isVectorKey(k) {
		return vectorDescriptor(v), true
	}
	switch vv := v.(type) {
	case string:
		if looksUnsafeMetaValue(vv) {
			return "<redacted>", true
		}
		return vv, true
	case map[string]any, []any:
		b, err := json.Marshal(vv)
		if err != nil {
			return "<unmarshalable>", true
		}
		if s := string(b); looksUnsafeMetaValue(s) {
			return "<redacted>", true
		} else {
			return s, true
		}
	default:
		// Numbers, bools, nil — safe scalars, no content to leak.
		return v, true
	}
}

// looksUnsafeMetaValue reports whether s could carry sensitive content that
// must not leave the box via the audit channel: anything long, multiline, that
// embeds a filesystem path, or that carries markdown structure (fenced code,
// bold, links, headings) reads as document/snippet content rather than a short
// scalar tag.
func looksUnsafeMetaValue(s string) bool {
	if len(s) > maxMetaValueChars {
		return true
	}
	if strings.ContainsAny(s, "\n\r") {
		return true
	}
	if strings.ContainsAny(s, "/\\") {
		return true
	}
	for _, marker := range []string{"```", "**", "](", "# "} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}
