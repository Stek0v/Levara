package ingest_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	_ "github.com/ncruces/go-sqlite3/driver"
	httpapi "github.com/stek0v/levara/internal/http"
	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/ingest"
)

type metadataFixture struct {
	t      *testing.T
	db     *sql.DB
	writer *ingest.MetadataWriter
	policy access.SQLPolicy
	ctx    context.Context
	actor  access.Actor
	result ingest.Result
}

func newMetadataFixture(t *testing.T, dialect string) metadataFixture {
	t.Helper()
	previous := httpapi.GetDBProvider()
	t.Cleanup(func() { httpapi.SetDBProvider(previous); ingest.SetSQLiteMode(false) })
	var db *sql.DB
	var schemaName string
	if dialect == "sqlite" {
		httpapi.SetDBProvider(httpapi.DBSQLite)
		ingest.SetSQLiteMode(true)
		var err error
		db, err = sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "metadata.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
	} else {
		dsn := os.Getenv("LEVARA_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("LEVARA_TEST_POSTGRES_DSN not set")
		}
		httpapi.SetDBProvider(httpapi.DBPostgres)
		ingest.SetSQLiteMode(false)
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		schema := fmt.Sprintf("metadata_authorized_%d", time.Now().UnixNano())
		cfg.RuntimeParams["search_path"] = schema
		db = stdlib.OpenDB(*cfg)
		if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
			t.Fatal(err)
		}
		schemaName = schema
	}
	t.Cleanup(func() {
		if schemaName != "" {
			_, _ = db.Exec("DROP SCHEMA " + schemaName + " CASCADE")
		}
		_ = db.Close()
	})
	if err := httpapi.MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	if err := access.EnsureIdentitySchema(ctx, db, httpapi.Q); err != nil {
		t.Fatal(err)
	}
	if err := access.EnsureBrowserSessionSchema(ctx, db, httpapi.Q); err != nil {
		t.Fatal(err)
	}
	f := metadataFixture{t: t, db: db, writer: ingest.NewMetadataWriterFromDB(db), policy: access.SQLPolicy{DB: db, Q: httpapi.Q, QA: httpapi.QArgs}, ctx: ctx, actor: access.Actor{UserID: "owner", TenantID: "a", AuthMethod: "jwt"}, result: ingest.Result{ID: "blob", Name: "document.txt", Extension: ".txt", MimeType: "text/plain", FilePath: "file:///fixture/derived", ContentHash: "derived-hash", FileSize: 7, OriginalFilePath: "file:///fixture/original", OriginalContentHash: "original-hash", OriginalFileSize: 11, Tags: `["known"]`, Room: "docs"}}
	for _, id := range []string{"owner", "editor", "foreign", "inactive", "admin"} {
		f.exec("INSERT INTO principals(id,type) VALUES($1,'user')", id)
		f.exec("INSERT INTO users(id,email,hashed_password,is_active,is_superuser) VALUES($1,$2,'locked',$3,$4)", id, id+"@test.invalid", id != "inactive", id == "admin")
	}
	f.exec("INSERT INTO tenants(id,name,owner_id) VALUES('a','A','owner'),('b','B','foreign')")
	for _, id := range []string{"owner", "editor", "inactive"} {
		f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES($1,'a')", id)
	}
	f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('foreign','b')")
	f.exec("INSERT INTO datasets(id,name,owner_id) VALUES('owned','Owned','owner'),('foreign','Foreign','foreign')")
	f.exec("INSERT INTO dataset_shares(id,dataset_id,user_id,role) VALUES('editor-share','owned','editor','editor')")
	return f
}
func (f metadataFixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Exec(httpapi.Q(query), args...); err != nil {
		f.t.Fatal(err)
	}
}
func (f metadataFixture) count(table string) int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}
func (f metadataFixture) publish(results []ingest.Result, actor access.Actor, id, name string) (int, error) {
	proof := f.proof()
	proof.Actor = actor
	return f.writer.WriteMetadataAuthorized(f.ctx, results, proof, id, name)
}

func (f metadataFixture) proof() access.MetadataActor {
	return access.MetadataActor{Actor: f.actor, Credential: access.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
}

func (f metadataFixture) register() access.DocumentResource {
	f.t.Helper()
	if _, err := f.publish([]ingest.Result{f.result}, f.actor, "owned", "Owned"); err != nil {
		f.t.Fatal(err)
	}
	r, err := f.policy.RegisterDocument(f.ctx, f.actor, access.DocumentRef{DatasetID: "owned", DataID: "blob"}, "a", access.DocumentRestricted)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func TestMetadataAuthorizedCredentials(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f := newMetadataFixture(t, dialect)
			f.exec("INSERT INTO credential_epochs(user_id,epoch,revoked_before) VALUES('owner',2,100)")
			f.exec("INSERT INTO api_keys(id,key_hash,user_id,permissions,revoked) VALUES('write-key','not-a-token','owner','read-write',false),('revoked-key','not-a-token','owner','read-write',true),('read-key','not-a-token','owner','read-only',false)")
			f.exec("INSERT INTO auth_sessions(id,user_id,expires_at,revoked) VALUES('live','owner',$1,false),('logged-out','owner',$2,true),('expired','owner',1,false)", time.Now().Add(time.Hour).Unix(), time.Now().Add(time.Hour).Unix())
			cases := []struct {
				name string
				edit func(*access.MetadataActor)
			}{
				{"missing-proof", func(a *access.MetadataActor) { a.Credential = access.MetadataCredential{} }},
				{"missing-user", func(a *access.MetadataActor) { a.UserID = "absent" }},
				{"anonymous", func(a *access.MetadataActor) { a.UserID = "" }},
				{"old-epoch-after-reactivation", func(a *access.MetadataActor) { a.Credential.Epoch = 1 }},
				{"expired-jwt", func(a *access.MetadataActor) { a.Credential.ExpiresAt = 1 }},
				{"wrong-tenant", func(a *access.MetadataActor) { a.TenantID = "b" }},
				{"unknown-session", func(a *access.MetadataActor) { a.Credential.SessionID = "unknown" }},
				{"logged-out-session", func(a *access.MetadataActor) { a.Credential.SessionID = "logged-out" }},
				{"expired-session", func(a *access.MetadataActor) { a.Credential.SessionID = "expired" }},
				{"revoked-key", func(a *access.MetadataActor) {
					a.Credential.Kind, a.Credential.KeyID, a.APIKeyPermissions = "api_key", "revoked-key", "read-write"
				}},
				{"read-only-key", func(a *access.MetadataActor) {
					a.Credential.Kind, a.Credential.KeyID, a.APIKeyPermissions = "api_key", "read-key", "read-only"
				}},
				{"key-permission-changed", func(a *access.MetadataActor) {
					a.Credential.Kind, a.Credential.KeyID, a.APIKeyPermissions = "api_key", "read-key", "read-write"
				}},
				{"unknown-key", func(a *access.MetadataActor) {
					a.Credential.Kind, a.Credential.KeyID, a.APIKeyPermissions = "api_key", "unknown", "read-write"
				}},
				{"old-external-assertion", func(a *access.MetadataActor) { a.Credential.Kind, a.Credential.IssuedAt = "external", 100 }},
				{"missing-external-issued-at-after-revoke", func(a *access.MetadataActor) { a.Credential.Kind = "external" }},
				{"expired-external-assertion", func(a *access.MetadataActor) {
					a.Credential.Kind, a.Credential.IssuedAt, a.Credential.ExpiresAt = "external", 101, 1
				}},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					a := f.proof()
					a.Credential.Epoch = 2
					tc.edit(&a)
					if n, err := f.writer.WriteMetadataAuthorized(f.ctx, []ingest.Result{f.result}, a, "new", "New"); err == nil || n != 0 {
						t.Fatalf("untrusted publication: n=%d err=%v", n, err)
					}
					if f.count("data") != 0 || f.count("dataset_data") != 0 || f.count("datasets") != 2 {
						t.Fatal("denied credential left partial metadata")
					}
				})
			}
			for _, kind := range []string{"jwt", "cookie", "api_key", "external", "shared-editor", "trusted-local"} {
				t.Run("allow-"+kind, func(t *testing.T) {
					a := f.proof()
					a.Credential.Epoch = 2
					switch kind {
					case "cookie":
						a.Credential.SessionID = "live"
					case "api_key":
						a.Credential.Kind, a.Credential.KeyID, a.APIKeyPermissions = "api_key", "write-key", "read-write"
					case "external":
						a.Credential.Kind, a.Credential.IssuedAt = "external", 101
					case "shared-editor":
						a.UserID, a.Credential.Epoch = "editor", 0
					case "trusted-local":
						a = access.MetadataActor{TrustedLocal: true}
					}
					r := f.result
					r.ID = kind
					if n, err := f.writer.WriteMetadataAuthorized(f.ctx, []ingest.Result{r}, a, "owned", "Owned"); err != nil || n != 1 {
						t.Fatalf("allowed publication: n=%d err=%v", n, err)
					}
					var owner string
					if err := f.db.QueryRow(httpapi.Q("SELECT owner_id FROM data WHERE id=$1"), kind).Scan(&owner); err != nil || owner != a.UserID {
						t.Fatalf("data owner=%q err=%v", owner, err)
					}
				})
			}
		})
	}
}

func TestMetadataAuthorizedAtomicityAndDatasetIdentity(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f := newMetadataFixture(t, dialect)
			for _, target := range [][2]string{{"new", "Foreign"}, {"owned", "Foreign"}, {"new", " "}, {"", "New"}} {
				if n, err := f.publish([]ingest.Result{f.result}, f.actor, target[0], target[1]); err == nil || n != 0 {
					t.Fatalf("ID/name conflict published: %v %d %v", target, n, err)
				}
			}
			f.exec("INSERT INTO data(id,name,owner_id) VALUES('foreign-data','Keep','foreign')")
			for _, user := range []string{"owner", "editor", "admin"} {
				a := f.actor
				a.UserID = user
				if user == "admin" {
					f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('admin','a')")
				}
				bad := f.result
				bad.ID = "foreign-data"
				if n, err := f.publish([]ingest.Result{f.result, bad}, a, "new", "New"); err == nil || n != 0 {
					t.Fatalf("foreign owner overwritten by %s: %d %v", user, n, err)
				}
				if f.count("datasets") != 2 || f.count("data") != 1 || f.count("dataset_data") != 0 {
					t.Fatal("batch failure left earlier insert/dataset/association")
				}
			}
			var owner, name string
			if err := f.db.QueryRow("SELECT owner_id,name FROM data WHERE id='foreign-data'").Scan(&owner, &name); err != nil || owner != "foreign" || name != "Keep" {
				t.Fatalf("foreign data mutated: %q %q %v", owner, name, err)
			}
			// A share authorizes a new upload, but must never transfer the dataset.
			a := f.actor
			a.UserID = "editor"
			if n, err := f.publish([]ingest.Result{f.result}, a, "owned", ""); err != nil || n != 1 {
				t.Fatalf("existing dataset ID without name: %d %v", n, err)
			}
			if err := f.db.QueryRow("SELECT owner_id FROM datasets WHERE id='owned'").Scan(&owner); err != nil || owner != "owner" {
				t.Fatalf("dataset ownership changed: %q %v", owner, err)
			}
		})
	}
}

func TestMetadataAuthorizedRegisteredReimport(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f := newMetadataFixture(t, dialect)
			r := f.register()
			const status = `{"docs":{"status":"COMPLETED"}}`
			f.exec("UPDATE data SET pipeline_status=$1, token_count=88, updated_at='2020-01-02 00:00:00+00:00' WHERE id='blob'", status)
			var beforeTime string
			if err := f.db.QueryRow("SELECT CAST(updated_at AS TEXT) FROM data WHERE id='blob'").Scan(&beforeTime); err != nil {
				t.Fatal(err)
			}
			for _, target := range [][2]string{{"owned", "Owned"}, {"new", "New"}, {"new", "New"}} {
				if n, err := f.publish([]ingest.Result{f.result}, f.actor, target[0], target[1]); err != nil || n != 1 {
					t.Fatalf("exact reimport: %d %v", n, err)
				}
			}
			var gotStatus, afterTime string
			var tokens int
			if err := f.db.QueryRow("SELECT pipeline_status,token_count,CAST(updated_at AS TEXT) FROM data WHERE id='blob'").Scan(&gotStatus, &tokens, &afterTime); err != nil || gotStatus != status || tokens != 88 || beforeTime != afterTime {
				t.Fatalf("exact reimport changed processing state: %q %d %q != %q: %v", gotStatus, tokens, beforeTime, afterTime, err)
			}
			mutations := []struct {
				name string
				edit func(*ingest.Result)
			}{
				{"name", func(r *ingest.Result) { r.Name += "new" }},
				{"extension", func(r *ingest.Result) { r.Extension = ".md" }},
				{"mime", func(r *ingest.Result) { r.MimeType = "text/markdown" }},
				{"derived-location", func(r *ingest.Result) { r.FilePath += "new" }},
				{"original-location", func(r *ingest.Result) { r.OriginalFilePath += "new" }},
				{"derived-content", func(r *ingest.Result) { r.ContentHash += "new" }},
				{"original-content", func(r *ingest.Result) { r.OriginalContentHash += "new" }},
				{"tags", func(r *ingest.Result) { r.Tags = `["secret"]` }},
				{"room", func(r *ingest.Result) { r.Room = "other" }},
				{"size", func(r *ingest.Result) { r.OriginalFileSize++ }},
			}
			for _, tc := range mutations {
				t.Run(tc.name, func(t *testing.T) {
					changed := f.result
					tc.edit(&changed)
					if n, err := f.publish([]ingest.Result{changed}, f.actor, "attempt", "Attempt"); n != 0 || !errors.Is(err, access.ErrDocumentVersionConflict) {
						t.Fatalf("registered mutation did not demand CAS: %d %v", n, err)
					}
					if f.count("data") != 1 || f.count("datasets") != 3 || f.count("dataset_data") != 2 {
						t.Fatal("registered conflict left partial data/association")
					}
					current, err := f.policy.GetDocumentResource(f.ctx, r.DocumentRef)
					if err != nil || current.ContentRevision != r.ContentRevision || current.ACLRevision != r.ACLRevision {
						t.Fatalf("registered revisions changed: %#v %v", current, err)
					}
				})
			}
		})
	}
}

func TestMetadataAuthorizedChecksEveryRegisteredAlias(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, kind := range []string{"hold", "tombstone", "other-tenant", "unlinked", "revoked-grant", "missing-source"} {
				t.Run(kind, func(t *testing.T) {
					f := newMetadataFixture(t, dialect)
					f.register()
					f.exec("INSERT INTO datasets(id,name,owner_id) VALUES('alias','Alias','editor')")
					f.exec("INSERT INTO dataset_data(dataset_id,data_id) VALUES('alias','blob')")
					f.exec("INSERT INTO document_resources(dataset_id,data_id,tenant_id,mode) VALUES('alias','blob','a','restricted')")
					f.exec("INSERT INTO document_grants(dataset_id,data_id,principal_kind,principal_id,role,granted_by) VALUES('alias','blob','user','owner','editor','editor')")
					// The same data owner can repeat while every registered alias grants write.
					if _, err := f.publish([]ingest.Result{f.result}, f.actor, "owned", "Owned"); err != nil {
						t.Fatal(err)
					}
					switch kind {
					case "hold":
						f.exec("UPDATE document_resources SET hold=true WHERE dataset_id='alias'")
					case "tombstone":
						f.exec("UPDATE document_resources SET tombstoned=true WHERE dataset_id='alias'")
					case "other-tenant":
						f.exec("UPDATE document_resources SET tenant_id='b' WHERE dataset_id='alias'")
					case "unlinked":
						f.exec("DELETE FROM dataset_data WHERE dataset_id='alias'")
					case "revoked-grant":
						f.exec("DELETE FROM document_grants WHERE dataset_id='alias'")
					case "missing-source":
						f.exec("DELETE FROM data WHERE id='blob'")
					}
					beforeData, beforeLinks := f.count("data"), f.count("dataset_data")
					if n, err := f.publish([]ingest.Result{f.result}, f.actor, "attempt", "Attempt"); err == nil || n != 0 {
						t.Fatalf("denied alias bypassed through new dataset: %d %v", n, err)
					}
					if f.count("data") != beforeData || f.count("dataset_data") != beforeLinks || f.count("datasets") != 3 {
						t.Fatal("denied alias created/resurrected data or association")
					}
				})
			}
		})
	}
}

func TestMetadataAuthorizedCancellation(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, kind := range []string{"occupied-pool", "write-lock"} {
				t.Run(kind, func(t *testing.T) {
					f := newMetadataFixture(t, dialect)
					f.db.SetMaxOpenConns(1)
					blocker, err := f.db.BeginTx(f.ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					defer blocker.Rollback()
					if kind == "write-lock" {
						f.db.SetMaxOpenConns(2)
						if _, err := blocker.ExecContext(f.ctx, "UPDATE users SET id=id WHERE id='owner'"); err != nil {
							t.Fatal(err)
						}
					}
					ctx, cancel := context.WithTimeout(f.ctx, 75*time.Millisecond)
					defer cancel()
					start := time.Now()
					n, err := f.writer.WriteMetadataAuthorized(ctx, []ingest.Result{f.result}, f.proof(), "new", "New")
					if err == nil || n != 0 || time.Since(start) > 2*time.Second {
						t.Fatalf("publication ignored cancellation: %d %v in %v", n, err, time.Since(start))
					}
					_ = blocker.Rollback()
					if f.count("data") != 0 || f.count("datasets") != 2 || f.count("dataset_data") != 0 {
						t.Fatal("canceled publication left partial SQL")
					}
				})
			}
		})
	}
}

func TestMetadataAuthorizedRechecksAfterConcurrentRevoke(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, kind := range []string{"epoch", "key", "share", "legacy-alias-share", "hold", "membership", "group"} {
				t.Run(kind, func(t *testing.T) {
					f := newMetadataFixture(t, dialect)
					f.db.SetMaxOpenConns(3)
					proof := f.proof() // captured before revocation, as at a transport boundary
					r := f.result
					query := "INSERT INTO credential_epochs(user_id,epoch,revoked_before) VALUES('owner',1,100)"
					switch kind {
					case "key":
						f.exec("INSERT INTO api_keys(id,key_hash,user_id,permissions) VALUES('key','not-a-token','owner','read-write')")
						proof.Credential.Kind, proof.Credential.KeyID, proof.APIKeyPermissions = "api_key", "key", "read-write"
						query = "UPDATE api_keys SET revoked=true WHERE id='key'"
					case "share":
						proof.UserID = "editor"
						query = "DELETE FROM dataset_shares WHERE id='editor-share'"
					case "legacy-alias-share":
						if _, err := f.publish([]ingest.Result{r}, f.actor, "owned", "Owned"); err != nil {
							t.Fatal(err)
						}
						f.exec("UPDATE datasets SET owner_id='editor' WHERE id='foreign'")
						f.exec("INSERT INTO dataset_shares(id,dataset_id,user_id,role) VALUES('legacy-share','foreign','owner','editor')")
						f.exec("INSERT INTO dataset_data(dataset_id,data_id) VALUES('foreign','blob')")
						query = "DELETE FROM dataset_shares WHERE id='legacy-share'"
					case "hold":
						f.register()
						query = "UPDATE document_resources SET hold=true,acl_revision=acl_revision+1 WHERE dataset_id='owned'"
					case "membership":
						query = "DELETE FROM user_tenant WHERE user_id='owner'"
					case "group":
						f.register()
						f.exec("UPDATE data SET owner_id='editor' WHERE id='blob'")
						f.exec("INSERT INTO principals(id,type) VALUES('group','group')")
						f.exec("INSERT INTO access_groups(id,tenant_id,name) VALUES('group','a','Editors')")
						f.exec("INSERT INTO access_group_members(group_id,tenant_id,user_id) VALUES('group','a','editor')")
						f.exec("INSERT INTO document_grants(dataset_id,data_id,principal_kind,principal_id,role,granted_by) VALUES('owned','blob','group','group','editor','owner')")
						proof.UserID = "editor"
						query = "DELETE FROM access_group_members WHERE group_id='group' AND user_id='editor'"
					}
					beforeData, beforeLinks := f.count("data"), f.count("dataset_data")
					revoke, err := f.db.BeginTx(f.ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					defer revoke.Rollback()
					if _, err := revoke.ExecContext(f.ctx, httpapi.Q(query)); err != nil {
						t.Fatal(err)
					}
					done := make(chan error, 1)
					go func() {
						n, err := f.writer.WriteMetadataAuthorized(f.ctx, []ingest.Result{r}, proof, "owned", "Owned")
						if n != 0 {
							err = fmt.Errorf("publication crossed revoke with %d rows", n)
						}
						done <- err
					}()
					select {
					case err := <-done:
						t.Fatalf("publication did not wait for SQL revocation: %v", err)
					case <-time.After(30 * time.Millisecond):
					}
					if err := revoke.Commit(); err != nil {
						t.Fatal(err)
					}
					select {
					case err := <-done:
						if err == nil {
							t.Fatal("captured credential/grant survived committed revocation")
						}
					case <-time.After(3 * time.Second):
						t.Fatal("metadata writer did not release after revocation")
					}
					if f.count("data") != beforeData || f.count("dataset_data") != beforeLinks || f.count("datasets") != 2 {
						t.Fatal("concurrent revoke left partial metadata")
					}
				})
			}
		})
	}
}

func TestMetadataAuthorizedConcurrentImports(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f := newMetadataFixture(t, dialect)
			const writers = 8
			start := make(chan struct{})
			done := make(chan error, writers)
			for range writers {
				go func() {
					<-start
					n, err := f.publish([]ingest.Result{f.result}, f.actor, "new", "New")
					if err == nil && n != 1 {
						err = fmt.Errorf("published %d results", n)
					}
					done <- err
				}()
			}
			close(start)
			for range writers {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			if f.count("data") != 1 || f.count("dataset_data") != 1 || f.count("datasets") != 3 {
				t.Fatal("same ID/name imported more than once")
			}
		})
	}
}

func TestMetadataAuthorizedInvalidWriter(t *testing.T) {
	var writer *ingest.MetadataWriter
	if n, err := writer.WriteMetadataAuthorized(context.Background(), nil, access.MetadataActor{}, "", ""); err != nil || n != 0 {
		t.Fatalf("empty input should be a no-op: %d %v", n, err)
	}
	if _, err := writer.WriteMetadataAuthorized(context.Background(), []ingest.Result{{ID: "blob"}}, access.MetadataActor{TrustedLocal: true}, "owned", "Owned"); !errors.Is(err, access.ErrDocumentInvalid) {
		t.Fatalf("nil writer did not fail closed: %v", err)
	}
}

func TestMetadataAuthorizedPostgresDeadlockRollsBack(t *testing.T) {
	f := newMetadataFixture(t, "postgres")
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	// Simulate a directory/group writer that acquired a later table first.
	// The production fence must surface a deadlock safely, never partial success.
	directory, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Rollback()
	if _, err = directory.ExecContext(ctx, "LOCK TABLE access_groups IN ROW EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		n   int
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		n, err := f.writer.WriteMetadataAuthorized(ctx, []ingest.Result{f.result}, f.proof(), "new", "New")
		done <- outcome{n, err}
	}()
	// Wait for the actual lock conflict, not a guessed scheduling delay.
	for {
		var waiting int
		err := f.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pg_locks WHERE relation='access_groups'::regclass AND NOT granted").Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("metadata writer never reached the lock conflict")
		case <-time.After(5 * time.Millisecond):
		}
	}
	_, directoryErr := directory.ExecContext(ctx, "UPDATE users SET is_active=false WHERE id='owner'")
	if directoryErr == nil {
		directoryErr = directory.Commit()
	} else {
		_ = directory.Rollback()
	}
	var result outcome
	select {
	case result = <-done:
	case <-ctx.Done():
		t.Fatal("deadlock left publication blocked")
	}
	isDeadlock := func(err error) bool {
		var pgerr *pgconn.PgError
		return errors.As(err, &pgerr) && pgerr.Code == "40P01"
	}
	if !isDeadlock(directoryErr) && !isDeadlock(result.err) {
		t.Fatalf("expected observed SQL deadlock, got directory=%v publication=%v", directoryErr, result.err)
	}
	if result.err != nil {
		if result.n != 0 || f.count("data") != 0 || f.count("dataset_data") != 0 || f.count("datasets") != 2 {
			t.Fatal("deadlock victim left partial metadata")
		}
	} else if result.n != 1 || f.count("data") != 1 || f.count("dataset_data") != 1 || f.count("datasets") != 3 {
		t.Fatal("successful transaction was incomplete")
	}
	var active bool
	if err := f.db.QueryRow("SELECT is_active FROM users WHERE id='owner'").Scan(&active); err != nil {
		t.Fatal(err)
	}
	if result.err == nil && !active {
		t.Fatal("revocation and stale publication both committed")
	}
	t.Logf("observed deadlock with safe rollback: directory=%v publication=%v", directoryErr, result.err)
}

func TestMetadataAuthorizedDatabaseFailures(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f := newMetadataFixture(t, dialect)
			if tx, _, err := f.policy.BeginMetadataWrite(context.Background(), f.proof(), dialect == "sqlite"); err == nil {
				_ = tx.Rollback()
				t.Fatal("unbounded metadata fence accepted")
			}
			for _, table := range []string{"credential_epochs", "document_resources"} {
				f.exec("ALTER TABLE " + table + " RENAME TO hidden_metadata_table")
				if n, err := f.publish([]ingest.Result{f.result}, f.actor, "new", "New"); err == nil || n != 0 {
					t.Fatalf("missing %s did not fail closed: %d %v", table, n, err)
				}
				f.exec("ALTER TABLE hidden_metadata_table RENAME TO " + table)
				if f.count("data") != 0 || f.count("dataset_data") != 0 || f.count("datasets") != 2 {
					t.Fatal("database failure left partial metadata")
				}
			}
		})
	}
}

func TestMetadataAuthorizedExpiryDuringPublication(t *testing.T) {
	f := newMetadataFixture(t, "postgres")
	// A real SQL trigger delays the write past token expiry without a production
	// test hook. The final check must roll back the dataset, row and association.
	f.exec(`CREATE FUNCTION metadata_delay() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN PERFORM pg_sleep(1.1); RETURN NEW; END; $$`)
	f.exec("CREATE TRIGGER metadata_delay BEFORE INSERT ON data FOR EACH ROW EXECUTE FUNCTION metadata_delay()")
	for _, kind := range []string{"jwt", "external"} {
		t.Run(kind, func(t *testing.T) {
			proof := f.proof()
			proof.Credential.Kind = kind
			proof.Credential.ExpiresAt = time.Now().Unix() + 1
			n, err := f.writer.WriteMetadataAuthorized(f.ctx, []ingest.Result{f.result}, proof, "new", "New")
			if n != 0 || !errors.Is(err, access.ErrRevokedCredential) {
				t.Fatalf("expiry during publication did not roll back: %d %v", n, err)
			}
			if f.count("data") != 0 || f.count("dataset_data") != 0 || f.count("datasets") != 2 {
				t.Fatal("expired token left partial SQL metadata")
			}
		})
	}
}
func TestMetadataAuthorizedRejectsInactiveAndForeignDataset(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, kind := range []string{"inactive", "foreign-dataset"} {
				t.Run(kind, func(t *testing.T) {
					f := newMetadataFixture(t, dialect)
					actor := f.actor
					id, name := "owned", "Owned"
					if kind == "inactive" {
						actor.UserID = "inactive"
					} else {
						id, name = "foreign", "Foreign"
					}
					if _, err := f.publish([]ingest.Result{f.result}, actor, id, name); err == nil {
						t.Fatal("request metadata bypassed active identity/dataset authorization")
					}
					if f.count("data") != 0 || f.count("dataset_data") != 0 {
						t.Fatal("denied publication left partial metadata")
					}
				})
			}
		})
	}
}
func TestMetadataAuthorizedRejectsHeldRegisteredAlias(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f := newMetadataFixture(t, dialect)
			if _, err := f.writer.WriteMetadata(f.ctx, []ingest.Result{f.result}, "owner", "owned", "Owned"); err != nil {
				t.Fatal(err)
			}
			ref := access.DocumentRef{DatasetID: "owned", DataID: "blob"}
			r, err := f.policy.RegisterDocument(f.ctx, f.actor, ref, "a", access.DocumentRestricted)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.policy.SetDocumentHold(f.ctx, f.actor, ref, r.ACLRevision, true); err != nil {
				t.Fatal(err)
			}
			changed := f.result
			changed.Name = "Changed without CAS"
			if _, err := f.publish([]ingest.Result{changed}, f.actor, "new-association", "New association"); err == nil {
				t.Fatal("held registered alias allowed metadata overwrite and new association")
			}
			var name string
			if err := f.db.QueryRow("SELECT name FROM data WHERE id='blob'").Scan(&name); err != nil {
				t.Fatal(err)
			}
			if name != f.result.Name || f.count("dataset_data") != 1 || f.count("datasets") != 2 {
				t.Fatal("failed hold check left partial changes")
			}
		})
	}
}
