package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// ── ToolSync ──

func TestToolSync_MissingRemoteURL(t *testing.T) {
	res := ToolSync(context.Background(), &fakeDeps{}, map[string]any{})
	if !res.IsError {
		t.Fatal("want IsError when remote_url missing")
	}
	if !strings.Contains(res.Content[0].Text, "'remote_url' required") {
		t.Errorf("wrong error text: %q", res.Content[0].Text)
	}
}

func TestToolSync_NilDB(t *testing.T) {
	res := ToolSync(context.Background(), &fakeDeps{}, map[string]any{
		"remote_url": "http://remote:8080/api/v1",
	})
	if !res.IsError {
		t.Fatal("want IsError when DB is nil")
	}
	if !strings.Contains(res.Content[0].Text, "database not configured") {
		t.Errorf("wrong error text: %q", res.Content[0].Text)
	}
}

func TestToolSync_ManifestFetchError(t *testing.T) {
	deps := setupCodifyDB(t) // any DB with tables is fine for this test
	deps.doSyncFn = func(ctx context.Context, remoteURL, direction string, types []string, since string, collections []string) (map[string]any, map[string]any, error) {
		return nil, nil, errors.New("connection refused")
	}
	res := ToolSync(context.Background(), deps, map[string]any{
		"remote_url": "http://remote:8080/api/v1",
	})
	if !res.IsError {
		t.Fatal("want IsError when manifest fetch fails")
	}
	if !strings.Contains(res.Content[0].Text, "connection refused") {
		t.Errorf("wrong error text: %q", res.Content[0].Text)
	}
}

func TestToolSync_PullDefault(t *testing.T) {
	deps := setupCodifyDB(t)
	var gotDirection string
	deps.doSyncFn = func(ctx context.Context, remoteURL, direction string, types []string, since string, collections []string) (map[string]any, map[string]any, error) {
		gotDirection = direction
		return map[string]any{"memories": "ok"}, map[string]any{"embed_model": "nomic"}, nil
	}

	res := ToolSync(context.Background(), deps, map[string]any{
		"remote_url": "http://remote:8080/api/v1",
		// direction omitted → default "pull"
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	if gotDirection != "pull" {
		t.Errorf("default direction=%q, want pull", gotDirection)
	}
	// Response should be JSON with remote_manifest key.
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Fatalf("response not JSON: %s", res.Content[0].Text)
	}
	if _, ok := out["remote_manifest"]; !ok {
		t.Error("remote_manifest not in response")
	}
}

func TestToolSync_PushDirection(t *testing.T) {
	deps := setupCodifyDB(t)
	var gotDirection string
	deps.doSyncFn = func(ctx context.Context, remoteURL, direction string, types []string, since string, collections []string) (map[string]any, map[string]any, error) {
		gotDirection = direction
		return map[string]any{}, map[string]any{}, nil
	}

	res := ToolSync(context.Background(), deps, map[string]any{
		"remote_url": "http://remote:8080/api/v1",
		"direction":  "push",
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	if gotDirection != "push" {
		t.Errorf("direction=%q, want push", gotDirection)
	}
}

func TestToolSync_TypesForwarded(t *testing.T) {
	deps := setupCodifyDB(t)
	var gotTypes []string
	deps.doSyncFn = func(ctx context.Context, remoteURL, direction string, types []string, since string, collections []string) (map[string]any, map[string]any, error) {
		gotTypes = types
		return map[string]any{}, map[string]any{}, nil
	}

	ToolSync(context.Background(), deps, map[string]any{
		"remote_url": "http://remote:8080/api/v1",
		"types":      []any{"memories", "graph"},
	})
	if len(gotTypes) != 2 || gotTypes[0] != "memories" || gotTypes[1] != "graph" {
		t.Errorf("types=%v, want [memories graph]", gotTypes)
	}
}

func TestToolSync_HeartbeatFired(t *testing.T) {
	deps := setupCodifyDB(t)
	deps.doSyncFn = func(ctx context.Context, remoteURL, direction string, types []string, since string, collections []string) (map[string]any, map[string]any, error) {
		return map[string]any{}, map[string]any{}, nil
	}
	fired := false
	deps.heartbeatFn = func(eventType string, payload any) {
		if eventType == "sync" {
			fired = true
		}
	}

	ToolSync(context.Background(), deps, map[string]any{
		"remote_url": "http://remote:8080/api/v1",
	})
	if !fired {
		t.Error("heartbeat 'sync' not fired")
	}
}

func TestToolSync_CollectionsForwarded(t *testing.T) {
	deps := setupCodifyDB(t)
	var gotCollections []string
	deps.doSyncFn = func(ctx context.Context, remoteURL, direction string, types []string, since string, collections []string) (map[string]any, map[string]any, error) {
		gotCollections = collections
		return map[string]any{}, map[string]any{}, nil
	}

	ToolSync(context.Background(), deps, map[string]any{
		"remote_url":  "http://remote:8080/api/v1",
		"collections": []any{"coll1", "coll2"},
	})
	if len(gotCollections) != 2 {
		t.Errorf("collections=%v, want [coll1 coll2]", gotCollections)
	}
}
