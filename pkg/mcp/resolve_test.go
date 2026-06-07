package mcp

import "testing"

// ResolveCollection decides the collection scope for a tool call. The
// priority order (arg > session default > forWrite fallback) is part of the
// MCP contract — clients rely on set_context to change the session default
// without re-specifying collection on every call.

func TestResolveCollection_ExplicitArgWins(t *testing.T) {
	sess := &Session{DefaultCollection: "session-default"}
	got := ResolveCollection(sess, map[string]any{"collection": "explicit"}, false)
	if got != "explicit" {
		t.Errorf("got %q, want explicit (arg should beat session default)", got)
	}
}

func TestResolveCollection_SessionDefault(t *testing.T) {
	sess := &Session{DefaultCollection: "from-session"}
	// No "collection" arg → session default used.
	if got := ResolveCollection(sess, map[string]any{}, false); got != "from-session" {
		t.Errorf("got %q, want from-session", got)
	}
}

func TestResolveCollection_WriteFallbackDefault(t *testing.T) {
	// No arg, no session default, forWrite=true → "default".
	if got := ResolveCollection(nil, nil, true); got != "default" {
		t.Errorf("got %q, want 'default' (write fallback)", got)
	}
	if got := ResolveCollection(&Session{}, map[string]any{}, true); got != "default" {
		t.Errorf("got %q, want 'default' (empty-session write fallback)", got)
	}
}

func TestResolveCollection_ReadFallbackEmpty(t *testing.T) {
	// No arg, no session default, forWrite=false → "" meaning "all
	// collections". Downstream search code treats empty as unscoped.
	if got := ResolveCollection(nil, nil, false); got != "" {
		t.Errorf("got %q, want empty (read fallback)", got)
	}
}

func TestResolveCollection_NilSessionOK(t *testing.T) {
	// Anonymous MCP client (no session yet): ResolveCollection must not
	// dereference nil session. Returns arg if present, else fallback.
	got := ResolveCollection(nil, map[string]any{"collection": "foo"}, false)
	if got != "foo" {
		t.Errorf("got %q, want foo", got)
	}
}

func TestResolveCollection_EmptyStringArgIsIgnored(t *testing.T) {
	// "collection": "" in args is treated as unset, falling through to
	// session default. Guards against clients that always include the field.
	sess := &Session{DefaultCollection: "kept"}
	got := ResolveCollection(sess, map[string]any{"collection": ""}, false)
	if got != "kept" {
		t.Errorf("got %q, want kept (empty arg should not override session default)", got)
	}
}

func TestResolveCollection_WrongTypeIgnored(t *testing.T) {
	// Type assertion failure (non-string value for "collection") falls
	// through to session default — MCP clients sending bad arg types
	// shouldn't silently hit an unintended collection.
	sess := &Session{DefaultCollection: "kept"}
	got := ResolveCollection(sess, map[string]any{"collection": 42}, false)
	if got != "kept" {
		t.Errorf("got %q, want kept (int arg should be ignored)", got)
	}
}
