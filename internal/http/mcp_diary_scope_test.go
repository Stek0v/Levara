package http

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/mcp"
)

func TestMCPDiaryAuthenticatedScope(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		h := asyncAuthorityHandler(f)
		app := asyncAuthorityApp(h, nil) // Real JWT verification, no X-Test-User middleware.
		for _, transport := range []struct{ name, path string }{{"legacy", "/mcp"}, {"latest", latestMCPPath}} {
			t.Run(transport.name, func(t *testing.T) {
				collection := "diary-" + transport.name
				const key = "entry"
				call := func(user, tool, agent, collection, value string) mcp.ToolResult {
					t.Helper()
					args := map[string]any{"agent": agent, "collection": collection, "key": key}
					if tool == "diary_write" {
						args["value"] = value
					}
					return asyncAuthorityRPC(t, app, h, transport.path, user, tool, args)
				}
				write := func(user, agent, collection, value string) {
					t.Helper()
					result := call(user, "diary_write", agent, collection, value)
					if result.IsError {
						var count int
						if err := f.db.QueryRow(Q("SELECT COUNT(*) FROM memories WHERE collection_name=$1"), collection).Scan(&count); err != nil {
							t.Fatal(err)
						}
						t.Fatalf("authenticated own diary_write must succeed: persisted collection rows=%d raw ToolResult=%s", count, diaryScopeRaw(result))
					}
				}
				read := func(user, agent, collection string) []diaryScopeEntry {
					t.Helper()
					return diaryScopeEntries(t, call(user, "diary_read", agent, collection, ""))
				}
				private := "peer-private-" + transport.name
				write("peer", " reviewer ", collection, private)
				var canonicalID, namespace, kind string
				if err := f.db.QueryRow(Q("SELECT id,owner_id,type FROM memories WHERE value=$1 AND collection_name=$2"), private, collection).Scan(&canonicalID, &namespace, &kind); err != nil || kind != "diary" {
					t.Fatalf("public write not persisted as diary: type=%q error=%v", kind, err)
				}
				t.Logf("public write actor=peer persisted id=%s namespace=%q", canonicalID, namespace)
				if expected := diaryPolicyNamespace("peer", "a", "reviewer"); namespace != expected {
					t.Errorf("authenticated namespace=%q want %q", namespace, expected)
				}
				if got := read("peer", "reviewer", collection); len(got) != 1 || got[0].Key != key || got[0].Value != private {
					t.Fatalf("same-owner trimmed-name read missing own entry: %+v", got)
				}
				write("peer", "other-agent", collection, "other-agent-control")
				write("peer", "reviewer", collection+"-other", "other-collection-control")
				if got := read("peer", "other-agent", collection); len(got) != 1 || got[0].Value != "other-agent-control" {
					t.Fatalf("other-agent control missing: %+v", got)
				}
				if got := read("peer", "reviewer", collection+"-other"); len(got) != 1 || got[0].Value != "other-collection-control" {
					t.Fatalf("other-collection control missing: %+v", got)
				}
				updated := "peer-updated-" + transport.name
				write("peer", "reviewer", collection, updated)
				var id string
				if err := f.db.QueryRow(Q("SELECT id FROM memories WHERE value=$1 AND collection_name=$2"), updated, collection).Scan(&id); err != nil || id != canonicalID {
					t.Fatalf("same-owner overwrite changed canonical ID: got=%q want=%q error=%v", id, canonicalID, err)
				}
				if got := read("peer", " reviewer ", collection); len(got) != 1 || got[0].Value != updated {
					t.Fatalf("same-owner overwrite or collection/agent filter failed: %+v", got)
				}
				before := diaryScopeSnapshot(t, f)
				for _, user := range []string{"owner", "foreign"} {
					if got := read(user, "reviewer", collection); len(got) != 0 {
						t.Errorf("distinct valid JWT user %q read peer private diary (same agent/key/collection): %+v", user, got)
					}
				}
				if !reflect.DeepEqual(before, diaryScopeSnapshot(t, f)) {
					t.Error("diary_read modified memory rows")
				}
				ownerText := "owner-private-" + transport.name
				write("owner", " reviewer ", collection, ownerText)
				after := diaryScopeSnapshot(t, f)
				if !reflect.DeepEqual(before[canonicalID], after[canonicalID]) {
					t.Errorf("distinct JWT owner overwrote peer canonical row: before=%s after=%s", before[canonicalID], after[canonicalID])
				}
				for rowID, row := range before {
					if rowID != canonicalID && row != after[rowID] {
						t.Errorf("other-agent/collection control changed: id=%s before=%s after=%s", rowID, row, after[rowID])
					}
				}
				if got := read("peer", "reviewer", collection); len(got) != 1 || got[0].Value != updated {
					t.Errorf("peer lost its own diary after another authenticated user's write: %+v", got)
				}
				if got := read("owner", "reviewer", collection); len(got) != 1 || got[0].Value != ownerText {
					t.Errorf("second authenticated user cannot read its own entry: %+v", got)
				}
			})
		}
	})
}

// Explicit legacy agent rows isolate read behavior from PostgreSQL's public
// write failure. They are SQL fixtures, not evidence that diary_write succeeded.
func TestMCPDiaryLegacyReadScope(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		h := asyncAuthorityHandler(f)
		app := asyncAuthorityApp(h, nil)
		local := &mcpHandler{cfg: h.cfg, sessions: mcp.NewSessionStore()}
		local.cfg.RequireAuth = false
		localApp := asyncAuthorityApp(local, nil)
		for _, transport := range []struct{ name, path string }{{"legacy", "/mcp"}, {"latest", latestMCPPath}} {
			t.Run(transport.name, func(t *testing.T) {
				collection := "seeded-diary-" + transport.name
				for _, seed := range []struct{ id, agent, collection, value string }{
					{"seed-" + transport.name, "reviewer", collection, "explicit-legacy-agent-row"},
					{"seed-agent-" + transport.name, "other-agent", collection, "seeded-agent-control"},
					{"seed-collection-" + transport.name, "reviewer", collection + "-other", "seeded-collection-control"},
				} {
					f.exec(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,is_pinned,pin_priority,created_at,updated_at)
						VALUES($1,'entry',$2,'diary',$3,$4,'','',FALSE,0,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, seed.id, seed.value, mcp.DiaryOwner(seed.agent), seed.collection)
				}
				read := func(user, agent, selectedCollection string) []diaryScopeEntry {
					t.Helper()
					return diaryScopeEntries(t, asyncAuthorityRPC(t, app, h, transport.path, user, "diary_read", map[string]any{"agent": agent, "collection": selectedCollection}))
				}
				before := diaryScopeSnapshot(t, f)
				for _, control := range []struct{ agent, collection, value string }{
					{" reviewer ", collection, "explicit-legacy-agent-row"},
					{"other-agent", collection, "seeded-agent-control"},
					{"reviewer", collection + "-other", "seeded-collection-control"},
				} {
					got := diaryScopeEntries(t, diaryPolicyRPC(t, localApp, local, transport.path, "", "", "diary_read", map[string]any{"agent": control.agent, "collection": control.collection}))
					if len(got) != 1 || got[0].Value != control.value {
						t.Fatalf("trusted-local legacy read/control failed: %+v want %q", got, control.value)
					}
				}
				for _, user := range []string{"peer", "owner", "foreign"} {
					if got := read(user, "reviewer", collection); len(got) != 0 {
						t.Errorf("legacy agent row is readable by distinct valid JWT user %q (SQL seed, not public write proof): %+v", user, got)
					}
				}
				if !reflect.DeepEqual(before, diaryScopeSnapshot(t, f)) {
					t.Error("legacy diary reads modified persisted rows")
				}
				updated := diaryPolicyRPC(t, localApp, local, transport.path, "", "", "diary_write", map[string]any{"agent": "reviewer", "collection": collection, "key": "entry", "value": "trusted-local-updated"})
				if updated.IsError {
					t.Errorf("trusted-local public write must preserve legacy namespace: %s", diaryScopeRaw(updated))
				} else {
					var value string
					if err := f.db.QueryRow(Q("SELECT value FROM memories WHERE id=$1 AND owner_id=$2"), "seed-"+transport.name, mcp.DiaryOwner("reviewer")).Scan(&value); err != nil || value != "trusted-local-updated" {
						t.Errorf("trusted-local update changed legacy identity: value=%q error=%v", value, err)
					}
				}
			})
		}
	})
}

type diaryScopeEntry struct {
	Key, Value string
}

func diaryScopeEntries(t *testing.T, result mcp.ToolResult) []diaryScopeEntry {
	t.Helper()
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("diary_read failed: raw ToolResult=%s", diaryScopeRaw(result))
	}
	var payload struct {
		Entries []diaryScopeEntry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
		t.Fatalf("diary_read shape: %v raw ToolResult=%s", err, diaryScopeRaw(result))
	}
	return payload.Entries
}

func diaryScopeRaw(result mcp.ToolResult) string {
	raw, _ := json.Marshal(result)
	return string(raw)
}

func diaryScopeSnapshot(t *testing.T, f *documentHTTPFixture) map[string]string {
	t.Helper()
	rows, err := f.db.Query("SELECT * FROM memories ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := make(map[string]string)
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range pointers {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		row := make(map[string]any)
		for i, column := range columns {
			if bytes, ok := values[i].([]byte); ok {
				values[i] = string(bytes)
			}
			row[column] = values[i]
		}
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		snapshot[fmt.Sprint(row["id"])] = string(raw)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func diaryPolicyNamespace(user, tenant, agent string) string {
	raw, _ := json.Marshal([3]string{user, tenant, strings.TrimSpace(agent)})
	return "diary:v1:" + base64.RawURLEncoding.EncodeToString(raw)
}

func TestMCPDiaryWhitespaceAgentPolicy(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		h := asyncAuthorityHandler(f)
		app := asyncAuthorityApp(h, nil)
		for _, transport := range []struct{ name, path string }{{"legacy", "/mcp"}, {"latest", latestMCPPath}} {
			t.Run(transport.name, func(t *testing.T) {
				before := diaryScopeSnapshot(t, f)
				for _, tool := range []string{"diary_write", "diary_read"} {
					result := asyncAuthorityRPC(t, app, h, transport.path, "peer", tool, map[string]any{"agent": " \t\n ", "collection": "whitespace-" + transport.name, "key": "entry", "value": "must-not-persist"})
					if !result.IsError || strings.Contains(diaryScopeRaw(result), "is_pinned") {
						t.Errorf("whitespace-only agent must fail before SQL: tool=%s result=%s", tool, diaryScopeRaw(result))
					}
				}
				if !reflect.DeepEqual(before, diaryScopeSnapshot(t, f)) {
					t.Error("invalid normalized agent mutated memories")
				}
			})
		}
	})
}

func TestMCPDiaryReadScanErrorPolicy(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		h := asyncAuthorityHandler(f)
		app := asyncAuthorityApp(h, nil)
		// Production timestamps are NOT NULL. A read-only projection injects a
		// native scan failure without weakening stored rows or using a fake driver.
		for _, transport := range []string{"legacy", "latest"} {
			collection := "scan-error-" + transport
			for i, owner := range []string{mcp.DiaryOwner("reviewer"), diaryPolicyNamespace("peer", "a", "reviewer")} {
				for _, key := range []string{"valid", "broken"} {
					stamp := "2026-01-01T00:00:00Z"
					if key == "valid" {
						stamp = "2026-01-02T00:00:00Z" // Valid row is accumulated before the broken row.
					}
					f.exec(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,is_pinned,pin_priority,created_at,updated_at)
						VALUES($1,$2,$3,'diary',$4,$5,'','',FALSE,0,$6,$7)`, fmt.Sprintf("scan-%s-%s-%d", key, transport, i), key, key+"-partial-text", owner, collection, stamp, stamp)
				}
			}
		}
		f.exec("ALTER TABLE memories RENAME TO diary_scan_rows")
		f.exec(`CREATE VIEW memories AS SELECT id,key,value,type,owner_id,collection_name,
			CASE WHEN key='broken' THEN NULL ELSE created_at END AS created_at,updated_at FROM diary_scan_rows`)
		for _, transport := range []struct{ name, path string }{{"legacy", "/mcp"}, {"latest", latestMCPPath}} {
			t.Run(transport.name, func(t *testing.T) {
				collection := "scan-error-" + transport.name
				before := diaryScopeSnapshot(t, f)
				result := asyncAuthorityRPC(t, app, h, transport.path, "peer", "diary_read", map[string]any{"agent": "reviewer", "collection": collection})
				if !result.IsError || strings.Contains(diaryScopeRaw(result), "valid-partial-text") {
					t.Errorf("SQL scan failure returned partial success/data: %s", diaryScopeRaw(result))
				}
				if !reflect.DeepEqual(before, diaryScopeSnapshot(t, f)) {
					t.Error("failed read modified memories")
				}
			})
		}
	})
}

func TestMCPDiaryTenantAndNamespacePolicy(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('peer','b')")
		f.exec("INSERT INTO tenants(id,name,owner_id) VALUES('a:b','Delimiter tenant','peer')")
		f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('peer','a:b')")
		h := asyncAuthorityHandler(f)
		app := asyncAuthorityApp(h, nil)
		for _, transport := range []struct{ name, path string }{{"legacy", "/mcp"}, {"latest", latestMCPPath}} {
			t.Run(transport.name, func(t *testing.T) {
				collection := "namespace-" + transport.name
				legacyAgent := strings.TrimPrefix(diaryPolicyNamespace("peer", "a", "reviewer"), "diary:v1:")
				legacyID := "encoded-legacy-" + transport.name
				f.exec(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,is_pinned,pin_priority,created_at,updated_at)
					VALUES($1,'same-key','legacy-suffix-control','diary',$2,$3,'','',FALSE,0,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, legacyID, mcp.DiaryOwner(legacyAgent), collection)
				before := diaryScopeSnapshot(t, f)
				for _, tuple := range []struct{ tenant, agent, value string }{
					{"a", "b:c", "delimiter-left"}, {"a:b", "c", "delimiter-right"},
					{"a", " reviewer ", "same-user-tenant-a"}, {"b", "reviewer", "same-user-tenant-b"},
				} {
					args := map[string]any{"agent": tuple.agent, "key": "same-key", "value": tuple.value, "collection": collection,
						"owner_id": "foreign", "actor_id": "forged", "tenant_id": "forged-tenant"}
					written := diaryPolicyRPC(t, app, h, transport.path, "peer", tuple.tenant, "diary_write", args)
					if written.IsError {
						t.Fatalf("valid selected-tenant write failed: %s", diaryScopeRaw(written))
					}
					var owner string
					if err := f.db.QueryRow(Q("SELECT owner_id FROM memories WHERE value=$1 AND collection_name=$2"), tuple.value, collection).Scan(&owner); err != nil {
						t.Fatal(err)
					}
					if want := diaryPolicyNamespace("peer", tuple.tenant, tuple.agent); owner != want {
						t.Errorf("verified tuple namespace=%q want=%q; caller owner/actor/tenant hints must be ignored", owner, want)
					}
				}
				for _, tuple := range []struct{ tenant, agent, value string }{
					{"a", "b:c", "delimiter-left"}, {"a:b", "c", "delimiter-right"},
					{"a", "reviewer", "same-user-tenant-a"}, {"b", "reviewer", "same-user-tenant-b"},
				} {
					got := diaryScopeEntries(t, diaryPolicyRPC(t, app, h, transport.path, "peer", tuple.tenant, "diary_read", map[string]any{"agent": tuple.agent, "collection": collection, "owner_id": "foreign", "tenant_id": "forged-tenant"}))
					if len(got) != 1 || got[0].Value != tuple.value {
						t.Errorf("user/tenant/agent tuple collided: tenant=%q agent=%q got=%+v want=%q", tuple.tenant, tuple.agent, got, tuple.value)
					}
				}
				after := diaryScopeSnapshot(t, f)
				if before[legacyID] != after[legacyID] {
					t.Errorf("authenticated encoded namespace collided with legacy agent equal to encoded suffix: before=%s after=%s", before[legacyID], after[legacyID])
				}
				if got := diaryScopeEntries(t, diaryPolicyRPC(t, app, h, transport.path, "peer", "a", "diary_read", map[string]any{"agent": legacyAgent, "collection": collection})); len(got) != 0 {
					t.Errorf("authenticated caller fell back to legacy encoded-suffix agent: %+v", got)
				}
			})
		}
	})
}

// These actors model authority already verified by the transport. Changing SQL
// credential state afterwards exercises diary's own live guard rather than the
// earlier HTTP authentication check.
type diaryPolicyMetadataDeps struct {
	mcp.Deps
	actor accesspkg.MetadataActor
}

func (d *diaryPolicyMetadataDeps) MetadataActor(context.Context) accesspkg.MetadataActor {
	return d.actor
}

func TestMCPDiaryLiveCredentialPolicy(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		h := asyncAuthorityHandler(f)
		for _, scenario := range []string{"live-jwt", "read-only-key", "blank-key-permissions", "revoked-key", "changed-key-permissions", "inactive-user", "revoked-epoch", "trusted-local-with-user", "canceled", "pool-deadline"} {
			t.Run(scenario, func(t *testing.T) {
				f.exec("UPDATE users SET is_active=true WHERE id='peer'")
				f.exec("DELETE FROM credential_epochs WHERE user_id='peer'")
				defer f.exec("UPDATE users SET is_active=true WHERE id='peer'")
				defer f.exec("DELETE FROM credential_epochs WHERE user_id='peer'")
				actor := accesspkg.MetadataActor{Actor: accesspkg.Actor{UserID: "peer", TenantID: "a"}, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
				readAllowed, writeAllowed := scenario == "live-jwt" || scenario == "read-only-key", scenario == "live-jwt"
				if strings.Contains(scenario, "key") {
					permissions := "read-write"
					switch scenario {
					case "read-only-key":
						permissions = "read"
					case "blank-key-permissions":
						permissions = ""
					}
					keyID := "diary-key-" + scenario
					f.exec("INSERT INTO api_keys(id,key_hash,user_id,permissions) VALUES($1,$2,'peer',$3)", keyID, apikeyHash(keyID), permissions)
					actor.APIKeyPermissions = permissions
					actor.Credential = accesspkg.MetadataCredential{Kind: "api_key", KeyID: keyID}
					switch scenario {
					case "revoked-key":
						f.exec("UPDATE api_keys SET revoked=true WHERE id=$1", keyID)
					case "changed-key-permissions":
						f.exec("UPDATE api_keys SET permissions='read' WHERE id=$1", keyID)
					}
				}
				switch scenario {
				case "inactive-user":
					f.exec("UPDATE users SET is_active=false WHERE id='peer'")
				case "revoked-epoch":
					f.exec("INSERT INTO credential_epochs(user_id,epoch,revoked_before) VALUES('peer',1,0)")
				case "trusted-local-with-user":
					actor.TrustedLocal = true
					actor.Credential = accesspkg.MetadataCredential{}
				}
				collection := "credential-" + scenario
				ownID := "own-" + scenario
				for _, seed := range []struct{ id, owner, value string }{
					{ownID, diaryPolicyNamespace("peer", "a", "reviewer"), "current-own-text"},
					{"legacy-" + scenario, mcp.DiaryOwner("reviewer"), "legacy-control-text"},
				} {
					f.exec(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,is_pinned,pin_priority,created_at,updated_at)
						VALUES($1,'entry',$2,'diary',$3,$4,'','',FALSE,0,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, seed.id, seed.value, seed.owner, collection)
				}
				deps := &diaryPolicyMetadataDeps{Deps: h, actor: actor}
				before := diaryScopeSnapshot(t, f)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if scenario == "canceled" {
					cancel()
				}
				if scenario == "pool-deadline" {
					conn, err := f.db.Conn(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					defer conn.Close()
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
					defer cancel()
					// Release after both canceled calls before querying snapshots.
					waits := f.db.Stats().WaitCount
					read := mcp.ToolDiaryRead(ctx, deps, map[string]any{"agent": "reviewer", "collection": collection})
					writeCtx, writeCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
					write := mcp.ToolDiaryWrite(writeCtx, deps, map[string]any{"agent": "reviewer", "collection": collection, "key": "entry", "value": "replacement"})
					writeCancel()
					if f.db.Stats().WaitCount < waits+2 {
						t.Error("read and fresh-context write did not independently wait for the held connection")
					}
					if !read.IsError || !write.IsError {
						t.Errorf("pool deadline allowed diary call: read=%s write=%s", diaryScopeRaw(read), diaryScopeRaw(write))
					}
					_ = conn.Close()
					if !reflect.DeepEqual(before, diaryScopeSnapshot(t, f)) {
						t.Error("pool acquisition timeout changed memories")
					}
					return
				}
				read := mcp.ToolDiaryRead(ctx, deps, map[string]any{"agent": "reviewer", "collection": collection})
				if readAllowed {
					got := diaryScopeEntries(t, read)
					if len(got) != 1 || got[0].Value != "current-own-text" {
						t.Errorf("live credential did not read its own namespace: %+v", got)
					}
				} else if !read.IsError {
					t.Errorf("missing/revoked/canceled authority read was not denied: %s", diaryScopeRaw(read))
				}
				write := mcp.ToolDiaryWrite(ctx, deps, map[string]any{"agent": "reviewer", "collection": collection, "key": "entry", "value": "replacement"})
				if writeAllowed {
					if write.IsError {
						t.Errorf("live JWT write failed: %s", diaryScopeRaw(write))
					} else {
						var id, value string
						if err := f.db.QueryRow(Q("SELECT id,value FROM memories WHERE owner_id=$1 AND collection_name=$2 AND key='entry'"), diaryPolicyNamespace("peer", "a", "reviewer"), collection).Scan(&id, &value); err != nil || id != ownID || value != "replacement" {
							t.Errorf("authorized write missed canonical owned row: id=%q value=%q error=%v", id, value, err)
						}
					}
				} else if !write.IsError || strings.Contains(diaryScopeRaw(write), "is_pinned") {
					t.Errorf("denied write reached mutation instead of authority guard: %s", diaryScopeRaw(write))
				}
				after := diaryScopeSnapshot(t, f)
				if writeAllowed {
					delete(before, ownID)
					delete(after, ownID)
				}
				if !reflect.DeepEqual(before, after) {
					t.Error("denied write or authorized publication changed protected control rows")
				}
			})
		}
	})
}

// This wrapper adds explicit tenant selection and a true no-auth caller to the
// existing real JWT legacy/latest helpers; it never sets fixture identity locals.
func diaryPolicyRPC(t *testing.T, app *fiber.App, h *mcpHandler, path, user, tenant, tool string, args map[string]any) mcp.ToolResult {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	meta := ""
	if path == latestMCPPath {
		meta = "," + latestMCPMetaParams()
	}
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s%s}}`, tool, encoded, meta)
	headers := make(map[string]string)
	if user != "" {
		headers["Authorization"] = "Bearer " + createJWT(user, user+"@test.invalid", h.cfg.JWTSecret)
	}
	if tenant != "" {
		headers["X-Tenant-Id"] = tenant
	}
	var envelope struct {
		Result mcp.ToolResult `json:"result"`
		Error  any            `json:"error"`
	}
	if path == latestMCPPath {
		for key, value := range latestMCPHeaders("tools/call") {
			headers[key] = value
		}
		headers["Mcp-Name"] = tool
		resp := latestMCPPost(t, app, body, headers)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("latest status=%d", resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
	} else {
		headers["Mcp-Session-Id"] = h.createSession(user)
		status, raw := postRPC(t, app, body, headers)
		if status != http.StatusOK {
			t.Fatalf("legacy status=%d body=%s", status, raw)
		}
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			t.Fatal(err)
		}
	}
	if envelope.Error != nil || len(envelope.Result.Content) == 0 {
		t.Fatalf("RPC response=%+v", envelope)
	}
	return envelope.Result
}
