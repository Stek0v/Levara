package main

import (
	"encoding/json"
	"runtime"
	"testing"
)

func TestVersionPayload(t *testing.T) {
	GitSHA = "abc1234"
	BuildTime = "2026-04-25T00:00:00Z"
	t.Cleanup(func() {
		GitSHA = "dev"
		BuildTime = "unknown"
	})

	raw, err := json.Marshal(versionPayload())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got struct {
		Version          string `json:"version"`
		BuildTime        string `json:"build_time"`
		GoVersion        string `json:"go_version"`
		ProtocolVersions struct {
			GRPC []string `json:"grpc"`
			MCP  string   `json:"mcp"`
		} `json:"protocol_versions"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Version != "abc1234" {
		t.Errorf("version: got %q want abc1234", got.Version)
	}
	if got.BuildTime != "2026-04-25T00:00:00Z" {
		t.Errorf("build_time: got %q", got.BuildTime)
	}
	if got.GoVersion != runtime.Version() {
		t.Errorf("go_version: got %q want %q", got.GoVersion, runtime.Version())
	}
	if got.ProtocolVersions.MCP != mcpProtocolVersion {
		t.Errorf("mcp: got %q want %q", got.ProtocolVersions.MCP, mcpProtocolVersion)
	}
	if len(got.ProtocolVersions.GRPC) != 2 || got.ProtocolVersions.GRPC[0] != "v1" || got.ProtocolVersions.GRPC[1] != "v2" {
		t.Errorf("grpc: got %v want [v1 v2]", got.ProtocolVersions.GRPC)
	}
}
