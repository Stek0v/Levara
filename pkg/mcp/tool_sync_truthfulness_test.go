package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestToolSyncTruthfulHeartbeatPreservesPartialResult(t *testing.T) {
	for _, status := range []string{"partial", "error"} {
		t.Run(status, func(t *testing.T) {
			deps := setupCodifyDB(t)
			deps.doSyncFn = func(context.Context, string, string, []string, string, []string) (map[string]any, map[string]any, error) {
				if status == "error" {
					return nil, nil, errors.New("connection refused")
				}
				return map[string]any{"status": "partial", "memories": map[string]int{"imported": 1}, "graph_error": "remote HTTP status 503"}, map[string]any{}, nil
			}
			var heartbeat map[string]any
			deps.heartbeatFn = func(event string, payload any) {
				if event == "sync" {
					heartbeat = payload.(map[string]any)
				}
			}
			result := ToolSync(context.Background(), deps, map[string]any{"remote_url": "http://offline.test"})
			if heartbeat["status"] != status {
				t.Errorf("heartbeat=%v", heartbeat)
			}
			if status == "error" {
				if !result.IsError {
					t.Error("preflight failure lost IsError")
				}
				return
			}
			if result.IsError {
				t.Fatal("partial success lost compatible JSON result")
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
				t.Fatal(err)
			}
			if payload["memories"] == nil || payload["graph_error"] == nil || payload["status"] != "partial" {
				t.Errorf("partial details missing: %v", payload)
			}
		})
	}
}
