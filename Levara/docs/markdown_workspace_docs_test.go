package docs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarkdownWorkspaceAgentHostExamples(t *testing.T) {
	for _, name := range []string{"claude-mcp.json", "cursor-mcp.json"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "examples", "agent-hosts", name))
			if err != nil {
				t.Fatal(err)
			}
			var cfg struct {
				MCPServers map[string]struct {
					URL     string            `json:"url"`
					Headers map[string]string `json:"headers"`
				} `json:"mcpServers"`
			}
			if err := json.Unmarshal(raw, &cfg); err != nil {
				t.Fatal(err)
			}
			levara := cfg.MCPServers["levara"]
			if levara.URL != "http://localhost:8080/mcp" {
				t.Fatalf("url=%q, want local Levara MCP endpoint", levara.URL)
			}
			if !strings.HasPrefix(levara.Headers["Authorization"], "Bearer ") {
				t.Fatalf("authorization header missing bearer prefix: %+v", levara.Headers)
			}
		})
	}

	codexRaw, err := os.ReadFile(filepath.Join("..", "examples", "agent-hosts", "codex-config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	codex := string(codexRaw)
	for _, required := range []string{
		"[mcp_servers.levara]",
		`url = "http://localhost:8080/mcp"`,
		"[mcp_servers.levara.headers]",
		`Authorization = "Bearer ${LEVARA_TOKEN}"`,
	} {
		if !strings.Contains(codex, required) {
			t.Fatalf("codex-config.toml missing %q", required)
		}
	}
}

func TestMarkdownWorkspaceAgentInstructionsNameRequiredTools(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "examples", "agent-hosts", "workspace-agent-instructions.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, tool := range []string{
		"workspace_context",
		"workspace_access_check",
		"workspace_search",
		"workspace_read",
		"workspace_context_artifacts",
		"workspace_reindex_artifacts",
		"workspace_conflicts",
		"workspace_write",
		"workspace_commit",
		"workspace_ops_status",
		"workspace_audit_log",
	} {
		if !strings.Contains(text, tool) {
			t.Fatalf("workspace-agent-instructions.md missing %s", tool)
		}
	}
}

func TestMarkdownWorkspaceDeploymentRecipeLinksExist(t *testing.T) {
	raw, err := os.ReadFile("markdown-workspace-deployment-recipes.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, rel := range []string{
		"examples/agent-hosts/claude-mcp.json",
		"examples/agent-hosts/cursor-mcp.json",
		"examples/agent-hosts/codex-config.toml",
		"examples/agent-hosts/workspace-agent-instructions.md",
		"examples/ops/prometheus-alerts.yml",
		"examples/ops/grafana-workspace-dashboard.json",
	} {
		if !strings.Contains(text, rel) {
			t.Fatalf("deployment recipe missing link text %s", rel)
		}
		if _, err := os.Stat(filepath.Join("..", rel)); err != nil {
			t.Fatalf("deployment recipe link %s does not resolve: %v", rel, err)
		}
	}
}

func TestMarkdownWorkspaceOpsExamples(t *testing.T) {
	dashboardRaw, err := os.ReadFile(filepath.Join("..", "examples", "ops", "grafana-workspace-dashboard.json"))
	if err != nil {
		t.Fatal(err)
	}
	var dashboard map[string]any
	if err := json.Unmarshal(dashboardRaw, &dashboard); err != nil {
		t.Fatal(err)
	}
	text := string(dashboardRaw)
	for _, metric := range []string{
		"levara_workspace_index_dead_letters",
		"levara_workspace_index_job_max_lag_seconds",
		"levara_workspace_watcher_pending_branches",
		"levara_workspace_audit_events_total",
	} {
		if !strings.Contains(text, metric) {
			t.Fatalf("grafana dashboard missing %s", metric)
		}
	}
	alertsRaw, err := os.ReadFile(filepath.Join("..", "examples", "ops", "prometheus-alerts.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(alertsRaw), "LevaraWorkspaceDeadLetters") {
		t.Fatal("prometheus alerts missing dead-letter alert")
	}
}
