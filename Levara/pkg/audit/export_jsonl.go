package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// errSinkClosed is returned by WriteEvent after Close. As a delivery error it
// lets the AsyncExporter's retry loop treat a shutdown race as transient
// without panicking on a closed file.
var errSinkClosed = errors.New("audit: event sink closed")

// EventFileSink writes sanitized audit.Event values as JSON lines to a daily
// file under dir, named audit-YYYY-MM-DD.jsonl. It mirrors FileLogger's
// rotation/retention discipline (gzip the prior day on rotation, prune files
// older than retentionDays) but for the generic Event stream and with a
// synchronous, error-returning WriteEvent so an AsyncExporter can retry
// transient write failures. The active file is never pruned.
type EventFileSink struct {
	dir           string
	retentionDays int

	mu          sync.Mutex
	currentDay  string
	currentFile *os.File
	enc         *json.Encoder
	closed      bool
}

// NewEventFileSink opens (or creates) a daily-rolling JSONL event log under
// dir. retentionDays <= 0 defaults to 30.
func NewEventFileSink(dir string, retentionDays int) (*EventFileSink, error) {
	if retentionDays <= 0 {
		retentionDays = 30
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("audit: mkdir %s: %w", dir, err)
	}
	s := &EventFileSink{dir: dir, retentionDays: retentionDays}
	if err := s.rotate(time.Now().UTC()); err != nil {
		return nil, err
	}
	return s, nil
}

// WriteEvent sanitizes and appends one event as a JSON line, rotating first if
// the UTC day changed. It returns an error on a real write failure so the
// exporter can retry; after Close it returns errSinkClosed.
func (s *EventFileSink) WriteEvent(e Event) error {
	if s == nil {
		return errSinkClosed
	}
	clean := SanitizeEvent(e)
	if clean.TS == "" {
		clean.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	now := time.Now().UTC()
	day := now.Format("2006-01-02")

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errSinkClosed
	}
	if day != s.currentDay {
		if err := s.rotateLocked(now); err != nil {
			return err
		}
	}
	if s.enc == nil {
		return errSinkClosed
	}
	return s.enc.Encode(clean)
}

// LogEvent satisfies EventSink by swallowing the error — for direct use as a
// best-effort sink. Prefer WriteEvent (via NewJSONLExporter) when retries and
// backpressure are wanted.
func (s *EventFileSink) LogEvent(e Event) {
	_ = s.WriteEvent(e)
}

// Close flushes and closes the current file. Subsequent writes return
// errSinkClosed.
func (s *EventFileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.currentFile == nil {
		return nil
	}
	err := s.currentFile.Close()
	s.currentFile = nil
	s.enc = nil
	return err
}

func (s *EventFileSink) rotate(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rotateLocked(now)
}

func (s *EventFileSink) rotateLocked(now time.Time) error {
	if s.currentFile != nil {
		prevPath := s.currentFile.Name()
		_ = s.currentFile.Close()
		s.currentFile = nil
		s.enc = nil
		if err := gzipFile(prevPath); err != nil {
			fmt.Fprintf(os.Stderr, "audit: gzip %s failed: %v\n", prevPath, err)
		}
	}
	day := now.Format("2006-01-02")
	path := filepath.Join(s.dir, "audit-"+day+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("audit: open %s: %w", path, err)
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	s.currentFile = f
	s.currentDay = day
	s.enc = enc
	s.pruneOldLocked(now)
	return nil
}

// pruneOldLocked removes audit-* files whose mod time predates the retention
// cutoff. The freshly-opened active file (audit-<today>.jsonl) carries a
// current mod time, so it is never a prune candidate; we also skip it by name
// as belt-and-suspenders.
func (s *EventFileSink) pruneOldLocked(now time.Time) {
	cutoff := now.AddDate(0, 0, -s.retentionDays)
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	activeName := ""
	if s.currentFile != nil {
		activeName = filepath.Base(s.currentFile.Name())
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
		if !strings.HasPrefix(name, "audit-") {
			continue
		}
		if name == activeName {
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
		_ = os.Remove(filepath.Join(s.dir, o.name))
	}
}

// NewJSONLExporter wires an EventFileSink behind an AsyncExporter: events are
// sanitized, buffered, and delivered to the daily JSONL file with retry and
// drop-on-overflow backpressure. Close drains the buffer then closes the file.
// This is the first concrete enterprise audit adapter; a SIEM adapter would
// follow the same shape (a WriteEvent-style deliverFunc behind AsyncExporter).
func NewJSONLExporter(dir string, retentionDays int, cfg ExportConfig) (*AsyncExporter, error) {
	sink, err := NewEventFileSink(dir, retentionDays)
	if err != nil {
		return nil, err
	}
	cfg.OnClose = sink.Close
	return NewAsyncExporter(sink.WriteEvent, cfg), nil
}
