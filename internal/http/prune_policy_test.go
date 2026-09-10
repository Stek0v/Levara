package http

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/mcp"
)

func pruneTestCall(t *testing.T, f *documentHTTPFixture, operation, credential, tenant string) bool {
	t.Helper()
	cfg := f.cfg
	cfg.RequireAuth = true
	actor := accesspkg.Actor{UserID: "root", TenantID: tenant}
	expires := time.Now().Add(time.Hour).Unix()
	if credential == "expired" {
		expires = time.Now().Add(-time.Hour).Unix()
	}
	if credential == "read-only" {
		actor.APIKeyPermissions = "read"
		f.exec("INSERT INTO api_keys(id,key_hash,user_id,permissions) VALUES('prune-read','test','root','read')")
	}
	if operation == "mcp-delete" || operation == "mcp-prune" {
		ctx := context.WithValue(context.Background(), mcp.UserIDKey, actor.UserID)
		ctx = context.WithValue(ctx, mcp.TenantIDKey, tenant)
		ctx = context.WithValue(ctx, mcp.ContextKey("mcp_api_key_permissions"), actor.APIKeyPermissions)
		kind := "jwt"
		keyID := ""
		if credential == "read-only" {
			kind, keyID = "api_key", "prune-read"
		}
		if credential == "missing" {
			kind = ""
		}
		ctx = context.WithValue(ctx, searchEgressKey{}, searchEgress{cfg: cfg, actor: actor, kind: kind, keyID: keyID, expiresAt: expires})
		h := &mcpHandler{cfg: cfg}
		if operation == "mcp-delete" {
			return mcp.ToolDelete(ctx, h, map[string]any{"dataset_id": "alpha"}).IsError
		}
		return mcp.ToolPrune(ctx, h).IsError
	}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user_id", actor.UserID)
		c.Locals("tenant_id", tenant)
		c.Locals("api_key_permissions", actor.APIKeyPermissions)
		if credential == "read-only" {
			c.Locals("verified_api_key", accesspkg.APIKeyIdentity{KeyID: "prune-read"})
		} else if credential != "missing" {
			c.Locals("verified_jwt", jwtPayload{Sub: actor.UserID, Exp: expires})
		}
		return c.Next()
	})
	handler := pruneDataHandler(cfg)
	if operation == "http-system" {
		handler = pruneSystemHandler(cfg)
	}
	app.Post("/prune", handler)
	response, err := app.Test(httptest.NewRequest("POST", "/prune", nil), -1)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode >= 400
}

func TestPrunePolicySuccessRetainsTombstonesAndAudit(t *testing.T) {
	for _, operation := range []string{"mcp-prune", "http-data", "http-system"} {
		t.Run(operation, func(t *testing.T) {
			documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
				f.exec("INSERT INTO graph_nodes(id) VALUES('node')")
				f.exec("CREATE TABLE mcp_audit_events(id TEXT PRIMARY KEY)")
				f.exec("INSERT INTO mcp_audit_events VALUES('must-retain')")
				if _, err := f.p.GrantDocument(context.Background(), f.owner, f.r.DocumentRef, f.r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "peer"}, accesspkg.RoleViewer); err != nil {
					t.Fatal(err)
				}
				for range 2 {
					if pruneTestCall(t, f, operation, "live", "") {
						t.Fatal("valid prune failed")
					}
					for table, want := range map[string]int{"datasets": 0, "data": 0, "dataset_data": 0, "document_grants": 0, "document_resources": 1, "mcp_audit_events": 1} {
						var count int
						if err := f.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != want {
							t.Errorf("%s=%d want=%d err=%v", table, count, want, err)
						}
					}
				}
				var tombstone bool
				var revision int
				if err := f.db.QueryRow("SELECT tombstoned,content_revision FROM document_resources").Scan(&tombstone, &revision); err != nil || !tombstone || revision != 2 {
					t.Fatalf("tombstone=%v revision=%d err=%v", tombstone, revision, err)
				}
				var nodes int
				if err := f.db.QueryRow("SELECT COUNT(*) FROM graph_nodes").Scan(&nodes); err != nil {
					t.Fatal(err)
				}
				want := 0
				if operation == "http-data" {
					want = 1
				}
				if nodes != want {
					t.Fatalf("graph nodes=%d want=%d", nodes, want)
				}
			})
		})
	}
}

func TestPrunePolicyHoldsBlockEveryTransport(t *testing.T) {
	for _, operation := range []string{"mcp-delete", "mcp-prune", "http-data", "http-system"} {
		t.Run(operation, func(t *testing.T) {
			for _, state := range []string{"live", "orphan", "tombstone"} {
				t.Run(state, func(t *testing.T) {
					documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
						f.exec("UPDATE document_resources SET hold=true")
						if state == "orphan" {
							f.exec("UPDATE document_resources SET data_id='missing'")
						}
						if state == "tombstone" {
							f.exec("UPDATE document_resources SET tombstoned=true")
						}
						if !pruneTestCall(t, f, operation, "live", "") {
							t.Error("administrator bypassed legal hold")
						}
						for _, table := range []string{"datasets", "data"} {
							var count int
							if err := f.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 2 {
								t.Errorf("%s changed under hold: count=%d err=%v", table, count, err)
							}
						}
					})
				})
			}
		})
	}
}

func TestPrunePolicyRejectsExpiredMissingReadonlyAndTenant(t *testing.T) {
	for _, operation := range []string{"mcp-prune", "http-data", "http-system"} {
		t.Run(operation, func(t *testing.T) {
			for _, credential := range []string{"expired", "missing", "read-only", "tenant"} {
				t.Run(credential, func(t *testing.T) {
					documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
						tenant := ""
						if credential == "tenant" {
							tenant = "a"
							f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('root','a')")
						}
						if !pruneTestCall(t, f, operation, credential, tenant) {
							t.Error("unqualified credential pruned instance")
						}
						var count int
						if err := f.db.QueryRow("SELECT COUNT(*) FROM datasets").Scan(&count); err != nil || count != 2 {
							t.Errorf("unauthorized prune count=%d err=%v", count, err)
						}
					})
				})
			}
		})
	}
}

func TestPrunePolicySQLFailureRollsBackGraphAndData(t *testing.T) {
	for _, operation := range []string{"mcp-prune", "http-data", "http-system"} {
		t.Run(operation, func(t *testing.T) {
			documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
				f.exec("INSERT INTO graph_nodes(id) VALUES('node')")
				f.exec("INSERT INTO graph_edges(id,source_id,target_id) VALUES('edge','node','node')")
				trigger := "CREATE TRIGGER fail_prune BEFORE DELETE ON datasets BEGIN SELECT RAISE(ABORT, 'injected'); END"
				if GetDBProvider() == DBPostgres {
					trigger = "CREATE FUNCTION fail_prune_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$; CREATE TRIGGER fail_prune BEFORE DELETE ON datasets FOR EACH ROW EXECUTE FUNCTION fail_prune_fn()"
				}
				f.exec(trigger)
				if !pruneTestCall(t, f, operation, "live", "") {
					t.Fatal("SQL failure reported success")
				}
				for table, want := range map[string]int{"datasets": 2, "data": 2, "dataset_data": 3, "graph_nodes": 1, "graph_edges": 1} {
					var count int
					if err := f.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != want {
						t.Errorf("partial wipe %s=%d want=%d err=%v", table, count, want, err)
					}
				}
				var tombstone bool
				if err := f.db.QueryRow("SELECT tombstoned FROM document_resources").Scan(&tombstone); err != nil || tombstone {
					t.Errorf("partial tombstone=%v err=%v", tombstone, err)
				}
			})
		})
	}
}

func TestPrunePolicyMCPDeleteUsesVerifiedTenant(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg := f.cfg
		cfg.RequireAuth = true
		ctx := context.WithValue(context.Background(), mcp.UserIDKey, "owner")
		ctx = context.WithValue(ctx, mcp.TenantIDKey, "b")
		ctx = context.WithValue(ctx, searchEgressKey{}, searchEgress{cfg: cfg, actor: accesspkg.Actor{UserID: "owner", TenantID: "b"}, kind: "jwt", expiresAt: time.Now().Add(time.Hour).Unix()})
		if r := mcp.ToolDelete(ctx, &mcpHandler{cfg: cfg}, map[string]any{"dataset_id": "alpha", "tenant_id": "a", "actor_id": "root"}); !r.IsError {
			t.Fatal("tool argument replaced verified tenant or actor")
		}
		var count int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM datasets").Scan(&count); err != nil || count != 2 {
			t.Fatalf("datasets=%d err=%v", count, err)
		}
	})
}
