package mcp

import (
	"fmt"
	"sync"
	"time"
)

// Session tracks one connected MCP client. Fields are exported so the HTTP
// handler in internal/http (and the future pkg/mcp handler) can read/write
// them directly — Session is a plain value object, not a hidden state
// machine. Concurrent mutation is the caller's responsibility; SessionStore
// below handles concurrent map access.
type Session struct {
	ID                string
	UserID            string // from Authorization header (JWT); empty for anonymous
	CreatedAt         time.Time
	SSECh             chan []byte // buffered channel for server-initiated SSE messages
	DefaultCollection string      // set via the set_context tool
	closeSSEOnce      sync.Once
}

// CloseSSE closes the server-initiated SSE channel at most once. It also
// tolerates a channel that was closed by older/external code paths so idle
// cleanup cannot take down the server with "close of closed channel".
func (s *Session) CloseSSE() {
	if s == nil || s.SSECh == nil {
		return
	}
	s.closeSSEOnce.Do(func() {
		defer func() {
			_ = recover()
		}()
		close(s.SSECh)
	})
}

// ResolveCollection picks the collection for a tool call. Priority:
//  1. explicit "collection" argument on the call
//  2. session default (set via set_context)
//  3. "default" for writes; empty string for reads (which means "all")
//
// Returning empty for reads is intentional: downstream search code treats it
// as "no filter" and scans every collection, which is the right fallback for
// an unscoped MCP client that hasn't called set_context yet.
func ResolveCollection(sess *Session, args map[string]any, forWrite bool) string {
	if coll, _ := args["collection"].(string); coll != "" {
		return coll
	}
	if sess != nil && sess.DefaultCollection != "" {
		return sess.DefaultCollection
	}
	if forWrite {
		return "default"
	}
	return ""
}

// SessionStore is the thread-safe registry of active MCP sessions. Create /
// Get / Delete are all O(1) under a single RWMutex. CleanupIdle sweeps stale
// entries older than the given maxAge and returns how many were evicted —
// external callers use that for metrics bookkeeping (we intentionally don't
// depend on the metrics package inside this package).
//
// An optional OnCountChange hook fires after every operation that modifies
// the store size, passing the new count. Handlers plug their own Prometheus
// gauge update here without SessionStore knowing about the metrics package.
type SessionStore struct {
	mu            sync.RWMutex
	sessions      map[string]*Session
	OnCountChange func(n int)
}

// NewSessionStore returns an empty store. OnCountChange may be set directly
// on the returned value; default nil means "no hook".
func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: make(map[string]*Session)}
}

// Get returns the session with the given ID, or nil if absent. Empty ID is
// treated as "no session" rather than triggering a zero-value lookup.
func (s *SessionStore) Get(id string) *Session {
	if id == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[id]
}

// Create registers a new session with a freshly-generated ID. The buffered
// SSECh has capacity 100 to absorb short bursts of notifications without
// blocking the producer. The returned Session is already visible to Get.
func (s *SessionStore) Create() *Session {
	id := fmt.Sprintf("mcp-%d-%s", time.Now().UnixNano(), RandomHex(8))
	sess := &Session{
		ID:        id,
		CreatedAt: time.Now(),
		SSECh:     make(chan []byte, 100),
	}
	s.mu.Lock()
	s.sessions[id] = sess
	count := len(s.sessions)
	s.mu.Unlock()
	s.notifyCount(count)
	return sess
}

// Adopt returns the session for id, creating one under that exact id if it
// doesn't already exist. Unlike Create (which mints a fresh server-side id),
// Adopt honors a client-supplied id. It exists to transparently re-establish
// a session after the in-memory store was reset — e.g. a backend restart wipes
// every session, then a client replays its old Mcp-Session-Id. Without Adopt
// that replay hits an unknown id and the handler 404s, which the Claude Code
// MCP client surfaces as a hang mid tool-call instead of re-initializing. The
// adopted session starts with an empty UserID; the caller rebinds the owner
// from the request's JWT. Idempotent and thread-safe; an empty id falls back
// to Create.
func (s *SessionStore) Adopt(id string) *Session {
	if id == "" {
		return s.Create()
	}
	s.mu.Lock()
	if existing, ok := s.sessions[id]; ok {
		s.mu.Unlock()
		return existing
	}
	sess := &Session{
		ID:        id,
		CreatedAt: time.Now(),
		SSECh:     make(chan []byte, 100),
	}
	s.sessions[id] = sess
	count := len(s.sessions)
	s.mu.Unlock()
	s.notifyCount(count)
	return sess
}

// Delete removes the session and closes its SSECh. Calling on an absent ID
// is a no-op (idempotent — lets callers retry cleanly on network hiccups).
// OnCountChange fires only when the store size actually changed.
func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if ok {
		sess.CloseSSE()
		delete(s.sessions, id)
	}
	count := len(s.sessions)
	s.mu.Unlock()
	if ok {
		s.notifyCount(count)
	}
}

// Count returns the current number of active sessions.
func (s *SessionStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}

// CleanupIdle removes sessions older than maxAge. Returns the number evicted
// so the caller can log / alert on large sweeps. Sessions' SSECh are closed
// as part of eviction — any goroutine blocked on a send will unblock with
// the zero value on the receive side.
func (s *SessionStore) CleanupIdle(maxAge time.Duration) int {
	now := time.Now()
	evicted := 0
	s.mu.Lock()
	for id, sess := range s.sessions {
		if now.Sub(sess.CreatedAt) > maxAge {
			sess.CloseSSE()
			delete(s.sessions, id)
			evicted++
		}
	}
	count := len(s.sessions)
	s.mu.Unlock()
	if evicted > 0 {
		s.notifyCount(count)
	}
	return evicted
}

func (s *SessionStore) notifyCount(n int) {
	if s.OnCountChange != nil {
		s.OnCountChange(n)
	}
}
