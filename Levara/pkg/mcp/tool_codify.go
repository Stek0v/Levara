package mcp

// Codify tool: static code analysis → graph + vector index.
// Migrated in F-4 wave 3p. No new Deps methods:
//   - DB() + Q() for graph_nodes / graph_edges upserts
//   - BaseCognifyConfig() for embed endpoint/model
//   - HasCollections() as a vector-engine gate
//   - CollectionInsert() for the per-entity vector upsert
//
// The original QArgs call for parameter reuse in the ON CONFLICT clause
// is replaced with excluded.* syntax (SQLite 3.24+ / PostgreSQL 9.5+),
// which works on both engines without the QArgs machinery.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/extract"
)

// ToolCodify parses code with extract.AnalyzeCode, stores extracted
// entities + relations as graph nodes/edges (when DB is configured), and
// optionally embeds entity descriptions into a named collection (when an
// embed service + collection manager are configured).
//
// Returns a JSON summary:
//
//	{"language": "...", "entities": N, "relations": M, "details": {...}}
//
// Error branch: missing code or filename → IsError.
// Non-error branches: DB-nil / embed-nil produce partial results without
// error — callers receive whatever was analysed.
func ToolCodify(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	code, _ := args["code"].(string)
	filename, _ := args["filename"].(string)
	if code == "" || filename == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'code' and 'filename' required"}},
			IsError: true,
		}
	}

	analysis := extract.AnalyzeCode(code, filename)

	db := deps.DB()
	if db != nil {
		// Upsert entities as graph nodes.
		// excluded.* replaces the pre-refactor QArgs($2, $3, $5) pattern
		// — identical semantics on SQLite 3.24+ and PostgreSQL 9.5+.
		for _, e := range analysis.Entities {
			props := fmt.Sprintf(`{"file":"%s","line":%d}`, e.File, e.Line)
			if e.Parent != "" {
				props = fmt.Sprintf(`{"file":"%s","line":%d,"parent":"%s"}`, e.File, e.Line, e.Parent)
			}
			nodeID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(e.Name+e.Type+e.File)).String()
			db.ExecContext(ctx, deps.Q(`
				INSERT INTO graph_nodes (id, name, type, description, properties)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT(id) DO UPDATE SET
					name = excluded.name,
					type = excluded.type,
					properties = excluded.properties`),
				nodeID, e.Name, e.Type, filename, props)
		}

		// Upsert relations as graph edges (DO NOTHING — idempotent).
		for _, r := range analysis.Relations {
			srcID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(r.Source)).String()
			tgtID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(r.Target)).String()
			edgeID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(r.Source+r.Relationship+r.Target)).String()
			db.ExecContext(ctx, deps.Q(`
				INSERT INTO graph_edges (id, source_id, target_id, relationship_name, properties)
				VALUES ($1, $2, $3, $4, '{}')
				ON CONFLICT(id) DO NOTHING`),
				edgeID, srcID, tgtID, r.Relationship)
		}
	}

	// Embed entity descriptions into collection when configured.
	// T3.2: use Deps.EmbedBatch so we share the process-wide embed client's
	// TCP pool instead of dialling fresh per codify invocation (the prior
	// embed.NewClient(...) path defeated the T3 shared-pool win).
	collection, _ := args["collection"].(string)
	if collection != "" && deps.EmbedAvailable() {
		var texts, ids []string
		for _, e := range analysis.Entities {
			texts = append(texts, e.Name+": "+e.Type+" in "+e.File)
			ids = append(ids, uuid.NewSHA1(uuid.NameSpaceOID, []byte(e.Name+e.Type+e.File)).String())
		}
		if len(texts) > 0 {
			if vecs, err := deps.EmbedBatch(ctx, texts); err == nil {
				for i, vec := range vecs {
					meta := fmt.Sprintf(`{"name":"%s","type":"%s","file":"%s"}`,
						analysis.Entities[i].Name, analysis.Entities[i].Type, analysis.Entities[i].File)
					deps.CollectionInsert(collection, ids[i], vec, meta)
				}
			}
		}
	}

	out, _ := json.MarshalIndent(map[string]any{
		"language":  analysis.Language,
		"entities":  len(analysis.Entities),
		"relations": len(analysis.Relations),
		"details":   analysis,
	}, "", "  ")
	return ToolResult{Content: []Content{{Type: "text", Text: string(out)}}}
}
