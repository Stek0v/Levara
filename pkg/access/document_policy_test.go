package access_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	_ "github.com/ncruces/go-sqlite3/driver"
	httpapi "github.com/stek0v/levara/internal/http"
	"github.com/stek0v/levara/pkg/access"
)

type documentFixture struct {
	t                                   *testing.T
	db                                  *sql.DB
	p                                   access.SQLPolicy
	ctx                                 context.Context
	owner, admin, viewer, peer, foreign access.Actor
	ref, other                          access.DocumentRef
}

func newDocumentFixture(t *testing.T, dialect string) documentFixture {
	t.Helper()
	previous := httpapi.GetDBProvider()
	t.Cleanup(func() { httpapi.SetDBProvider(previous) })
	var db *sql.DB
	if dialect == "sqlite" {
		httpapi.SetDBProvider(httpapi.DBSQLite)
		var err error
		db, err = sql.Open("sqlite3", t.TempDir()+"/documents.db")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
	} else {
		dsn := os.Getenv("LEVARA_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("LEVARA_TEST_POSTGRES_DSN is not set")
		}
		httpapi.SetDBProvider(httpapi.DBPostgres)
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		schema := fmt.Sprintf("document_policy_%d", time.Now().UnixNano())
		cfg.RuntimeParams["search_path"] = schema
		db = stdlib.OpenDB(*cfg)
		t.Cleanup(func() { _ = db.Close() })
		if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = db.Exec("DROP SCHEMA " + schema + " CASCADE") })
	}
	if err := httpapi.MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	f := documentFixture{t: t, db: db, p: access.SQLPolicy{DB: db, Q: httpapi.Q, QA: httpapi.QArgs}, ctx: context.Background(),
		owner: access.Actor{UserID: "owner", TenantID: "a"}, admin: access.Actor{UserID: "root", TenantID: "b"},
		viewer: access.Actor{UserID: "viewer", TenantID: "a"}, peer: access.Actor{UserID: "peer", TenantID: "a"},
		foreign: access.Actor{UserID: "foreign", TenantID: "b"},
		ref:     access.DocumentRef{DatasetID: "alpha", DataID: "blob"}, other: access.DocumentRef{DatasetID: "beta", DataID: "blob"}}
	for _, id := range []string{"owner", "root", "viewer", "peer", "foreign", "inactive"} {
		f.exec("INSERT INTO principals(id,type) VALUES($1,'user')", id)
		f.exec("INSERT INTO users(id,email,hashed_password,is_superuser,is_active) VALUES($1,$2,'locked',$3,$4)", id, id+"@test.invalid", id == "root", id != "inactive")
	}
	f.exec("INSERT INTO tenants(id,name,owner_id) VALUES('a','A','owner'),('b','B','foreign')")
	for _, id := range []string{"owner", "viewer", "peer", "inactive"} {
		f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES($1,'a')", id)
	}
	f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('foreign','b')")
	f.exec("INSERT INTO datasets(id,name,owner_id) VALUES('alpha','Alpha','owner'),('beta','Beta','owner')")
	f.exec("INSERT INTO data(id,name,owner_id) VALUES('blob','Shared bytes','owner'),('secret','Other document','owner')")
	f.exec("INSERT INTO dataset_data(dataset_id,data_id) VALUES('alpha','blob'),('beta','blob'),('alpha','secret')")
	f.exec("INSERT INTO dataset_shares(id,dataset_id,user_id,role) VALUES('share','alpha','viewer','viewer')")
	return f
}

func (f documentFixture) exec(query string, args ...any) {
	f.t.Helper()
	query, args = httpapi.QArgs(query, args...)
	if _, err := f.db.Exec(query, args...); err != nil {
		f.t.Fatal(err)
	}
}
func (f documentFixture) registered(ref access.DocumentRef, mode string) access.DocumentResource {
	f.t.Helper()
	r, err := f.p.RegisterDocument(f.ctx, f.owner, ref, "a", mode)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}
func (f documentFixture) allowed(actor access.Actor, ref access.DocumentRef, action string, want bool) {
	f.t.Helper()
	d, err := f.p.AuthorizeDocument(f.ctx, actor, ref, action)
	if err != nil || d.Allowed != want {
		f.t.Fatalf("actor=%s ref=%+v action=%s decision=%+v err=%v want=%v", actor.UserID, ref, action, d, err, want)
	}
}
func documentDialects(t *testing.T, run func(*testing.T, documentFixture)) {
	t.Helper()
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) { run(t, newDocumentFixture(t, dialect)) })
	}
}

func TestDocumentPolicyRestrictedAndInheritance(t *testing.T) {
	documentDialects(t, func(t *testing.T, f documentFixture) {
		r := f.registered(f.ref, access.DocumentRestricted)
		again, err := f.p.RegisterDocument(f.ctx, f.owner, f.ref, "a", access.DocumentRestricted)
		if err != nil || again != r {
			t.Fatalf("registration is not idempotent: %+v %v", again, err)
		}
		f.allowed(f.owner, f.ref, access.ActionShare, true)
		f.allowed(f.admin, f.ref, access.ActionShare, true) // admin selected another tenant, without A membership
		f.allowed(f.viewer, f.ref, access.ActionRead, false)
		f.allowed(f.peer, f.ref, access.ActionRead, false)
		forged := f.peer
		forged.Superuser = true
		f.allowed(forged, f.ref, access.ActionRead, false)
		r, err = f.p.GrantDocument(f.ctx, f.owner, f.ref, r.ACLRevision, access.DocumentPrincipal{Kind: access.DocumentUser, ID: "peer"}, access.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		f.allowed(f.peer, f.ref, access.ActionRead, true)
		f.allowed(f.peer, f.ref, access.ActionWrite, false)
		f.allowed(f.peer, f.other, access.ActionRead, false)
		f.allowed(f.peer, access.DocumentRef{DatasetID: "alpha", DataID: "secret"}, access.ActionRead, false)
		wrongTenant := f.peer
		wrongTenant.TenantID = "b"
		f.allowed(wrongTenant, f.ref, access.ActionRead, false)
		r, err = f.p.SetDocumentMode(f.ctx, f.owner, f.ref, r.ACLRevision, access.DocumentInherit)
		if err != nil {
			t.Fatal(err)
		}
		f.allowed(f.viewer, f.ref, access.ActionRead, true)
		f.allowed(f.viewer, f.ref, access.ActionWrite, false)
		f.exec("DELETE FROM user_tenant WHERE user_id='owner' AND tenant_id='a'")
		f.allowed(f.owner, f.ref, access.ActionRead, false)
		f.allowed(f.admin, f.ref, access.ActionRead, true)
	})
}

func TestStructuredArtifactRetiresAfterLastDatasetAlias(t *testing.T) {
	documentDialects(t, func(t *testing.T, f documentFixture) {
		f.exec(`INSERT INTO document_structured_artifacts
			(id,data_id,source_revision,raw_content_hash,artifact_sha256,byte_size,storage_location,state)
			VALUES('artifact','blob',1,'source','artifact',2,'storage://artifact','active')`)
		r := f.registered(f.ref, access.DocumentRestricted)
		if err := f.p.DeleteDocumentAssociation(f.ctx, f.owner, f.ref, r.ACLRevision, r.ContentRevision); err != nil {
			t.Fatal(err)
		}
		var state string
		if err := f.db.QueryRow("SELECT state FROM document_structured_artifacts WHERE id='artifact'").Scan(&state); err != nil || state != "active" {
			t.Fatalf("shared artifact state=%q err=%v", state, err)
		}
		if err := f.p.DeleteDocumentAssociation(f.ctx, f.owner, f.other, 0, 0); err != nil {
			t.Fatal(err)
		}
		if err := f.db.QueryRow("SELECT state FROM document_structured_artifacts WHERE id='artifact'").Scan(&state); err != nil || state != "retired" {
			t.Fatalf("last-alias artifact state=%q err=%v", state, err)
		}
	})
}

func TestDocumentPolicyRegistrationTenantIsImmutable(t *testing.T) {
	documentDialects(t, func(t *testing.T, f documentFixture) {
		wrongTenant := f.owner
		wrongTenant.TenantID = "b"
		for _, actor := range []access.Actor{wrongTenant, f.peer, {UserID: "missing", TenantID: "a"}, {UserID: "inactive", TenantID: "a"}} {
			if _, err := f.p.RegisterDocument(f.ctx, actor, f.ref, "a", access.DocumentInherit); !errors.Is(err, access.ErrDocumentForbidden) {
				t.Fatalf("actor=%+v registration err=%v", actor, err)
			}
		}
		r := f.registered(f.ref, access.DocumentInherit)
		if _, err := f.p.RegisterDocument(f.ctx, f.admin, f.ref, "b", access.DocumentInherit); !errors.Is(err, access.ErrDocumentVersionConflict) {
			t.Fatalf("tenant reassignment accepted: %v", err)
		}
		stored, err := f.p.GetDocumentResource(f.ctx, f.ref)
		if err != nil || stored != r {
			t.Fatalf("failed registration changed resource: %+v %v", stored, err)
		}
		if _, err := f.p.RegisterDocument(f.ctx, f.admin, access.DocumentRef{DatasetID: "alpha", DataID: "missing"}, "a", access.DocumentInherit); !errors.Is(err, access.ErrDocumentNotFound) {
			t.Fatalf("nonexistent link registered: %v", err)
		}
		// Instance administration can register an existing inclusion for a tenant
		// without claiming membership in that tenant.
		if _, err := f.p.RegisterDocument(f.ctx, f.admin, f.other, "a", access.DocumentRestricted); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDocumentPolicyConcurrentRevisionCAS(t *testing.T) {
	documentDialects(t, func(t *testing.T, f documentFixture) {
		r := f.registered(f.ref, access.DocumentRestricted)
		start := make(chan struct{})
		type outcome struct {
			id  string
			err error
		}
		results := make(chan outcome, 2)
		for _, id := range []string{"peer", "viewer"} {
			go func(id string) {
				<-start
				_, err := f.p.GrantDocument(f.ctx, f.owner, f.ref, r.ACLRevision, access.DocumentPrincipal{Kind: access.DocumentUser, ID: id}, access.RoleViewer)
				results <- outcome{id, err}
			}(id)
		}
		close(start)
		var winner string
		var conflicts int
		for i := 0; i < 2; i++ {
			result := <-results
			if result.err == nil {
				if winner != "" {
					t.Fatal("two writes accepted one ACL revision")
				}
				winner = result.id
			} else if errors.Is(result.err, access.ErrDocumentVersionConflict) {
				conflicts++
			} else {
				t.Fatal(result.err)
			}
		}
		if winner == "" || conflicts != 1 {
			t.Fatalf("winner=%s conflicts=%d", winner, conflicts)
		}
		stored, err := f.p.GetDocumentResource(f.ctx, f.ref)
		if err != nil || stored.ACLRevision != r.ACLRevision+1 {
			t.Fatalf("resource: %+v %v", stored, err)
		}
		f.allowed(f.peer, f.ref, access.ActionRead, winner == "peer")
		f.allowed(f.viewer, f.ref, access.ActionRead, winner == "viewer")
	})
}

func TestDocumentPolicyGrantCASAndRollback(t *testing.T) {
	documentDialects(t, func(t *testing.T, f documentFixture) {
		r := f.registered(f.ref, access.DocumentRestricted)
		for _, id := range []string{"foreign", "inactive", "missing"} {
			_, err := f.p.GrantDocument(f.ctx, f.owner, f.ref, r.ACLRevision, access.DocumentPrincipal{Kind: access.DocumentUser, ID: id}, access.RoleViewer)
			if !errors.Is(err, access.ErrDocumentForbidden) {
				t.Fatalf("target=%s err=%v", id, err)
			}
			unchanged, err := f.p.GetDocumentResource(f.ctx, f.ref)
			if err != nil || unchanged != r {
				t.Fatalf("failed grant changed revision: %+v %v", unchanged, err)
			}
		}
		readOnly := f.owner
		readOnly.APIKeyPermissions = "read"
		if _, err := f.p.GrantDocument(f.ctx, readOnly, f.ref, r.ACLRevision, access.DocumentPrincipal{Kind: access.DocumentUser, ID: "peer"}, access.RoleAdmin); !errors.Is(err, access.ErrDocumentForbidden) {
			t.Fatal(err)
		}
		oldRevision := r.ACLRevision
		var err error
		r, err = f.p.GrantDocument(f.ctx, f.owner, f.ref, r.ACLRevision, access.DocumentPrincipal{Kind: access.DocumentUser, ID: "peer"}, access.RoleAdmin)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.p.RevokeDocument(f.ctx, f.owner, f.ref, oldRevision, access.DocumentPrincipal{Kind: access.DocumentUser, ID: "peer"}); !errors.Is(err, access.ErrDocumentVersionConflict) {
			t.Fatal(err)
		}
		f.allowed(f.peer, f.ref, access.ActionShare, true)
		r, err = f.p.RevokeDocument(f.ctx, f.owner, f.ref, r.ACLRevision, access.DocumentPrincipal{Kind: access.DocumentUser, ID: "peer"})
		if err != nil {
			t.Fatal(err)
		}
		f.allowed(f.peer, f.ref, access.ActionRead, false)
		// Force a database error after revision CAS, rather than only testing validation.
		f.exec("DROP TABLE document_grants")
		if _, err := f.p.GrantDocument(f.ctx, f.owner, f.ref, r.ACLRevision, access.DocumentPrincipal{Kind: access.DocumentUser, ID: "peer"}, access.RoleViewer); err == nil {
			t.Fatal("missing grants table accepted")
		}
		unchanged, err := f.p.GetDocumentResource(f.ctx, f.ref)
		if err != nil || unchanged != r {
			t.Fatalf("SQL failure did not roll back CAS: %+v %v", unchanged, err)
		}
		if d, err := f.p.AuthorizeDocument(f.ctx, f.peer, f.ref, access.ActionRead); err == nil || d.Allowed {
			t.Fatalf("policy error not explicit: %+v %v", d, err)
		}
	})
}

func TestDocumentPolicyTombstoneHoldAndContentRevision(t *testing.T) {
	documentDialects(t, func(t *testing.T, f documentFixture) {
		r := f.registered(f.ref, access.DocumentInherit)
		f.registered(f.other, access.DocumentInherit)
		r, err := f.p.SetDocumentHold(f.ctx, f.owner, f.ref, r.ACLRevision, true)
		if err != nil {
			t.Fatal(err)
		}
		f.allowed(f.owner, f.ref, access.ActionRead, true)
		f.allowed(f.admin, f.ref, access.ActionDelete, false)
		if _, err := f.p.AdvanceDocumentContent(f.ctx, f.owner, f.ref, r.ACLRevision, r.ContentRevision); !errors.Is(err, access.ErrDocumentForbidden) {
			t.Fatal(err)
		}
		if _, err := f.p.TombstoneDocument(f.ctx, f.admin, f.ref, r.ACLRevision, r.ContentRevision); !errors.Is(err, access.ErrDocumentForbidden) {
			t.Fatal(err)
		}
		r, err = f.p.SetDocumentHold(f.ctx, f.owner, f.ref, r.ACLRevision, false)
		if err != nil {
			t.Fatal(err)
		}
		previousContent := r.ContentRevision
		r, err = f.p.AdvanceDocumentContent(f.ctx, f.owner, f.ref, r.ACLRevision, r.ContentRevision)
		if err != nil || r.ContentRevision != previousContent+1 {
			t.Fatalf("advance: %+v %v", r, err)
		}
		if _, err := f.p.TombstoneDocument(f.ctx, f.owner, f.ref, r.ACLRevision, previousContent); !errors.Is(err, access.ErrDocumentVersionConflict) {
			t.Fatal(err)
		}
		r, err = f.p.TombstoneDocument(f.ctx, f.owner, f.ref, r.ACLRevision, r.ContentRevision)
		if err != nil || !r.Tombstoned {
			t.Fatalf("tombstone: %+v %v", r, err)
		}
		f.exec("DELETE FROM dataset_data WHERE dataset_id='alpha' AND data_id='blob'")
		f.allowed(f.owner, f.ref, access.ActionRead, false)
		f.allowed(f.owner, f.other, access.ActionRead, true)
		f.exec("INSERT INTO dataset_data(dataset_id,data_id) VALUES('alpha','blob')")
		f.allowed(f.owner, f.ref, access.ActionRead, false)
		if _, err := f.p.RegisterDocument(f.ctx, f.owner, f.ref, "a", access.DocumentInherit); !errors.Is(err, access.ErrDocumentVersionConflict) {
			t.Fatal(err)
		}
		stored, err := f.p.GetDocumentResource(f.ctx, f.ref)
		if err != nil || stored != r {
			t.Fatalf("tombstone lost: %+v %v", stored, err)
		}
	})
}

func TestDocumentPolicyLegacyAndIdentityFailClosed(t *testing.T) {
	documentDialects(t, func(t *testing.T, f documentFixture) {
		f.allowed(f.viewer, f.ref, access.ActionRead, true)
		f.allowed(f.peer, f.ref, access.ActionRead, false)
		f.allowed(f.owner, access.DocumentRef{DatasetID: "alpha", DataID: "missing"}, access.ActionRead, false)
		f.registered(f.ref, access.DocumentInherit)
		for _, actor := range []access.Actor{{UserID: "inactive", TenantID: "a"}, {UserID: "missing", TenantID: "a"}, {}} {
			f.allowed(actor, f.ref, access.ActionRead, false)
		}
		f.exec("UPDATE users SET is_active=FALSE WHERE id='root'")
		f.allowed(f.admin, f.ref, access.ActionRead, false)
		f.exec("DELETE FROM dataset_data WHERE dataset_id='alpha' AND data_id='blob'")
		f.allowed(f.owner, f.ref, access.ActionRead, false)
		// Failure to load the registration must not look like an unregistered legacy row.
		f.exec("ALTER TABLE document_resources RENAME TO inaccessible_document_resources")
		if d, err := f.p.AuthorizeDocument(f.ctx, f.owner, f.other, access.ActionRead); err == nil || d.Allowed {
			t.Fatalf("schema error became legacy inheritance: %+v %v", d, err)
		}
	})
}

func TestDocumentPolicyEditorCannotManage(t *testing.T) {
	operations := []struct {
		name   string
		action string
		mutate func(documentFixture, access.Actor, access.DocumentResource) (access.DocumentResource, error)
	}{
		{"grant_self_admin", access.ActionShare, func(f documentFixture, actor access.Actor, r access.DocumentResource) (access.DocumentResource, error) {
			return f.p.GrantDocument(f.ctx, actor, f.ref, r.ACLRevision, access.DocumentPrincipal{Kind: access.DocumentUser, ID: actor.UserID}, access.RoleAdmin)
		}},
		{"revoke_grant", access.ActionShare, func(f documentFixture, actor access.Actor, r access.DocumentResource) (access.DocumentResource, error) {
			return f.p.RevokeDocument(f.ctx, actor, f.ref, r.ACLRevision, access.DocumentPrincipal{Kind: access.DocumentUser, ID: "viewer"})
		}},
		{"change_mode", access.ActionShare, func(f documentFixture, actor access.Actor, r access.DocumentResource) (access.DocumentResource, error) {
			mode := access.DocumentInherit
			if r.Mode == mode {
				mode = access.DocumentRestricted
			}
			return f.p.SetDocumentMode(f.ctx, actor, f.ref, r.ACLRevision, mode)
		}},
		{"set_hold", access.ActionShare, func(f documentFixture, actor access.Actor, r access.DocumentResource) (access.DocumentResource, error) {
			return f.p.SetDocumentHold(f.ctx, actor, f.ref, r.ACLRevision, true)
		}},
		{"tombstone", access.ActionDelete, func(f documentFixture, actor access.Actor, r access.DocumentResource) (access.DocumentResource, error) {
			return f.p.TombstoneDocument(f.ctx, actor, f.ref, r.ACLRevision, r.ContentRevision)
		}},
	}
	for _, source := range []string{"direct", "group", "inherited"} {
		t.Run(source, func(t *testing.T) {
			for _, op := range operations {
				t.Run(op.name, func(t *testing.T) {
					documentDialects(t, func(t *testing.T, f documentFixture) {
						mode := access.DocumentRestricted
						if source == "inherited" {
							mode = access.DocumentInherit
						}
						r := f.registered(f.ref, mode)
						var err error
						switch source {
						case "direct":
							r, err = f.p.GrantDocument(f.ctx, f.owner, f.ref, r.ACLRevision, access.DocumentPrincipal{Kind: access.DocumentUser, ID: "peer"}, access.RoleEditor)
						case "group":
							g, groupErr := f.p.CreateGroup(f.ctx, f.owner, "a", "Editors")
							if groupErr != nil {
								t.Fatal(groupErr)
							}
							if _, groupErr := f.p.ReplaceGroupMembers(f.ctx, f.owner, g.ID, g.Revision, []string{"peer"}); groupErr != nil {
								t.Fatal(groupErr)
							}
							r, err = f.p.GrantDocument(f.ctx, f.owner, f.ref, r.ACLRevision, access.DocumentPrincipal{Kind: access.DocumentGroup, ID: g.ID}, access.RoleEditor)
						case "inherited":
							f.exec("INSERT INTO dataset_shares(id,dataset_id,user_id,role) VALUES('editor-share','alpha','peer','editor')")
						}
						if err != nil {
							t.Fatal(err)
						}
						r, err = f.p.GrantDocument(f.ctx, f.owner, f.ref, r.ACLRevision, access.DocumentPrincipal{Kind: access.DocumentUser, ID: "viewer"}, access.RoleViewer)
						if err != nil {
							t.Fatal(err)
						}
						// A real content update remains available to an editor.
						f.allowed(f.peer, f.ref, access.ActionRead, true)
						r, err = f.p.AdvanceDocumentContent(f.ctx, f.peer, f.ref, r.ACLRevision, r.ContentRevision)
						if err != nil {
							t.Fatal(err)
						}
						decision, err := f.p.AuthorizeDocument(f.ctx, f.peer, f.ref, op.action)
						if err != nil || decision.Allowed {
							t.Errorf("editor management decision=%+v err=%v", decision, err)
						}
						if _, err := op.mutate(f, f.peer, r); !errors.Is(err, access.ErrDocumentForbidden) {
							t.Errorf("editor mutation must be forbidden: %v", err)
						}
						stored, err := f.p.GetDocumentResource(f.ctx, f.ref)
						if err != nil || stored != r {
							t.Errorf("denied mutation changed resource: got %+v want %+v err=%v", stored, r, err)
						}
						decision, err = f.p.AuthorizeDocument(f.ctx, f.peer, f.ref, access.ActionRead)
						if err != nil || !decision.Allowed || decision.Role != access.RoleEditor {
							t.Errorf("editor role changed: %+v %v", decision, err)
						}
						var victimRole string
						err = f.db.QueryRow("SELECT role FROM document_grants WHERE dataset_id='alpha' AND data_id='blob' AND principal_kind='user' AND principal_id='viewer'").Scan(&victimRole)
						if err != nil || victimRole != access.RoleViewer {
							t.Errorf("denied revoke changed grant: role=%s err=%v", victimRole, err)
						}
						if t.Failed() {
							return
						}
						// The same operation succeeds after a real manager grants admin.
						r, err = f.p.GrantDocument(f.ctx, f.owner, f.ref, r.ACLRevision, access.DocumentPrincipal{Kind: access.DocumentUser, ID: "peer"}, access.RoleAdmin)
						if err != nil {
							t.Fatal(err)
						}
						after, err := op.mutate(f, f.peer, r)
						if err != nil || after.ACLRevision != r.ACLRevision+1 {
							t.Fatalf("admin mutation rejected: %+v %v", after, err)
						}
					})
				})
			}
		})
	}
}
