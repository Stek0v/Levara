package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentContractEmbeddedAndNonEmpty(t *testing.T) {
	if len(agentContractMD) < 200 {
		t.Fatalf("agent contract markdown looks too short (%d bytes) — embed likely failed", len(agentContractMD))
	}
	if !strings.Contains(agentContractMD, "Levara Agent Contract") {
		t.Errorf("contract missing expected title")
	}
}

func TestAgentContractSHAStable(t *testing.T) {
	a := AgentContractSHA()
	b := AgentContractSHA()
	if a != b {
		t.Errorf("SHA not stable: %s vs %s", a, b)
	}
	if len(a) != 64 {
		t.Errorf("expected 64-char hex SHA, got %d chars", len(a))
	}
}

func TestToolLevaraInstructionsReturnsValidJSON(t *testing.T) {
	res := ToolLevaraInstructions(context.Background(), nil, nil)
	if res.IsError {
		t.Fatalf("unexpected IsError: %+v", res.Content)
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(res.Content))
	}
	var parsed struct {
		Version         string `json:"version"`
		ContentSHA      string `json:"content_sha"`
		ContentMarkdown string `json:"content_markdown"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if parsed.Version == "" || parsed.ContentSHA == "" || parsed.ContentMarkdown == "" {
		t.Errorf("missing required fields: %+v", parsed)
	}
	if parsed.ContentSHA != AgentContractSHA() {
		t.Errorf("returned SHA mismatch")
	}
}
