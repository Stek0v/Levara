// Package observe provides structured JSON logging and error tracking for Cognevra.
//
// Usage:
//
//	logger := observe.NewLogger("handler")
//	logger.Info("request processed", map[string]any{"latency_ms": 12})
//	logger.Error("search failed", err, map[string]any{"collection": "docs"})
//
// ErrorTracker collects recent errors for the /api/v1/errors diagnostic endpoint.
package observe

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"
)

// Level is a structured log severity.
type Level string

const (
	LevelInfo  Level = "INFO"
	LevelWarn  Level = "WARN"
	LevelError Level = "ERROR"
	LevelDebug Level = "DEBUG"
)

// LogEntry is a single structured log record (JSON).
type LogEntry struct {
	Timestamp string         `json:"ts"`
	Level     Level          `json:"level"`
	Message   string         `json:"msg"`
	Component string         `json:"component,omitempty"`
	Error     string         `json:"error,omitempty"`
	Duration  int64          `json:"duration_ms,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
	Caller    string         `json:"caller,omitempty"`
}

// Logger emits structured JSON logs to stderr.
type Logger struct {
	component string
	output    *os.File
	mu        sync.Mutex
	minLevel  Level
}

var levelOrder = map[Level]int{
	LevelDebug: 0,
	LevelInfo:  1,
	LevelWarn:  2,
	LevelError: 3,
}

// NewLogger creates a Logger for the given component name.
// Output goes to stderr by default.
func NewLogger(component string) *Logger {
	minLevel := LevelInfo
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		switch Level(v) {
		case LevelDebug, LevelInfo, LevelWarn, LevelError:
			minLevel = Level(v)
		}
	}
	return &Logger{
		component: component,
		output:    os.Stderr,
		minLevel:  minLevel,
	}
}

func (l *Logger) emit(level Level, msg string, err error, extra ...map[string]any) {
	if levelOrder[level] < levelOrder[l.minLevel] {
		return
	}

	entry := LogEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Level:     level,
		Message:   msg,
		Component: l.component,
	}

	if err != nil {
		entry.Error = err.Error()
	}

	if len(extra) > 0 && extra[0] != nil {
		entry.Extra = extra[0]
	}

	// Capture caller (skip 2: emit -> public method -> caller)
	if _, file, line, ok := runtime.Caller(2); ok {
		entry.Caller = fmt.Sprintf("%s:%d", file, line)
	}

	data, jsonErr := json.Marshal(entry)
	if jsonErr != nil {
		// Fallback: plain text
		fmt.Fprintf(l.output, "[%s] %s: %s\n", level, l.component, msg)
		return
	}

	l.mu.Lock()
	l.output.Write(data)
	l.output.Write([]byte{'\n'})
	l.mu.Unlock()
}

// Info logs an informational message.
func (l *Logger) Info(msg string, extra ...map[string]any) {
	l.emit(LevelInfo, msg, nil, extra...)
}

// Warn logs a warning.
func (l *Logger) Warn(msg string, extra ...map[string]any) {
	l.emit(LevelWarn, msg, nil, extra...)
}

// Error logs an error with optional error value.
func (l *Logger) Error(msg string, err error, extra ...map[string]any) {
	l.emit(LevelError, msg, err, extra...)
}

// Debug logs a debug message (only emitted when LOG_LEVEL=DEBUG).
func (l *Logger) Debug(msg string, extra ...map[string]any) {
	l.emit(LevelDebug, msg, nil, extra...)
}

// ---------------------------------------------------------------------------
// ErrorTracker — ring buffer of recent errors for diagnostic endpoint
// ---------------------------------------------------------------------------

// ErrorRecord is a single tracked error.
type ErrorRecord struct {
	Timestamp string `json:"timestamp"`
	Component string `json:"component"`
	Message   string `json:"message"`
	Stack     string `json:"stack,omitempty"`
	Count     int    `json:"count"`
}

// ErrorTracker collects the most recent errors in a ring buffer.
// Safe for concurrent use.
type ErrorTracker struct {
	mu     sync.RWMutex
	errors []ErrorRecord
	max    int
	// dedup: key = component+message -> index in errors slice
	dedup map[string]int
}

// NewErrorTracker creates a tracker that keeps at most maxErrors entries.
func NewErrorTracker(maxErrors int) *ErrorTracker {
	if maxErrors <= 0 {
		maxErrors = 100
	}
	return &ErrorTracker{
		errors: make([]ErrorRecord, 0, maxErrors),
		max:    maxErrors,
		dedup:  make(map[string]int),
	}
}

// Track records an error. Duplicate component+message pairs increment Count
// instead of creating new entries.
func (et *ErrorTracker) Track(component, message string, err error) {
	et.mu.Lock()
	defer et.mu.Unlock()

	key := component + "|" + message
	if idx, ok := et.dedup[key]; ok && idx < len(et.errors) {
		et.errors[idx].Count++
		et.errors[idx].Timestamp = time.Now().UTC().Format(time.RFC3339)
		return
	}

	// Capture stack trace (skip 2: Track -> caller)
	stack := ""
	buf := make([]byte, 4096)
	n := runtime.Stack(buf, false)
	stack = string(buf[:n])

	rec := ErrorRecord{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Component: component,
		Message:   message,
		Stack:     stack,
		Count:     1,
	}
	if err != nil {
		rec.Message = message + ": " + err.Error()
	}

	if len(et.errors) >= et.max {
		// Evict oldest: shift left
		oldKey := et.errors[0].Component + "|" + et.errors[0].Message
		delete(et.dedup, oldKey)
		et.errors = et.errors[1:]
		// Reindex dedup map
		for k, v := range et.dedup {
			et.dedup[k] = v - 1
		}
	}

	et.dedup[key] = len(et.errors)
	et.errors = append(et.errors, rec)
}

// Recent returns the last `limit` errors (newest first).
func (et *ErrorTracker) Recent(limit int) []ErrorRecord {
	et.mu.RLock()
	defer et.mu.RUnlock()

	n := len(et.errors)
	if limit <= 0 || limit > n {
		limit = n
	}

	result := make([]ErrorRecord, limit)
	for i := range limit {
		result[i] = et.errors[n-1-i]
	}
	return result
}

// Clear removes all tracked errors.
func (et *ErrorTracker) Clear() {
	et.mu.Lock()
	defer et.mu.Unlock()
	et.errors = et.errors[:0]
	et.dedup = make(map[string]int)
}

// Count returns the total number of tracked errors.
func (et *ErrorTracker) Count() int {
	et.mu.RLock()
	defer et.mu.RUnlock()
	return len(et.errors)
}
