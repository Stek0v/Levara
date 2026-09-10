package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/audit"
)

type documentHTTPFixture struct {
	t           *testing.T
	db          *sql.DB
	app         *fiber.App
	cfg         APIConfig
	p           accesspkg.SQLPolicy
	r           accesspkg.DocumentResource
	owner       accesspkg.Actor
	auditEvents *[]audit.Event
}

func documentHTTPDialects(t *testing.T, run func(*testing.T, *documentHTTPFixture)) {
	t.Helper()
	for _, dialect := range []DBProvider{DBSQLite, DBPostgres} {
		t.Run(string(dialect), func(t *testing.T) {
			previous := GetDBProvider()
			SetDBProvider(dialect)
			t.Cleanup(func() { SetDBProvider(previous) })
			var db *sql.DB
			var err error
			var schema string
			if dialect == DBSQLite {
				db, err = sql.Open("sqlite3", filepath.Join(t.TempDir(), "documents.db"))
				if err != nil {
					t.Fatal(err)
				}
				db.SetMaxOpenConns(1)
			} else {
				dsn := os.Getenv("LEVARA_TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("LEVARA_TEST_POSTGRES_DSN is not set")
				}
				config, err := pgx.ParseConfig(dsn)
				if err != nil {
					t.Fatal(err)
				}
				schema = fmt.Sprintf("document_http_%d", time.Now().UnixNano())
				config.RuntimeParams["search_path"] = schema
				db = stdlib.OpenDB(*config)
				if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() {
				if schema != "" {
					_, _ = db.Exec("DROP SCHEMA " + schema + " CASCADE")
				}
				_ = db.Close()
			})
			if err := MigrateSchema(db); err != nil {
				t.Fatal(err)
			}
			// Match production identity startup before exercising egress fences.
			if err := accesspkg.EnsureIdentitySchema(context.Background(), db, Q); err != nil {
				t.Fatal(err)
			}
			if err := accesspkg.EnsureBrowserSessionSchema(context.Background(), db, Q); err != nil {
				t.Fatal(err)
			}
			events := []audit.Event{}
			f := &documentHTTPFixture{t: t, db: db, cfg: APIConfig{DB: db, StoragePath: t.TempDir()}, p: accesspkg.SQLPolicy{DB: db, Q: Q, QA: QArgs}, owner: accesspkg.Actor{UserID: "owner", TenantID: "a"}, auditEvents: &events}
			f.cfg.WorkspaceAuditSink = audit.EventSinkFunc(func(event audit.Event) { events = append(events, event) })
			for _, id := range []string{"owner", "viewer", "peer", "foreign", "root", "inactive"} {
				f.exec("INSERT INTO principals(id,type) VALUES($1,'user')", id)
				f.exec("INSERT INTO users(id,email,hashed_password,is_active,is_superuser) VALUES($1,$2,'locked',$3,$4)", id, id+"@test.invalid", id != "inactive", id == "root")
			}
			f.exec("INSERT INTO tenants(id,name,owner_id) VALUES('a','A','owner'),('b','B','foreign')")
			for _, id := range []string{"owner", "viewer", "peer", "inactive"} {
				f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES($1,'a')", id)
			}
			f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('foreign','b')")
			f.exec("INSERT INTO datasets(id,name,owner_id) VALUES('alpha','Alpha','owner'),('beta','Beta','owner')")
			for _, id := range []string{"blob", "visible"} {
				path := filepath.Join(f.cfg.StoragePath, id+".txt")
				if err := os.WriteFile(path, []byte(id+" bytes"), 0600); err != nil {
					t.Fatal(err)
				}
				f.exec("INSERT INTO data(id,name,owner_id,raw_data_location,data_size,raw_content_hash) VALUES($1,$2,'owner',$3,10,$4)", id, id+" name", "file://"+path, fmt.Sprintf("%x", sha256.Sum256([]byte(id+" bytes"))))
			}
			f.exec("INSERT INTO dataset_data(dataset_id,data_id) VALUES('alpha','blob'),('alpha','visible'),('beta','blob')")
			f.exec("INSERT INTO dataset_shares(id,dataset_id,user_id,role) VALUES('viewer-share','alpha','viewer','viewer')")
			f.r, err = f.p.RegisterDocument(context.Background(), f.owner, accesspkg.DocumentRef{DatasetID: "alpha", DataID: "blob"}, "a", accesspkg.DocumentRestricted)
			if err != nil {
				t.Fatal(err)
			}
			f.app = fiber.New(fiber.Config{DisableStartupMessage: true})
			// These locals model the already-authenticated middleware boundary;
			// the real policy still checks users, tenant membership and key limits.
			f.app.Use(func(c *fiber.Ctx) error {
				c.Locals("user_id", c.Get("X-Test-User"))
				c.Locals("verified_jwt", jwtPayload{Sub: c.Get("X-Test-User"), Exp: time.Now().Add(time.Hour).Unix()})
				c.Locals("api_key_permissions", c.Get("X-Test-Key"))
				return c.Next()
			})
			f.app.Use(TenantMiddleware(AccessConfig{DB: db}))
			f.routes()
			run(t, f)
		})
	}
}

func (f *documentHTTPFixture) routes() {
	api := f.app.Group("/api/v1")
	RegisterDocumentPolicyAPI(api, f.cfg)
	api.Get("/datasets", datasetsListHandler(f.cfg))
	api.Get("/datasets/:id/data", datasetDataHandler(f.cfg))
	api.Delete("/datasets/:id", datasetDeleteHandler(f.cfg))
	api.Get("/datasets/:id/data/:dataId/raw", datasetDataRawHandler(f.cfg))
	api.Get("/datasets/:id/data/:dataId/raw/url", datasetDataRawURLHandler(f.cfg))
	api.Delete("/datasets/:id/data/:dataId", datasetDataDeleteHandler(f.cfg))
	api.Patch("/datasets/:id/data/:dataId", updateDataHandler(f.cfg))
}

// Older CRUD fixtures deliberately model legacy source rows. Add the policy
// tables touched by legacy mutation and deletion without pulling in full auth.
func installDocumentRegistryFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	found := false
	for _, stmt := range schemaSQLiteStatements {
		if strings.HasPrefix(strings.TrimSpace(stmt), "CREATE TABLE IF NOT EXISTS document_resources (") {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatal(err)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("document registry DDL missing")
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS document_structured_artifacts (
			id TEXT PRIMARY KEY, data_id TEXT NOT NULL, source_revision INTEGER NOT NULL,
			raw_content_hash TEXT NOT NULL, artifact_sha256 TEXT NOT NULL, byte_size INTEGER NOT NULL,
			storage_location TEXT NOT NULL UNIQUE, destination TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL DEFAULT 'active', created_at TEXT DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS document_index_publications (data_id TEXT);
		CREATE TABLE IF NOT EXISTS source_revision_counter (
			id INTEGER PRIMARY KEY CHECK(id=1), value INTEGER NOT NULL CHECK(value>=1)
		);
		INSERT OR IGNORE INTO source_revision_counter(id,value) VALUES(1,1);
	`); err != nil {
		t.Fatal(err)
	}
}

func (f *documentHTTPFixture) exec(query string, args ...any) {
	f.t.Helper()
	query, args = QArgs(query, args...)
	if _, err := f.db.Exec(query, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *documentHTTPFixture) request(user, method, path, body string, headers ...string) (int, []byte, string) {
	f.t.Helper()
	req := httptest.NewRequest(method, "/api/v1"+path, bytes.NewBufferString(body))
	req.Header.Set("X-Test-User", user)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := f.app.Test(req, -1)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return resp.StatusCode, data, resp.Header.Get("ETag")
}

func TestDocumentRenameCleansRetiredStructuredArtifact(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.exec("DELETE FROM dataset_data WHERE dataset_id='beta' AND data_id='blob'")
		artifact := []byte(`{"value":"old"}`)
		root, err := filepath.EvalSymlinks(f.cfg.StoragePath)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "ingest-authorized", "rename", "0-structured")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, artifact, 0600); err != nil {
			t.Fatal(err)
		}
		artifactHash := fmt.Sprintf("%x", sha256.Sum256(artifact))
		f.exec(`INSERT INTO document_structured_artifacts
			(id,data_id,source_revision,raw_content_hash,artifact_sha256,byte_size,storage_location,destination,state)
			VALUES('rename-artifact','blob',1,$1,$2,$3,$4,$5,'active')`,
			fmt.Sprintf("%x", sha256.Sum256([]byte("blob bytes"))), artifactHash, len(artifact), "file://"+path, "local:"+root)
		status, body, _ := f.request("owner", "PATCH", "/datasets/alpha/data/blob", "renamed", "If-Match", documentETag(f.r))
		if status != 200 || !bytes.Contains(body, []byte(`"artifact_cleanup_pending":false`)) {
			t.Fatalf("rename=%d: %s", status, body)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("retired artifact remains: %v", err)
		}
		var count int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM document_structured_artifacts").Scan(&count); err != nil || count != 0 {
			t.Fatalf("artifact inventory=%d err=%v", count, err)
		}
	})
}

func TestDocumentHTTPRawAndListingPolicy(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		for _, path := range []string{"/datasets/alpha/data/blob/raw", "/datasets/alpha/data/blob/raw/url"} {
			status, body, _ := f.request("viewer", "GET", path, "")
			if status != 403 {
				t.Errorf("restricted dataset viewer: status=%d body=%s", status, body)
			}
		}
		var err error
		f.r, err = f.p.GrantDocument(context.Background(), f.owner, f.r.DocumentRef, f.r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "peer"}, accesspkg.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		status, body, _ := f.request("peer", "GET", "/datasets/alpha/data/blob/raw", "")
		if status != 200 || string(body) != "blob bytes" {
			t.Errorf("direct grant: status=%d body=%s", status, body)
		}
		status, body, _ = f.request("viewer", "GET", "/datasets/alpha/data", "")
		var docs []DataDTO
		if err := json.Unmarshal(body, &docs); err != nil {
			t.Fatal(err)
		}
		if status != 200 || len(docs) != 1 || docs[0].ID != "visible" || strings.Contains(string(body), "file://") {
			t.Errorf("filtered listing: status=%d body=%s", status, body)
		}
		status, body, _ = f.request("viewer", "GET", "/datasets", "")
		var datasets []DatasetDTO
		if err := json.Unmarshal(body, &datasets); err != nil {
			t.Fatal(err)
		}
		if status != 200 || len(datasets) != 1 || datasets[0].RecordCount != 1 || datasets[0].TotalSize != 10 {
			t.Errorf("filtered counts: status=%d body=%s", status, body)
		}
	})
}

func TestDatasetActivityFiltersRestrictedDocuments(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.app.Get("/api/v1/datasets/:id/activity", datasetActivityHandler(f.cfg))
		code, body, _ := f.request("viewer", "GET", "/datasets/alpha/activity", "")
		if code != 200 || !bytes.Contains(body, []byte("visible name")) || bytes.Contains(body, []byte("blob name")) {
			t.Fatalf("restricted activity metadata leaked: status=%d body=%s", code, body)
		}
		grant, err := f.p.GrantDocument(context.Background(), f.owner, f.r.DocumentRef, f.r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "viewer"}, accesspkg.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		f.r = grant
		code, body, _ = f.request("viewer", "GET", "/datasets/alpha/activity", "")
		if code != 200 || !bytes.Contains(body, []byte("blob name")) {
			t.Fatalf("directly granted activity metadata missing: status=%d body=%s", code, body)
		}
	})
}

func TestDocumentHTTPHoldPrecedesDelete(t *testing.T) {
	for _, path := range []string{"/datasets/alpha/data/blob", "/datasets/alpha"} {
		t.Run(path, func(t *testing.T) {
			documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
				var err error
				f.r, err = f.p.SetDocumentHold(context.Background(), f.owner, f.r.DocumentRef, f.r.ACLRevision, true)
				if err != nil {
					t.Fatal(err)
				}
				status, body, _ := f.request("owner", "DELETE", path, "", "If-Match", fmt.Sprintf("\"%d:%d\"", f.r.ACLRevision, f.r.ContentRevision))
				if status != 403 {
					t.Errorf("held deletion: status=%d body=%s", status, body)
				}
				var count int
				if err := f.db.QueryRow("SELECT COUNT(*) FROM dataset_data WHERE dataset_id='alpha' AND data_id='blob'").Scan(&count); err != nil || count != 1 {
					t.Errorf("held association was deleted: count=%d err=%v", count, err)
				}
			})
		})
	}
}

func (f *documentHTTPFixture) expect(user, method, path, body string, want int, headers ...string) []byte {
	f.t.Helper()
	status, data, _ := f.request(user, method, path, body, headers...)
	if status != want {
		f.t.Fatalf("%s %s user=%s: status=%d want=%d body=%s", method, path, user, status, want, data)
	}
	return data
}

func (f *documentHTTPFixture) current() accesspkg.DocumentResource {
	f.t.Helper()
	r, err := f.p.GetDocumentResource(context.Background(), f.r.DocumentRef)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func TestDocumentHTTPManagementAndGroups(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		const base = "/datasets/alpha/data/blob"
		for _, user := range []string{"viewer", "peer", "foreign", "inactive", ""} {
			want := 403
			if user == "inactive" {
				want = 401
			}
			f.expect(user, "GET", base+"/policy", "", want)
		}
		f.expect("owner", "GET", base+"/policy", "", 200, "X-Test-Key", "read")
		f.expect("owner", "POST", "/datasets/alpha/data/visible/policy", `{"tenant_id":"b","mode":"restricted"}`, 403)
		f.expect("root", "POST", "/datasets/alpha/data/visible/policy", `{"tenant_id":"a","mode":"inherit"}`, 200)
		grant := fmt.Sprintf(`{"acl_revision":%d,"principal_kind":"user","principal_id":"peer","role":"editor"}`, f.r.ACLRevision)
		f.expect("owner", "POST", base+"/grants", grant, 403, "X-Test-Key", "read")
		f.expect("owner", "POST", base+"/grants", grant, 200)
		f.expect("peer", "GET", base, "", 200)
		f.expect("peer", "GET", "/datasets/alpha/data", "", 403)
		f.expect("peer", "GET", "/datasets/beta/data/blob/raw", "", 403)
		f.expect("peer", "GET", base+"/raw", "", 403, "X-Tenant-Id", "b")
		f.expect("owner", "POST", base+"/grants", grant, 409)
		r := f.current()
		f.expect("peer", "POST", base+"/grants", fmt.Sprintf(`{"acl_revision":%d,"principal_kind":"user","principal_id":"peer","role":"admin"}`, r.ACLRevision), 403, "X-Test-Key", "write")
		f.expect("peer", "PATCH", base+"/policy", fmt.Sprintf(`{"acl_revision":%d,"hold":true}`, r.ACLRevision), 403)
		f.expect("peer", "DELETE", base, "", 403, "If-Match", documentETag(r))
		f.expect("owner", "POST", base+"/grants", fmt.Sprintf(`{"acl_revision":%d,"principal_kind":"user","principal_id":"foreign","role":"viewer"}`, r.ACLRevision), 403)
		f.expect("owner", "DELETE", base+"/grants/user/peer", fmt.Sprintf(`{"acl_revision":%d}`, r.ACLRevision), 200)
		f.expect("peer", "GET", base+"/raw", "", 403)
		groupBody := f.expect("owner", "POST", "/document-groups", `{"tenant_id":"a","name":"Readers"}`, 200)
		var group struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		}
		if err := json.Unmarshal(groupBody, &group); err != nil {
			t.Fatal(err)
		}
		groupPath := "/document-groups/" + group.ID
		f.expect("owner", "GET", groupPath, "", 200, "X-Test-Key", "read")
		f.expect("foreign", "GET", groupPath, "", 403)
		f.expect("owner", "PUT", groupPath+"/members", `{"revision":1,"members":["foreign"]}`, 403)
		f.expect("owner", "PUT", groupPath+"/members", `{"revision":1,"members":["peer"]}`, 200)
		f.expect("owner", "PUT", groupPath+"/members", `{"revision":1,"members":[]}`, 409)
		r = f.current()
		f.expect("owner", "POST", base+"/grants", fmt.Sprintf(`{"acl_revision":%d,"principal_kind":"group","principal_id":%q,"role":"viewer"}`, r.ACLRevision, group.ID), 200)
		f.expect("peer", "GET", base+"/raw", "", 200)
		f.expect("owner", "PUT", groupPath+"/members", `{"revision":2,"members":[]}`, 200)
		f.expect("peer", "GET", base+"/raw", "", 403)
		r = f.current()
		f.expect("owner", "PATCH", base+"/policy", fmt.Sprintf(`{"acl_revision":%d,"mode":"inherit"}`, r.ACLRevision), 200)
		f.expect("viewer", "GET", base+"/raw", "", 200)
		r = f.current()
		f.expect("owner", "PATCH", base+"/policy", fmt.Sprintf(`{"acl_revision":%d,"hold":true}`, r.ACLRevision), 200)
		f.expect("root", "PATCH", base, "renamed", 403, "If-Match", documentETag(f.current()))
		f.exec("UPDATE users SET is_active=FALSE WHERE id='owner'")
		f.expect("owner", "GET", base+"/policy", "", 401)

		seen := map[string]bool{}
		seenDenied := false
		for _, event := range *f.auditEvents {
			if event.Source != "document.rest" || (event.Outcome != "success" && event.Outcome != "denied" && event.Outcome != "failure") || event.ActorID != event.VerifiedScope.ActorID {
				t.Fatalf("invalid document audit event: %+v", event)
			}
			if event.Outcome == "success" {
				if !event.VerifiedScope.Verified {
					t.Fatalf("successful document audit lacks verified scope: %+v", event)
				}
				seen[event.Type] = true
			}
			seenDenied = seenDenied || event.Outcome == "denied"
		}
		for _, eventType := range []string{"grant", "revoke", "group_create", "group_members_replace", "set_mode", "set_hold"} {
			if !seen[eventType] {
				t.Errorf("missing %s audit event: %+v", eventType, *f.auditEvents)
			}
		}
		if !seenDenied {
			t.Errorf("missing denied document audit event: %+v", *f.auditEvents)
		}
	})
}

func TestDocumentHTTPRecipientAndSharedDiscovery(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		group, err := f.p.CreateGroup(context.Background(), f.owner, "a", "Reviewers")
		if err != nil {
			t.Fatal(err)
		}
		group, err = f.p.ReplaceGroupMembers(context.Background(), f.owner, group.ID, group.Revision, []string{"viewer"})
		if err != nil {
			t.Fatal(err)
		}

		body := f.expect("owner", "GET", "/datasets/alpha/data/blob/recipients", "", 200)
		var recipients accesspkg.DocumentRecipients
		if err := json.Unmarshal(body, &recipients); err != nil {
			t.Fatal(err)
		}
		if recipients.TenantID != "a" || len(recipients.Users) != 3 || len(recipients.Groups) != 1 || recipients.Groups[0].ID != group.ID || recipients.Groups[0].MemberCount != 1 {
			t.Fatalf("recipients=%+v", recipients)
		}
		if strings.Contains(string(body), "foreign@test.invalid") || strings.Contains(string(body), "inactive@test.invalid") {
			t.Fatalf("foreign or inactive recipient leaked: %s", body)
		}
		f.expect("owner", "GET", "/datasets/alpha/data/blob/recipients", "", 403, "X-Test-Key", "read")
		f.expect("viewer", "GET", "/datasets/alpha/data/blob/recipients", "", 403)

		f.r, err = f.p.GrantDocument(context.Background(), f.owner, f.r.DocumentRef, f.r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentGroup, ID: group.ID}, accesspkg.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		body = f.expect("viewer", "GET", "/documents/shared", "", 200)
		var shared struct {
			Documents []accesspkg.SharedDocument `json:"documents"`
			Limit     int                        `json:"limit"`
		}
		if err := json.Unmarshal(body, &shared); err != nil {
			t.Fatal(err)
		}
		if shared.Limit != 50 || len(shared.Documents) != 1 || shared.Documents[0].DatasetID != "alpha" || shared.Documents[0].DataID != "blob" || shared.Documents[0].Role != accesspkg.RoleViewer {
			t.Fatalf("shared documents=%+v", shared)
		}
		f.expect("foreign", "GET", "/documents/shared", "", 200)
		f.expect("inactive", "GET", "/documents/shared", "", 401)
		f.expect("viewer", "GET", "/documents/shared?limit=0", "", 400)

		f.r, err = f.p.GrantDocument(context.Background(), f.owner, f.r.DocumentRef, f.r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "peer"}, accesspkg.RoleAdmin)
		if err != nil {
			t.Fatal(err)
		}
		f.expect("peer", "GET", "/datasets/alpha/data/blob/recipients", "", 200)
		group, err = f.p.ReplaceGroupMembers(context.Background(), f.owner, group.ID, group.Revision, nil)
		if err != nil {
			t.Fatal(err)
		}
		body = f.expect("viewer", "GET", "/documents/shared", "", 200)
		if strings.Contains(string(body), `"data_id":"blob"`) {
			t.Fatalf("removed group member retained shared discovery: %s", body)
		}
		f.r, err = f.p.RevokeDocument(context.Background(), f.owner, f.r.DocumentRef, f.r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "peer"})
		if err != nil {
			t.Fatal(err)
		}
		f.expect("peer", "GET", "/datasets/alpha/data/blob/recipients", "", 403)
		body = f.expect("peer", "GET", "/documents/shared", "", 200)
		if strings.Contains(string(body), `"data_id":"blob"`) {
			t.Fatalf("revoked user retained shared discovery: %s", body)
		}

		body = f.expect("owner", "POST", "/datasets/alpha/data/visible/policy", `{"mode":"restricted"}`, 200)
		if !strings.Contains(string(body), `"tenant_id":"a"`) {
			t.Fatalf("active tenant was not used for registration: %s", body)
		}
	})
}

func TestDocumentHTTPDiscoveryRechecksRevokedCredential(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		group, err := f.p.CreateGroup(context.Background(), f.owner, "a", "Credential fence")
		if err != nil {
			t.Fatal(err)
		}
		for _, endpoint := range []struct {
			name, user, path string
		}{
			{"policy", "owner", "/datasets/alpha/data/blob/policy"},
			{"recipients", "owner", "/datasets/alpha/data/blob/recipients"},
			{"group", "owner", "/document-groups/" + group.ID},
			{"shared", "viewer", "/documents/shared"},
		} {
			for _, credential := range []string{"api_key", "browser_session"} {
				t.Run(endpoint.name+"/"+credential, func(t *testing.T) {
					verified := make(chan struct{})
					proceed := make(chan struct{})
					cfg := f.cfg
					cfg.RequireAuth = true
					keyID := endpoint.name + "-" + credential
					expires := time.Now().Add(time.Hour).Unix()
					var sessionID string
					if credential == "api_key" {
						f.exec("INSERT INTO api_keys(id,key_hash,user_id,permissions) VALUES($1,$2,$3,'write')", keyID, "hash-"+keyID, endpoint.user)
					} else {
						var err error
						sessionID, err = accesspkg.CreateBrowserSession(context.Background(), f.db, Q, endpoint.user, expires)
						if err != nil {
							t.Fatal(err)
						}
					}

					app := fiber.New(fiber.Config{DisableStartupMessage: true})
					app.Use(func(c *fiber.Ctx) error {
						c.Locals("user_id", endpoint.user)
						c.Locals("tenant_id", "a")
						if credential == "api_key" {
							c.Locals("api_key_permissions", "write")
							c.Locals("verified_api_key", accesspkg.APIKeyIdentity{KeyID: keyID, UserID: endpoint.user, Permissions: "write"})
						} else {
							c.Locals("verified_jwt", jwtPayload{Sub: endpoint.user, Exp: expires, SessionID: sessionID})
						}
						close(verified)
						<-proceed
						return c.Next()
					})
					RegisterDocumentPolicyAPI(app.Group("/api/v1"), cfg)
					type result struct {
						status int
						body   []byte
						err    error
					}
					done := make(chan result, 1)
					go func() {
						resp, err := app.Test(httptest.NewRequest("GET", "/api/v1"+endpoint.path, nil), -1)
						if err != nil {
							done <- result{err: err}
							return
						}
						defer resp.Body.Close()
						body, readErr := io.ReadAll(resp.Body)
						done <- result{status: resp.StatusCode, body: body, err: readErr}
					}()
					<-verified
					if credential == "api_key" {
						f.exec("UPDATE api_keys SET revoked=TRUE WHERE id=$1", keyID)
					} else if err := accesspkg.RevokeBrowserSession(context.Background(), f.db, Q, endpoint.user, sessionID); err != nil {
						t.Fatal(err)
					}
					close(proceed)
					got := <-done
					if got.err != nil || got.status != fiber.StatusUnauthorized || bytes.Contains(got.body, []byte("blob")) || bytes.Contains(got.body, []byte("@test.invalid")) {
						t.Fatalf("revoked discovery status=%d body=%s err=%v", got.status, got.body, got.err)
					}
				})
			}
		}
	})
}

func TestDocumentSharedDiscoveryFenceBlocksAuthorityChangeUntilDrain(t *testing.T) {
	for _, change := range []string{"grant_revoke", "group_member_removal", "user_deactivate"} {
		t.Run(change, func(t *testing.T) {
			documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
				ctx := context.Background()
				var revoke func(context.Context) error
				switch change {
				case "group_member_removal":
					group, err := f.p.CreateGroup(ctx, f.owner, "a", "Fence group")
					if err != nil {
						t.Fatal(err)
					}
					group, err = f.p.ReplaceGroupMembers(ctx, f.owner, group.ID, group.Revision, []string{"viewer"})
					if err != nil {
						t.Fatal(err)
					}
					f.r, err = f.p.GrantDocument(ctx, f.owner, f.r.DocumentRef, f.r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentGroup, ID: group.ID}, accesspkg.RoleViewer)
					if err != nil {
						t.Fatal(err)
					}
					revoke = func(ctx context.Context) error {
						_, err := f.p.ReplaceGroupMembers(ctx, f.owner, group.ID, group.Revision, nil)
						return err
					}
				default:
					var err error
					f.r, err = f.p.GrantDocument(ctx, f.owner, f.r.DocumentRef, f.r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "viewer"}, accesspkg.RoleViewer)
					if err != nil {
						t.Fatal(err)
					}
					if change == "grant_revoke" {
						revoke = func(ctx context.Context) error {
							_, err := f.p.RevokeDocument(ctx, f.owner, f.r.DocumentRef, f.r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "viewer"})
							return err
						}
					} else {
						revoke = func(ctx context.Context) error {
							_, err := f.db.ExecContext(ctx, Q("UPDATE users SET is_active=FALSE WHERE id=$1"), "viewer")
							return err
						}
					}
				}

				cfg := f.cfg
				cfg.RequireAuth = true
				discovered := make(chan struct{})
				allowDrain := make(chan struct{})
				app := fiber.New(fiber.Config{DisableStartupMessage: true})
				app.Use(func(c *fiber.Ctx) error {
					c.Locals("user_id", "viewer")
					c.Locals("tenant_id", "a")
					c.Locals("verified_jwt", jwtPayload{Sub: "viewer", Exp: time.Now().Add(time.Hour).Unix()})
					return c.Next()
				})
				app.Get("/probe", func(c *fiber.Ctx) error {
					requestCtx, cancel := apiRequestContext(c)
					defer cancel()
					return withProtectedPolicyResponse(c, cfg, requestCtx, func(ctx context.Context, p accesspkg.SQLPolicy) error {
						documents, err := p.ListSharedDocuments(ctx, workspaceActorFromFiber(c), 50)
						if err != nil {
							return documentHTTPError(err)
						}
						close(discovered)
						<-allowDrain
						return c.JSON(fiber.Map{"documents": documents})
					})
				})
				type responseResult struct {
					status int
					body   []byte
					err    error
				}
				responseDone := make(chan responseResult, 1)
				go func() {
					resp, err := app.Test(httptest.NewRequest("GET", "/probe", nil), -1)
					if err != nil {
						responseDone <- responseResult{err: err}
						return
					}
					defer resp.Body.Close()
					body, readErr := io.ReadAll(resp.Body)
					responseDone <- responseResult{status: resp.StatusCode, body: body, err: readErr}
				}()
				<-discovered
				revokerStarted := make(chan struct{})
				revoked := make(chan error, 1)
				go func() {
					close(revokerStarted)
					revokeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					revoked <- revoke(revokeCtx)
				}()
				<-revokerStarted
				select {
				case err := <-revoked:
					t.Fatalf("authority change escaped discovery fence: %v", err)
				case <-time.After(100 * time.Millisecond):
				}
				close(allowDrain)
				got := <-responseDone
				if got.err != nil || got.status != fiber.StatusOK || !bytes.Contains(got.body, []byte(`"data_id":"blob"`)) {
					t.Fatalf("fenced response status=%d body=%s err=%v", got.status, got.body, got.err)
				}
				if err := <-revoked; err != nil {
					t.Fatal(err)
				}
				documents, err := f.p.ListSharedDocuments(context.Background(), accesspkg.Actor{UserID: "viewer", TenantID: "a"}, 50)
				if change == "user_deactivate" {
					if !errors.Is(err, accesspkg.ErrDocumentForbidden) {
						t.Fatalf("deactivated user discovery err=%v", err)
					}
				} else if err != nil || len(documents) != 0 {
					t.Fatalf("revoked authority retained discovery: documents=%+v err=%v", documents, err)
				}
			})
		})
	}
}

type documentPresignStorage struct {
	*memStorage
	presigns int
}

func (s *documentPresignStorage) PresignGet(context.Context, string, time.Duration) (string, error) {
	s.presigns++
	return "https://object.invalid/capability", nil
}

func TestDocumentHTTPRegisteredURLAlwaysProxy(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		store := &documentPresignStorage{memStorage: newMemStorage()}
		if err := store.Save(context.Background(), "secret", strings.NewReader("storage bytes")); err != nil {
			t.Fatal(err)
		}
		f.exec("UPDATE data SET raw_data_location='storage://secret',original_data_location='storage://secret' WHERE id='blob'")
		f.cfg.FileStorage = store
		// Install the configured storage on distinct routes to test the exact
		// production handlers without replacing an already registered Fiber route.
		f.app.Get("/api/v1/storage/:id/:dataId/url", datasetDataRawURLHandler(f.cfg))
		f.app.Get("/api/v1/storage/:id/:dataId/raw", datasetDataRawHandler(f.cfg))
		body := f.expect("owner", "GET", "/storage/alpha/blob/url", "", 200)
		if store.presigns != 0 || strings.Contains(string(body), "storage://") || strings.Contains(string(body), "capability") || !strings.Contains(string(body), `"presigned":false`) {
			t.Fatalf("registered URL bypass: %s presigns=%d", body, store.presigns)
		}
		for _, user := range []string{"foreign", "viewer", ""} {
			f.expect(user, "GET", "/storage/alpha/blob/url", "", 403)
		}
		f.expect("owner", "GET", "/storage/alpha/blob/raw?original=true", "", 200)
		f.exec("UPDATE document_resources SET tombstoned=TRUE WHERE dataset_id='alpha' AND data_id='blob'")
		f.expect("owner", "GET", "/storage/alpha/blob/raw?original=true", "", 403)
	})
}

func TestDocumentHTTPDeleteRenameAndRollback(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		const base = "/datasets/alpha/data/blob"
		f.expect("owner", "DELETE", base, "", 428)
		f.expect("owner", "DELETE", base, "", 409, "If-Match", `"99:1"`)
		f.expect("owner", "PATCH", base, "new name", 409, "If-Match", documentETag(f.r))
		if f.current() != f.r {
			t.Fatal("failed shared rename advanced version")
		}
		f.exec("DELETE FROM dataset_data WHERE dataset_id='beta' AND data_id='blob'")
		f.expect("owner", "PATCH", base, "new name", 200, "If-Match", documentETag(f.r))
		r := f.current()
		// Force failure after tombstone CAS/unlink. Both must roll back.
		if GetDBProvider() == DBSQLite {
			f.exec("CREATE TRIGGER reject_doc_delete BEFORE DELETE ON data BEGIN SELECT RAISE(ABORT,'test delete failure'); END")
		} else {
			f.exec("CREATE FUNCTION reject_doc_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test delete failure'; END $$")
			f.exec("CREATE TRIGGER reject_doc_delete BEFORE DELETE ON data FOR EACH ROW EXECUTE FUNCTION reject_doc_delete()")
		}
		f.expect("owner", "DELETE", base, "", 500, "If-Match", documentETag(r))
		if f.current() != r {
			t.Fatal("SQL failure left tombstone/revision")
		}
		f.expect("owner", "GET", base+"/raw", "", 200)
		if GetDBProvider() == DBSQLite {
			f.exec("DROP TRIGGER reject_doc_delete")
		} else {
			f.exec("DROP TRIGGER reject_doc_delete ON data")
		}
		f.expect("owner", "DELETE", base, "", 200, "If-Match", documentETag(r))
		if !f.current().Tombstoned {
			t.Fatal("deleted association has no tombstone")
		}
		f.expect("owner", "GET", base+"/raw", "", 403)
	})
}

func TestDocumentHTTPDatasetDeleteRollsBackAll(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		other, err := f.p.RegisterDocument(context.Background(), f.owner, accesspkg.DocumentRef{DatasetID: "alpha", DataID: "visible"}, "a", accesspkg.DocumentRestricted)
		if err != nil {
			t.Fatal(err)
		}
		other, err = f.p.SetDocumentHold(context.Background(), f.owner, other.DocumentRef, other.ACLRevision, true)
		if err != nil {
			t.Fatal(err)
		}
		f.expect("owner", "DELETE", "/datasets/alpha", "", 403)
		if f.current() != f.r {
			t.Fatal("later held doc left earlier tombstone")
		}
		if _, err = f.p.SetDocumentHold(context.Background(), f.owner, other.DocumentRef, other.ACLRevision, false); err != nil {
			t.Fatal(err)
		}
		// The dataset DELETE itself can fail after all document CAS writes.
		// A restricting FK proves those tombstones roll back on both dialects.
		f.exec("CREATE TABLE prevent_dataset_delete (dataset_id TEXT REFERENCES datasets(id))")
		f.exec("INSERT INTO prevent_dataset_delete(dataset_id) VALUES('alpha')")
		f.expect("owner", "DELETE", "/datasets/alpha", "", 500)
		if f.current() != f.r {
			t.Fatal("failed dataset SQL deletion left tombstone")
		}
		f.exec("DROP TABLE prevent_dataset_delete")
		f.expect("owner", "DELETE", "/datasets/alpha", "", 200)
		if !f.current().Tombstoned {
			t.Fatal("dataset deletion removed registry")
		}
		f.expect("owner", "GET", "/datasets/beta/data/blob/raw", "", 200)
	})
}

func TestDocumentHTTPPolicySQLErrorDoesNotFallback(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.exec("ALTER TABLE document_resources RENAME TO unavailable_document_resources")
		for _, path := range []string{"/datasets/alpha/data/blob/raw", "/datasets/alpha/data/blob/raw/url", "/datasets/alpha/data", "/datasets"} {
			f.expect("owner", "GET", path, "", 500)
		}
	})
}

func TestDocumentHTTPLegacyLinkCannotRenameRegisteredBlob(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		if _, err := f.p.SetDocumentHold(context.Background(), f.owner, f.r.DocumentRef, f.r.ACLRevision, true); err != nil {
			t.Fatal(err)
		}
		status, body, _ := f.request("owner", "PATCH", "/datasets/beta/data/blob", "changed through legacy link")
		if status != 409 {
			t.Errorf("legacy alias changed registered source: status=%d body=%s", status, body)
		}
		var name string
		if err := f.db.QueryRow("SELECT name FROM data WHERE id='blob'").Scan(&name); err != nil || name != "blob name" {
			t.Errorf("held source name changed: name=%q err=%v", name, err)
		}
		f.expect("owner", "PATCH", "/datasets/alpha/data/visible", "legacy name", 200)
	})
}

func TestDocumentHTTPManagementRoutesAreRegistered(t *testing.T) {
	app := fiber.New()
	RegisterAPI(app.Group("/api/v1"), APIConfig{StoragePath: t.TempDir(), WorkspacePath: t.TempDir()})
	for _, path := range []string{"/api/v1/document-groups", "/api/v1/datasets/a/data/b/policy"} {
		resp, err := app.Test(httptest.NewRequest("POST", path, strings.NewReader(`{}`)), -1)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == 404 {
			t.Fatalf("management route not dispatched: %s status=%d", path, resp.StatusCode)
		}
	}
}

type documentPolicyState struct {
	resources, grants, groups, members, principals int
	aclRevision, groupRevision                     int64
	mode                                           string
	hold                                           bool
}

func snapshotDocumentPolicyState(t *testing.T, f *documentHTTPFixture, groupID string) documentPolicyState {
	t.Helper()
	var state documentPolicyState
	for query, target := range map[string]*int{
		"SELECT COUNT(*) FROM document_resources":   &state.resources,
		"SELECT COUNT(*) FROM document_grants":      &state.grants,
		"SELECT COUNT(*) FROM access_groups":        &state.groups,
		"SELECT COUNT(*) FROM access_group_members": &state.members,
		"SELECT COUNT(*) FROM principals":           &state.principals,
	} {
		if err := f.db.QueryRow(query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.QueryRow(Q("SELECT acl_revision,mode,hold FROM document_resources WHERE dataset_id=$1 AND data_id=$2"), "alpha", "blob").Scan(&state.aclRevision, &state.mode, &state.hold); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(Q("SELECT revision FROM access_groups WHERE id=$1"), groupID).Scan(&state.groupRevision); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestDocumentHTTPRevokedCredentialCannotMutatePolicy(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ctx := context.Background()
		group, err := f.p.CreateGroup(ctx, f.owner, "a", "Revocation fence")
		if err != nil {
			t.Fatal(err)
		}
		f.r, err = f.p.GrantDocument(ctx, f.owner, f.r.DocumentRef, f.r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "peer"}, accesspkg.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}

		for _, credential := range []string{"api_key", "jwt_session"} {
			t.Run(credential, func(t *testing.T) {
				keyID := "revocation-" + credential
				expires := time.Now().Add(time.Hour).Unix()
				var sessionID string
				if credential == "api_key" {
					f.exec("INSERT INTO api_keys(id,key_hash,user_id,permissions) VALUES($1,$2,'owner','write')", keyID, "hash-"+keyID)
				} else {
					sessionID, err = accesspkg.CreateBrowserSession(ctx, f.db, Q, "owner", expires)
					if err != nil {
						t.Fatal(err)
					}
				}

				mutations := []struct {
					name, method, path, body, eventType string
				}{
					{"register", "POST", "/datasets/alpha/data/visible/policy", `{"tenant_id":"a","mode":"restricted"}`, "register"},
					{"grant", "POST", "/datasets/alpha/data/blob/grants", fmt.Sprintf(`{"acl_revision":%d,"principal_kind":"user","principal_id":"viewer","role":"viewer"}`, f.r.ACLRevision), "grant"},
					{"revoke", "DELETE", "/datasets/alpha/data/blob/grants/user/peer", fmt.Sprintf(`{"acl_revision":%d}`, f.r.ACLRevision), "revoke"},
					{"mode", "PATCH", "/datasets/alpha/data/blob/policy", fmt.Sprintf(`{"acl_revision":%d,"mode":"inherit"}`, f.r.ACLRevision), "set_mode"},
					{"hold", "PATCH", "/datasets/alpha/data/blob/policy", fmt.Sprintf(`{"acl_revision":%d,"hold":true}`, f.r.ACLRevision), "set_hold"},
					{"group_create", "POST", "/document-groups", `{"tenant_id":"a","name":"Denied group"}`, "group_create"},
					{"group_members", "PUT", "/document-groups/" + group.ID + "/members", fmt.Sprintf(`{"revision":%d,"members":["peer"]}`, group.Revision), "group_members_replace"},
				}

				for i, mutation := range mutations {
					t.Run(mutation.name, func(t *testing.T) {
						verified := make(chan struct{})
						proceed := make(chan struct{})
						cfg := f.cfg
						cfg.RequireAuth = true
						app := fiber.New(fiber.Config{DisableStartupMessage: true})
						app.Use(func(c *fiber.Ctx) error {
							c.Locals("user_id", "owner")
							c.Locals("tenant_id", "a")
							if credential == "api_key" {
								c.Locals("api_key_permissions", "write")
								c.Locals("verified_api_key", accesspkg.APIKeyIdentity{KeyID: keyID, UserID: "owner", Permissions: "write"})
							} else {
								c.Locals("verified_jwt", jwtPayload{Sub: "owner", Exp: expires, SessionID: sessionID})
							}
							close(verified)
							<-proceed
							return c.Next()
						})
						RegisterDocumentPolicyAPI(app.Group("/api/v1"), cfg)

						before := snapshotDocumentPolicyState(t, f, group.ID)
						auditStart := len(*f.auditEvents)
						req := httptest.NewRequest(mutation.method, "/api/v1"+mutation.path, strings.NewReader(mutation.body))
						req.Header.Set("Content-Type", "application/json")
						type result struct {
							status int
							body   []byte
							err    error
						}
						done := make(chan result, 1)
						go func() {
							resp, err := app.Test(req, -1)
							if err != nil {
								done <- result{err: err}
								return
							}
							defer resp.Body.Close()
							body, readErr := io.ReadAll(resp.Body)
							done <- result{status: resp.StatusCode, body: body, err: readErr}
						}()
						<-verified
						if credential == "api_key" {
							f.exec("UPDATE api_keys SET revoked=TRUE WHERE id=$1", keyID)
						} else if err := accesspkg.RevokeBrowserSession(ctx, f.db, Q, "owner", sessionID); err != nil {
							t.Fatal(err)
						}
						close(proceed)
						got := <-done
						if got.err != nil || got.status != fiber.StatusUnauthorized {
							t.Fatalf("request after verified revocation: status=%d body=%s err=%v", got.status, got.body, got.err)
						}
						if after := snapshotDocumentPolicyState(t, f, group.ID); after != before {
							t.Fatalf("revoked credential mutated policy: before=%+v after=%+v", before, after)
						}
						events := (*f.auditEvents)[auditStart:]
						if len(events) != 1 || events[0].Type != mutation.eventType || events[0].Outcome != "denied" {
							t.Fatalf("revoked mutation audit=%+v", events)
						}

						if i+1 < len(mutations) {
							if credential == "api_key" {
								f.exec("UPDATE api_keys SET revoked=FALSE WHERE id=$1", keyID)
							} else {
								sessionID, err = accesspkg.CreateBrowserSession(ctx, f.db, Q, "owner", expires)
								if err != nil {
									t.Fatal(err)
								}
							}
						}
					})
				}
			})
		}
	})
}
