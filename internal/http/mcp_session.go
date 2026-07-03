package http

import (
	"time"
)

// getOrValidateSession retrieves an existing session by ID, or returns nil.
func (h *mcpHandler) getOrValidateSession(sessionID string) *mcpSession {
	return h.sessions.Get(sessionID)
}

// createSession creates a new MCP session and returns its ID.
func (h *mcpHandler) createSession(userID string) string {
	sess := h.sessions.Create()
	sess.UserID = userID
	return sess.ID
}

// adoptSession re-establishes a session under a client-supplied id (e.g. one
// replayed after a backend restart wiped the in-memory store), binding its
// owner to userID when known. Returns the session — existing or freshly
// adopted. See SessionStore.Adopt for the lifecycle rationale.
func (h *mcpHandler) adoptSession(sessionID, userID string) *mcpSession {
	sess := h.sessions.Adopt(sessionID)
	if userID != "" {
		sess.UserID = userID
	}
	return sess
}

// deleteSession removes a session.
func (h *mcpHandler) deleteSession(id string) {
	h.sessions.Delete(id)
}

// sessionCleanupLoop periodically cleans idle sessions and updates data metrics.
func (h *mcpHandler) sessionCleanupLoop() {
	h.updateDataMetrics()
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		h.sessions.CleanupIdle(time.Hour)
		h.updateDataMetrics()
	}
}
