package http

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/runreg"
)

func TestHTTPBackedToolOutputsMatchRegisteredSchemas_RoundTrip(t *testing.T) {
	byName := make(map[string]mcp.Tool)
	for _, tool := range mcp.ToolDescriptors() {
		byName[tool.Name] = tool
	}

	h, db := observabilityTestHandler(t)
	now := time.Now().UTC()
	h.cfg.Runs.Store("r-fail", &runreg.Status{
		RunID: "r-fail", Status: "FAILED", Stage: "extract",
		Message: "boom", StartedAt: now,
	})
	if _, err := db.Exec(`INSERT INTO heartbeats(id,event_type,payload,created_at) VALUES(?,?,?,?)`,
		"hb-doctor", "doctor", `{"status":"fail","checks":[{"name":"postgres","status":"fail","message":"down"}]}`, now.Format(time.RFC3339)); err != nil {
		t.Fatalf("seed doctor heartbeat: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO heartbeats(id,event_type,payload,created_at) VALUES(?,?,?,?)`,
		"hb-sync", "sync", `{"direction":"pull","remote":"http://remote","types":["memories"]}`, now.Format(time.RFC3339)); err != nil {
		t.Fatalf("seed sync heartbeat: %v", err)
	}

	recH, _, _ := reconcileTestHandler(t)

	cases := []struct {
		name string
		res  mcpToolResult
	}{
		{"doctor", h.toolDoctor(context.Background(), map[string]any{})},
		{"heartbeat", h.toolHeartbeat(context.Background(), map[string]any{})},
		{"runtime_stats", h.toolRuntimeStats(context.Background(), map[string]any{})},
		{"ingestion_status", h.toolIngestionStatus(context.Background(), map[string]any{})},
		{"recent_errors", h.toolRecentErrors(context.Background(), map[string]any{})},
		{"sync_status", h.toolSyncStatus(context.Background(), map[string]any{})},
		{"reconcile_memory", recH.toolReconcileMemory(context.Background(), map[string]any{})},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.res.IsError {
				t.Fatalf("tool returned error: %s", tc.res.Content[0].Text)
			}
			if err := validateMCPOutputSchema(byName[tc.name].OutputSchema, tc.res.Content[0].Text, "$"); err != nil {
				t.Fatalf("%s output does not match schema: %v\n%s", tc.name, err, tc.res.Content[0].Text)
			}
		})
	}
}

func validateMCPOutputSchema(schema map[string]any, raw string, path string) error {
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return fmt.Errorf("%s: non-object JSON: %w", path, err)
	}
	return validateMCPValue(schema, payload, path)
}

func validateMCPValue(schema map[string]any, value any, path string) error {
	if schema == nil {
		return nil
	}
	typ, _ := schema["type"].(string)
	if typ == "" {
		return nil
	}
	switch typ {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: type %T, want object", path, value)
		}
		props, _ := schema["properties"].(map[string]any)
		for key := range obj {
			if len(props) > 0 {
				if _, ok := props[key]; !ok {
					return fmt.Errorf("%s.%s: key not declared in schema", path, key)
				}
			}
		}
		for key, prop := range props {
			v, ok := obj[key]
			if !ok || v == nil {
				continue
			}
			child, ok := prop.(map[string]any)
			if !ok {
				continue
			}
			if err := validateMCPValue(child, v, path+"."+key); err != nil {
				return err
			}
		}
	case "array":
		arr, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s: type %T, want array", path, value)
		}
		itemSchema, _ := schema["items"].(map[string]any)
		for i, item := range arr {
			if err := validateMCPValue(itemSchema, item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s: type %T, want string", path, value)
		}
	case "integer":
		n, ok := value.(float64)
		if !ok || math.Trunc(n) != n {
			return fmt.Errorf("%s: value %v (%T), want integer", path, value, value)
		}
	case "number":
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("%s: type %T, want number", path, value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s: type %T, want boolean", path, value)
		}
	default:
		return fmt.Errorf("%s: unsupported schema type %q", path, typ)
	}
	return nil
}
