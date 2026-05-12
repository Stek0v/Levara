package docs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

func TestMarkdownWorkspaceUserScenarios(t *testing.T) {
	raw, err := os.ReadFile("markdown-workspace-user-scenarios.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)

	scenarios := regexp.MustCompile(`(?m)^### S([0-2][0-9]|30)\. `).FindAllStringSubmatch(text, -1)
	if len(scenarios) != 30 {
		t.Fatalf("scenario count=%d, want 30", len(scenarios))
	}
	for i := 1; i <= 30; i++ {
		want := regexp.MustCompile(`(?m)^### S` + regexp.QuoteMeta(fmt.Sprintf("%02d", i)) + `\. `)
		if !want.MatchString(text) {
			t.Fatalf("scenario S%02d missing", i)
		}
	}

	for _, section := range []string{
		"## Solo Workflows",
		"## Team Workflows",
		"Автотесты:",
		"Corner cases:",
	} {
		if !strings.Contains(text, section) {
			t.Fatalf("scenario doc missing %q", section)
		}
	}

	for _, tool := range []string{
		"workspace_context",
		"workspace_search",
		"workspace_read",
		"workspace_write",
		"workspace_commit",
		"workspace_revert",
		"workspace_conflicts",
		"workspace_context_artifacts",
		"workspace_reindex_artifacts",
		"workspace_ops_status",
		"workspace_gc",
	} {
		if !strings.Contains(text, tool) {
			t.Fatalf("scenario doc missing tool reference %q", tool)
		}
	}

	for _, pathRef := range []string{
		"Levara/internal/http/workspace_test.go::TestWorkspaceAPIWriteReadAndReindexUseFilesystemTruth",
		"Levara/internal/http/workspace_eval_test.go::TestWorkspaceRetrievalQualityEval",
		"Levara/cmd/cli/workspace_e2e_test.go::TestWorkspaceCLIFullCycleWriteSearchCommitRevert",
		"Levara/pkg/agenthosts/install_test.go::TestInstallWritesBackupAndPreservesExistingConfig",
		"Levara/internal/store/hnsw_race_test.go::TestHNSW_ReinsertDeletedEntryRefreshesEntryLayer",
	} {
		if !strings.Contains(text, pathRef) {
			t.Fatalf("scenario doc missing test path %q", pathRef)
		}
	}
}

func TestMarkdownWorkspaceCapabilityParity(t *testing.T) {
	raw, err := os.ReadFile("markdown-workspace-capability-parity.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		"| Access preflight | `POST /workspace/access/check` | `not exposed` | `workspace_access_check` | `intentional-gap` |",
		"| Bootstrap context | `GET /workspace/context` | `levara workspace context` | `workspace_context` | `parity` |",
		"| Exact read | `GET /workspace/read` | `levara workspace read` | `workspace_read` | `parity` |",
		"| Indexed write | `POST /workspace/write` | `levara workspace write` | `workspace_write` | `parity` |",
		"| Run start | `POST /workspace/runs/start` | `levara workspace run start` | `workspace_run_start` | `parity` |",
		"| Commit | `POST /workspace/commit` | `levara workspace commit` | `workspace_commit` | `parity` |",
		"| Revert | `POST /workspace/revert` | `levara workspace revert` | `workspace_revert` | `parity` |",
		"| GC / dry-run | `POST /workspace/gc` | `levara workspace gc` | `workspace_gc` | `parity` |",
		"| Search by active generation | `GET /search` plus workspace resolution in server layer | `levara search ...` | `workspace_search` | `functional-parity` |",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("capability parity doc missing row %q", required)
		}
	}

	for _, section := range []string{
		"## Parity Table",
		"## Intentional Gaps",
		"## DoD For Future Parity Work",
	} {
		if !strings.Contains(text, section) {
			t.Fatalf("capability parity doc missing %q", section)
		}
	}
}

func TestMarkdownWorkspaceAnswerContractDoc(t *testing.T) {
	raw, err := os.ReadFile("markdown-workspace-answer-contract.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		"`workspace_search`",
		"`workspace_read`",
		"`answer_contract.required = true`",
		"`answer_contract.read_tool = \"workspace_read\"`",
		"`source_uri`",
		"`stale`",
		"`potentially_stale`",
		"`workspace://<project>/<branch>/<path>#<anchor>`",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("answer contract doc missing %q", required)
		}
	}
}

func TestMarkdownWorkspaceConflictModelDoc(t *testing.T) {
	raw, err := os.ReadFile("markdown-workspace-conflict-model.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		"`filesystem_truth_wins`",
		"`expected_file_digest`",
		"`workspace_conflicts.has_conflicts=true`",
		"`workspace_revert`",
		"`workspace_reconcile`",
		"`dirty_paths`",
		"`unindexed_paths`",
		"`missing_indexed_paths`",
		"`dead_letter`",
		"`failed`",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("conflict model doc missing %q", required)
		}
	}
}
