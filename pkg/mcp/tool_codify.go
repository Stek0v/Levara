package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/extract"
	"github.com/stek0v/levara/pkg/graph"
	"github.com/stek0v/levara/pkg/orchestrator"
)

// ToolCodify performs checked static analysis and synchronously publishes through
// the existing immutable-source pipeline. Without SQL it remains analysis-only.
func ToolCodify(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	code, _ := args["code"].(string)
	filename, _ := args["filename"].(string)
	if code == "" || filename == "" {
		return toolError("Error: 'code' and 'filename' required")
	}
	analysis, err := extract.AnalyzeCodeChecked(code, filename)
	if err != nil {
		return toolError(err.Error())
	}
	if err := ctx.Err(); err != nil {
		return toolError("code publication canceled")
	}
	if deps.DB() != nil {
		cfg := deps.BaseCognifyConfig()
		collection, _ := args["collection"].(string)
		if strings.TrimSpace(collection) == "" {
			collection = "code_knowledge"
		}
		cfg.Collection, cfg.DocumentTitle = collection, filename
		cfg.DatasetID, cfg.DocumentID = uuid.NewString(), ""
		cfg.StaticGraph = codifyStaticGraph(analysis, filename)
		cfg.SkipGraph, cfg.GenerateTriplets, cfg.ParentChild = false, false, false
		cfg.MinChunkChars = 1
		cfg, err = deps.PrepareCognify(ctx, []string{code}, cfg)
		if err != nil {
			return toolError("code source unavailable")
		}
		cfg.AttemptID = uuid.NewString()
		if err := deps.ClaimPipelineAttempt(ctx, cfg.DatasetID, cfg.DocumentID, cfg.Collection, cfg.AttemptID, cfg.SourceRevision, cfg.RawContentHash); err != nil {
			return toolError("code publication unavailable")
		}
		updates := make(chan orchestrator.Progress, 16)
		done := make(chan error, 1)
		go func() { done <- deps.RunPipeline(ctx, []string{code}, cfg, updates) }()
		var last orchestrator.Progress
		for update := range updates {
			last = update
		}
		runErr := <-done
		if runErr == nil {
			runErr = ctx.Err()
		}
		if runErr != nil {
			_ = deps.PersistPipelineStatus(cfg.DatasetID, cfg.DocumentID, cfg.Collection, "FAILED", cfg.SourceRevision, cfg.RawContentHash,
				last.ChunksCreated, last.EntitiesExtracted, last.EdgesExtracted, last.ElapsedMs, cfg.AttemptID)
			return toolError("code publication failed")
		}
		if !deps.PipelineFinalizesStatus() {
			if err := deps.PersistPipelineStatus(cfg.DatasetID, cfg.DocumentID, cfg.Collection, "COMPLETED", cfg.SourceRevision, cfg.RawContentHash,
				last.ChunksCreated, last.EntitiesExtracted, last.EdgesExtracted, last.ElapsedMs, cfg.AttemptID); err != nil {
				return toolError("code publication status unavailable")
			}
		}
	}
	return jsonResult(map[string]any{
		"language": analysis.Language, "entities": len(analysis.Entities),
		"relations": len(analysis.Relations),
		"text":      fmt.Sprintf("%s: %d entities, %d relations", analysis.Language, len(analysis.Entities), len(analysis.Relations)),
		"details":   analysis,
	})
}

// IDs describe syntax within one file; the pipeline adds immutable source scope.
// Unknown or ambiguous names become explicit references, never arbitrary joins.
func codifyStaticGraph(analysis extract.CodeAnalysis, filename string) *orchestrator.ExtractedGraph {
	result := &orchestrator.ExtractedGraph{}
	ids := map[string]bool{}
	names := map[string][]string{}
	add := func(key, name, typ, description string) string {
		encoded, _ := json.Marshal([]string{filename, key})
		id := uuid.NewSHA1(uuid.NameSpaceOID, encoded).String()
		if !ids[id] {
			ids[id] = true
			result.Nodes = append(result.Nodes, graph.DedupNode{ID: id, Name: name, Type: typ, Description: description})
		}
		return id
	}
	module := add("module", filename, "module", "Code file "+filename)
	for _, e := range analysis.Entities {
		qualified := e.Name
		if e.Parent != "" {
			qualified = e.Parent + "." + e.Name
		}
		key := fmt.Sprintf("declaration:%s:%s:%d", e.Type, qualified, e.Line)
		id := add(key, filename+"::"+qualified, e.Type, fmt.Sprintf("%s in %s at line %d", qualified, filename, e.Line))
		names[qualified] = append(names[qualified], id)
		if qualified != e.Name {
			names[e.Name] = append(names[e.Name], id)
		}
	}
	endpoint := func(name string) string {
		if name == filename {
			return module
		}
		candidates := names[name]
		if len(candidates) == 1 {
			return candidates[0]
		}
		return add("reference:"+name, filename+"::reference:"+name, "reference", "Syntactic reference "+name+" in "+filename)
	}
	for _, r := range analysis.Relations {
		result.Edges = append(result.Edges, graph.DedupEdge{
			SourceID: endpoint(r.Source), TargetID: endpoint(r.Target),
			RelationshipName: r.Relationship,
			EdgeText:         r.Source + " " + r.Relationship + " " + r.Target,
		})
	}
	return result
}
