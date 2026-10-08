package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stek0v/levara/pkg/extract"
	"github.com/stek0v/levara/pkg/orchestrator"
)

type codifyClaimFailure struct{ *fakeDeps }

func (d codifyClaimFailure) ClaimPipelineAttempt(context.Context, string, string, string, string, int64, string) error {
	return errors.New("claim failed")
}

func TestCodifyStaticResolvedEndpoints(t *testing.T) {
	a := extract.CodeAnalysis{
		Entities: []extract.CodeEntity{
			{Name: "Thing", Type: "class", Line: 1},
			{Name: "Run", Type: "method", Parent: "A", Line: 2},
			{Name: "Run", Type: "method", Parent: "B", Line: 3},
			{Name: "Only", Type: "function", Line: 4},
		},
		Relations: []extract.CodeRelation{
			{Source: "quoted \"file.go", Target: "fmt", Relationship: "IMPORTS"},
			{Source: "A.Run", Target: "Only", Relationship: "CALLS"},
			{Source: "Run", Target: "remote.Call", Relationship: "CALLS"},
		},
	}
	g := codifyStaticGraph(a, "quoted \"file.go")
	nodes := map[string]string{}
	for _, n := range g.Nodes {
		if !strings.Contains(n.Name, "quoted \"file.go") {
			t.Fatalf("lost file identity: %+v", n)
		}
		if _, exists := nodes[n.ID]; exists {
			t.Fatalf("duplicate node ID")
		}
		nodes[n.ID] = n.Name
	}
	for _, e := range g.Edges {
		if nodes[e.SourceID] == "" || nodes[e.TargetID] == "" {
			t.Fatalf("dangling edge: %+v", e)
		}
	}
	if !strings.Contains(nodes[g.Edges[2].SourceID], "::reference:Run") {
		t.Fatal("ambiguous method picked a declaration")
	}
	if nodes[g.Edges[1].SourceID] != "quoted \"file.go::A.Run" {
		t.Fatal("qualified method failed to resolve")
	}
	other := codifyStaticGraph(a, "other.go")
	if other.Nodes[1].ID == g.Nodes[1].ID {
		t.Fatal("files share declaration identity")
	}
}

func TestCodifyStaticSynchronousSourcePipeline(t *testing.T) {
	deps := setupCodifyDB(t)
	prepared, ran := false, false
	deps.prepareFn = func(ctx context.Context, texts []string, cfg orchestrator.Config) (orchestrator.Config, error) {
		prepared = true
		if cfg.DatasetID == "" || cfg.DocumentID != "" {
			t.Fatal("new source missing server fallback dataset")
		}
		if texts[0] != "package main\nfunc Hello() {}\n" || cfg.DocumentTitle != "main.go" || cfg.Collection != "explicit" {
			t.Fatal("wrong immutable source config")
		}
		cfg.DatasetID, cfg.DocumentID, cfg.SourceRevision, cfg.RawContentHash = "dataset", "document", 4, strings.Repeat("a", 64)
		return cfg, nil
	}
	deps.pipelineFn = func(ctx context.Context, texts []string, cfg orchestrator.Config, updates chan<- orchestrator.Progress) error {
		defer close(updates)
		if !prepared || cfg.StaticGraph == nil || cfg.AttemptID == "" || cfg.SourceRevision != 4 || cfg.DocumentID != "document" || cfg.SkipGraph {
			t.Error("lost prepared source/static config")
			return errors.New("invalid static config")
		}
		// Exceed the channel buffer: the synchronous caller must drain progress.
		for i := 0; i < 100; i++ {
			updates <- orchestrator.Progress{Stage: "writing"}
		}
		ran = true
		return nil
	}
	r := ToolCodify(context.Background(), deps, map[string]any{"code": "package main\nfunc Hello() {}\n", "filename": "main.go", "collection": "explicit"})
	if r.IsError || !ran {
		t.Fatalf("returned before native pipeline completed: %+v", r)
	}
}

func TestCodifyStaticFailures(t *testing.T) {
	for _, stage := range []string{"prepare", "claim", "pipeline", "canceled"} {
		t.Run(stage, func(t *testing.T) {
			deps := setupCodifyDB(t)
			calls := 0
			deps.pipelineFn = func(ctx context.Context, texts []string, cfg orchestrator.Config, p chan<- orchestrator.Progress) error {
				defer close(p)
				calls++
				return errors.New("configured SQL/embed failure")
			}
			var actual Deps = deps
			ctx := context.Background()
			switch stage {
			case "prepare":
				deps.prepareFn = func(context.Context, []string, orchestrator.Config) (orchestrator.Config, error) {
					return orchestrator.Config{}, errors.New("source SQL failure")
				}
			case "claim":
				actual = codifyClaimFailure{deps}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			r := ToolCodify(ctx, actual, map[string]any{"code": "package main\nfunc Hello() {}", "filename": "main.go"})
			if !r.IsError {
				t.Fatal("failed processing reported success")
			}
			if stage != "pipeline" && calls != 0 {
				t.Fatal("pipeline ran after failed admission")
			}
		})
	}
}

func TestCodifyStaticInvalidSourceHasNoEffects(t *testing.T) {
	for _, tc := range []struct{ filename, code string }{
		{"main.js", "function hello(){}"}, {"main.ts", "class A {}"},
		{"main.rb", "puts 'hi'"}, {"main.go", "package main\nfunc ("},
	} {
		t.Run(tc.filename, func(t *testing.T) {
			deps := setupCodifyDB(t)
			calls := 0
			deps.prepareFn = func(context.Context, []string, orchestrator.Config) (orchestrator.Config, error) {
				calls++
				return orchestrator.Config{}, nil
			}
			r := ToolCodify(context.Background(), deps, map[string]any{"code": tc.code, "filename": tc.filename})
			if !r.IsError || calls != 0 {
				t.Fatal("invalid source admitted to persistence")
			}
		})
	}
}

func TestCodifyStaticSupportedEmptyAndPythonNilDB(t *testing.T) {
	for _, tc := range []struct{ filename, code string }{
		{"main.GO", "package main\n"}, {"app.PY", "# heuristic-only source\n"},
		{"app.py", "class A:\n    def run(self):\n        external.call()\n"},
	} {
		t.Run(tc.filename, func(t *testing.T) {
			deps := &fakeDeps{}
			deps.prepareFn = func(context.Context, []string, orchestrator.Config) (orchestrator.Config, error) {
				t.Fatal("nil DB performed source ingestion")
				return orchestrator.Config{}, nil
			}
			r := ToolCodify(context.Background(), deps, map[string]any{"code": tc.code, "filename": tc.filename})
			if r.IsError {
				t.Fatalf("supported analysis failed: %+v", r)
			}
		})
	}
}
