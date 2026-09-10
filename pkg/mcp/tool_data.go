package mcp

// Dataset-facing tools: delete, prune, list_data, add.
// Extracted from deps.go during F-4 wave 3j-split.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/ingest"
)

// dataActor uses only the identity attached by the authenticated MCP transport.
func dataActor(ctx context.Context) access.Actor {
	user, _ := ctx.Value(UserIDKey).(string)
	permissions, _ := ctx.Value(ContextKey("mcp_api_key_permissions")).(string)
	tenant, _ := ctx.Value(TenantIDKey).(string)
	return access.Actor{UserID: user, APIKeyPermissions: permissions, TenantID: tenant}
}

// ToolDelete deletes a dataset after the shared object-level access check.
func ToolDelete(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	dsID, _ := args["dataset_id"].(string)
	if dsID == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'dataset_id' required"}},
			IsError: true,
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	policy := access.SQLPolicy{DB: deps.DB(), Q: deps.Q}
	actor := deps.MetadataActor(ctx)
	if deps.DB() == nil {
		return toolError("dataset delete unavailable")
	}
	if !access.APIKeyAllows(actor.APIKeyPermissions, access.ActionDelete) || actor.Credential.Kind == "api_key" && strings.TrimSpace(actor.APIKeyPermissions) == "" {
		return toolError("dataset access denied")
	}
	if !actor.TrustedLocal {
		c := actor.Credential
		if actor.UserID == "" || policy.RecheckCredential(ctx, actor.UserID, c.Kind, c.KeyID, actor.APIKeyPermissions, c.SessionID, c.Epoch, c.IssuedAt, c.ExpiresAt) != nil {
			return toolError("dataset access denied")
		}
	}
	if err := policy.DeleteDatasetWithDocuments(ctx, actor.Actor, dsID); err != nil && !(actor.TrustedLocal && errors.Is(err, access.ErrDocumentNotFound)) {
		return toolError("dataset delete denied or unavailable")
	}
	message := fmt.Sprintf("Dataset %s deleted.", dsID)
	if writer, err := ingest.NewMetadataWriterForStorage(deps.DB(), deps.StorageBackend()); err != nil || writer.CleanupRetiredStructuredArtifacts(ctx, "", deps.StoragePath(), deps.StorageBackend()) != nil {
		message += " Structured artifact cleanup is pending."
	}
	return statusResult(true, message)
}

// ToolPrune shares the HTTP policy: live authority, every legal hold, durable
// tombstones, and all SQL deletes are checked under one bounded transaction.
func ToolPrune(ctx context.Context, deps Deps) ToolResult {
	if err := (access.SQLPolicy{DB: deps.DB(), Q: deps.Q}).PruneData(ctx, deps.MetadataActor(ctx), true); err != nil {
		return toolError("prune denied or unavailable")
	}
	message := "All data pruned."
	if writer, err := ingest.NewMetadataWriterForStorage(deps.DB(), deps.StorageBackend()); err != nil || writer.CleanupRetiredStructuredArtifacts(ctx, "", deps.StoragePath(), deps.StorageBackend()) != nil {
		message += " Structured artifact cleanup is pending."
	}
	return statusResult(true, message)
}

// listDataItemCap is the LIMIT applied to the data / datasets SELECTs.
// Matches the pre-refactor numbers — 200 for filtered data rows, 100
// for the unfiltered datasets listing.
const (
	listDataItemCap     = 200
	listDataDatasetsCap = 100
)

// ToolListData lists the available data for MCP clients.
//
// Three modes, chosen by argument shape:
//   - Collections not configured → "[]" (deployments without the vector
//     engine surface nothing here, matching the pre-refactor contract).
//   - With "room" or "tags" filter → SELECT from the data table.
//   - Otherwise → collection names from the manager + recent datasets.
//
// All DB errors are swallowed; a failing query simply contributes no
// items to the output.
func ToolListData(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	if !deps.HasCollections() {
		return jsonResult(map[string]any{"datasets": []any{}})
	}

	var wantTags []string
	if rawTags, ok := args["tags"].([]any); ok {
		for _, t := range rawTags {
			if s, ok := t.(string); ok && s != "" {
				wantTags = append(wantTags, s)
			}
		}
	}
	roomFilter, _ := args["room"].(string)
	hasFilter := len(wantTags) > 0 || roomFilter != ""

	// ACL scoping (finding H13, 2026-09-03 review): nil = unrestricted
	// (superuser or no-auth mode); a non-nil, empty slice means the caller
	// may see nothing; otherwise only the listed dataset IDs are visible.
	allowed := deps.AllowedDatasetIDs(ctx)
	allowedDatasetSet := map[string]bool{}
	allowedCollectionNames := map[string]bool{}
	for _, id := range allowed {
		allowedDatasetSet[id] = true
	}
	// Vector collections are named after their dataset, so a caller allowed
	// to see a dataset may also see its collection by name. Resolve visible
	// dataset names separately: names must never authorize dataset IDs,
	// and dataset IDs must never authorize another dataset's collection name.
	if allowed != nil && deps.DB() != nil && len(allowed) > 0 {
		ph := make([]string, len(allowed))
		args := make([]any, len(allowed))
		for i, id := range allowed {
			ph[i] = fmt.Sprintf("$%d", i+1)
			args[i] = id
		}
		q := deps.Q("SELECT name FROM datasets WHERE id IN (" + strings.Join(ph, ",") + ")")
		if rows, err := deps.DB().QueryContext(ctx, q, args...); err == nil {
			for rows.Next() {
				var name string
				if rows.Scan(&name) == nil {
					allowedCollectionNames[name] = true
				}
			}
			rows.Close()
		}
	}

	var items []map[string]any
	if !hasFilter {
		for _, c := range deps.ListCollections() {
			if allowed != nil && !allowedCollectionNames[c] {
				continue
			}
			items = append(items, map[string]any{"collection": c, "type": "vector_collection"})
		}
	}

	if db := deps.DB(); db != nil {
		if hasFilter {
			items = append(items, listDataFiltered(ctx, db, deps.Q, roomFilter, wantTags, allowed)...)
		} else {
			items = append(items, listDataUnfiltered(ctx, db, deps.Q, allowed, allowedDatasetSet)...)
		}
	}

	return jsonResult(map[string]any{"datasets": items})
}

// listDataFiltered runs the tag/room-scoped SELECT against the data
// table. Returns an empty slice if the query fails — MCP callers
// distinguish "no match" from "error" by checking the enclosing
// ToolResult.IsError flag, which the filtered path never sets.
func listDataFiltered(ctx context.Context, db *sql.DB, rewrite func(string) string, roomFilter string, wantTags []string, allowed []string) []map[string]any {
	var conds []string
	var qargs []any
	pos := 1
	if roomFilter != "" {
		conds = append(conds, fmt.Sprintf("room = $%d", pos))
		qargs = append(qargs, roomFilter)
		pos++
	}
	for _, t := range wantTags {
		// JSON tag list is stored as a string like ["a","b"]; LIKE works
		// on both PG and SQLite because we match the quoted tag token.
		conds = append(conds, fmt.Sprintf("tags LIKE $%d", pos))
		qargs = append(qargs, "%\""+t+"\"%")
		pos++
	}
	if allowed != nil {
		if len(allowed) == 0 {
			return nil
		}
		ph := make([]string, len(allowed))
		for i, id := range allowed {
			ph[i] = fmt.Sprintf("$%d", pos)
			qargs = append(qargs, id)
			pos++
		}
		conds = append(conds, "EXISTS (SELECT 1 FROM dataset_data dd WHERE dd.data_id = data.id AND dd.dataset_id IN ("+strings.Join(ph, ",")+"))")
	}
	sqlStr := `SELECT id, name, extension, room, tags FROM data`
	if len(conds) > 0 {
		sqlStr += " WHERE " + strings.Join(conds, " AND ")
	}
	sqlStr += fmt.Sprintf(" ORDER BY created_at DESC LIMIT %d", listDataItemCap)

	rows, err := db.QueryContext(ctx, rewrite(sqlStr), qargs...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var id, name, ext, rm, tg string
		rows.Scan(&id, &name, &ext, &rm, &tg)
		out = append(out, map[string]any{
			"id":        id,
			"name":      name,
			"extension": ext,
			"room":      rm,
			"tags":      json.RawMessage(tg),
			"type":      "data",
		})
	}
	return out
}

// listDataUnfiltered lists the most recent datasets (used when no
// room/tags filter is supplied). allowed == nil means no ACL filtering
// (anonymous/dev/superuser); otherwise only datasets whose id is in the
// allowed set are returned (finding H13, 2026-09-03 review).
func listDataUnfiltered(ctx context.Context, db *sql.DB, rewrite func(string) string, allowed []string, allowedDatasetSet map[string]bool) []map[string]any {
	rows, err := db.QueryContext(ctx, rewrite(fmt.Sprintf("SELECT id, name FROM datasets ORDER BY created_at DESC LIMIT %d", listDataDatasetsCap)))
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var id, name string
		rows.Scan(&id, &name)
		if allowed != nil && !allowedDatasetSet[id] {
			continue
		}
		out = append(out, map[string]any{"id": id, "name": name, "type": "dataset"})
	}
	return out
}

// defaultStoragePath is used when Deps.StoragePath() returns empty.
// Matches the pre-refactor fallback in internal/http.
const defaultStoragePath = "data/uploads"

// ToolAdd resolves and authorizes its dataset before writing bytes, then
// commits metadata using the authenticated owner's identity.
func ToolAdd(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	data, _ := args["data"].(string)
	if data == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'data' required"}},
			IsError: true,
		}
	}

	datasetName, _ := args["dataset_name"].(string)
	if datasetName == "" {
		datasetName = "default"
	}

	actor := dataActor(ctx)
	if !access.APIKeyAllows(actor.APIKeyPermissions, access.ActionWrite) {
		return toolError("API key permissions denied")
	}
	dsID := uuid.New().String()
	if db := deps.DB(); db != nil {
		var existing string
		err := db.QueryRowContext(ctx, deps.Q("SELECT id FROM datasets WHERE name = $1"), datasetName).Scan(&existing)
		if err == nil {
			decision, err := (access.SQLPolicy{DB: db, Q: deps.Q}).AuthorizeDataset(ctx, actor, existing, access.ActionWrite)
			if err != nil {
				return toolError("dataset access check failed")
			}
			if !decision.Allowed {
				return toolError("dataset access denied")
			}
			dsID = existing
		} else if !errors.Is(err, sql.ErrNoRows) {
			return toolError("dataset lookup failed: " + err.Error())
		}
	}

	storagePath := deps.StoragePath()
	if storagePath == "" {
		storagePath = defaultStoragePath
	}

	var tags []string
	if rawTags, ok := args["tags"].([]any); ok {
		for _, t := range rawTags {
			if s, ok := t.(string); ok && s != "" {
				tags = append(tags, s)
			}
		}
	}
	room, _ := args["room"].(string)

	items := []ingest.Item{{
		Text:        data,
		DatasetName: datasetName,
		OwnerID:     actor.UserID,
		Tags:        tags,
		Room:        room,
	}}
	var results []ingest.Result
	var err error
	if db := deps.DB(); db != nil {
		w, configErr := ingest.NewMetadataWriterForStorage(db, deps.StorageBackend())
		if configErr != nil {
			return toolError("storage destination unavailable")
		}
		results, _, err = w.IngestAuthorized(ctx, items, nil, storagePath, deps.StorageBackend(), deps.MetadataActor(ctx), dsID, datasetName)
	} else if deps.MetadataActor(ctx).TrustedLocal {
		results, err = ingest.IngestStored(ctx, items, storagePath, deps.StorageBackend())
	} else {
		return toolError("metadata storage unavailable")
	}
	if err != nil {
		return toolError("ingestion denied or unavailable")
	}

	dataID := ""
	if len(results) > 0 {
		dataID = results[0].ID
	}
	return jsonResult(map[string]any{
		"dataset_id": dsID,
		"data_id":    dataID,
		"status":     "ingested",
		"message":    fmt.Sprintf("Data ingested into dataset '%s' (items: %d). Use 'cognify' tool to build knowledge graph.", datasetName, len(results)),
	})
}
