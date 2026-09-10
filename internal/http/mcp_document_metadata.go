package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	accesspkg "github.com/stek0v/levara/pkg/access"
)

// Filter each inclusion before returning metadata. Dataset membership alone
// neither reveals restricted documents nor excludes direct document grants.
func (h *mcpHandler) listDocumentMetadata(ctx context.Context, args map[string]any) mcpToolResult {
	fail := func() mcpToolResult {
		return mcpToolResult{IsError: true, Content: []mcpContent{{Type: "text", Text: "document metadata unavailable"}}}
	}
	result := func(items []map[string]any) mcpToolResult {
		body := map[string]any{"datasets": items}
		raw, err := json.Marshal(body)
		if err != nil {
			return fail()
		}
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(raw)}}, StructuredContent: body}
	}
	actor, err := sessionActor(ctx, h.cfg, accesspkg.ActionRead)
	if err != nil {
		return fail()
	}
	room, _ := args["room"].(string)
	want := []string{}
	if raw, ok := args["tags"].([]any); ok {
		for _, item := range raw {
			tag, ok := item.(string)
			if !ok {
				return fail()
			}
			if tag != "" {
				want = append(want, tag)
			}
		}
	}
	if room == "" && len(want) == 0 {
		items, err := h.listMetadataDatasets(ctx, actor)
		if err != nil {
			return fail()
		}
		return result(items)
	}
	items := []map[string]any{}
	if h.cfg.DB == nil {
		if h.cfg.RequireAuth {
			return fail()
		}
		return result(items)
	}
	base := `SELECT d.id,dd.dataset_id,d.name,d.extension,d.room,d.tags FROM data d JOIN dataset_data dd ON dd.data_id=d.id WHERE 1=1`
	params := []any{}
	if room != "" {
		base += " AND d.room=$1"
		params = append(params, room)
	}
	dataCursor, datasetCursor := "", ""
	seen := map[string]bool{}
	p := documentSQLPolicy(h.cfg)
	for len(items) < 200 {
		query, values := base, append([]any(nil), params...)
		if dataCursor != "" {
			query += fmt.Sprintf(" AND (d.id,dd.dataset_id)>($%d,$%d)", len(values)+1, len(values)+2)
			values = append(values, dataCursor, datasetCursor)
		}
		query += " ORDER BY d.id,dd.dataset_id LIMIT 128"
		rows, err := h.cfg.DB.QueryContext(ctx, Q(query), values...)
		if err != nil {
			return fail()
		}
		type row struct{ id, dataset, name, extension, room, tags string }
		page := []row{}
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.dataset, &r.name, &r.extension, &r.room, &r.tags); err != nil {
				rows.Close()
				return fail()
			}
			page = append(page, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fail()
		}
		for _, r := range page {
			dataCursor, datasetCursor = r.id, r.dataset
			if seen[r.id] {
				continue
			}
			var tags []string
			if json.Unmarshal([]byte(r.tags), &tags) != nil {
				return fail()
			}
			match := true
			for _, tag := range want {
				found := false
				for _, actual := range tags {
					if actual == tag {
						found = true
						break
					}
				}
				if !found {
					match = false
					break
				}
			}
			if !match {
				continue
			}
			resource, err := p.GetDocumentResource(ctx, accesspkg.DocumentRef{DatasetID: r.dataset, DataID: r.id})
			if err != nil && !errors.Is(err, accesspkg.ErrDocumentNotFound) {
				return fail()
			}
			source := searchDocumentSource{DatasetID: r.dataset, DocumentID: r.id, ContentRevision: resource.ContentRevision}
			source.SourceRevision, source.RawContentHash, err = p.SourceVersion(ctx, accesspkg.DocumentRef{DatasetID: r.dataset, DataID: r.id})
			if err != nil && !errors.Is(err, accesspkg.ErrDocumentVersionConflict) {
				return fail()
			}
			allowed, err := searchDocumentAllowed(ctx, h.cfg, actor, source)
			if err != nil {
				return fail()
			}
			if !allowed {
				continue
			}
			trackSearchSource(ctx, source)
			seen[r.id] = true
			items = append(items, map[string]any{"id": r.id, "dataset_id": r.dataset, "name": r.name, "extension": r.extension, "room": r.room, "tags": tags, "type": "data"})
			if len(items) == 200 {
				break
			}
		}
		if len(page) < 128 {
			break
		}
	}
	return result(items)
}

func (h *mcpHandler) listMetadataDatasets(ctx context.Context, actor accesspkg.Actor) ([]map[string]any, error) {
	items := []map[string]any{}
	visibleCollections := map[string]string{}
	admin := actor.UserID == "" && !h.cfg.RequireAuth
	if h.cfg.DB != nil {
		p := documentSQLPolicy(h.cfg)
		if actor.UserID != "" && actor.TenantID == "" {
			var err error
			admin, err = p.IsSuperuser(ctx, actor.UserID)
			if err != nil {
				return nil, err
			}
		}
		cursor := ""
		for len(items) < 100 {
			rows, err := h.cfg.DB.QueryContext(ctx, Q("SELECT id,name FROM datasets WHERE id>$1 ORDER BY id LIMIT 128"), cursor)
			if err != nil {
				return nil, err
			}
			type row struct{ id, name string }
			page := []row{}
			for rows.Next() {
				var r row
				if err := rows.Scan(&r.id, &r.name); err != nil {
					rows.Close()
					return nil, err
				}
				page = append(page, r)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
			for _, r := range page {
				cursor = r.id
				allowed, err := p.AuthorizeDataset(ctx, actor, r.id, accesspkg.ActionRead)
				if err != nil {
					return nil, err
				}
				if !allowed.Allowed {
					continue
				}
				trackSearchSource(ctx, searchDocumentSource{DatasetID: r.id, DatasetMetadataOnly: true})
				visibleCollections[r.name] = r.id
				items = append(items, map[string]any{"id": r.id, "name": r.name, "type": "dataset"})
				if len(items) == 100 {
					break
				}
			}
			if len(page) < 128 {
				break
			}
		}
	}
	for _, name := range h.ListCollections() {
		id, allowed := visibleCollections[name]
		if !allowed && !admin {
			continue
		}
		if allowed {
			trackSearchSource(ctx, searchDocumentSource{DatasetID: id, DatasetMetadataOnly: true})
		} else {
			requireAdminSearchEvidence(ctx)
		}
		items = append(items, map[string]any{"collection": name, "type": "vector_collection"})
		if len(items) == 200 {
			break
		}
	}
	return items, nil
}
