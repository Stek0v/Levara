package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
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
)

type documentHTTPFixture struct {
	t     *testing.T
	db    *sql.DB
	app   *fiber.App
	cfg   APIConfig
	p     accesspkg.SQLPolicy
	r     accesspkg.DocumentResource
	owner accesspkg.Actor
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
			f := &documentHTTPFixture{t: t, db: db, cfg: APIConfig{DB: db, StoragePath: t.TempDir()}, p: accesspkg.SQLPolicy{DB: db, Q: Q, QA: QArgs}, owner: accesspkg.Actor{UserID: "owner", TenantID: "a"}}
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
			f.expect(user, "GET", base+"/policy", "", 403)
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
		f.expect("owner", "GET", base+"/policy", "", 403)
	})
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

func TestDocumentHTTPManagementRoutesRemainGated(t *testing.T) {
	app := fiber.New()
	RegisterAPI(app.Group("/api/v1"), APIConfig{StoragePath: t.TempDir(), WorkspacePath: t.TempDir()})
	for _, path := range []string{"/api/v1/document-groups", "/api/v1/datasets/a/data/b/policy"} {
		resp, err := app.Test(httptest.NewRequest("POST", path, strings.NewReader(`{}`)), -1)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatalf("management exposed before rollout: %s status=%d", path, resp.StatusCode)
		}
	}
}
