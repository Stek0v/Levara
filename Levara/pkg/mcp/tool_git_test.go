package mcp

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stek0v/cognevra/pipeline"
	"github.com/stek0v/cognevra/pkg/orchestrator"
)

// makeGitRepo sets up a minimal git repo with two commits in a
// tempdir. Returns the path. Tests skip on missing `git` binary.
func makeGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	dir := t.TempDir()

	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		// Set an explicit identity so `git commit` doesn't error on
		// boxes without global git config.
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v — %s", args, err, out)
		}
	}

	run("git", "init", "-q")
	run("git", "config", "commit.gpgsign", "false")

	// Two commits so git log has content.
	f1 := filepath.Join(dir, "a.txt")
	writeFile(t, f1, "hello world\n")
	run("git", "add", "a.txt")
	run("git", "commit", "-q", "-m", "initial")

	f2 := filepath.Join(dir, "b.txt")
	writeFile(t, f2, "second change\n")
	run("git", "add", "b.txt")
	run("git", "commit", "-q", "-m", "feat: add b")

	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	cmd := exec.Command("sh", "-c", "cat > "+path)
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("writeFile %s: %v — %s", path, err, out)
	}
}

// ── ToolAnalyzeCommits ──

func TestToolAnalyzeCommits_MissingRepoPath(t *testing.T) {
	res := ToolAnalyzeCommits(context.Background(), &fakeDeps{}, map[string]any{})
	if !res.IsError {
		t.Fatal("want IsError when repo_path missing")
	}
	if !strings.Contains(res.Content[0].Text, "'repo_path' required") {
		t.Errorf("wrong error text: %q", res.Content[0].Text)
	}
}

func TestToolAnalyzeCommits_ParseLogError(t *testing.T) {
	// Point at a directory that isn't a git repo → ParseLog fails.
	res := ToolAnalyzeCommits(context.Background(), &fakeDeps{}, map[string]any{
		"repo_path": t.TempDir(),
	})
	if !res.IsError {
		t.Fatal("want IsError on non-repo path")
	}
	if !strings.Contains(res.Content[0].Text, "Error parsing git log") {
		t.Errorf("wrong error text: %q", res.Content[0].Text)
	}
}

func TestToolAnalyzeCommits_TextOnlyWhenNoEmbed(t *testing.T) {
	repo := makeGitRepo(t)
	// baseCfg.EmbedEndpoint empty → text-only branch.
	deps := &fakeDeps{baseCfg: orchestrator.Config{}}
	res := ToolAnalyzeCommits(context.Background(), deps, map[string]any{
		"repo_path": repo,
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "Analyzed") || !strings.Contains(text, "text only") {
		t.Errorf("text-only response wrong: %q", text)
	}
	// Initial commit message should be present.
	if !strings.Contains(text, "initial") {
		t.Errorf("preview missing commit subject; text=%q", text)
	}
}

func TestToolAnalyzeCommits_CognifyBranchStartsPipeline(t *testing.T) {
	repo := makeGitRepo(t)
	// Embed configured → cognify path. Use heartbeatFn to sync with
	// the goroutine (last call in the goroutine body).
	done := make(chan struct{})
	var heartbeatEvent string
	var heartbeatPayload map[string]any
	deps := &fakeDeps{
		baseCfg: orchestrator.Config{EmbedEndpoint: "http://embed"},
	}
	deps.heartbeatFn = func(eventType string, payload any) {
		heartbeatEvent = eventType
		if p, ok := payload.(map[string]any); ok {
			heartbeatPayload = p
		}
		close(done)
	}

	res := ToolAnalyzeCommits(context.Background(), deps, map[string]any{
		"repo_path": repo,
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "Cognify pipeline started") {
		t.Errorf("wrong message: %q", text)
	}
	// Extract run_id from the text and verify registry has it.
	marker := "run_id: "
	idx := strings.Index(text, marker)
	if idx < 0 {
		t.Fatalf("no run_id marker in %q", text)
	}
	rest := text[idx+len(marker):]
	end := strings.IndexAny(rest, ").")
	if end < 0 {
		t.Fatalf("can't parse run_id from %q", text)
	}
	runID := rest[:end]

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("heartbeat never fired; pipeline goroutine stuck")
	}

	if heartbeatEvent != "analyze_commits" {
		t.Errorf("heartbeat event=%q, want analyze_commits", heartbeatEvent)
	}
	if heartbeatPayload["status"] != "COMPLETED" {
		t.Errorf("heartbeat status=%v, want COMPLETED", heartbeatPayload["status"])
	}
	if heartbeatPayload["run_id"] != runID {
		t.Errorf("heartbeat run_id=%v, want %q", heartbeatPayload["run_id"], runID)
	}

	// Registry should hold the same terminal status (close(done)
	// happens-after status.Status write).
	s, ok := deps.Runs().Load(runID)
	if !ok {
		t.Fatal("runID missing from registry")
	}
	if s.Status != "COMPLETED" {
		t.Errorf("registry Status=%q, want COMPLETED", s.Status)
	}
}

func TestToolAnalyzeCommits_CognifyPipelineConfigOverrides(t *testing.T) {
	// Verify the pre-refactor-parity overrides and per-call settings
	// reach the pipeline. We install a pipelineFn to capture the
	// config the tool actually hands off.
	repo := makeGitRepo(t)

	var captured orchestrator.Config
	done := make(chan struct{})
	deps := &fakeDeps{
		baseCfg: orchestrator.Config{EmbedEndpoint: "http://embed"},
	}
	deps.pipelineFn = func(ctx context.Context, texts []string, cfg orchestrator.Config, progress chan<- orchestrator.Progress) error {
		captured = cfg
		close(progress)
		return nil
	}
	deps.heartbeatFn = func(string, any) { close(done) }

	ToolAnalyzeCommits(context.Background(), deps, map[string]any{"repo_path": repo})
	<-done

	if captured.LLMProvider != nil {
		t.Errorf("LLMProvider should be nil, got %v", captured.LLMProvider)
	}
	if captured.BM25Indexes != nil {
		t.Errorf("BM25Indexes should be nil, got %v", captured.BM25Indexes)
	}
	if captured.Collection != analyzeCommitsCollection {
		t.Errorf("Collection=%q, want %q", captured.Collection, analyzeCommitsCollection)
	}
	if !captured.GenerateTriplets {
		t.Error("GenerateTriplets should be true")
	}
	if captured.DatasetID == "" {
		t.Error("DatasetID should be populated")
	}
}

func TestToolAnalyzeCommits_LimitRespected(t *testing.T) {
	repo := makeGitRepo(t)
	deps := &fakeDeps{}
	res := ToolAnalyzeCommits(context.Background(), deps, map[string]any{
		"repo_path": repo,
		"limit":     float64(1), // only 1 commit → should report "1"
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "Analyzed 1 commits") {
		t.Errorf("limit not respected; text=%q", res.Content[0].Text)
	}
}

// ── ToolGitSearch ──

func TestToolGitSearch_MissingQuery(t *testing.T) {
	res := ToolGitSearch(context.Background(), &fakeDeps{}, map[string]any{})
	if !res.IsError {
		t.Fatal("want IsError when query missing")
	}
	if !strings.Contains(res.Content[0].Text, "'query' required") {
		t.Errorf("wrong error text: %q", res.Content[0].Text)
	}
}

func TestToolGitSearch_EmbedNotConfigured(t *testing.T) {
	deps := &fakeDeps{}
	res := ToolGitSearch(context.Background(), deps, map[string]any{"query": "q"})
	if res.IsError {
		t.Errorf("unexpected IsError: %q", res.Content[0].Text)
	}
	if res.Content[0].Text != "No results (embedding service not configured)" {
		t.Errorf("wrong text: %q", res.Content[0].Text)
	}
}

func TestToolGitSearch_TargetsGitCommitsCollection(t *testing.T) {
	var gotColl string
	var gotTopK int
	deps := &fakeDeps{
		searchPipelineFn: func(doRerank bool) SearchPipeline {
			return &fakeSearchPipeline{
				byText: func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error) {
					gotColl = coll
					gotTopK = topK
					return []pipeline.ScoredResult{
						{ID: "c1", Score: 0.9, Metadata: json.RawMessage(`{"msg":"fix"}`)},
					}, nil
				},
			}
		},
	}
	res := ToolGitSearch(context.Background(), deps, map[string]any{"query": "q"})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	if gotColl != analyzeCommitsCollection {
		t.Errorf("collection=%q, want %q", gotColl, analyzeCommitsCollection)
	}
	if gotTopK != gitSearchTopK {
		t.Errorf("topK=%d, want %d", gotTopK, gitSearchTopK)
	}
	// Response should be a JSON array of hit objects.
	var hits []map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &hits); err != nil {
		t.Fatalf("response not JSON: %s", res.Content[0].Text)
	}
	if len(hits) != 1 || hits[0]["id"] != "c1" {
		t.Errorf("hits wrong: %+v", hits)
	}
}

func TestToolGitSearch_NoResultsText(t *testing.T) {
	deps := &fakeDeps{
		searchPipelineFn: func(bool) SearchPipeline {
			return &fakeSearchPipeline{
				byText: func(context.Context, string, string, int) ([]pipeline.ScoredResult, error) {
					return nil, nil
				},
			}
		},
	}
	res := ToolGitSearch(context.Background(), deps, map[string]any{"query": "q"})
	if res.IsError {
		t.Errorf("unexpected IsError: %q", res.Content[0].Text)
	}
	if res.Content[0].Text != "No matching commits found." {
		t.Errorf("wrong text: %q", res.Content[0].Text)
	}
}

func TestToolGitSearch_PipelineErrorSurfaces(t *testing.T) {
	deps := &fakeDeps{
		searchPipelineFn: func(bool) SearchPipeline {
			return &fakeSearchPipeline{
				byText: func(context.Context, string, string, int) ([]pipeline.ScoredResult, error) {
					return nil, errSearch("boom")
				},
			}
		},
	}
	res := ToolGitSearch(context.Background(), deps, map[string]any{"query": "q"})
	if !res.IsError {
		t.Fatal("want IsError on SearchByText error")
	}
	if !strings.Contains(res.Content[0].Text, "Search error: boom") {
		t.Errorf("wrong error text: %q", res.Content[0].Text)
	}
}

// errSearch implements error for the pipeline-error test without
// pulling in a dependency.
type errSearch string

func (e errSearch) Error() string { return string(e) }
