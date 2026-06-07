// workspace_parity_test.go — proves the REST and MCP workspace surfaces share
// one access-policy code path (pkg/access via authorizeWorkspace) and that
// denied responses on either surface leak no private state.
package http

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seedWorkspaceMatrix wires one project owned by ownerID and shares it with the
// supplied user→role pairs. Every named principal is inserted as a real user
// row so a missing share means "authenticated non-member", not "unknown user".
func seedWorkspaceMatrix(t *testing.T, cfg APIConfig, ownerID, projectID string, foreigners []string, shares map[string]string) {
	t.Helper()
	insertUser := func(id string) {
		if _, err := cfg.DB.Exec(`INSERT OR IGNORE INTO users(id, email, is_superuser) VALUES (?, ?, 0)`, id, id+"@example.com"); err != nil {
			t.Fatalf("insert user %s: %v", id, err)
		}
	}
	insertUser(ownerID)
	if _, err := cfg.DB.Exec(`INSERT INTO datasets(id, owner_id) VALUES (?, ?)`, projectID, ownerID); err != nil {
		t.Fatalf("insert dataset: %v", err)
	}
	for user, role := range shares {
		insertUser(user)
		shareWorkspaceACL(t, cfg.DB, projectID, user, role)
	}
	for _, user := range foreigners {
		insertUser(user)
	}
}

func mcpResultText(res mcpToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// restWorkspaceReadDenied issues the real REST read route as userID and reports
// whether access was denied (HTTP 403). The seeded fixtures guarantee an
// authorized read of an existing file returns 200, so 403 is an unambiguous
// policy denial rather than a missing-file error.
func restWorkspaceReadDenied(t *testing.T, cfg APIConfig, userID, projectID, path string) (bool, []byte) {
	t.Helper()
	app := workspaceACLApp(userID, cfg)
	req := httptest.NewRequest(http.MethodGet, "/workspace/read?project_id="+projectID+"&branch=main&path="+path, nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode == http.StatusForbidden, buf.Bytes()
}

func restWorkspaceWriteDenied(t *testing.T, cfg APIConfig, userID, projectID, path, text string) (bool, []byte) {
	t.Helper()
	app := workspaceACLApp(userID, cfg)
	body, status := workspaceTestPost(t, app, "/workspace/write", map[string]any{
		"project_id": projectID,
		"branch":     "main",
		"path":       path,
		"text":       text,
		"index":      false,
	})
	return status == http.StatusForbidden, body
}

// mcpWorkspaceReadDenied drives the MCP read tool as userID. "Denied" is the
// shared access error specifically, so an authorized-but-other failure would
// not be mistaken for a policy denial.
func mcpWorkspaceReadDenied(t *testing.T, cfg APIConfig, userID, projectID, path string) (bool, string) {
	t.Helper()
	ctx := context.WithValue(context.Background(), mcpUserIDKey, userID)
	h := &mcpHandler{cfg: cfg}
	res := h.toolWorkspaceRead(ctx, map[string]any{"project_id": projectID, "branch": "main", "path": path})
	text := mcpResultText(res)
	return res.IsError && strings.Contains(text, errWorkspaceAccessDenied.Error()), text
}

func mcpWorkspaceWriteDenied(t *testing.T, cfg APIConfig, userID, projectID, path, text string) (bool, string) {
	t.Helper()
	ctx := context.WithValue(context.Background(), mcpUserIDKey, userID)
	h := &mcpHandler{cfg: cfg}
	res := h.toolWorkspaceWrite(ctx, map[string]any{
		"project_id": projectID,
		"branch":     "main",
		"path":       path,
		"text":       text,
		"index":      false,
	})
	out := mcpResultText(res)
	return res.IsError && strings.Contains(out, errWorkspaceAccessDenied.Error()), out
}

// TestWorkspaceRESTMCPAccessParity proves both transports reach the same
// allow/deny verdict for the same principal and action across owner, shared
// editor, shared viewer, and authenticated non-member. This is the acceptance
// criterion "MCP workspace tools and REST workspace routes share the same
// policy code" expressed as behavior.
func TestWorkspaceRESTMCPAccessParity(t *testing.T) {
	cfg, closeFn := newWorkspaceACLTestConfig(t)
	defer closeFn()
	seedWorkspaceMatrix(t, cfg, "user-a", "payments",
		[]string{"user-c"},
		map[string]string{"user-b": RoleViewer, "user-d": RoleEditor},
	)

	// Seed a readable file via the owner so authorized reads return 200.
	ownerApp := workspaceACLApp("user-a", cfg)
	if body, status := workspaceTestPost(t, ownerApp, "/workspace/write", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"path":       "docs/shared.md",
		"text":       "# Shared\n\nReadable.",
		"index":      false,
	}); status != http.StatusOK {
		t.Fatalf("seed read file status=%d body=%s", status, body)
	}

	cases := []struct {
		user           string
		readDenied     bool
		writeDenied    bool
		identityReason string
	}{
		{"user-a", false, false, "owner"},
		{"user-d", false, false, "editor"},
		{"user-b", false, true, "viewer can read, not write"},
		{"user-c", true, true, "non-member"},
	}

	for _, tc := range cases {
		t.Run(tc.user, func(t *testing.T) {
			restRead, _ := restWorkspaceReadDenied(t, cfg, tc.user, "payments", "docs/shared.md")
			mcpRead, _ := mcpWorkspaceReadDenied(t, cfg, tc.user, "payments", "docs/shared.md")
			if restRead != mcpRead {
				t.Fatalf("read parity broken (%s): REST denied=%v, MCP denied=%v", tc.identityReason, restRead, mcpRead)
			}
			if restRead != tc.readDenied {
				t.Fatalf("read verdict (%s): denied=%v, want %v", tc.identityReason, restRead, tc.readDenied)
			}

			restWrite, _ := restWorkspaceWriteDenied(t, cfg, tc.user, "payments", "docs/w-rest-"+tc.user+".md", "# W\n\nrest")
			mcpWrite, _ := mcpWorkspaceWriteDenied(t, cfg, tc.user, "payments", "docs/w-mcp-"+tc.user+".md", "# W\n\nmcp")
			if restWrite != mcpWrite {
				t.Fatalf("write parity broken (%s): REST denied=%v, MCP denied=%v", tc.identityReason, restWrite, mcpWrite)
			}
			if restWrite != tc.writeDenied {
				t.Fatalf("write verdict (%s): denied=%v, want %v", tc.identityReason, restWrite, tc.writeDenied)
			}
		})
	}
}

// TestWorkspaceDeniedResponsesDoNotLeak asserts that REST and MCP denials never
// echo the private file path, the file content, a collection name, a tenant id,
// or the search query text. The denial must read as a flat "access denied".
func TestWorkspaceDeniedResponsesDoNotLeak(t *testing.T) {
	cfg, closeFn := newWorkspaceACLTestConfig(t)
	defer closeFn()
	seedWorkspaceMatrix(t, cfg, "user-a", "payments",
		[]string{"user-c"},
		map[string]string{"user-b": RoleViewer},
	)

	const (
		secretPath    = "docs/secret.md"
		secretContent = "TOPSECRET-CLASSIFIED-PAYLOAD-9f3c"
		secretQuery   = "SECRET_QUERY_TOKEN_xyzzy"
		secretColl    = "secret_collection_zzz"
		tenantToken   = "tenant-zzz-do-not-leak"
	)
	leakTokens := []string{secretPath, secretContent, secretQuery, secretColl, tenantToken}

	ownerApp := workspaceACLApp("user-a", cfg)
	if body, status := workspaceTestPost(t, ownerApp, "/workspace/write", map[string]any{
		"project_id": "payments",
		"branch":     "main",
		"path":       secretPath,
		"text":       "# Secret\n\n" + secretContent,
		"index":      false,
	}); status != http.StatusOK {
		t.Fatalf("seed secret status=%d body=%s", status, body)
	}

	assertNoLeak := func(label string, body []byte) {
		for _, tok := range leakTokens {
			if bytes.Contains(body, []byte(tok)) {
				t.Fatalf("%s denied response leaked %q: %s", label, tok, body)
			}
		}
	}

	// REST read denial (foreign user-c).
	if denied, body := restWorkspaceReadDenied(t, cfg, "user-c", "payments", secretPath); !denied {
		t.Fatalf("REST foreign read not denied")
	} else {
		assertNoLeak("REST read", body)
	}

	// MCP read denial.
	if denied, text := mcpWorkspaceReadDenied(t, cfg, "user-c", "payments", secretPath); !denied {
		t.Fatalf("MCP foreign read not denied")
	} else {
		assertNoLeak("MCP read", []byte(text))
	}

	// REST write denial (viewer user-b lacks write).
	if denied, body := restWorkspaceWriteDenied(t, cfg, "user-b", "payments", secretPath, "# Secret\n\n"+secretContent); !denied {
		t.Fatalf("REST viewer write not denied")
	} else {
		assertNoLeak("REST write", body)
	}

	// MCP write denial.
	if denied, text := mcpWorkspaceWriteDenied(t, cfg, "user-b", "payments", secretPath, "# Secret\n\n"+secretContent); !denied {
		t.Fatalf("MCP viewer write not denied")
	} else {
		assertNoLeak("MCP write", []byte(text))
	}

	// MCP search denial — workspace search is MCP-only. The query text and
	// collection name must not survive into the denial.
	ctx := context.WithValue(context.Background(), mcpUserIDKey, "user-c")
	h := &mcpHandler{cfg: cfg}
	searchRes := h.toolWorkspaceSearch(ctx, map[string]any{
		"project_id":   "payments",
		"branch":       "main",
		"collection":   secretColl,
		"search_query": secretQuery,
	})
	if !searchRes.IsError || !strings.Contains(mcpResultText(searchRes), errWorkspaceAccessDenied.Error()) {
		t.Fatalf("MCP foreign search not denied: %+v", searchRes)
	}
	assertNoLeak("MCP search", []byte(mcpResultText(searchRes)))
}
