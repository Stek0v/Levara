package mcp

// Graph community tools: list_communities + prune_graph.
// Migrated in F-4 wave 3n. Grouped together because both operate on
// graph metadata and neither needs new Deps methods — DB() and
// LogHeartbeat() were added in earlier waves.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/community"
)

// ToolListCommunities queries the graph_communities table and returns a
// JSON array of community objects. Returns "[]" (not IsError) on nil DB
// or SQL error — empty is a valid state for graphs with no communities
// yet.
//
// Args: limit (float64, default 20), min_members (float64, default 2),
// level (float64, optional).
func ToolListCommunities(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	actor := dataActor(ctx)
	authenticated := actor.UserID != ""
	if actor.TenantID != "" || !access.APIKeyAllows(actor.APIKeyPermissions, access.ActionRead) {
		return toolError("instance administrator required for global community summaries")
	}
	db := deps.DB()
	if db == nil {
		if authenticated {
			return toolError("community access unavailable")
		}
		return jsonResult(map[string]any{"communities": []any{}})
	}
	policy := access.SQLPolicy{DB: db, Q: deps.Q}
	if authenticated {
		active, e1 := policy.IsActive(ctx, actor.UserID)
		admin, e2 := policy.IsSuperuser(ctx, actor.UserID)
		if e1 != nil || e2 != nil {
			return toolError("community access unavailable")
		}
		if !active || !admin {
			return toolError("instance administrator required for global community summaries")
		}
	}
	allowed, err := graphDatasetScope(ctx, deps)
	if err != nil {
		return toolError("community access unavailable")
	}
	if allowed != nil {
		return toolError("instance administrator required for global community summaries")
	}
	limit, minMembers := 20, 2
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}
	if v, ok := args["min_members"].(float64); ok {
		minMembers = int(v)
	}
	columns := "id,level,parent_id,member_count,summary"
	if authenticated {
		columns += ",generation,sources_json,lineage_verified"
	}
	query := "SELECT " + columns + " FROM graph_communities WHERE member_count >= $1"
	queryArgs := []any{minMembers}
	if v, ok := args["level"].(float64); ok {
		query += " AND level=$2"
		queryArgs = append(queryArgs, int(v))
	}
	query += fmt.Sprintf(" ORDER BY level ASC,member_count DESC LIMIT $%d", len(queryArgs)+1)
	queryArgs = append(queryArgs, limit)
	rows, err := db.QueryContext(ctx, deps.Q(query), queryArgs...)
	if err != nil {
		if authenticated {
			return toolError("community access unavailable")
		}
		return jsonResult(map[string]any{"communities": []any{}})
	}
	type candidate struct {
		id, parent, summary, generation, sources string
		level, members, verified                 int
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		var err error
		if authenticated {
			err = rows.Scan(&c.id, &c.level, &c.parent, &c.members, &c.summary, &c.generation, &c.sources, &c.verified)
		} else {
			err = rows.Scan(&c.id, &c.level, &c.parent, &c.members, &c.summary)
		}
		if err != nil {
			if authenticated {
				rows.Close()
				return toolError("community access unavailable")
			}
			continue
		}
		candidates = append(candidates, c)
	}
	rowErr := rows.Err()
	rows.Close() // Release pool1 before native source/policy reads.
	if rowErr != nil && authenticated {
		return toolError("community access unavailable")
	}
	communities := []map[string]any{}
	var evidence []CommunityEvidence
	for _, c := range candidates {
		if authenticated {
			if c.verified != 1 || c.generation == "" {
				continue
			}
			sources, err := community.ParseSources(c.sources)
			if err != nil {
				continue
			}
			if err := community.CheckSources(ctx, policy, sources); err != nil {
				if communityInvalidSource(err) {
					continue
				}
				return toolError("community access unavailable")
			}
			if err := authorizeCommunitySources(ctx, policy, actor, sources, 0); err != nil {
				if communityInvalidSource(err) {
					continue
				}
				return toolError("community access unavailable")
			}
			evidence = append(evidence, CommunityEvidence{ID: c.id, Generation: c.generation, SourcesJSON: c.sources})
		}
		communities = append(communities, map[string]any{"id": c.id, "level": c.level, "parent_id": c.parent, "member_count": c.members, "summary": c.summary})
	}
	result := jsonResult(map[string]any{"communities": communities})
	result.CommunityEvidence = evidence
	return result
}

func communityInvalidSource(err error) bool {
	return errors.Is(err, access.ErrDocumentInvalid) || errors.Is(err, access.ErrDocumentNotFound) || errors.Is(err, access.ErrDocumentVersionConflict)
}

func authorizeCommunitySources(ctx context.Context, p access.SQLPolicy, actor access.Actor, sources []community.Source, depth int) error {
	seen := make(map[community.Source]bool)
	var visit func([]community.Source, int) error
	visit = func(sources []community.Source, depth int) error {
		if depth >= 16 || len(sources) > 256 {
			return access.ErrDocumentInvalid
		}
		for _, s := range sources {
			s.InputSHA256 = ""
			if seen[s] {
				continue
			}
			if len(seen) >= 256 {
				return access.ErrDocumentInvalid
			}
			seen[s] = true
			decision, err := p.AuthorizeDocument(ctx, actor, access.DocumentRef{DatasetID: s.DatasetID, DataID: s.DocumentID}, access.ActionRead)
			if err != nil {
				return err
			}
			if !decision.Allowed {
				return access.ErrDocumentInvalid
			}
			if s.Derived {
				lineage, present, err := p.DocumentIndexLineage(ctx, access.DocumentRef{DatasetID: s.DatasetID, DataID: s.DocumentID}, s.ContentRevision, s.Collection, s.Generation)
				if err != nil {
					return err
				}
				if !present {
					return access.ErrDocumentVersionConflict
				}
				var children []community.Source
				if len(lineage.SourcesJSON) > 128<<10 || json.Unmarshal([]byte(lineage.SourcesJSON), &children) != nil {
					return access.ErrDocumentInvalid
				}
				if err := visit(children, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(sources, depth)
}

// ToolPruneGraph removes superseded graph edges (and optionally orphan
// nodes) according to community.PruneConfig. Defaults to dry-run=true
// so the first call is always a preview.
//
// Args: max_age_days (float64), dry_run (bool), include_orphan_nodes (bool).
//
// Error branch: DB nil → `{"edges_deleted":0}` (not IsError);
// community.PruneGraph error → IsError.
func ToolPruneGraph(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	actor := dataActor(ctx)
	if !access.APIKeyAllows(actor.APIKeyPermissions, access.ActionDelete) {
		return toolError("API key permissions denied")
	}
	// Dry-run counts also expose the global graph and require the same role.
	if actor.UserID != "" {
		policy := access.SQLPolicy{DB: deps.DB(), Q: deps.Q}
		active, err := policy.IsActive(ctx, actor.UserID)
		if err != nil {
			return toolError("admin access check failed")
		}
		super, err := policy.IsSuperuser(ctx, actor.UserID)
		if err != nil {
			return toolError("admin access check failed")
		}
		if !active || !super {
			return toolError("active instance administrator required")
		}
	}
	db := deps.DB()
	if db == nil {
		return jsonResult(map[string]any{"edges_deleted": 0, "nodes_deleted": 0, "dry_run": true})
	}

	cfg := community.PruneConfig{
		MaxAgeDays:      90,
		KeepSuperseding: true,
		DryRun:          true,
	}
	if days, ok := args["max_age_days"].(float64); ok && days > 0 {
		cfg.MaxAgeDays = int(days)
	}
	if dr, ok := args["dry_run"].(bool); ok {
		cfg.DryRun = dr
	}
	if io, ok := args["include_orphan_nodes"].(bool); ok {
		cfg.IncludeOrphans = io
	}

	result, err := community.PruneGraph(ctx, db, cfg)
	if err != nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Error: %v", err)}},
			IsError: true,
		}
	}

	deps.LogHeartbeat("prune", result)
	return jsonResult(map[string]any{
		"edges_deleted":      result.EdgesDeleted,
		"edges_would_delete": result.EdgesWouldDelete,
		"orphan_nodes":       result.OrphanNodes,
		"members_cleaned_up": result.MembersCleanedUp,
		"dry_run":            cfg.DryRun,
	})
}
