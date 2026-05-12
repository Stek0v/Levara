package http

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stek0v/levara/internal/metrics"
	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/bm25"
	mcppkg "github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/workspace"
)

func TestWorkspaceAPIIndexDeleteAndManifest(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)

	indexBody := map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "gen-1",
		"path":                "docs/payment.md",
		"text":                "# Payment\n\nBounded timeout retry policy.",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
		"room":                "payments",
		"tags":                []string{"latency"},
	}
	body, status := workspaceTestPost(t, app, "/workspace/index", indexBody)
	if status != http.StatusOK {
		t.Fatalf("index status=%d body=%s", status, body)
	}
	var indexResp workspaceIndexResponse
	if err := json.Unmarshal(body, &indexResp); err != nil {
		t.Fatal(err)
	}
	if indexResp.Result.ChunksCreated == 0 {
		t.Fatal("expected chunks")
	}
	if indexResp.ActiveGeneration != "gen-1" {
		t.Fatalf("active_generation=%q, want gen-1", indexResp.ActiveGeneration)
	}
	if got := workspaceTestRecordCount(cfg, indexResp.Result.Collection); got != indexResp.Result.ChunksCreated {
		t.Fatalf("vector count=%d, want chunks=%d", got, indexResp.Result.ChunksCreated)
	}
	if hits := cfg.BM25Indexes[indexResp.Result.Collection].Search("bounded timeout", 10); len(hits) == 0 {
		t.Fatal("expected BM25 hit for indexed markdown")
	}

	manifestBody := workspaceTestGet(t, app, "/workspace/manifest?project_id=payments&branch=main", http.StatusOK)
	var manifest map[string]any
	if err := json.Unmarshal(manifestBody, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["active_generation"] != "gen-1" {
		t.Fatalf("manifest active_generation=%v, want gen-1", manifest["active_generation"])
	}
	if manifest["chunks_count"].(float64) != float64(indexResp.Result.ChunksCreated) {
		t.Fatalf("manifest chunks_count=%v, want %d", manifest["chunks_count"], indexResp.Result.ChunksCreated)
	}

	deleteApp := fiber.New()
	deleteCfg := cfg
	deleteCfg.EmbedEndpoint = ""
	deleteCfg.EmbedClient = nil
	RegisterWorkspaceAPI(deleteApp, deleteCfg)
	deleteBody := map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"generation": "gen-1",
		"collection": indexResp.Result.Collection,
		"path":       "docs/payment.md",
	}
	body, status = workspaceTestPost(t, deleteApp, "/workspace/delete", deleteBody)
	if status != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", status, body)
	}
	if got := workspaceTestRecordCount(cfg, indexResp.Result.Collection); got != 0 {
		t.Fatalf("vector count after delete=%d, want 0", got)
	}
	if hits := cfg.BM25Indexes[indexResp.Result.Collection].Search("bounded timeout", 10); len(hits) != 0 {
		t.Fatalf("BM25 hits after delete=%d, want 0", len(hits))
	}
}

func TestWorkspaceAPIWriteReadAndReindexUseFilesystemTruth(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)

	body, status := workspaceTestPost(t, app, "/workspace/write", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "gen-1",
		"path":                "docs/source.md",
		"text":                "# Source\n\nFilesystem truth with exact phrase.",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if status != http.StatusOK {
		t.Fatalf("write status=%d body=%s", status, body)
	}
	var writeResp workspaceWriteResponse
	if err := json.Unmarshal(body, &writeResp); err != nil {
		t.Fatal(err)
	}
	if writeResp.Indexed == nil || writeResp.Indexed.Result.ChunksCreated == 0 {
		t.Fatalf("write response missing index result: %+v", writeResp)
	}

	readBody := workspaceTestGet(t, app, "/workspace/read?project_id=payments&branch=main&path=docs/source.md", http.StatusOK)
	var readResp workspaceReadResponse
	if err := json.Unmarshal(readBody, &readResp); err != nil {
		t.Fatal(err)
	}
	if readResp.Text != "# Source\n\nFilesystem truth with exact phrase." {
		t.Fatalf("read text=%q", readResp.Text)
	}
	if len(readResp.Chunks) == 0 {
		t.Fatal("read should include manifest chunk records")
	}

	filePath, _, err := workspaceFilePath(cfg, "payments", "main", "docs/source.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("# Source\n\nReindexed phrase from disk."), 0644); err != nil {
		t.Fatal(err)
	}
	body, status = workspaceTestPost(t, app, "/workspace/reindex", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "gen-2",
		"paths":               []string{"docs/source.md"},
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if status != http.StatusOK {
		t.Fatalf("reindex status=%d body=%s", status, body)
	}
	var reindexResp workspaceReindexResponse
	if err := json.Unmarshal(body, &reindexResp); err != nil {
		t.Fatal(err)
	}
	if reindexResp.ActiveGeneration != "gen-2" {
		t.Fatalf("active generation=%q, want gen-2", reindexResp.ActiveGeneration)
	}
	if len(reindexResp.Results) != 1 || reindexResp.Results[0].ChunksCreated == 0 {
		t.Fatalf("bad reindex result: %+v", reindexResp.Results)
	}
}

func TestWorkspaceWriteExpectedDigestRejectsConflictingWrite(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)

	body, status := workspaceTestPost(t, app, "/workspace/write", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"path":       "docs/lock.md",
		"text":       "# Lock\n\nOriginal.",
		"index":      false,
	})
	if status != http.StatusOK {
		t.Fatalf("write status=%d body=%s", status, body)
	}
	currentDigest := digestText("# Lock\n\nOriginal.")
	body, status = workspaceTestPost(t, app, "/workspace/write", map[string]any{
		"project_id":           "payments",
		"branch":               "main",
		"path":                 "docs/lock.md",
		"text":                 "# Lock\n\nRejected.",
		"index":                false,
		"expected_file_digest": "stale-digest",
	})
	if status != http.StatusBadRequest || !bytes.Contains(body, []byte("workspace write conflict")) {
		t.Fatalf("conflicting write status=%d body=%s", status, body)
	}
	readBody := workspaceTestGet(t, app, "/workspace/read?project_id=payments&branch=main&path=docs/lock.md", http.StatusOK)
	if bytes.Contains(readBody, []byte("Rejected")) {
		t.Fatalf("conflicting write modified file: %s", readBody)
	}
	body, status = workspaceTestPost(t, app, "/workspace/write", map[string]any{
		"project_id":           "payments",
		"branch":               "main",
		"path":                 "docs/lock.md",
		"text":                 "# Lock\n\nAccepted.",
		"index":                false,
		"expected_file_digest": currentDigest,
	})
	if status != http.StatusOK {
		t.Fatalf("matching digest write status=%d body=%s", status, body)
	}
}

func TestWorkspaceAPIAccessDeniedDoesNotRevealFilesystemState(t *testing.T) {
	cfg, closeFn := newWorkspaceACLTestConfig(t)
	defer closeFn()
	seedWorkspaceACL(t, cfg.DB, "user-a", "user-b", "payments", "")
	allowed, err := checkWorkspaceAccess(context.Background(), cfg.DB, "user-b", "payments", workspaceAccessRead)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("direct ACL check allowed foreign project")
	}

	filePath, _, err := workspaceFilePath(cfg, "payments", "main", "docs/private.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("# Private\n\nHidden."), 0644); err != nil {
		t.Fatal(err)
	}

	app := workspaceACLApp("user-b", cfg)
	body := workspaceTestGet(t, app, "/workspace/read?project_id=payments&branch=main&path=docs/private.md", http.StatusForbidden)
	if bytes.Contains(body, []byte("docs/private.md")) || bytes.Contains(body, []byte("Hidden")) {
		t.Fatalf("denied response leaked file details: %s", body)
	}
}

func TestWorkspaceAPIViewerCanReadButCannotWrite(t *testing.T) {
	cfg, closeFn := newWorkspaceACLTestConfig(t)
	defer closeFn()
	seedWorkspaceACL(t, cfg.DB, "user-a", "user-b", "payments", RoleViewer)

	filePath, _, err := workspaceFilePath(cfg, "payments", "main", "docs/shared.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("# Shared\n\nReadable."), 0644); err != nil {
		t.Fatal(err)
	}

	app := workspaceACLApp("user-b", cfg)
	readBody := workspaceTestGet(t, app, "/workspace/read?project_id=payments&branch=main&path=docs/shared.md", http.StatusOK)
	if !bytes.Contains(readBody, []byte("Readable.")) {
		t.Fatalf("read response missing content: %s", readBody)
	}
	_, status := workspaceTestPost(t, app, "/workspace/write", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"path":       "docs/shared.md",
		"text":       "# Shared\n\nEdited.",
		"index":      false,
	})
	if status != http.StatusForbidden {
		t.Fatalf("viewer write status=%d, want 403", status)
	}
}

func TestWorkspaceAPIEditorCanWrite(t *testing.T) {
	cfg, closeFn := newWorkspaceACLTestConfig(t)
	defer closeFn()
	seedWorkspaceACL(t, cfg.DB, "user-a", "user-b", "payments", RoleEditor)

	app := workspaceACLApp("user-b", cfg)
	body, status := workspaceTestPost(t, app, "/workspace/write", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"path":       "docs/editor.md",
		"text":       "# Editor\n\nWritable.",
		"index":      false,
	})
	if status != http.StatusOK {
		t.Fatalf("editor write status=%d body=%s", status, body)
	}
}

func TestWorkspaceAccessCheckPreflightHonorsRoles(t *testing.T) {
	cfg, closeFn := newWorkspaceACLTestConfig(t)
	defer closeFn()
	seedWorkspaceACL(t, cfg.DB, "user-a", "user-b", "payments", RoleViewer)

	ownerApp := workspaceACLApp("user-a", cfg)
	body, status := workspaceTestPost(t, ownerApp, "/workspace/access/check", map[string]any{
		"project_id": "payments",
		"access":     "write",
	})
	if status != http.StatusOK {
		t.Fatalf("owner access check status=%d body=%s", status, body)
	}
	var owner workspaceAccessCheckResponse
	if err := json.Unmarshal(body, &owner); err != nil {
		t.Fatal(err)
	}
	if !owner.Allowed || owner.Role != RoleAdmin || owner.Reason != "owner" {
		t.Fatalf("owner access=%+v, want admin owner allowed", owner)
	}

	viewerApp := workspaceACLApp("user-b", cfg)
	body, status = workspaceTestPost(t, viewerApp, "/workspace/access/check", map[string]any{
		"project_id": "payments",
		"access":     "read",
	})
	if status != http.StatusOK {
		t.Fatalf("viewer read check status=%d body=%s", status, body)
	}
	var viewerRead workspaceAccessCheckResponse
	if err := json.Unmarshal(body, &viewerRead); err != nil {
		t.Fatal(err)
	}
	if !viewerRead.Allowed || viewerRead.Role != RoleViewer {
		t.Fatalf("viewer read access=%+v, want allowed viewer", viewerRead)
	}

	body, status = workspaceTestPost(t, viewerApp, "/workspace/access/check", map[string]any{
		"project_id": "payments",
		"access":     "write",
	})
	if status != http.StatusOK {
		t.Fatalf("viewer write check status=%d body=%s", status, body)
	}
	var viewerWrite workspaceAccessCheckResponse
	if err := json.Unmarshal(body, &viewerWrite); err != nil {
		t.Fatal(err)
	}
	if viewerWrite.Allowed || viewerWrite.Reason != "role_insufficient" {
		t.Fatalf("viewer write access=%+v, want denied role_insufficient", viewerWrite)
	}

	foreignApp := workspaceACLApp("user-c", cfg)
	body, status = workspaceTestPost(t, foreignApp, "/workspace/access/check", map[string]any{
		"project_id": "payments",
		"access":     "read",
	})
	if status != http.StatusOK {
		t.Fatalf("foreign read check status=%d body=%s", status, body)
	}
	var foreign workspaceAccessCheckResponse
	if err := json.Unmarshal(body, &foreign); err != nil {
		t.Fatal(err)
	}
	if foreign.Allowed || foreign.Reason != "denied" {
		t.Fatalf("foreign access=%+v, want denied", foreign)
	}
}

func TestWorkspaceAuditLogRecordsSuccessAndDenialWithoutContent(t *testing.T) {
	cfg, closeFn := newWorkspaceACLTestConfig(t)
	defer closeFn()
	seedWorkspaceACL(t, cfg.DB, "user-a", "user-b", "payments", "")

	ownerApp := workspaceACLApp("user-a", cfg)
	secretText := "# Secret\n\nClassified audit payload."
	body, status := workspaceTestPost(t, ownerApp, "/workspace/write", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"path":       "docs/private-audit.md",
		"text":       secretText,
		"index":      false,
	})
	if status != http.StatusOK {
		t.Fatalf("owner write status=%d body=%s", status, body)
	}

	foreignApp := workspaceACLApp("user-b", cfg)
	body = workspaceTestGet(t, foreignApp, "/workspace/read?project_id=payments&branch=main&path=docs/private-audit.md", http.StatusForbidden)
	if bytes.Contains(body, []byte("docs/private-audit.md")) || bytes.Contains(body, []byte("Classified audit payload")) {
		t.Fatalf("denied read leaked details: %s", body)
	}

	body = workspaceTestGet(t, ownerApp, "/workspace/audit?project_id=payments&limit=20", http.StatusOK)
	if bytes.Contains(body, []byte("Classified audit payload")) || bytes.Contains(body, []byte("docs/private-audit.md")) {
		t.Fatalf("audit log leaked content/path: %s", body)
	}
	var logResp workspaceAuditLogResponse
	if err := json.Unmarshal(body, &logResp); err != nil {
		t.Fatal(err)
	}
	if !workspaceAuditHasEvent(logResp.Events, "write", "success") {
		t.Fatalf("audit events missing successful write: %+v", logResp.Events)
	}
	if !workspaceAuditHasEvent(logResp.Events, "read", "denied") {
		t.Fatalf("audit events missing denied read: %+v", logResp.Events)
	}
}

func TestWorkspaceContextBootstrapActiveAndInitGuidance(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)

	body, status := workspaceTestPost(t, app, "/workspace/write", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "context-gen-1",
		"collection":          "context_collection",
		"path":                "docs/context.md",
		"text":                "# Context\n\nBootstrap active collection.",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if status != http.StatusOK {
		t.Fatalf("write status=%d body=%s", status, body)
	}

	body = workspaceTestGet(t, app, "/workspace/context?project_id=payments&branch=main", http.StatusOK)
	var ctxResp workspaceContextResponse
	if err := json.Unmarshal(body, &ctxResp); err != nil {
		t.Fatal(err)
	}
	if ctxResp.RecommendedSearchType != "HYBRID" || !ctxResp.ExactReadRequired {
		t.Fatalf("context policy wrong: %+v", ctxResp)
	}
	if len(ctxResp.Projects) != 1 || len(ctxResp.Projects[0].Branches) != 1 {
		t.Fatalf("context projects=%+v, want one project/branch", ctxResp.Projects)
	}
	branch := ctxResp.Projects[0].Branches[0]
	if branch.ActiveGeneration != "context-gen-1" || branch.ActiveCollection != "context_collection" || branch.ActiveChunkCount == 0 {
		t.Fatalf("active branch context=%+v", branch)
	}

	body = workspaceTestGet(t, app, "/workspace/context?project_id=empty&branch=main", http.StatusOK)
	if err := json.Unmarshal(body, &ctxResp); err != nil {
		t.Fatal(err)
	}
	if len(ctxResp.Projects) != 1 || len(ctxResp.Projects[0].Branches) != 1 {
		t.Fatalf("empty context projects=%+v", ctxResp.Projects)
	}
	if len(ctxResp.Projects[0].Branches[0].InitializationPath) == 0 {
		t.Fatalf("empty context missing initialization guidance: %+v", ctxResp.Projects[0].Branches[0])
	}
}

func TestWorkspaceContextRespectsACLProjectList(t *testing.T) {
	cfg, closeFn := newWorkspaceACLTestConfig(t)
	defer closeFn()
	for _, userID := range []string{"user-a", "user-b", "user-c"} {
		if _, err := cfg.DB.Exec(`INSERT INTO users(id, email, is_superuser) VALUES (?, ?, 0)`, userID, userID+"@example.com"); err != nil {
			t.Fatalf("insert user %s: %v", userID, err)
		}
	}
	for _, ds := range []struct {
		id, owner string
	}{
		{"owned-a", "user-a"},
		{"shared-c", "user-c"},
		{"foreign-c", "user-c"},
	} {
		if _, err := cfg.DB.Exec(`INSERT INTO datasets(id, owner_id) VALUES (?, ?)`, ds.id, ds.owner); err != nil {
			t.Fatalf("insert dataset %s: %v", ds.id, err)
		}
	}
	shareWorkspaceACL(t, cfg.DB, "shared-c", "user-a", RoleViewer)

	app := workspaceACLApp("user-a", cfg)
	body := workspaceTestGet(t, app, "/workspace/context", http.StatusOK)
	var ctxResp workspaceContextResponse
	if err := json.Unmarshal(body, &ctxResp); err != nil {
		t.Fatal(err)
	}
	projects := workspaceContextProjectIDSet(ctxResp.Projects)
	if !projects["owned-a"] || !projects["shared-c"] {
		t.Fatalf("context projects=%v, want owned-a and shared-c", projects)
	}
	if projects["foreign-c"] {
		t.Fatalf("context leaked foreign project: %v", projects)
	}
}

func TestWorkspaceAPIViewerRoleMatrixForWorkspaceOps(t *testing.T) {
	cfg, closeFn := newWorkspaceACLTestConfig(t)
	defer closeFn()
	seedWorkspaceACL(t, cfg.DB, "user-a", "user-b", "payments", RoleViewer)

	ownerApp := workspaceACLApp("user-a", cfg)
	body, status := workspaceTestPost(t, ownerApp, "/workspace/write", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "acl-viewer-gen-1",
		"collection":          "acl_viewer_gen_1",
		"path":                "docs/viewer.md",
		"text":                "# Viewer\n\nShared viewer anchor.",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if status != http.StatusOK {
		t.Fatalf("owner write status=%d body=%s", status, body)
	}
	body, status = workspaceTestPost(t, ownerApp, "/workspace/commit", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"message":    "viewer-baseline",
	})
	if status != http.StatusOK {
		t.Fatalf("owner commit status=%d body=%s", status, body)
	}
	var commit workspaceCommitRecord
	if err := json.Unmarshal(body, &commit); err != nil {
		t.Fatal(err)
	}

	viewerApp := workspaceACLApp("user-b", cfg)
	for _, endpoint := range []string{
		"/workspace/context?project_id=payments&branch=main",
		"/workspace/ops/status?project_id=payments&branch=main",
		"/workspace/conflicts?project_id=payments&branch=main",
		"/workspace/read?project_id=payments&branch=main&path=docs/viewer.md",
	} {
		if got := workspaceTestGet(t, viewerApp, endpoint, http.StatusOK); len(got) == 0 {
			t.Fatalf("viewer endpoint %s returned empty body", endpoint)
		}
	}

	body, status = workspaceTestPost(t, viewerApp, "/workspace/commit", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"message":    "viewer-should-fail",
	})
	if status != http.StatusForbidden {
		t.Fatalf("viewer commit status=%d body=%s", status, body)
	}

	body, status = workspaceTestPost(t, viewerApp, "/workspace/revert", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"commit_id":  commit.CommitID,
	})
	if status != http.StatusForbidden {
		t.Fatalf("viewer revert status=%d body=%s", status, body)
	}

	body, status = workspaceTestPost(t, viewerApp, "/workspace/gc", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"dry_run":    true,
	})
	if status != http.StatusForbidden {
		t.Fatalf("viewer gc status=%d body=%s", status, body)
	}
}

func TestWorkspaceAPIForeignProjectEndpointsDoNotLeakMetadata(t *testing.T) {
	cfg, closeFn := newWorkspaceACLTestConfig(t)
	defer closeFn()
	seedWorkspaceACL(t, cfg.DB, "user-a", "user-b", "payments", "")

	ownerApp := workspaceACLApp("user-a", cfg)
	body, status := workspaceTestPost(t, ownerApp, "/workspace/write", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "acl-hidden-gen",
		"collection":          "acl_hidden_collection",
		"path":                "docs/private.md",
		"text":                "# Hidden\n\nSensitive ACL anchor.",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if status != http.StatusOK {
		t.Fatalf("owner write status=%d body=%s", status, body)
	}

	foreignApp := workspaceACLApp("user-b", cfg)
	for _, endpoint := range []string{
		"/workspace/context?project_id=payments&branch=main",
		"/workspace/ops/status?project_id=payments&branch=main",
		"/workspace/conflicts?project_id=payments&branch=main",
		"/workspace/audit?project_id=payments&limit=20",
	} {
		body = workspaceTestGet(t, foreignApp, endpoint, http.StatusForbidden)
		for _, forbidden := range []string{
			"docs/private.md",
			"Sensitive ACL anchor",
			"acl-hidden-gen",
			"acl_hidden_collection",
		} {
			if bytes.Contains(body, []byte(forbidden)) {
				t.Fatalf("endpoint %s leaked %q in body=%s", endpoint, forbidden, body)
			}
		}
	}
}

func TestWorkspaceContextReportsCorruptManifestPerProject(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	projectRoot := workspaceProjectRoot(cfg, "broken", "main")
	if err := os.MkdirAll(filepath.Join(projectRoot, "docs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "docs", "broken.md"), []byte("# Broken\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manifestPath := workspaceManifestPath(cfg, "broken", "main")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte("{not-json"), 0644); err != nil {
		t.Fatal(err)
	}

	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)
	body := workspaceTestGet(t, app, "/workspace/context", http.StatusOK)
	var ctxResp workspaceContextResponse
	if err := json.Unmarshal(body, &ctxResp); err != nil {
		t.Fatal(err)
	}
	if len(ctxResp.Projects) != 1 || len(ctxResp.Projects[0].Branches) != 1 {
		t.Fatalf("context projects=%+v, want corrupt project branch", ctxResp.Projects)
	}
	if ctxResp.Projects[0].Branches[0].Error == "" {
		t.Fatalf("corrupt manifest was not reported: %+v", ctxResp.Projects[0].Branches[0])
	}
}

func TestWorkspaceContextArtifactsRegistryListsAndReindexes(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)

	apiPath, _, err := workspaceFilePath(cfg, "payments", "main", "artifacts/api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(apiPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(apiPath, []byte("openapi: 3.0.0\npaths:\n  /payments:\n    get:\n      summary: Payment status lookup\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ddlPath, _, err := workspaceFilePath(cfg, "payments", "main", "artifacts/db/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ddlPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ddlPath, []byte("CREATE TABLE payment_events(id text primary key);\n"), 0644); err != nil {
		t.Fatal(err)
	}
	registry := workspaceContextArtifactRegistry{
		Version: workspaceArtifactRegistryVersion,
		Includes: []workspaceContextArtifactInclude{{
			ProjectID: "payments",
			Branch:    "main",
			Glob:      "artifacts/api/**/*.yaml",
			Kind:      "openapi",
			Room:      "api",
			Tags:      []string{"payments"},
		}},
		Artifacts: []workspaceContextArtifactRequest{{
			ID:        "payments-ddl",
			ProjectID: "payments",
			Branch:    "main",
			Path:      "artifacts/db/schema.sql",
			Kind:      "ddl",
			Room:      "db",
			Tags:      []string{"schema"},
		}},
	}
	raw, _ := json.MarshalIndent(registry, "", "  ")
	if err := os.MkdirAll(filepath.Dir(workspaceContextArtifactRegistryPath(cfg)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspaceContextArtifactRegistryPath(cfg), append(raw, '\n'), 0644); err != nil {
		t.Fatal(err)
	}

	body := workspaceTestGet(t, app, "/workspace/context/artifacts?project_id=payments&branch=main", http.StatusOK)
	var listResp workspaceContextArtifactsResponse
	if err := json.Unmarshal(body, &listResp); err != nil {
		t.Fatal(err)
	}
	if listResp.Total != 2 {
		t.Fatalf("artifacts total=%d, want 2; body=%s", listResp.Total, body)
	}
	if listResp.Artifacts[0].Digest == "" || !listResp.Artifacts[0].Exists {
		t.Fatalf("artifact metadata missing: %+v", listResp.Artifacts[0])
	}

	body, status := workspaceTestPost(t, app, "/workspace/context/artifacts/reindex", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "artifacts-gen-1",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if status != http.StatusOK {
		t.Fatalf("artifact reindex status=%d body=%s", status, body)
	}
	var reindexResp workspaceReindexArtifactsResponse
	if err := json.Unmarshal(body, &reindexResp); err != nil {
		t.Fatal(err)
	}
	if len(reindexResp.Artifacts) != 2 || len(reindexResp.Results) != 2 {
		t.Fatalf("reindex artifacts=%d results=%d, want 2/2", len(reindexResp.Artifacts), len(reindexResp.Results))
	}
	search := (&mcpHandler{cfg: cfg}).executeToolInner(context.Background(), nil, "workspace_search", map[string]any{
		"project_id":   "payments",
		"branch":       "main",
		"search_query": "payment status lookup",
		"search_type":  "CHUNKS_LEXICAL",
	})
	if search.IsError || !strings.Contains(search.Content[0].Text, "artifacts/api/openapi.yaml") {
		t.Fatalf("artifact search failed or missing source: %+v", search.Content)
	}
}

func TestWorkspaceContextArtifactsRegistryRejectsBrokenJSON(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)

	registryPath := workspaceContextArtifactRegistryPath(cfg)
	if err := os.MkdirAll(filepath.Dir(registryPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, []byte("{broken-json"), 0644); err != nil {
		t.Fatal(err)
	}

	body := workspaceTestGet(t, app, "/workspace/context/artifacts?project_id=payments&branch=main", http.StatusBadRequest)
	if !strings.Contains(string(body), "error") {
		t.Fatalf("broken registry response missing error: %s", body)
	}
}

func TestWorkspaceContextArtifactsBranchDeletedAndIndexFiltering(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)

	mainAPIPath, _, err := workspaceFilePath(cfg, "payments", "main", "artifacts/api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(mainAPIPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mainAPIPath, []byte("openapi: 3.0.0\ninfo:\n  title: Main API\n"), 0644); err != nil {
		t.Fatal(err)
	}

	devAPIPath, _, err := workspaceFilePath(cfg, "payments", "dev", "artifacts/api/dev-openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(devAPIPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(devAPIPath, []byte("openapi: 3.0.0\ninfo:\n  title: Dev API\n"), 0644); err != nil {
		t.Fatal(err)
	}

	registry := workspaceContextArtifactRegistry{
		Version: workspaceArtifactRegistryVersion,
		Includes: []workspaceContextArtifactInclude{
			{
				ProjectID: "payments",
				Branch:    "main",
				Glob:      "artifacts/api/**/*.yaml",
				Kind:      "openapi",
				Tags:      []string{"main"},
			},
			{
				ProjectID: "payments",
				Branch:    "dev",
				Glob:      "artifacts/api/**/*.yaml",
				Kind:      "openapi",
				Tags:      []string{"dev"},
			},
		},
		Artifacts: []workspaceContextArtifactRequest{
			{
				ID:        "payments-missing-runbook",
				ProjectID: "payments",
				Branch:    "main",
				Path:      "docs/runbooks/missing.md",
				Kind:      "runbook",
				Tags:      []string{"deleted"},
			},
			{
				ID:               "payments-tf-noindex",
				ProjectID:        "payments",
				Branch:           "main",
				Path:             "artifacts/infra/network.tf",
				Kind:             "terraform",
				Index:            boolPtr(false),
				IncludeInContext: boolPtr(true),
			},
		},
	}

	tfPath, _, err := workspaceFilePath(cfg, "payments", "main", "artifacts/infra/network.tf")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(tfPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tfPath, []byte(`resource "null_resource" "net" {}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	raw, _ := json.MarshalIndent(registry, "", "  ")
	if err := os.MkdirAll(filepath.Dir(workspaceContextArtifactRegistryPath(cfg)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspaceContextArtifactRegistryPath(cfg), append(raw, '\n'), 0644); err != nil {
		t.Fatal(err)
	}

	mainBody := workspaceTestGet(t, app, "/workspace/context/artifacts?project_id=payments&branch=main", http.StatusOK)
	var mainResp workspaceContextArtifactsResponse
	if err := json.Unmarshal(mainBody, &mainResp); err != nil {
		t.Fatal(err)
	}
	if mainResp.Total != 3 {
		t.Fatalf("main artifacts total=%d, want 3; body=%s", mainResp.Total, mainBody)
	}
	foundMissing := false
	foundNoIndex := false
	for _, artifact := range mainResp.Artifacts {
		switch artifact.ID {
		case "payments-missing-runbook":
			foundMissing = true
			if artifact.Exists || artifact.Digest != "" || artifact.Bytes != 0 {
				t.Fatalf("missing artifact should be flagged deleted: %+v", artifact)
			}
		case "payments-tf-noindex":
			foundNoIndex = true
			if artifact.Index {
				t.Fatalf("terraform artifact should have index=false: %+v", artifact)
			}
		}
		if strings.Contains(artifact.Path, "dev-openapi.yaml") {
			t.Fatalf("main branch should not include dev artifact: %+v", artifact)
		}
	}
	if !foundMissing || !foundNoIndex {
		t.Fatalf("expected deleted and no-index artifacts in main response: %+v", mainResp.Artifacts)
	}

	devBody := workspaceTestGet(t, app, "/workspace/context/artifacts?project_id=payments&branch=dev", http.StatusOK)
	var devResp workspaceContextArtifactsResponse
	if err := json.Unmarshal(devBody, &devResp); err != nil {
		t.Fatal(err)
	}
	if devResp.Total != 1 || devResp.Artifacts[0].Branch != "dev" || !strings.Contains(devResp.Artifacts[0].Path, "dev-openapi.yaml") {
		t.Fatalf("dev branch artifacts=%+v, want only dev include", devResp.Artifacts)
	}

	indexOnlyBody := workspaceTestGet(t, app, "/workspace/context/artifacts?project_id=payments&branch=main&index_only=true", http.StatusOK)
	var indexOnlyResp workspaceContextArtifactsResponse
	if err := json.Unmarshal(indexOnlyBody, &indexOnlyResp); err != nil {
		t.Fatal(err)
	}
	for _, artifact := range indexOnlyResp.Artifacts {
		if artifact.ID == "payments-tf-noindex" {
			t.Fatalf("index_only response leaked index=false artifact: %+v", indexOnlyResp.Artifacts)
		}
	}
}

func TestWorkspaceContextArtifactsReindexFiltersByIDAndKind(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)

	apiPath, _, err := workspaceFilePath(cfg, "payments", "main", "artifacts/api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(apiPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(apiPath, []byte("openapi: 3.0.0\npaths:\n  /health:\n    get:\n      summary: Health\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ddlPath, _, err := workspaceFilePath(cfg, "payments", "main", "artifacts/db/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ddlPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ddlPath, []byte("CREATE TABLE invoices(id text primary key);\n"), 0644); err != nil {
		t.Fatal(err)
	}

	registry := workspaceContextArtifactRegistry{
		Version: workspaceArtifactRegistryVersion,
		Artifacts: []workspaceContextArtifactRequest{
			{
				ID:        "payments-openapi",
				ProjectID: "payments",
				Branch:    "main",
				Path:      "artifacts/api/openapi.yaml",
				Kind:      "openapi",
			},
			{
				ID:        "payments-ddl",
				ProjectID: "payments",
				Branch:    "main",
				Path:      "artifacts/db/schema.sql",
				Kind:      "ddl",
			},
		},
	}
	raw, _ := json.MarshalIndent(registry, "", "  ")
	if err := os.MkdirAll(filepath.Dir(workspaceContextArtifactRegistryPath(cfg)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspaceContextArtifactRegistryPath(cfg), append(raw, '\n'), 0644); err != nil {
		t.Fatal(err)
	}

	body, status := workspaceTestPost(t, app, "/workspace/context/artifacts/reindex", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "artifacts-gen-selective",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
		"artifact_ids":        []string{"payments-ddl"},
		"kinds":               []string{"ddl"},
	})
	if status != http.StatusOK {
		t.Fatalf("selective artifact reindex status=%d body=%s", status, body)
	}
	var reindexResp workspaceReindexArtifactsResponse
	if err := json.Unmarshal(body, &reindexResp); err != nil {
		t.Fatal(err)
	}
	if len(reindexResp.Artifacts) != 1 || reindexResp.Artifacts[0].ID != "payments-ddl" {
		t.Fatalf("reindexed artifacts=%+v, want only ddl artifact", reindexResp.Artifacts)
	}
	if len(reindexResp.Results) != 1 {
		t.Fatalf("reindex results=%d, want 1", len(reindexResp.Results))
	}
	search := (&mcpHandler{cfg: cfg}).executeToolInner(context.Background(), nil, "workspace_search", map[string]any{
		"project_id":   "payments",
		"branch":       "main",
		"search_query": "invoices primary key",
		"search_type":  "CHUNKS_LEXICAL",
	})
	if search.IsError || !strings.Contains(search.Content[0].Text, "artifacts/db/schema.sql") {
		t.Fatalf("ddl artifact search failed or missing source: %+v", search.Content)
	}
	if strings.Contains(search.Content[0].Text, "artifacts/api/openapi.yaml") {
		t.Fatalf("selective reindex should not surface openapi artifact: %+v", search.Content)
	}

	body, status = workspaceTestPost(t, app, "/workspace/context/artifacts/reindex", map[string]any{
		"project_id":   "payments",
		"branch":       "main",
		"generation":   "artifacts-gen-empty",
		"artifact_ids": []string{"missing-id"},
	})
	if status != http.StatusBadRequest || !strings.Contains(string(body), "no matching context artifacts") {
		t.Fatalf("missing artifact id should fail deterministically: status=%d body=%s", status, body)
	}
}

func boolPtr(v bool) *bool {
	return &v
}

func TestWorkspaceSearchHonorsProjectRBAC(t *testing.T) {
	cfg, closeFn := newWorkspaceACLTestConfig(t)
	defer closeFn()
	seedWorkspaceACL(t, cfg.DB, "user-a", "user-b", "payments", "")
	app := workspaceACLApp("user-a", cfg)

	body, status := workspaceTestPost(t, app, "/workspace/write", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "acl-gen-1",
		"collection":          "acl_kb",
		"path":                "docs/search.md",
		"text":                "# Search\n\nSecret payment anchor.",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if status != http.StatusOK {
		t.Fatalf("owner write status=%d body=%s", status, body)
	}

	app = workspaceACLApp("user-b", cfg)
	body, status = workspaceTestPost(t, app, "/search/text", map[string]any{
		"query_text": "secret payment anchor",
		"query_type": "CHUNKS_LEXICAL",
		"collection": "acl_kb",
		"top_k":      5,
	})
	if status != http.StatusOK {
		t.Fatalf("search status=%d body=%s", status, body)
	}
	var hits []map[string]any
	if err := json.Unmarshal(body, &hits); err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("foreign user saw workspace search hits: %+v", hits)
	}

	shareWorkspaceACL(t, cfg.DB, "payments", "user-b", RoleViewer)
	body, status = workspaceTestPost(t, app, "/search/text", map[string]any{
		"query_text": "secret payment anchor",
		"query_type": "CHUNKS_LEXICAL",
		"collection": "acl_kb",
		"top_k":      5,
	})
	if status != http.StatusOK {
		t.Fatalf("shared search status=%d body=%s", status, body)
	}
	if err := json.Unmarshal(body, &hits); err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("shared user hits=%d, want 1 body=%s", len(hits), body)
	}
}

func TestWorkspaceAPIReconcileBuildsGenerationFromCurrentMarkdown(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)

	for path, text := range map[string]string{
		"docs/a.md": "# A\n\nAlpha source.",
		"docs/b.md": "# B\n\nBeta source.",
	} {
		body, status := workspaceTestPost(t, app, "/workspace/write", map[string]any{
			"project_id": "payments",
			"branch":     "main",
			"path":       path,
			"text":       text,
			"index":      false,
		})
		if status != http.StatusOK {
			t.Fatalf("write %s status=%d body=%s", path, status, body)
		}
	}

	body, status := workspaceTestPost(t, app, "/workspace/reconcile", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "gen-1",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if status != http.StatusOK {
		t.Fatalf("reconcile gen1 status=%d body=%s", status, body)
	}
	var first workspaceReconcileResponse
	if err := json.Unmarshal(body, &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Paths) != 2 || first.Paths[0] != "docs/a.md" || first.Paths[1] != "docs/b.md" {
		t.Fatalf("paths=%v", first.Paths)
	}
	if first.ActiveGeneration != "gen-1" {
		t.Fatalf("active generation=%q, want gen-1", first.ActiveGeneration)
	}

	bPath, _, err := workspaceFilePath(cfg, "payments", "main", "docs/b.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(bPath); err != nil {
		t.Fatal(err)
	}
	body, status = workspaceTestPost(t, app, "/workspace/reconcile", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "gen-2",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if status != http.StatusOK {
		t.Fatalf("reconcile gen2 status=%d body=%s", status, body)
	}
	manifest, _, err := loadWorkspaceManifest(cfg, "payments", "main")
	if err != nil {
		t.Fatal(err)
	}
	activeChunks := manifest.ListChunks(workspace.ChunkFilter{ActiveOnly: true})
	if len(activeChunks) == 0 {
		t.Fatal("expected active chunks")
	}
	for _, rec := range activeChunks {
		if rec.Path == "docs/b.md" {
			t.Fatalf("removed file appeared in active generation: %+v", rec)
		}
	}
}

func TestWorkspaceIndexJobsRecordSuccessFailureAndRetry(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)

	body, status := workspaceTestPost(t, app, "/workspace/write", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"path":       "docs/job.md",
		"text":       "# Job\n\nDurable indexing job.",
		"index":      false,
	})
	if status != http.StatusOK {
		t.Fatalf("write status=%d body=%s", status, body)
	}
	body, status = workspaceTestPost(t, app, "/workspace/reconcile", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "job-gen-1",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if status != http.StatusOK {
		t.Fatalf("reconcile status=%d body=%s", status, body)
	}
	jobsBody := workspaceTestGet(t, app, "/workspace/jobs?project_id=payments&branch=main", http.StatusOK)
	var jobsResp struct {
		Jobs []workspaceIndexJob `json:"jobs"`
	}
	if err := json.Unmarshal(jobsBody, &jobsResp); err != nil {
		t.Fatal(err)
	}
	if len(jobsResp.Jobs) != 1 || jobsResp.Jobs[0].Status != workspaceIndexJobCompleted || jobsResp.Jobs[0].Request.Operation != "reconcile" {
		t.Fatalf("jobs after reconcile=%+v", jobsResp.Jobs)
	}

	body, status = workspaceTestPost(t, app, "/workspace/reindex", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "job-gen-retry",
		"paths":               []string{"docs/missing.md"},
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if status != http.StatusBadRequest {
		t.Fatalf("missing reindex status=%d body=%s", status, body)
	}
	jobsBody = workspaceTestGet(t, app, "/workspace/jobs?project_id=payments&branch=main&status=failed", http.StatusOK)
	if err := json.Unmarshal(jobsBody, &jobsResp); err != nil {
		t.Fatal(err)
	}
	if len(jobsResp.Jobs) != 1 {
		t.Fatalf("failed jobs=%+v, want 1", jobsResp.Jobs)
	}
	failed := jobsResp.Jobs[0]
	if failed.Attempts != 1 || failed.LastError == "" || failed.Request.Operation != "reindex" {
		t.Fatalf("bad failed job: %+v", failed)
	}

	body, status = workspaceTestPost(t, app, "/workspace/write", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"path":       "docs/missing.md",
		"text":       "# Missing\n\nRetry now succeeds.",
		"index":      false,
	})
	if status != http.StatusOK {
		t.Fatalf("write missing status=%d body=%s", status, body)
	}
	body, status = workspaceTestPost(t, app, "/workspace/jobs/retry", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"job_id":     failed.ID,
	})
	if status != http.StatusOK {
		t.Fatalf("retry status=%d body=%s", status, body)
	}
	var retryResp workspaceRetryIndexJobResponse
	if err := json.Unmarshal(body, &retryResp); err != nil {
		t.Fatal(err)
	}
	if retryResp.Job.Status != workspaceIndexJobCompleted || retryResp.Job.Attempts != 2 || retryResp.Job.LastError != "" {
		t.Fatalf("retry job=%+v, want completed attempts=2", retryResp.Job)
	}
}

func TestWorkspaceEnqueueIndexJobCoalescesDuplicatePayload(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)

	payload := map[string]any{
		"operation":           "reindex",
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "enqueue-gen-1",
		"paths":               []string{"docs/b.md", "docs/a.md"},
		"activate_generation": true,
	}
	body, status := workspaceTestPost(t, app, "/workspace/jobs/enqueue", payload)
	if status != http.StatusOK {
		t.Fatalf("enqueue status=%d body=%s", status, body)
	}
	var first struct {
		Job workspaceIndexJob `json:"job"`
	}
	if err := json.Unmarshal(body, &first); err != nil {
		t.Fatal(err)
	}
	if first.Job.Status != workspaceIndexJobPending || first.Job.Attempts != 0 {
		t.Fatalf("first job=%+v, want pending attempts=0", first.Job)
	}

	payload["paths"] = []string{"docs/a.md", "docs/b.md"}
	body, status = workspaceTestPost(t, app, "/workspace/jobs/enqueue", payload)
	if status != http.StatusOK {
		t.Fatalf("duplicate enqueue status=%d body=%s", status, body)
	}
	var second struct {
		Job workspaceIndexJob `json:"job"`
	}
	if err := json.Unmarshal(body, &second); err != nil {
		t.Fatal(err)
	}
	if second.Job.ID != first.Job.ID {
		t.Fatalf("duplicate job id=%q, want %q", second.Job.ID, first.Job.ID)
	}

	jobs, err := listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{ProjectID: "payments", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != first.Job.ID {
		t.Fatalf("jobs=%+v, want one coalesced job", jobs)
	}
}

func TestWorkspaceIndexWorkerProcessesPendingJob(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)

	body, status := workspaceTestPost(t, app, "/workspace/write", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"path":       "docs/async.md",
		"text":       "# Async\n\nWorker indexed markdown.",
		"index":      false,
	})
	if status != http.StatusOK {
		t.Fatalf("write status=%d body=%s", status, body)
	}
	job, err := enqueueWorkspaceIndexJobFromPayload(cfg, workspaceIndexJobPayload{
		Operation:          "reconcile",
		ProjectID:          "payments",
		Branch:             "main",
		Generation:         "async-gen-1",
		ChunkStrategy:      "paragraph",
		MinChunkChars:      1,
		ActivateGeneration: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != workspaceIndexJobPending {
		t.Fatalf("enqueued status=%s, want pending", job.Status)
	}

	stop := StartWorkspaceIndexWorker(context.Background(), cfg, WorkspaceIndexWorkerOptions{
		Interval:    10 * time.Millisecond,
		Backoff:     10 * time.Millisecond,
		MaxAttempts: 2,
		Logf:        t.Logf,
	})
	defer stop()

	waitForWorkspaceCondition(t, 2*time.Second, func() bool {
		done, err := loadWorkspaceIndexJobPath(workspaceIndexJobPath(cfg, "payments", "main", job.ID))
		if err != nil || done.Status != workspaceIndexJobCompleted || done.Attempts != 1 || done.LastError != "" {
			return false
		}
		manifest, _, err := loadWorkspaceManifest(cfg, "payments", "main")
		if err != nil {
			return false
		}
		return manifest.ActiveGeneration == "async-gen-1" && len(manifest.ListChunks(workspace.ChunkFilter{ActiveOnly: true})) > 0
	})
}

func TestWorkspaceIndexWorkerBackoffAndDeadLetter(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()

	job, err := enqueueWorkspaceIndexJobFromPayload(cfg, workspaceIndexJobPayload{
		Operation:          "reindex",
		ProjectID:          "payments",
		Branch:             "main",
		Generation:         "dead-letter-gen",
		Paths:              []string{"docs/missing.md"},
		ChunkStrategy:      "paragraph",
		MinChunkChars:      1,
		ActivateGeneration: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	opts := WorkspaceIndexWorkerOptions{
		Interval:    time.Hour,
		Backoff:     time.Millisecond,
		MaxAttempts: 2,
		Logf:        t.Logf,
	}
	workspaceIndexWorkerTick(context.Background(), cfg, normalizeWorkspaceIndexWorkerOptions(opts))

	failed, err := loadWorkspaceIndexJobPath(workspaceIndexJobPath(cfg, "payments", "main", job.ID))
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != workspaceIndexJobFailed || failed.Attempts != 1 || failed.NextRunAt == "" || failed.LastError == "" {
		t.Fatalf("after first tick=%+v, want failed with next_run_at", failed)
	}

	time.Sleep(2 * time.Millisecond)
	workspaceIndexWorkerTick(context.Background(), cfg, normalizeWorkspaceIndexWorkerOptions(opts))
	dead, err := loadWorkspaceIndexJobPath(workspaceIndexJobPath(cfg, "payments", "main", job.ID))
	if err != nil {
		t.Fatal(err)
	}
	if dead.Status != workspaceIndexJobDeadLetter || dead.Attempts != 2 || dead.DeadLetterAt == "" || dead.NextRunAt != "" || dead.LastError == "" {
		t.Fatalf("after second tick=%+v, want dead letter", dead)
	}
}

func TestWorkspaceOpsStatusReportsJobsWatcherAuditAndMetrics(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	cfg.WorkspaceWatcher = NewWorkspaceWatchState()
	cfg.WorkspaceWatcher.recordBranchChange(workspaceWatchKey{ProjectID: "payments", Branch: "main"}, 1)

	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)

	writeSuccessBefore := testutil.ToFloat64(metrics.WorkspaceAuditEventsTotal.WithLabelValues("rest", "write", "success"))
	body, status := workspaceTestPost(t, app, "/workspace/write", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"path":       "docs/ops.md",
		"text":       "# Ops\n\nOperational audit event.",
		"index":      false,
	})
	if status != http.StatusOK {
		t.Fatalf("write status=%d body=%s", status, body)
	}
	if got := testutil.ToFloat64(metrics.WorkspaceAuditEventsTotal.WithLabelValues("rest", "write", "success")); got < writeSuccessBefore+1 {
		t.Fatalf("write audit counter=%v, want at least %v", got, writeSuccessBefore+1)
	}

	job, err := enqueueWorkspaceIndexJobFromPayload(cfg, workspaceIndexJobPayload{
		Operation:          "reindex",
		ProjectID:          "payments",
		Branch:             "main",
		Generation:         "ops-dead-letter-gen",
		Paths:              []string{"docs/missing.md"},
		ChunkStrategy:      "paragraph",
		MinChunkChars:      1,
		ActivateGeneration: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	opts := normalizeWorkspaceIndexWorkerOptions(WorkspaceIndexWorkerOptions{
		Backoff:     time.Millisecond,
		MaxAttempts: 2,
		Logf:        t.Logf,
	})
	workspaceIndexWorkerTick(context.Background(), cfg, opts)
	time.Sleep(2 * time.Millisecond)
	workspaceIndexWorkerTick(context.Background(), cfg, opts)
	dead, err := loadWorkspaceIndexJobPath(workspaceIndexJobPath(cfg, "payments", "main", job.ID))
	if err != nil {
		t.Fatal(err)
	}
	if dead.Status != workspaceIndexJobDeadLetter {
		t.Fatalf("job status=%s, want dead_letter", dead.Status)
	}

	body = workspaceTestGet(t, app, "/workspace/ops/status?project_id=payments&branch=main", http.StatusOK)
	var resp workspaceOpsStatus
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Jobs.Total != 1 || resp.Jobs.ByStatus[string(workspaceIndexJobDeadLetter)] != 1 || resp.Jobs.DeadLetterCount != 1 {
		t.Fatalf("jobs status=%+v, want one dead-letter job", resp.Jobs)
	}
	if resp.Jobs.OldestDeadLetter == "" || resp.Jobs.NewestUpdatedAt == "" {
		t.Fatalf("job timestamps missing: %+v", resp.Jobs)
	}
	if resp.Watcher.PendingBranches != 1 {
		t.Fatalf("watcher pending=%d, want 1", resp.Watcher.PendingBranches)
	}
	if resp.Audit.TotalEvents == 0 || resp.Audit.BySource["rest"] == 0 || resp.Audit.ByResult["success"] == 0 {
		t.Fatalf("audit summary missing rest success event: %+v", resp.Audit)
	}
	if got := testutil.ToFloat64(metrics.WorkspaceIndexJobs.WithLabelValues("dead_letter")); got != 1 {
		t.Fatalf("dead_letter gauge=%v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.WorkspaceIndexDeadLetters); got != 1 {
		t.Fatalf("dead-letter total gauge=%v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.WorkspaceWatcherPendingBranches); got != 1 {
		t.Fatalf("watcher pending gauge=%v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.WorkspaceAuditStoredEvents); got < 1 {
		t.Fatalf("audit stored gauge=%v, want >=1", got)
	}
}

func TestWorkspaceOpsStatusSkipsMalformedAuditRows(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()

	if err := recordWorkspaceAuditEvent(cfg, workspaceAuditEvent{
		Source:    "rest",
		Operation: "read",
		ProjectID: "payments",
		Branch:    "main",
		Result:    "success",
	}); err != nil {
		t.Fatal(err)
	}
	path := workspaceAuditPath(cfg, "payments", time.Now().UTC())
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{not-json}\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	status, err := collectWorkspaceOpsStatus(cfg, workspaceOpsStatusRequest{ProjectID: "payments", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if status.Audit.TotalEvents != 1 || status.Audit.BySource["rest"] != 1 {
		t.Fatalf("audit status=%+v, want only valid row counted", status.Audit)
	}
}

func TestWorkspaceOpsStatusProjectScopeIncludesAllBranches(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()

	for _, branch := range []string{"main", "dev"} {
		if _, err := enqueueWorkspaceIndexJobFromPayload(cfg, workspaceIndexJobPayload{
			Operation:  "reconcile",
			ProjectID:  "payments",
			Branch:     branch,
			Generation: "ops-" + branch,
		}); err != nil {
			t.Fatal(err)
		}
	}
	projectStatus, err := collectWorkspaceOpsStatus(cfg, workspaceOpsStatusRequest{ProjectID: "payments"})
	if err != nil {
		t.Fatal(err)
	}
	if projectStatus.Jobs.Total != 2 || projectStatus.Jobs.ByStatus[string(workspaceIndexJobPending)] != 2 {
		t.Fatalf("project jobs=%+v, want pending jobs from both branches", projectStatus.Jobs)
	}
	mainStatus, err := collectWorkspaceOpsStatus(cfg, workspaceOpsStatusRequest{ProjectID: "payments", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if mainStatus.Jobs.Total != 1 {
		t.Fatalf("main jobs=%+v, want one branch-scoped job", mainStatus.Jobs)
	}
}

func TestWorkspaceOpsStatusHandlesInvalidJobTimestamp(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()

	job := workspaceIndexJob{
		ID:        "job_bad_time",
		Status:    workspaceIndexJobPending,
		CreatedAt: "not-a-timestamp",
		UpdatedAt: "also-not-a-timestamp",
		Request: workspaceIndexJobPayload{
			Operation:  "reconcile",
			ProjectID:  "payments",
			Branch:     "main",
			Generation: "ops-bad-time",
		},
	}
	if err := saveWorkspaceIndexJobPath(workspaceIndexJobPath(cfg, "payments", "main", job.ID), job); err != nil {
		t.Fatal(err)
	}
	status, err := collectWorkspaceOpsStatus(cfg, workspaceOpsStatusRequest{ProjectID: "payments", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if status.Jobs.Total != 1 || status.Jobs.ByStatus[string(workspaceIndexJobPending)] != 1 {
		t.Fatalf("jobs=%+v, want one pending job", status.Jobs)
	}
	if status.Jobs.MaxLagSeconds != 0 {
		t.Fatalf("max lag=%v, want 0 for invalid timestamp", status.Jobs.MaxLagSeconds)
	}
}

func TestWorkspaceConflictsDetectsFilesystemDrift(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)

	for _, item := range []struct {
		path string
		text string
	}{
		{"docs/a.md", "# A\n\nOriginal A."},
		{"docs/delete-me.md", "# Delete\n\nOriginal delete."},
	} {
		body, status := workspaceTestPost(t, app, "/workspace/write", map[string]any{
			"project_id":          "payments",
			"branch":              "main",
			"generation":          "conflict-gen-1",
			"path":                item.path,
			"text":                item.text,
			"chunk_strategy":      "paragraph",
			"min_chunk_chars":     1,
			"activate_generation": true,
		})
		if status != http.StatusOK {
			t.Fatalf("write %s status=%d body=%s", item.path, status, body)
		}
	}
	aPath, _, err := workspaceFilePath(cfg, "payments", "main", "docs/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aPath, []byte("# A\n\nChanged A."), 0644); err != nil {
		t.Fatal(err)
	}
	newPath, _, err := workspaceFilePath(cfg, "payments", "main", "docs/new.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("# New\n\nNot indexed yet."), 0644); err != nil {
		t.Fatal(err)
	}
	deletePath, _, err := workspaceFilePath(cfg, "payments", "main", "docs/delete-me.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(deletePath); err != nil {
		t.Fatal(err)
	}

	body := workspaceTestGet(t, app, "/workspace/conflicts?project_id=payments&branch=main", http.StatusOK)
	var resp workspaceConflictResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.HasConflicts {
		t.Fatalf("has_conflicts=false; body=%s", body)
	}
	if !workspaceConflictHasPath(resp.DirtyPaths, "docs/a.md") {
		t.Fatalf("dirty paths=%+v, want docs/a.md", resp.DirtyPaths)
	}
	if !workspaceConflictHasPath(resp.UnindexedPaths, "docs/new.md") {
		t.Fatalf("unindexed paths=%+v, want docs/new.md", resp.UnindexedPaths)
	}
	if !workspaceConflictHasPath(resp.MissingIndexedPaths, "docs/delete-me.md") {
		t.Fatalf("missing paths=%+v, want docs/delete-me.md", resp.MissingIndexedPaths)
	}
	mcpResp := (&mcpHandler{cfg: cfg}).executeToolInner(context.Background(), nil, "workspace_conflicts", map[string]any{
		"project_id": "payments",
		"branch":     "main",
	})
	if mcpResp.IsError || !strings.Contains(mcpResp.Content[0].Text, "filesystem_truth_wins") {
		t.Fatalf("workspace_conflicts MCP failed: %+v", mcpResp.Content)
	}
}

func TestWorkspaceConflictsMissingActiveGenerationIsActionable(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)

	body := workspaceTestGet(t, app, "/workspace/conflicts?project_id=payments&branch=main", http.StatusOK)
	var resp workspaceConflictResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.HasConflicts {
		t.Fatalf("has_conflicts=false; body=%s", body)
	}
	if resp.ActiveGeneration != "" {
		t.Fatalf("active_generation=%q, want empty", resp.ActiveGeneration)
	}
	if !workspaceConflictHasAction(resp.RecommendedActions, "workspace_reconcile") {
		t.Fatalf("recommended_actions=%v, want workspace_reconcile guidance", resp.RecommendedActions)
	}
}

func TestWorkspaceConflictsIncludeWatcherAndJobSignals(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	cfg.WorkspaceWatcher = NewWorkspaceWatchState()
	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)

	body, status := workspaceTestPost(t, app, "/workspace/write", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "conflict-signal-gen-1",
		"path":                "docs/ops.md",
		"text":                "# Ops\n\nIndexed baseline.",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if status != http.StatusOK {
		t.Fatalf("baseline write status=%d body=%s", status, body)
	}

	cfg.WorkspaceWatcher.recordBranchError(workspaceWatchKey{ProjectID: "payments", Branch: "main"}, errors.New("watcher backlog"))

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, job := range []workspaceIndexJob{
		{
			ID:        "job_failed_conflicts",
			Status:    workspaceIndexJobFailed,
			Attempts:  1,
			CreatedAt: now,
			UpdatedAt: now,
			LastError: "embed timeout",
			Request: workspaceIndexJobPayload{
				Operation:  "reconcile",
				ProjectID:  "payments",
				Branch:     "main",
				Generation: "conflict-gen-failed",
			},
		},
		{
			ID:           "job_dead_conflicts",
			Status:       workspaceIndexJobDeadLetter,
			Attempts:     3,
			CreatedAt:    now,
			UpdatedAt:    now,
			DeadLetterAt: now,
			LastError:    "panic in worker",
			Request: workspaceIndexJobPayload{
				Operation:  "reconcile",
				ProjectID:  "payments",
				Branch:     "main",
				Generation: "conflict-gen-dead",
			},
		},
	} {
		if err := saveWorkspaceIndexJobPath(workspaceIndexJobPath(cfg, "payments", "main", job.ID), job); err != nil {
			t.Fatal(err)
		}
	}

	body = workspaceTestGet(t, app, "/workspace/conflicts?project_id=payments&branch=main", http.StatusOK)
	var resp workspaceConflictResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.HasConflicts {
		t.Fatalf("has_conflicts=false; body=%s", body)
	}
	if resp.Watcher.LastError != "watcher backlog" || !resp.Watcher.Pending {
		t.Fatalf("watcher=%+v, want pending watcher error", resp.Watcher)
	}
	if resp.JobsByStatus[string(workspaceIndexJobFailed)] != 1 || resp.JobsByStatus[string(workspaceIndexJobDeadLetter)] != 1 {
		t.Fatalf("jobs_by_status=%v, want failed=1 dead_letter=1", resp.JobsByStatus)
	}
	for _, want := range []string{"watcher error state", "failed entries", "dead_letter entries"} {
		if !workspaceConflictHasAction(resp.RecommendedActions, want) {
			t.Fatalf("recommended_actions=%v, want substring %q", resp.RecommendedActions, want)
		}
	}
}

func TestWorkspaceConflictsAfterRevertRequireReconcileToClearDrift(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)

	body, status := workspaceTestPost(t, app, "/workspace/write", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "conflict-revert-gen-1",
		"path":                "docs/a.md",
		"text":                "# A\n\nOriginal content.",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if status != http.StatusOK {
		t.Fatalf("write gen1 status=%d body=%s", status, body)
	}
	body, status = workspaceTestPost(t, app, "/workspace/commit", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"message":    "original",
	})
	if status != http.StatusOK {
		t.Fatalf("commit original status=%d body=%s", status, body)
	}
	var first workspaceCommitRecord
	if err := json.Unmarshal(body, &first); err != nil {
		t.Fatal(err)
	}

	body, status = workspaceTestPost(t, app, "/workspace/write", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "conflict-revert-gen-2",
		"path":                "docs/a.md",
		"text":                "# A\n\nChanged content.",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if status != http.StatusOK {
		t.Fatalf("write gen2 status=%d body=%s", status, body)
	}

	body, status = workspaceTestPost(t, app, "/workspace/revert", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"commit_id":  first.CommitID,
	})
	if status != http.StatusOK {
		t.Fatalf("revert status=%d body=%s", status, body)
	}

	conflictBody := workspaceTestGet(t, app, "/workspace/conflicts?project_id=payments&branch=main", http.StatusOK)
	var conflictResp workspaceConflictResponse
	if err := json.Unmarshal(conflictBody, &conflictResp); err != nil {
		t.Fatal(err)
	}
	if !conflictResp.HasConflicts || !workspaceConflictHasPath(conflictResp.DirtyPaths, "docs/a.md") {
		t.Fatalf("conflicts after revert=%+v", conflictResp)
	}
	if !workspaceConflictHasAction(conflictResp.RecommendedActions, "workspace_reconcile") {
		t.Fatalf("recommended_actions=%v, want reconcile guidance", conflictResp.RecommendedActions)
	}

	body, status = workspaceTestPost(t, app, "/workspace/reconcile", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "conflict-revert-gen-3",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if status != http.StatusOK {
		t.Fatalf("reconcile status=%d body=%s", status, body)
	}

	conflictBody = workspaceTestGet(t, app, "/workspace/conflicts?project_id=payments&branch=main", http.StatusOK)
	if err := json.Unmarshal(conflictBody, &conflictResp); err != nil {
		t.Fatal(err)
	}
	if conflictResp.HasConflicts {
		t.Fatalf("expected conflicts to clear after reconcile: %+v", conflictResp)
	}
	if !workspaceConflictHasAction(conflictResp.RecommendedActions, "No action required") {
		t.Fatalf("recommended_actions=%v, want no-op guidance", conflictResp.RecommendedActions)
	}
}

func TestWorkspaceWatcherCanEnqueueAsyncReconcile(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()

	filePath, _, err := workspaceFilePath(cfg, "payments", "main", "docs/watched-async.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("# Watched Async\n\nQueued reconcile."), 0644); err != nil {
		t.Fatal(err)
	}

	generation, err := workspaceWatchReconcile(context.Background(), cfg, WorkspaceWatchOptions{
		GenerationPrefix: "watch-async",
		ChunkStrategy:    "paragraph",
		MinChunkChars:    1,
		AsyncIndex:       true,
	}, workspaceWatchKey{ProjectID: "payments", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := listWorkspaceIndexJobs(cfg, workspaceIndexJobsRequest{ProjectID: "payments", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs=%+v, want one queued watcher job", jobs)
	}
	if jobs[0].Status != workspaceIndexJobPending || jobs[0].Request.Operation != "reconcile" || jobs[0].Request.Generation != generation {
		t.Fatalf("watcher queued job=%+v, generation=%s", jobs[0], generation)
	}
	manifest, _, err := loadWorkspaceManifest(cfg, "payments", "main")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ActiveGeneration == generation {
		t.Fatalf("async watcher should not activate generation before worker runs")
	}
}

func TestWorkspaceWatcherDebouncesAndReconcilesFilesystemChanges(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	cfg.WorkspaceWatcher = NewWorkspaceWatchState()
	stop := StartWorkspaceWatcher(context.Background(), cfg, WorkspaceWatchOptions{
		Interval:         10 * time.Millisecond,
		Debounce:         20 * time.Millisecond,
		GenerationPrefix: "test-watch",
		ChunkStrategy:    "paragraph",
		MinChunkChars:    1,
		Logf:             t.Logf,
	})
	defer stop()

	filePath, _, err := workspaceFilePath(cfg, "payments", "main", "docs/watched.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("# Watched\n\nWatcher generated index."), 0644); err != nil {
		t.Fatal(err)
	}

	waitForWorkspaceCondition(t, 2*time.Second, func() bool {
		manifest, _, err := loadWorkspaceManifest(cfg, "payments", "main")
		if err != nil {
			return false
		}
		if !strings.HasPrefix(manifest.ActiveGeneration, "test-watch-") {
			return false
		}
		for _, rec := range manifest.ListChunks(workspace.ChunkFilter{ActiveOnly: true}) {
			if rec.Path == "docs/watched.md" {
				return true
			}
		}
		return false
	})

	waitForWorkspaceCondition(t, 2*time.Second, func() bool {
		status := cfg.WorkspaceWatcher.Snapshot()
		return status.ReconcileCount > 0 && strings.HasPrefix(status.LastGeneration, "test-watch-")
	})

	status := cfg.WorkspaceWatcher.Snapshot()
	if !status.Enabled {
		t.Fatal("watcher status should be enabled")
	}
	if status.ScanCount == 0 {
		t.Fatal("watcher status did not record scans")
	}
	if status.ReconcileCount == 0 {
		t.Fatal("watcher status did not record reconciliation")
	}
	if !strings.HasPrefix(status.LastGeneration, "test-watch-") {
		t.Fatalf("last generation=%q", status.LastGeneration)
	}

	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)
	body := workspaceTestGet(t, app, "/workspace/watch/status", http.StatusOK)
	var restStatus WorkspaceWatchStatus
	if err := json.Unmarshal(body, &restStatus); err != nil {
		t.Fatal(err)
	}
	if restStatus.ReconcileCount == 0 {
		t.Fatalf("REST status missing reconcile count: %+v", restStatus)
	}
}

func TestWorkspaceWatcherStatusPersistsAndLoads(t *testing.T) {
	statusPath := filepath.Join(t.TempDir(), ".kb", "watch-status.json")
	first := NewWorkspaceWatchState()
	first.configurePersistence(statusPath)
	first.markStarted(WorkspaceWatchOptions{
		Interval:         10 * time.Millisecond,
		Debounce:         20 * time.Millisecond,
		GenerationPrefix: "persist-test",
	})
	first.recordScan(2, 1)
	first.recordReconcile(workspaceWatchKey{ProjectID: "payments", Branch: "main"}, "persist-gen-1", 0)
	first.markStopped()

	second := NewWorkspaceWatchState()
	if err := second.loadPersisted(statusPath); err != nil {
		t.Fatal(err)
	}
	status := second.Snapshot()
	if status.Enabled {
		t.Fatal("loaded status should preserve stopped state")
	}
	if status.ScanCount != 1 {
		t.Fatalf("scan_count=%d, want 1", status.ScanCount)
	}
	if status.ReconcileCount != 1 {
		t.Fatalf("reconcile_count=%d, want 1", status.ReconcileCount)
	}
	if status.LastGeneration != "persist-gen-1" {
		t.Fatalf("last_generation=%q, want persist-gen-1", status.LastGeneration)
	}
	if status.LastProjectID != "payments" || status.LastBranch != "main" {
		t.Fatalf("loaded project/branch=%s/%s", status.LastProjectID, status.LastBranch)
	}
}

func TestWorkspaceWatcherStatusTracksBranches(t *testing.T) {
	statusPath := filepath.Join(t.TempDir(), ".kb", "watch-status.json")
	state := NewWorkspaceWatchState()
	state.configurePersistence(statusPath)
	state.recordScanBranches(
		map[workspaceWatchKey]string{{ProjectID: "payments", Branch: "main"}: "fp-1"},
		map[workspaceWatchKey]time.Time{{ProjectID: "payments", Branch: "main"}: time.Now()},
	)
	state.recordBranchChange(workspaceWatchKey{ProjectID: "payments", Branch: "main"}, 1)
	state.recordReconcile(workspaceWatchKey{ProjectID: "payments", Branch: "main"}, "watch-gen-1", 0)

	status := state.Snapshot()
	branch := status.Branches["payments/main"]
	if branch.ProjectID != "payments" || branch.Branch != "main" {
		t.Fatalf("branch status identity wrong: %+v", branch)
	}
	if branch.Pending {
		t.Fatalf("branch should not be pending after reconcile: %+v", branch)
	}
	if branch.LastGeneration != "watch-gen-1" || branch.ReconcileCount != 1 {
		t.Fatalf("branch reconcile status wrong: %+v", branch)
	}

	loaded := NewWorkspaceWatchState()
	if err := loaded.loadPersisted(statusPath); err != nil {
		t.Fatal(err)
	}
	if loaded.Snapshot().Branches["payments/main"].LastGeneration != "watch-gen-1" {
		t.Fatalf("persisted branches not loaded: %+v", loaded.Snapshot().Branches)
	}
}

func TestWorkspaceAPIRunArtifactsAreDurableMarkdown(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)

	body, status := workspaceTestPost(t, app, "/workspace/runs/start", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"run_id":     "run-1",
		"prompt":     "Investigate payment timeout.",
		"command":    "go test ./...",
		"result":     "All tests passed.",
		"metadata": map[string]any{
			"agent": "codex",
		},
	})
	if status != http.StatusOK {
		t.Fatalf("run start status=%d body=%s", status, body)
	}
	var start workspaceRunResponse
	if err := json.Unmarshal(body, &start); err != nil {
		t.Fatal(err)
	}
	if start.RunID != "run-1" {
		t.Fatalf("run_id=%q, want run-1", start.RunID)
	}

	body = workspaceTestGet(t, app, "/workspace/runs/get?project_id=payments&branch=main&run_id=run-1", http.StatusOK)
	var got workspaceRunResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Files["prompt.md"] != "Investigate payment timeout." {
		t.Fatalf("prompt.md=%q", got.Files["prompt.md"])
	}
	if got.Files["command.md"] != "go test ./..." {
		t.Fatalf("command.md=%q", got.Files["command.md"])
	}
	if !bytes.Contains([]byte(got.Files["metadata.md"]), []byte("agent: codex")) {
		t.Fatalf("metadata.md missing agent: %q", got.Files["metadata.md"])
	}
}

func TestWorkspaceAPICommitLogAndRevertSnapshotsTruthLayer(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)

	body, status := workspaceTestPost(t, app, "/workspace/write", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"path":       "docs/a.md",
		"text":       "# A\n\nOriginal content.",
	})
	if status != http.StatusOK {
		t.Fatalf("write original status=%d body=%s", status, body)
	}
	body, status = workspaceTestPost(t, app, "/workspace/commit", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"message":    "original",
		"author":     "agent:codex",
	})
	if status != http.StatusOK {
		t.Fatalf("commit original status=%d body=%s", status, body)
	}
	var first workspaceCommitRecord
	if err := json.Unmarshal(body, &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Files) != 1 || first.Files[0].Path != "docs/a.md" {
		t.Fatalf("first files=%+v", first.Files)
	}

	body, status = workspaceTestPost(t, app, "/workspace/write", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"path":       "docs/a.md",
		"text":       "# A\n\nChanged content.",
	})
	if status != http.StatusOK {
		t.Fatalf("write changed status=%d body=%s", status, body)
	}
	body, status = workspaceTestPost(t, app, "/workspace/write", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"path":       "docs/b.md",
		"text":       "# B\n\nNew file.",
	})
	if status != http.StatusOK {
		t.Fatalf("write new status=%d body=%s", status, body)
	}
	body, status = workspaceTestPost(t, app, "/workspace/commit", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"message":    "changed",
	})
	if status != http.StatusOK {
		t.Fatalf("commit changed status=%d body=%s", status, body)
	}
	var second workspaceCommitRecord
	if err := json.Unmarshal(body, &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Files) != 2 {
		t.Fatalf("second files=%+v", second.Files)
	}

	body = workspaceTestGet(t, app, "/workspace/log?project_id=payments&branch=main", http.StatusOK)
	var logResp workspaceLogResponse
	if err := json.Unmarshal(body, &logResp); err != nil {
		t.Fatal(err)
	}
	if len(logResp.Commits) != 2 {
		t.Fatalf("log commits=%d, want 2", len(logResp.Commits))
	}

	body, status = workspaceTestPost(t, app, "/workspace/revert", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"commit_id":  first.CommitID,
	})
	if status != http.StatusOK {
		t.Fatalf("revert status=%d body=%s", status, body)
	}
	readBody := workspaceTestGet(t, app, "/workspace/read?project_id=payments&branch=main&path=docs/a.md", http.StatusOK)
	var readResp workspaceReadResponse
	if err := json.Unmarshal(readBody, &readResp); err != nil {
		t.Fatal(err)
	}
	if readResp.Text != "# A\n\nOriginal content." {
		t.Fatalf("reverted text=%q", readResp.Text)
	}
	bPath, _, err := workspaceFilePath(cfg, "payments", "main", "docs/b.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("docs/b.md should be removed by revert, stat err=%v", err)
	}
}

func TestWorkspaceAPIGCRemovesPendingGenerationCollectionsAndBM25(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	app := fiber.New()
	RegisterWorkspaceAPI(app, cfg)

	body, status := workspaceTestPost(t, app, "/workspace/index", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "gen-1",
		"path":                "docs/old.md",
		"text":                "# Old\n\nLegacy keyword only here.",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if status != http.StatusOK {
		t.Fatalf("gen1 index status=%d body=%s", status, body)
	}
	var gen1 workspaceIndexResponse
	_ = json.Unmarshal(body, &gen1)
	if hits := cfg.BM25Indexes[gen1.Result.Collection].Search("legacy keyword", 10); len(hits) == 0 {
		t.Fatal("expected gen1 BM25 hit before GC")
	}

	body, status = workspaceTestPost(t, app, "/workspace/index", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "gen-2",
		"path":                "docs/new.md",
		"text":                "# New\n\nCurrent keyword lives here.",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if status != http.StatusOK {
		t.Fatalf("gen2 index status=%d body=%s", status, body)
	}
	var gen2 workspaceIndexResponse
	_ = json.Unmarshal(body, &gen2)

	body, status = workspaceTestPost(t, app, "/workspace/gc", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"dry_run":    true,
	})
	if status != http.StatusOK {
		t.Fatalf("gc dry-run status=%d body=%s", status, body)
	}
	var dryRun workspaceGCResponse
	if err := json.Unmarshal(body, &dryRun); err != nil {
		t.Fatal(err)
	}
	if !dryRun.Result.DryRun || len(dryRun.Result.Generations) != 1 || len(dryRun.Result.DroppedCollections) != 1 {
		t.Fatalf("dry-run result=%+v, want one pending generation/collection", dryRun.Result)
	}
	if !cfg.Collections.Has(gen1.Result.Collection) {
		t.Fatalf("dry-run dropped old collection %q", gen1.Result.Collection)
	}

	body, status = workspaceTestPost(t, app, "/workspace/gc", map[string]any{
		"project_id": "payments",
		"branch":     "main",
	})
	if status != http.StatusOK {
		t.Fatalf("gc status=%d body=%s", status, body)
	}
	if cfg.Collections.Has(gen1.Result.Collection) {
		t.Fatalf("old collection %q still exists", gen1.Result.Collection)
	}
	if _, ok := cfg.BM25Indexes[gen1.Result.Collection]; ok {
		t.Fatalf("old BM25 collection %q still exists", gen1.Result.Collection)
	}
	if !cfg.Collections.Has(gen2.Result.Collection) {
		t.Fatalf("active collection %q was removed", gen2.Result.Collection)
	}
}

func TestWorkspaceMCPToolsDispatch(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	h := &mcpHandler{cfg: cfg}

	result := h.executeToolInner(context.Background(), nil, "workspace_index", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "gen-1",
		"path":                "docs/payment.md",
		"text":                "# Payment\n\nMCP bounded timeout note.",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if result.IsError {
		t.Fatalf("workspace_index MCP error: %+v", result.Content)
	}
	var indexResp workspaceIndexResponse
	if err := json.Unmarshal([]byte(result.Content[0].Text), &indexResp); err != nil {
		t.Fatal(err)
	}
	if indexResp.Result.ChunksCreated == 0 {
		t.Fatal("expected MCP index chunks")
	}

	manifest := h.executeToolInner(context.Background(), nil, "workspace_manifest", map[string]any{
		"project_id": "payments",
		"branch":     "main",
	})
	if manifest.IsError {
		t.Fatalf("workspace_manifest MCP error: %+v", manifest.Content)
	}
	if !bytes.Contains([]byte(manifest.Content[0].Text), []byte(`"active_generation": "gen-1"`)) {
		t.Fatalf("manifest response missing active generation: %s", manifest.Content[0].Text)
	}
	contextResult := h.executeToolInner(context.Background(), nil, "workspace_context", map[string]any{
		"project_id": "payments",
		"branch":     "main",
	})
	if contextResult.IsError {
		t.Fatalf("workspace_context MCP error: %+v", contextResult.Content)
	}
	if !bytes.Contains([]byte(contextResult.Content[0].Text), []byte(`"exact_read_required": true`)) {
		t.Fatalf("workspace_context missing exact-read policy: %s", contextResult.Content[0].Text)
	}

	write := h.executeToolInner(context.Background(), nil, "workspace_write", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"path":       "docs/mcp.md",
		"text":       "# MCP\n\nWritten through MCP.",
		"index":      false,
	})
	if write.IsError {
		t.Fatalf("workspace_write MCP error: %+v", write.Content)
	}
	read := h.executeToolInner(context.Background(), nil, "workspace_read", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"path":       "docs/mcp.md",
	})
	if read.IsError {
		t.Fatalf("workspace_read MCP error: %+v", read.Content)
	}
	if !bytes.Contains([]byte(read.Content[0].Text), []byte("Written through MCP.")) {
		t.Fatalf("read response missing text: %s", read.Content[0].Text)
	}
	runStart := h.executeToolInner(context.Background(), nil, "workspace_run_start", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"run_id":     "mcp-run",
		"prompt":     "MCP prompt",
	})
	if runStart.IsError {
		t.Fatalf("workspace_run_start MCP error: %+v", runStart.Content)
	}
	runGet := h.executeToolInner(context.Background(), nil, "workspace_run_get", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"run_id":     "mcp-run",
	})
	if runGet.IsError {
		t.Fatalf("workspace_run_get MCP error: %+v", runGet.Content)
	}
	if !bytes.Contains([]byte(runGet.Content[0].Text), []byte("MCP prompt")) {
		t.Fatalf("run get response missing prompt: %s", runGet.Content[0].Text)
	}
	commit := h.executeToolInner(context.Background(), nil, "workspace_commit", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"message":    "mcp snapshot",
	})
	if commit.IsError {
		t.Fatalf("workspace_commit MCP error: %+v", commit.Content)
	}
	logResult := h.executeToolInner(context.Background(), nil, "workspace_log", map[string]any{
		"project_id": "payments",
		"branch":     "main",
	})
	if logResult.IsError {
		t.Fatalf("workspace_log MCP error: %+v", logResult.Content)
	}
	if !bytes.Contains([]byte(logResult.Content[0].Text), []byte("mcp snapshot")) {
		t.Fatalf("workspace_log response missing commit: %s", logResult.Content[0].Text)
	}
	statusResult := h.executeToolInner(context.Background(), nil, "workspace_watch_status", map[string]any{})
	if statusResult.IsError {
		t.Fatalf("workspace_watch_status MCP error: %+v", statusResult.Content)
	}
	if !bytes.Contains([]byte(statusResult.Content[0].Text), []byte(`"enabled"`)) {
		t.Fatalf("workspace_watch_status missing status body: %s", statusResult.Content[0].Text)
	}
	enqueue := h.executeToolInner(context.Background(), nil, "workspace_enqueue_index_job", map[string]any{
		"operation":           "reconcile",
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "mcp-enqueued-gen",
		"activate_generation": true,
	})
	if enqueue.IsError {
		t.Fatalf("workspace_enqueue_index_job MCP error: %+v", enqueue.Content)
	}
	if !bytes.Contains([]byte(enqueue.Content[0].Text), []byte(`"status": "pending"`)) {
		t.Fatalf("workspace_enqueue_index_job missing pending body: %s", enqueue.Content[0].Text)
	}
	jobs := h.executeToolInner(context.Background(), nil, "workspace_index_jobs", map[string]any{
		"project_id": "payments",
		"branch":     "main",
	})
	if jobs.IsError {
		t.Fatalf("workspace_index_jobs MCP error: %+v", jobs.Content)
	}
	if !bytes.Contains([]byte(jobs.Content[0].Text), []byte(`"jobs"`)) {
		t.Fatalf("workspace_index_jobs missing jobs body: %s", jobs.Content[0].Text)
	}
	ops := h.executeToolInner(context.Background(), nil, "workspace_ops_status", map[string]any{
		"project_id": "payments",
		"branch":     "main",
	})
	if ops.IsError {
		t.Fatalf("workspace_ops_status MCP error: %+v", ops.Content)
	}
	if !bytes.Contains([]byte(ops.Content[0].Text), []byte(`"dead_letter_count"`)) {
		t.Fatalf("workspace_ops_status missing ops body: %s", ops.Content[0].Text)
	}
	artifacts := h.executeToolInner(context.Background(), nil, "workspace_context_artifacts", map[string]any{
		"project_id": "payments",
		"branch":     "main",
	})
	if artifacts.IsError {
		t.Fatalf("workspace_context_artifacts MCP error: %+v", artifacts.Content)
	}
	if !bytes.Contains([]byte(artifacts.Content[0].Text), []byte(`"artifacts"`)) {
		t.Fatalf("workspace_context_artifacts missing artifacts body: %s", artifacts.Content[0].Text)
	}
	conflicts := h.executeToolInner(context.Background(), nil, "workspace_conflicts", map[string]any{
		"project_id": "payments",
		"branch":     "main",
	})
	if conflicts.IsError {
		t.Fatalf("workspace_conflicts MCP error: %+v", conflicts.Content)
	}
	if !bytes.Contains([]byte(conflicts.Content[0].Text), []byte(`"policy"`)) {
		t.Fatalf("workspace_conflicts missing policy body: %s", conflicts.Content[0].Text)
	}
}

func TestWorkspaceMCPAccessCheckAndAuditLog(t *testing.T) {
	cfg, closeFn := newWorkspaceACLTestConfig(t)
	defer closeFn()
	seedWorkspaceACL(t, cfg.DB, "user-a", "user-b", "payments", RoleViewer)
	h := &mcpHandler{cfg: cfg}

	viewerCtx := context.WithValue(context.Background(), mcpUserIDKey, "user-b")
	check := h.executeTool(viewerCtx, nil, "workspace_access_check", map[string]any{
		"project_id": "payments",
		"access":     "write",
	})
	if check.IsError {
		t.Fatalf("workspace_access_check error: %+v", check.Content)
	}
	var accessResp workspaceAccessCheckResponse
	if err := json.Unmarshal([]byte(check.Content[0].Text), &accessResp); err != nil {
		t.Fatal(err)
	}
	if accessResp.Allowed || accessResp.Role != RoleViewer || accessResp.Reason != "role_insufficient" {
		t.Fatalf("viewer write access=%+v, want denied viewer", accessResp)
	}

	ownerCtx := context.WithValue(context.Background(), mcpUserIDKey, "user-a")
	secretText := "# MCP Secret\n\nMCP classified audit payload."
	write := h.executeTool(ownerCtx, nil, "workspace_write", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"path":       "docs/mcp-private-audit.md",
		"text":       secretText,
		"index":      false,
	})
	if write.IsError {
		t.Fatalf("owner workspace_write error: %+v", write.Content)
	}

	foreignCtx := context.WithValue(context.Background(), mcpUserIDKey, "user-c")
	denied := h.executeTool(foreignCtx, nil, "workspace_read", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"path":       "docs/mcp-private-audit.md",
	})
	if !denied.IsError {
		t.Fatalf("foreign workspace_read should fail: %s", denied.Content[0].Text)
	}

	audit := h.executeTool(ownerCtx, nil, "workspace_audit_log", map[string]any{
		"project_id": "payments",
		"limit":      20,
	})
	if audit.IsError {
		t.Fatalf("workspace_audit_log error: %+v", audit.Content)
	}
	body := []byte(audit.Content[0].Text)
	if bytes.Contains(body, []byte("MCP classified audit payload")) || bytes.Contains(body, []byte("docs/mcp-private-audit.md")) {
		t.Fatalf("MCP audit leaked content/path: %s", body)
	}
	var logResp workspaceAuditLogResponse
	if err := json.Unmarshal(body, &logResp); err != nil {
		t.Fatal(err)
	}
	if !workspaceAuditHasEvent(logResp.Events, "write", "success") {
		t.Fatalf("MCP audit missing successful write: %+v", logResp.Events)
	}
	if !workspaceAuditHasEvent(logResp.Events, "read", "denied") {
		t.Fatalf("MCP audit missing denied read: %+v", logResp.Events)
	}
}

func TestWorkspaceMCPSearchResolvesActiveCollectionAndFreshness(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	h := &mcpHandler{cfg: cfg}

	index := h.executeToolInner(context.Background(), nil, "workspace_write", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "gen-active",
		"collection":          "payments_active",
		"path":                "docs/payment.md",
		"text":                "# Payment\n\nBounded timeout retry policy.",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if index.IsError {
		t.Fatalf("workspace_write error: %+v", index.Content)
	}

	search := h.executeToolInner(context.Background(), nil, "workspace_search", map[string]any{
		"project_id":   "payments",
		"branch":       "main",
		"search_query": "bounded timeout retry",
		"search_type":  "CHUNKS_LEXICAL",
		"top_k":        5,
	})
	if search.IsError {
		t.Fatalf("workspace_search error: %+v", search.Content)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(search.Content[0].Text), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["collection"] != "payments_active" {
		t.Fatalf("collection=%v, want payments_active; body=%s", resp["collection"], search.Content[0].Text)
	}
	if resp["generation"] != "gen-active" || resp["active_generation"] != "gen-active" {
		t.Fatalf("generation fields wrong: %v", resp)
	}
	freshness := resp["freshness"].(map[string]any)
	if freshness["stale"] != false {
		t.Fatalf("freshness.stale=%v, want false", freshness["stale"])
	}
	if freshness["active_chunk_count"].(float64) == 0 {
		t.Fatalf("freshness missing active chunk count: %+v", freshness)
	}
	results := resp["results"].([]any)
	if len(results) == 0 {
		t.Fatalf("workspace_search returned no hits: %s", search.Content[0].Text)
	}
	first := results[0].(map[string]any)
	if first["path"] != "docs/payment.md" {
		t.Fatalf("path=%v, want docs/payment.md; result=%+v", first["path"], first)
	}
	if !strings.Contains(first["text"].(string), "Bounded timeout") {
		t.Fatalf("result text not enriched: %+v", first)
	}
	if resp["exact_read_required"] != true {
		t.Fatalf("exact_read_required=%v, want true", resp["exact_read_required"])
	}
	contract := resp["answer_contract"].(map[string]any)
	if contract["read_tool"] != "workspace_read" || contract["exact_read_required"] != true {
		t.Fatalf("answer contract=%+v, want workspace_read exact-read rule", contract)
	}
	requiredFields, ok := contract["required_fields"].([]any)
	if !ok {
		t.Fatalf("answer contract missing required_fields slice: %+v", contract)
	}
	for _, want := range []string{
		"project_id", "branch", "path", "generation", "collection",
		"heading_path", "chunk_id", "vector_id", "source_uri",
	} {
		if !workspaceAnySliceContainsString(requiredFields, want) {
			t.Fatalf("answer contract required_fields missing %q: %+v", want, requiredFields)
		}
	}
	citation := first["citation"].(map[string]any)
	if citation["path"] != "docs/payment.md" || citation["generation"] != "gen-active" || citation["collection"] != "payments_active" {
		t.Fatalf("citation=%+v, want exact source metadata", citation)
	}
	for _, field := range []string{"source_id", "project_id", "branch", "path", "generation", "collection", "chunk_id", "vector_id", "source_uri", "read_tool", "read_args"} {
		if _, ok := citation[field]; !ok {
			t.Fatalf("citation missing %q: %+v", field, citation)
		}
	}
	if citation["stale"] != false || citation["potentially_stale"] != false {
		t.Fatalf("fresh citation flags wrong: %+v", citation)
	}
	readArgs := citation["read_args"].(map[string]any)
	if readArgs["project_id"] != "payments" || readArgs["path"] != "docs/payment.md" {
		t.Fatalf("citation read_args=%+v, want workspace_read arguments", readArgs)
	}
	read := h.executeToolInner(context.Background(), nil, "workspace_read", readArgs)
	if read.IsError {
		t.Fatalf("workspace_read from citation args failed: %+v", read.Content)
	}
	var readResp workspaceReadResponse
	if err := json.Unmarshal([]byte(read.Content[0].Text), &readResp); err != nil {
		t.Fatal(err)
	}
	if readResp.Citation.SourceURI == "" || len(readResp.Citations) == 0 {
		t.Fatalf("read response missing citation contract: %+v", readResp.Citation)
	}
}

func TestWorkspaceReadResponseReturnsChunkCitationsWithHeadingAnchors(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	h := &mcpHandler{cfg: cfg}

	write := h.executeToolInner(context.Background(), nil, "workspace_write", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "gen-headings",
		"collection":          "payments_headings",
		"path":                "docs/runbook.md",
		"text":                "# Runbook\n\nIntro summary.\n\n## Retry Policy\n\nRetry policy exact anchor.\n",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if write.IsError {
		t.Fatalf("workspace_write error: %+v", write.Content)
	}

	read := h.executeToolInner(context.Background(), nil, "workspace_read", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"path":       "docs/runbook.md",
	})
	if read.IsError {
		t.Fatalf("workspace_read error: %+v", read.Content)
	}
	var resp workspaceReadResponse
	if err := json.Unmarshal([]byte(read.Content[0].Text), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Citations) == 0 {
		t.Fatalf("workspace_read returned no chunk citations: %+v", resp)
	}
	var anchored bool
	for _, citation := range resp.Citations {
		if citation.ProjectID != "payments" || citation.Branch != "main" || citation.Path != "docs/runbook.md" {
			t.Fatalf("chunk citation source mismatch: %+v", citation)
		}
		if citation.SourceURI == "" || citation.ReadTool != "workspace_read" {
			t.Fatalf("chunk citation missing source uri or read tool: %+v", citation)
		}
		if citation.HeadingAnchor != "" && strings.Contains(citation.SourceURI, "#") {
			anchored = true
		}
	}
	if !anchored {
		t.Fatalf("expected at least one anchored chunk citation: %+v", resp.Citations)
	}
}

func TestWorkspaceMCPSearchMarksExplicitOldGenerationStale(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	h := &mcpHandler{cfg: cfg}

	old := h.executeToolInner(context.Background(), nil, "workspace_index", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "gen-old",
		"collection":          "payments_old",
		"path":                "docs/payment.md",
		"text":                "# Payment\n\nOriginal rollback anchor.",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if old.IsError {
		t.Fatalf("old index error: %+v", old.Content)
	}
	current := h.executeToolInner(context.Background(), nil, "workspace_index", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "gen-current",
		"collection":          "payments_current",
		"path":                "docs/payment.md",
		"text":                "# Payment\n\nChanged rollback anchor.",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if current.IsError {
		t.Fatalf("current index error: %+v", current.Content)
	}

	search := h.executeToolInner(context.Background(), nil, "workspace_search", map[string]any{
		"project_id":   "payments",
		"branch":       "main",
		"generation":   "gen-old",
		"search_query": "original rollback anchor",
		"search_type":  "CHUNKS_LEXICAL",
	})
	if search.IsError {
		t.Fatalf("workspace_search old generation error: %+v", search.Content)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(search.Content[0].Text), &resp); err != nil {
		t.Fatal(err)
	}
	freshness := resp["freshness"].(map[string]any)
	if freshness["stale"] != true || freshness["reason"] != "requested_generation_is_not_active" {
		t.Fatalf("freshness=%+v, want stale requested_generation_is_not_active", freshness)
	}
	first := resp["results"].([]any)[0].(map[string]any)
	citation := first["citation"].(map[string]any)
	if citation["stale"] != true || citation["potentially_stale"] != false {
		t.Fatalf("stale citation flags wrong: %+v", citation)
	}
	if resp["collection"] != "payments_old" {
		t.Fatalf("collection=%v, want payments_old", resp["collection"])
	}
	if bytes.Contains([]byte(search.Content[0].Text), []byte("Changed rollback anchor")) {
		t.Fatalf("old generation search leaked current text: %s", search.Content[0].Text)
	}
}

func TestWorkspaceMCPSearchRequiresExplicitCollectionForAmbiguousGeneration(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	h := &mcpHandler{cfg: cfg}

	for _, tc := range []struct {
		path, text, collection string
	}{
		{"docs/a.md", "# A\n\nAlpha shared generation.", "multi_a"},
		{"docs/b.md", "# B\n\nBeta shared generation.", "multi_b"},
	} {
		res := h.executeToolInner(context.Background(), nil, "workspace_index", map[string]any{
			"project_id":          "payments",
			"branch":              "main",
			"generation":          "gen-multi",
			"collection":          tc.collection,
			"path":                tc.path,
			"text":                tc.text,
			"chunk_strategy":      "paragraph",
			"min_chunk_chars":     1,
			"activate_generation": true,
		})
		if res.IsError {
			t.Fatalf("index %s error: %+v", tc.path, res.Content)
		}
	}

	ambiguous := h.executeToolInner(context.Background(), nil, "workspace_search", map[string]any{
		"project_id":   "payments",
		"branch":       "main",
		"search_query": "alpha",
		"search_type":  "CHUNKS_LEXICAL",
	})
	if !ambiguous.IsError {
		t.Fatalf("ambiguous workspace_search should fail: %s", ambiguous.Content[0].Text)
	}
	if !strings.Contains(ambiguous.Content[0].Text, "multiple collections") {
		t.Fatalf("ambiguous error=%q, want multiple collections", ambiguous.Content[0].Text)
	}

	explicit := h.executeToolInner(context.Background(), nil, "workspace_search", map[string]any{
		"project_id":   "payments",
		"branch":       "main",
		"collection":   "multi_a",
		"search_query": "alpha",
		"search_type":  "CHUNKS_LEXICAL",
	})
	if explicit.IsError {
		t.Fatalf("explicit collection workspace_search error: %+v", explicit.Content)
	}
	if !bytes.Contains([]byte(explicit.Content[0].Text), []byte("Alpha shared generation")) {
		t.Fatalf("explicit search missing alpha hit: %s", explicit.Content[0].Text)
	}
}

func TestWorkspaceMCPSearchMissingActiveGenerationIsActionable(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	h := &mcpHandler{cfg: cfg}

	res := h.executeToolInner(context.Background(), nil, "workspace_search", map[string]any{
		"project_id":   "payments",
		"branch":       "main",
		"search_query": "anything",
		"search_type":  "CHUNKS_LEXICAL",
	})
	if !res.IsError {
		t.Fatalf("workspace_search without active generation should fail: %s", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "active generation missing") || !strings.Contains(res.Content[0].Text, "workspace_reconcile") {
		t.Fatalf("missing active generation error is not actionable: %q", res.Content[0].Text)
	}
}

func TestWorkspaceMCPSearchAccessDeniedDoesNotLeak(t *testing.T) {
	cfg, closeFn := newWorkspaceACLTestConfig(t)
	defer closeFn()
	seedWorkspaceACL(t, cfg.DB, "user-a", "user-b", "payments", "")
	h := &mcpHandler{cfg: cfg}

	ownerCtx := context.WithValue(context.Background(), mcpUserIDKey, "user-a")
	write := h.executeToolInner(ownerCtx, nil, "workspace_write", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "private-gen",
		"collection":          "private_collection",
		"path":                "docs/private.md",
		"text":                "# Private\n\nHidden payment marker.",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if write.IsError {
		t.Fatalf("owner workspace_write error: %+v", write.Content)
	}

	foreignCtx := context.WithValue(context.Background(), mcpUserIDKey, "user-b")
	res := h.executeToolInner(foreignCtx, nil, "workspace_search", map[string]any{
		"project_id":   "payments",
		"branch":       "main",
		"search_query": "hidden payment marker",
		"search_type":  "CHUNKS_LEXICAL",
	})
	if !res.IsError {
		t.Fatalf("foreign workspace_search should fail: %s", res.Content[0].Text)
	}
	body := []byte(res.Content[0].Text)
	for _, forbidden := range [][]byte{
		[]byte("docs/private.md"),
		[]byte("Hidden payment marker"),
		[]byte("private_collection"),
	} {
		if bytes.Contains(body, forbidden) {
			t.Fatalf("denied workspace_search leaked %q in %s", forbidden, body)
		}
	}
}

func TestWorkspaceMCPSearchFreshnessUsesProjectBranchWatcherStatus(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	cfg.WorkspaceWatcher = NewWorkspaceWatchState()
	h := &mcpHandler{cfg: cfg}

	write := h.executeToolInner(context.Background(), nil, "workspace_write", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "fresh-gen",
		"collection":          "fresh_collection",
		"path":                "docs/fresh.md",
		"text":                "# Fresh\n\nPayment freshness anchor.",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if write.IsError {
		t.Fatalf("workspace_write error: %+v", write.Content)
	}

	cfg.WorkspaceWatcher.recordBranchChange(workspaceWatchKey{ProjectID: "other", Branch: "main"}, 1)
	search := h.executeToolInner(context.Background(), nil, "workspace_search", map[string]any{
		"project_id":   "payments",
		"branch":       "main",
		"search_query": "freshness anchor",
		"search_type":  "CHUNKS_LEXICAL",
	})
	if search.IsError {
		t.Fatalf("workspace_search error: %+v", search.Content)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(search.Content[0].Text), &resp); err != nil {
		t.Fatal(err)
	}
	freshness := resp["freshness"].(map[string]any)
	if freshness["potentially_stale"] != false {
		t.Fatalf("foreign pending branch should not mark payments stale: %+v", freshness)
	}

	cfg.WorkspaceWatcher.recordBranchChange(workspaceWatchKey{ProjectID: "payments", Branch: "main"}, 2)
	search = h.executeToolInner(context.Background(), nil, "workspace_search", map[string]any{
		"project_id":   "payments",
		"branch":       "main",
		"search_query": "freshness anchor",
		"search_type":  "CHUNKS_LEXICAL",
	})
	if search.IsError {
		t.Fatalf("workspace_search with pending branch error: %+v", search.Content)
	}
	if err := json.Unmarshal([]byte(search.Content[0].Text), &resp); err != nil {
		t.Fatal(err)
	}
	freshness = resp["freshness"].(map[string]any)
	if freshness["potentially_stale"] != true || freshness["reason"] != "watcher_branch_has_pending_reconcile" {
		t.Fatalf("payments pending branch did not mark freshness: %+v", freshness)
	}
	if freshness["watcher_branch_pending"] != true {
		t.Fatalf("watcher_branch_pending missing: %+v", freshness)
	}
	first := resp["results"].([]any)[0].(map[string]any)
	citation := first["citation"].(map[string]any)
	if citation["stale"] != false || citation["potentially_stale"] != true {
		t.Fatalf("pending watcher citation flags wrong: %+v", citation)
	}
}

func TestWorkspaceMCPAccessDeniedForForeignProject(t *testing.T) {
	cfg, closeFn := newWorkspaceACLTestConfig(t)
	defer closeFn()
	seedWorkspaceACL(t, cfg.DB, "user-a", "user-b", "payments", "")
	filePath, _, err := workspaceFilePath(cfg, "payments", "main", "docs/private.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("# Private\n\nHidden."), 0644); err != nil {
		t.Fatal(err)
	}
	h := &mcpHandler{cfg: cfg}
	ctx := context.WithValue(context.Background(), mcpUserIDKey, "user-b")
	res := h.executeToolInner(ctx, nil, "workspace_read", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"path":       "docs/private.md",
	})
	if !res.IsError {
		t.Fatal("foreign workspace_read should fail")
	}
	if !bytes.Contains([]byte(res.Content[0].Text), []byte("workspace access denied")) {
		t.Fatalf("unexpected MCP error: %s", res.Content[0].Text)
	}
	if bytes.Contains([]byte(res.Content[0].Text), []byte("Hidden")) || bytes.Contains([]byte(res.Content[0].Text), []byte("docs/private.md")) {
		t.Fatalf("MCP denial leaked file details: %s", res.Content[0].Text)
	}
}

func TestWorkspaceMCPForeignProjectOpsDoNotLeakMetadata(t *testing.T) {
	cfg, closeFn := newWorkspaceACLTestConfig(t)
	defer closeFn()
	seedWorkspaceACL(t, cfg.DB, "user-a", "user-b", "payments", "")

	ownerCtx := context.WithValue(context.Background(), mcpUserIDKey, "user-a")
	h := &mcpHandler{cfg: cfg}
	write := h.executeToolInner(ownerCtx, nil, "workspace_write", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "mcp-hidden-gen",
		"collection":          "mcp_hidden_collection",
		"path":                "docs/private-ops.md",
		"text":                "# Private Ops\n\nMCP hidden metadata anchor.",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if write.IsError {
		t.Fatalf("owner workspace_write error: %+v", write.Content)
	}

	foreignCtx := context.WithValue(context.Background(), mcpUserIDKey, "user-b")
	for _, tool := range []string{"workspace_context", "workspace_ops_status", "workspace_conflicts", "workspace_audit_log"} {
		res := h.executeToolInner(foreignCtx, nil, tool, map[string]any{
			"project_id": "payments",
			"branch":     "main",
			"limit":      20,
		})
		if !res.IsError {
			t.Fatalf("foreign %s should fail: %+v", tool, res.Content)
		}
		body := []byte(res.Content[0].Text)
		for _, forbidden := range [][]byte{
			[]byte("docs/private-ops.md"),
			[]byte("MCP hidden metadata anchor"),
			[]byte("mcp-hidden-gen"),
			[]byte("mcp_hidden_collection"),
		} {
			if bytes.Contains(body, forbidden) {
				t.Fatalf("tool %s leaked %q in %s", tool, forbidden, body)
			}
		}
	}
}

func TestMCPInitializeStoresAuthenticatedUser(t *testing.T) {
	secret := "test-secret"
	h := &mcpHandler{
		cfg:      APIConfig{JWTSecret: secret, RequireAuth: true},
		sessions: mcppkg.NewSessionStore(),
	}
	app := fiber.New()
	app.Post("/mcp", h.handleRPC)

	reqBody := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+createJWT("user-b", "b@example.com", secret))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initialize status=%d", resp.StatusCode)
	}
	sessionID := resp.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("missing MCP session id")
	}
	sess := h.sessions.Get(sessionID)
	if sess == nil || sess.UserID != "user-b" {
		t.Fatalf("session user=%v, want user-b", sess)
	}
}

func TestWorkspaceMCPFullCycleSearchCommitRevertGC(t *testing.T) {
	cfg, closeFn := newWorkspaceTestConfig(t)
	defer closeFn()
	h := &mcpHandler{cfg: cfg}

	write := h.executeToolInner(context.Background(), nil, "workspace_write", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "cycle-gen-1",
		"collection":          "cycle_gen_1",
		"path":                "docs/cycle.md",
		"text":                "# Cycle\n\nOriginal rollback anchor.",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if write.IsError {
		t.Fatalf("workspace_write original error: %+v", write.Content)
	}
	searchOriginal := h.executeToolInner(context.Background(), nil, "search", map[string]any{
		"search_query": "original rollback anchor",
		"search_type":  "CHUNKS_LEXICAL",
		"collection":   "cycle_gen_1",
		"top_k":        5,
	})
	if searchOriginal.IsError {
		t.Fatalf("search original error: %+v", searchOriginal.Content)
	}
	if !bytes.Contains([]byte(searchOriginal.Content[0].Text), []byte("Original rollback anchor")) {
		t.Fatalf("original search missing text: %s", searchOriginal.Content[0].Text)
	}

	commit := h.executeToolInner(context.Background(), nil, "workspace_commit", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"message":    "original checkpoint",
	})
	if commit.IsError {
		t.Fatalf("workspace_commit error: %+v", commit.Content)
	}
	var commitRecord workspaceCommitRecord
	if err := json.Unmarshal([]byte(commit.Content[0].Text), &commitRecord); err != nil {
		t.Fatal(err)
	}

	edit := h.executeToolInner(context.Background(), nil, "workspace_write", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"generation":          "cycle-gen-2",
		"collection":          "cycle_gen_2",
		"path":                "docs/cycle.md",
		"text":                "# Cycle\n\nChanged rollback anchor.",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if edit.IsError {
		t.Fatalf("workspace_write edit error: %+v", edit.Content)
	}
	searchChanged := h.executeToolInner(context.Background(), nil, "search", map[string]any{
		"search_query": "changed rollback anchor",
		"search_type":  "CHUNKS_LEXICAL",
		"collection":   "cycle_gen_2",
		"top_k":        5,
	})
	if searchChanged.IsError {
		t.Fatalf("search changed error: %+v", searchChanged.Content)
	}
	if !bytes.Contains([]byte(searchChanged.Content[0].Text), []byte("Changed rollback anchor")) {
		t.Fatalf("changed search missing text: %s", searchChanged.Content[0].Text)
	}

	revert := h.executeToolInner(context.Background(), nil, "workspace_revert", map[string]any{
		"project_id":          "payments",
		"branch":              "main",
		"commit_id":           commitRecord.CommitID,
		"reindex":             true,
		"generation":          "cycle-gen-3",
		"collection":          "cycle_gen_3",
		"chunk_strategy":      "paragraph",
		"min_chunk_chars":     1,
		"activate_generation": true,
	})
	if revert.IsError {
		t.Fatalf("workspace_revert error: %+v", revert.Content)
	}
	searchReverted := h.executeToolInner(context.Background(), nil, "search", map[string]any{
		"search_query": "original rollback anchor",
		"search_type":  "CHUNKS_LEXICAL",
		"collection":   "cycle_gen_3",
		"top_k":        5,
	})
	if searchReverted.IsError {
		t.Fatalf("search reverted error: %+v", searchReverted.Content)
	}
	if !bytes.Contains([]byte(searchReverted.Content[0].Text), []byte("Original rollback anchor")) {
		t.Fatalf("reverted search missing original text: %s", searchReverted.Content[0].Text)
	}
	if bytes.Contains([]byte(searchReverted.Content[0].Text), []byte("Changed rollback anchor")) {
		t.Fatalf("reverted search leaked changed text: %s", searchReverted.Content[0].Text)
	}

	gc := h.executeToolInner(context.Background(), nil, "workspace_gc", map[string]any{
		"project_id": "payments",
		"branch":     "main",
	})
	if gc.IsError {
		t.Fatalf("workspace_gc error: %+v", gc.Content)
	}
	if cfg.Collections.Has("cycle_gen_1") || cfg.Collections.Has("cycle_gen_2") {
		t.Fatalf("gc did not drop stale collections: have gen1=%v gen2=%v", cfg.Collections.Has("cycle_gen_1"), cfg.Collections.Has("cycle_gen_2"))
	}
	if !cfg.Collections.Has("cycle_gen_3") {
		t.Fatal("gc dropped active collection")
	}
}

func newWorkspaceTestConfig(t *testing.T) (APIConfig, func()) {
	t.Helper()
	dir := t.TempDir()
	cm, err := store.NewCollectionManager(2, filepath.Join(dir, "vectors"))
	if err != nil {
		t.Fatal(err)
	}
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		resp := struct {
			Data []item `json:"data"`
		}{Data: make([]item, len(req.Input))}
		for i, text := range req.Input {
			resp.Data[i] = item{Index: i, Embedding: []float32{float32(len(text)), float32(i + 1)}}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	cfg := APIConfig{
		WorkspacePath: filepath.Join(dir, "workspace"),
		EmbedEndpoint: embedSrv.URL,
		EmbedModel:    "test-embed",
		Collections:   cm,
		BM25Indexes:   map[string]*bm25.Index{},
	}
	return cfg, func() {
		embedSrv.Close()
		_ = cm.Close()
	}
}

func newWorkspaceACLTestConfig(t *testing.T) (APIConfig, func()) {
	t.Helper()
	cfg, closeBase := newWorkspaceTestConfig(t)
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "workspace-acl.db"))
	if err != nil {
		closeBase()
		t.Fatal(err)
	}
	SetDBProvider(DBSQLite)
	if _, err := db.Exec(graphSchemaSQL); err != nil {
		_ = db.Close()
		closeBase()
		t.Fatalf("create ACL schema: %v", err)
	}
	cfg.DB = db
	return cfg, func() {
		_ = db.Close()
		closeBase()
		SetDBProvider(DBPostgres)
	}
}

func workspaceACLApp(userID string, cfg APIConfig) *fiber.App {
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		if userID != "" {
			c.Locals("user_id", userID)
		}
		return c.Next()
	})
	RegisterWorkspaceAPI(app, cfg)
	app.Post("/search/text", searchHandler(cfg))
	return app
}

func seedWorkspaceACL(t *testing.T, db *sql.DB, ownerID, userID, projectID, role string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO users(id, email, is_superuser) VALUES (?, ?, 0)`, ownerID, ownerID+"@example.com"); err != nil {
		t.Fatalf("insert owner: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id, email, is_superuser) VALUES (?, ?, 0)`, userID, userID+"@example.com"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO datasets(id, owner_id) VALUES (?, ?)`, projectID, ownerID); err != nil {
		t.Fatalf("insert dataset: %v", err)
	}
	if role != "" {
		shareWorkspaceACL(t, db, projectID, userID, role)
	}
}

func shareWorkspaceACL(t *testing.T, db *sql.DB, projectID, userID, role string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT OR REPLACE INTO dataset_shares(id, dataset_id, user_id, role, granted_by, created_at)
		 VALUES (?, ?, ?, ?, '', '')`,
		"share-"+projectID+"-"+userID, projectID, userID, role,
	); err != nil {
		t.Fatalf("share workspace: %v", err)
	}
}

func workspaceTestPost(t *testing.T, app *fiber.App, path string, payload map[string]any) ([]byte, int) {
	t.Helper()
	data, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), resp.StatusCode
}

func workspaceTestGet(t *testing.T, app *fiber.App, path string, wantStatus int) []byte {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("GET %s status=%d body=%s", path, resp.StatusCode, buf.String())
	}
	return buf.Bytes()
}

func workspaceTestRecordCount(cfg APIConfig, collection string) int {
	meta := cfg.Collections.GetMeta(collection)
	if meta == nil {
		return 0
	}
	return meta.RecordCount
}

func workspaceAuditHasEvent(events []workspaceAuditEvent, operation, result string) bool {
	for _, event := range events {
		if event.Operation == operation && event.Result == result {
			return true
		}
	}
	return false
}

func workspaceConflictHasPath(paths []workspaceConflictPath, path string) bool {
	for _, item := range paths {
		if item.Path == path {
			return true
		}
	}
	return false
}

func workspaceConflictHasAction(actions []string, want string) bool {
	for _, action := range actions {
		if strings.Contains(action, want) {
			return true
		}
	}
	return false
}

func workspaceContextProjectIDSet(projects []workspaceProjectContext) map[string]bool {
	out := map[string]bool{}
	for _, project := range projects {
		out[project.ProjectID] = true
	}
	return out
}

func workspaceAnySliceContainsString(xs []any, want string) bool {
	for _, x := range xs {
		if s, ok := x.(string); ok && s == want {
			return true
		}
	}
	return false
}

func waitForWorkspaceCondition(t *testing.T, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
