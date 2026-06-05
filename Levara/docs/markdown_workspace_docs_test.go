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

func TestFullTestingScenariosCoversProductLadder(t *testing.T) {
	raw, err := os.ReadFile("full-testing-scenarios.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		"Personal / Local",
		"Solo Pro",
		"Team",
		"Enterprise",
		"TenantFilterSQL",
		"workspace_context -> workspace_write -> workspace_search",
		"pkg/access",
		"LEVARA_PROFILE=enterprise",
		"Release Gates",
		"Automation Backlog",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("full-testing-scenarios.md missing %q", required)
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
		"| Manifest read | `GET /workspace/manifest` | `levara workspace manifest` | `workspace_manifest` | `parity` |",
		"| Exact read | `GET /workspace/read` | `levara workspace read` | `workspace_read` | `parity` |",
		"| Indexed write | `POST /workspace/write` | `levara workspace write` | `workspace_write` | `parity` |",
		"| Direct index file | `POST /workspace/index` | `levara workspace index` | `workspace_index` | `parity` |",
		"| Delete indexed path | `POST /workspace/delete` | `levara workspace delete` | `workspace_delete` | `parity` |",
		"| Reindex paths | `POST /workspace/reindex` | `levara workspace reindex` | `workspace_reindex_paths` | `parity` |",
		"| Reconcile generation | `POST /workspace/reconcile` | `levara workspace reconcile` | `workspace_reconcile` | `parity` |",
		"| Watch status | `GET /workspace/watch/status` | `levara workspace watch-status` | `workspace_watch_status` | `parity` |",
		"| Run start | `POST /workspace/runs/start` | `levara workspace run start` | `workspace_run_start` | `parity` |",
		"| Run get | `GET /workspace/runs/get` | `levara workspace run get` | `workspace_run_get` | `parity` |",
		"| Commit | `POST /workspace/commit` | `levara workspace commit` | `workspace_commit` | `parity` |",
		"| Log | `GET /workspace/log` | `levara workspace log` | `workspace_log` | `parity` |",
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

func TestMarkdownWorkspaceCapabilityParityMatchesSource(t *testing.T) {
	docRaw, err := os.ReadFile("markdown-workspace-capability-parity.md")
	if err != nil {
		t.Fatal(err)
	}
	docText := string(docRaw)

	workspaceRaw, err := os.ReadFile(filepath.Join("..", "internal", "http", "workspace.go"))
	if err != nil {
		t.Fatal(err)
	}
	workspaceText := string(workspaceRaw)

	mcpRaw, err := os.ReadFile(filepath.Join("..", "internal", "http", "mcp.go"))
	if err != nil {
		t.Fatal(err)
	}
	mcpText := string(mcpRaw)

	cliRaw, err := os.ReadFile(filepath.Join("..", "cmd", "cli", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	cliText := string(cliRaw)

	type parityRow struct {
		name         string
		docRow       string
		restNeedle   string
		cliNeedle    string
		mcpNeedle    string
		cliForbidden string
		mcpForbidden string
	}

	rows := []parityRow{
		{
			name:         "access preflight",
			docRow:       "| Access preflight | `POST /workspace/access/check` | `not exposed` | `workspace_access_check` | `intentional-gap` |",
			restNeedle:   `app.Post("/workspace/access/check", workspaceAccessCheckHandler(cfg))`,
			mcpNeedle:    `case "workspace_access_check":`,
			cliForbidden: `case "access-check":`,
		},
		{
			name:       "bootstrap context",
			docRow:     "| Bootstrap context | `GET /workspace/context` | `levara workspace context` | `workspace_context` | `parity` |",
			restNeedle: `app.Get("/workspace/context", workspaceContextHandler(cfg))`,
			cliNeedle:  `case "context":`,
			mcpNeedle:  `case "workspace_context":`,
		},
		{
			name:         "audit log",
			docRow:       "| Audit log | `GET /workspace/audit` | `not exposed` | `workspace_audit_log` | `intentional-gap` |",
			restNeedle:   `app.Get("/workspace/audit", workspaceAuditLogHandler(cfg))`,
			mcpNeedle:    `case "workspace_audit_log":`,
			cliForbidden: `case "audit":`,
		},
		{
			name:         "context artifacts list",
			docRow:       "| Context artifacts list | `GET /workspace/context/artifacts` | `not exposed` | `workspace_context_artifacts` | `intentional-gap` |",
			restNeedle:   `app.Get("/workspace/context/artifacts", workspaceContextArtifactsHandler(cfg))`,
			mcpNeedle:    `case "workspace_context_artifacts":`,
			cliForbidden: `context-artifacts`,
		},
		{
			name:         "context artifacts reindex",
			docRow:       "| Context artifacts reindex | `POST /workspace/context/artifacts/reindex` | `not exposed` | `workspace_reindex_artifacts` | `intentional-gap` |",
			restNeedle:   `app.Post("/workspace/context/artifacts/reindex", workspaceReindexArtifactsHandler(cfg))`,
			mcpNeedle:    `case "workspace_reindex_artifacts":`,
			cliForbidden: `reindex-artifacts`,
		},
		{
			name:       "ops status",
			docRow:     "| Ops status | `GET /workspace/ops/status` | `levara workspace ops-status` | `workspace_ops_status` | `parity` |",
			restNeedle: `app.Get("/workspace/ops/status", workspaceOpsStatusHandler(cfg))`,
			cliNeedle:  `case "ops-status":`,
			mcpNeedle:  `case "workspace_ops_status":`,
		},
		{
			name:       "conflict report",
			docRow:     "| Conflict report | `GET /workspace/conflicts` | `levara workspace conflicts` | `workspace_conflicts` | `parity` |",
			restNeedle: `app.Get("/workspace/conflicts", workspaceConflictsHandler(cfg))`,
			cliNeedle:  `case "conflicts":`,
			mcpNeedle:  `case "workspace_conflicts":`,
		},
		{
			name:       "manifest read",
			docRow:     "| Manifest read | `GET /workspace/manifest` | `levara workspace manifest` | `workspace_manifest` | `parity` |",
			restNeedle: `app.Get("/workspace/manifest", workspaceManifestHandler(cfg))`,
			cliNeedle:  `case "manifest":`,
			mcpNeedle:  `case "workspace_manifest":`,
		},
		{
			name:       "exact read",
			docRow:     "| Exact read | `GET /workspace/read` | `levara workspace read` | `workspace_read` | `parity` |",
			restNeedle: `app.Get("/workspace/read", workspaceReadHandler(cfg))`,
			cliNeedle:  `case "read":`,
			mcpNeedle:  `case "workspace_read":`,
		},
		{
			name:       "indexed write",
			docRow:     "| Indexed write | `POST /workspace/write` | `levara workspace write` | `workspace_write` | `parity` |",
			restNeedle: `app.Post("/workspace/write", workspaceWriteHandler(cfg))`,
			cliNeedle:  `case "write":`,
			mcpNeedle:  `case "workspace_write":`,
		},
		{
			name:       "direct index file",
			docRow:     "| Direct index file | `POST /workspace/index` | `levara workspace index` | `workspace_index` | `parity` |",
			restNeedle: `app.Post("/workspace/index", workspaceIndexHandler(cfg))`,
			cliNeedle:  `case "index":`,
			mcpNeedle:  `case "workspace_index":`,
		},
		{
			name:       "delete indexed path",
			docRow:     "| Delete indexed path | `POST /workspace/delete` | `levara workspace delete` | `workspace_delete` | `parity` |",
			restNeedle: `app.Post("/workspace/delete", workspaceDeleteHandler(cfg))`,
			cliNeedle:  `case "delete":`,
			mcpNeedle:  `case "workspace_delete":`,
		},
		{
			name:       "reindex paths",
			docRow:     "| Reindex paths | `POST /workspace/reindex` | `levara workspace reindex` | `workspace_reindex_paths` | `parity` |",
			restNeedle: `app.Post("/workspace/reindex", workspaceReindexHandler(cfg))`,
			cliNeedle:  `case "reindex":`,
			mcpNeedle:  `case "workspace_reindex_paths":`,
		},
		{
			name:       "reconcile generation",
			docRow:     "| Reconcile generation | `POST /workspace/reconcile` | `levara workspace reconcile` | `workspace_reconcile` | `parity` |",
			restNeedle: `app.Post("/workspace/reconcile", workspaceReconcileHandler(cfg))`,
			cliNeedle:  `case "reconcile":`,
			mcpNeedle:  `case "workspace_reconcile":`,
		},
		{
			name:       "watch status",
			docRow:     "| Watch status | `GET /workspace/watch/status` | `levara workspace watch-status` | `workspace_watch_status` | `parity` |",
			restNeedle: `app.Get("/workspace/watch/status", workspaceWatchStatusHandler(cfg))`,
			cliNeedle:  `case "watch-status":`,
			mcpNeedle:  `case "workspace_watch_status":`,
		},
		{
			name:       "run start",
			docRow:     "| Run start | `POST /workspace/runs/start` | `levara workspace run start` | `workspace_run_start` | `parity` |",
			restNeedle: `app.Post("/workspace/runs/start", workspaceRunStartHandler(cfg))`,
			cliNeedle:  `case "run":`,
			mcpNeedle:  `case "workspace_run_start":`,
		},
		{
			name:       "run get",
			docRow:     "| Run get | `GET /workspace/runs/get` | `levara workspace run get` | `workspace_run_get` | `parity` |",
			restNeedle: `app.Get("/workspace/runs/get", workspaceRunGetHandler(cfg))`,
			cliNeedle:  `case "run":`,
			mcpNeedle:  `case "workspace_run_get":`,
		},
		{
			name:       "commit",
			docRow:     "| Commit | `POST /workspace/commit` | `levara workspace commit` | `workspace_commit` | `parity` |",
			restNeedle: `app.Post("/workspace/commit", workspaceCommitHandler(cfg))`,
			cliNeedle:  `case "commit":`,
			mcpNeedle:  `case "workspace_commit":`,
		},
		{
			name:       "log",
			docRow:     "| Log | `GET /workspace/log` | `levara workspace log` | `workspace_log` | `parity` |",
			restNeedle: `app.Get("/workspace/log", workspaceLogHandler(cfg))`,
			cliNeedle:  `case "log":`,
			mcpNeedle:  `case "workspace_log":`,
		},
		{
			name:       "revert",
			docRow:     "| Revert | `POST /workspace/revert` | `levara workspace revert` | `workspace_revert` | `parity` |",
			restNeedle: `app.Post("/workspace/revert", workspaceRevertHandler(cfg))`,
			cliNeedle:  `case "revert":`,
			mcpNeedle:  `case "workspace_revert":`,
		},
		{
			name:       "gc dry-run",
			docRow:     "| GC / dry-run | `POST /workspace/gc` | `levara workspace gc` | `workspace_gc` | `parity` |",
			restNeedle: `app.Post("/workspace/gc", workspaceGCHandler(cfg))`,
			cliNeedle:  `case "gc":`,
			mcpNeedle:  `case "workspace_gc":`,
		},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			if !strings.Contains(docText, row.docRow) {
				t.Fatalf("parity doc missing row %q", row.docRow)
			}
			if row.restNeedle != "" && !strings.Contains(workspaceText, row.restNeedle) {
				t.Fatalf("workspace.go missing REST route %q", row.restNeedle)
			}
			if row.cliNeedle != "" && !strings.Contains(cliText, row.cliNeedle) {
				t.Fatalf("cli main.go missing command dispatch %q", row.cliNeedle)
			}
			if row.mcpNeedle != "" && !strings.Contains(mcpText, row.mcpNeedle) {
				t.Fatalf("mcp.go missing tool dispatch %q", row.mcpNeedle)
			}
			if row.cliForbidden != "" && strings.Contains(cliText, row.cliForbidden) {
				t.Fatalf("cli main.go unexpectedly exposes %q", row.cliForbidden)
			}
			if row.mcpForbidden != "" && strings.Contains(mcpText, row.mcpForbidden) {
				t.Fatalf("mcp.go unexpectedly exposes %q", row.mcpForbidden)
			}
		})
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
