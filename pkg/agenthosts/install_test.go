package agenthosts

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMergeMCPJSONPreservesExistingServersAndFields(t *testing.T) {
	existing := []byte(`{
  "mcpServers": {
    "other": {"url": "http://other/mcp"},
    "levara": {
      "url": "http://old/mcp",
      "headers": {"X-Existing": "keep"},
      "disabled": false
    }
  },
  "unrelated": true
}`)
	merged, err := Merge(HostClaude, existing, "http://localhost:8081/mcp", "MY_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(merged, &root); err != nil {
		t.Fatal(err)
	}
	if root["unrelated"] != true {
		t.Fatalf("unrelated field not preserved: %s", merged)
	}
	servers := root["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Fatalf("existing server missing after merge: %s", merged)
	}
	levara := servers["levara"].(map[string]any)
	if levara["url"] != "http://localhost:8081/mcp" || levara["disabled"] != false {
		t.Fatalf("levara server merge wrong: %+v", levara)
	}
	headers := levara["headers"].(map[string]any)
	if headers["X-Existing"] != "keep" || headers["Authorization"] != "Bearer ${MY_TOKEN}" {
		t.Fatalf("headers merge wrong: %+v", headers)
	}
}

func TestMergeMCPJSONRejectsInvalidExistingJSON(t *testing.T) {
	if _, err := Merge(HostCursor, []byte("{bad-json"), "", ""); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}

func TestMergeCodexTOMLPreservesOtherSectionsAndReplacesLevara(t *testing.T) {
	existing := []byte(`[profile.default]
model = "gpt-5.5"

[mcp_servers.other]
url = "http://other/mcp"

[mcp_servers.levara]
url = "http://old/mcp"
stale = true

[mcp_servers.levara.headers]
Authorization = "Bearer old"

[tools]
enabled = true
`)
	merged, err := Merge(HostCodex, existing, "http://localhost:8081/mcp", "MY_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	text := string(merged)
	for _, want := range []string{
		"[profile.default]",
		`model = "gpt-5.5"`,
		"[mcp_servers.other]",
		`url = "http://other/mcp"`,
		"[tools]",
		`enabled = true`,
		"[mcp_servers.levara]",
		`url = "http://localhost:8081/mcp"`,
		"[mcp_servers.levara.headers]",
		`Authorization = "Bearer ${MY_TOKEN}"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("merged TOML missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "stale = true") || strings.Contains(text, "Bearer old") {
		t.Fatalf("old Levara section was not replaced:\n%s", text)
	}
}

func TestInstallWritesBackupAndPreservesExistingConfig(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	existing := []byte(`{"mcpServers":{"other":{"url":"http://other/mcp"}}}` + "\n")
	if err := os.WriteFile(target, existing, 0644); err != nil {
		t.Fatal(err)
	}

	result, err := Install(InstallOptions{
		Host:      HostCursor,
		Target:    target,
		ServerURL: "http://localhost:8081/mcp",
		TokenEnv:  "MY_TOKEN",
		Now:       time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.BackupPath == "" {
		t.Fatalf("result=%+v, want changed with backup", result)
	}
	backup, err := os.ReadFile(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(backup, existing) {
		t.Fatalf("backup=%s, want original=%s", backup, existing)
	}
	updated, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(updated, []byte(`"levara"`)) || !bytes.Contains(updated, []byte(`Bearer ${MY_TOKEN}`)) {
		t.Fatalf("updated config missing Levara server: %s", updated)
	}
}

func TestInstallDryRunDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, ".mcp.json")
	result, err := Install(InstallOptions{
		Host:   HostClaude,
		Target: target,
		DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || !result.Changed || len(result.Content) == 0 {
		t.Fatalf("dry-run result=%+v", result)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote target, stat err=%v", err)
	}
}

func TestParseHost(t *testing.T) {
	if got, err := ParseHost("Claude"); err != nil || got != HostClaude {
		t.Fatalf("ParseHost Claude = %q, %v", got, err)
	}
	if _, err := ParseHost("unknown"); err == nil {
		t.Fatal("expected unsupported host error")
	}
}
