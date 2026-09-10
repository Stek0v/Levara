package access_test

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"

	httpapi "github.com/stek0v/levara/internal/http"
	"github.com/stek0v/levara/pkg/access"
)

func managedDirectoryFixture(t *testing.T, dialect string) (documentFixture, access.SCIMStore, []string) {
	t.Helper()
	f := newDocumentFixture(t, dialect)
	s := access.SCIMStore{DB: f.db, Q: httpapi.Q, TenantID: "a"}
	if err := s.EnsureSchema(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := access.EnsureDirectoryGroupSchema(f.ctx, f.db, httpapi.Q); err != nil {
		t.Fatal(err)
	}
	if err := s.BindDirectory(f.ctx, "directory", "a"); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, subject := range []string{"alice", "bob"} {
		id, _, err := s.ProvisionCreate(f.ctx, access.SCIMUser{Issuer: "directory", ExternalID: subject, Email: subject + "@directory.invalid", Active: true})
		if err != nil {
			t.Fatal(err)
		}
		f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES($1,'a')", id)
		ids = append(ids, id)
	}
	return f, s, ids
}

func TestSCIMManagedGroupIdentityAndDocumentRevocation(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, s, users := managedDirectoryFixture(t, dialect)
			if err := s.BindDirectory(f.ctx, "directory", "b"); !errors.Is(err, access.ErrSCIMConflict) {
				t.Fatalf("directory tenant reassigned: %v", err)
			}
			manual, err := f.p.CreateGroup(f.ctx, f.owner, "a", "Readers")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.CreateSCIMGroup(f.ctx, "directory", "external-readers", "Readers", users); !errors.Is(err, access.ErrSCIMConflict) {
				t.Fatalf("manual group adopted: %v", err)
			}
			current, err := f.p.GetGroup(f.ctx, f.owner, manual.ID)
			if err != nil || !reflect.DeepEqual(current, manual) {
				t.Fatalf("manual group changed: %+v %v", current, err)
			}
			g, created, err := s.CreateSCIMGroup(f.ctx, "directory", "external-readers", "Managed Readers", []string{users[0]})
			if err != nil || !created {
				t.Fatalf("create: %+v %v", g, err)
			}
			if _, err := s.GetSCIMGroup(f.ctx, "foreign-directory", g.ID); !errors.Is(err, access.ErrSCIMNotFound) {
				t.Fatalf("foreign issuer read group: %v", err)
			}
			otherTenant := s
			otherTenant.TenantID = "b"
			if _, err := otherTenant.GetSCIMGroup(f.ctx, "directory", g.ID); !errors.Is(err, access.ErrSCIMNotFound) {
				t.Fatalf("foreign tenant read group: %v", err)
			}
			again, created, err := s.CreateSCIMGroup(f.ctx, "directory", "external-readers", "Admin", users)
			if err != nil || created || !reflect.DeepEqual(again, g) {
				t.Fatalf("retry replaced state: %+v %v", again, err)
			}
			if _, err := f.p.ReplaceGroupMembers(f.ctx, f.owner, g.ID, g.Revision, users); !errors.Is(err, access.ErrGroupForbidden) {
				t.Fatalf("local writer replaced directory group: %v", err)
			}
			if _, err := f.p.CreateGroup(f.ctx, f.owner, "a", g.DisplayName); !errors.Is(err, access.ErrGroupForbidden) {
				t.Fatalf("local create adopted directory group: %v", err)
			}
			r := f.registered(f.ref, access.DocumentRestricted)
			r, err = f.p.GrantDocument(f.ctx, f.owner, f.ref, r.ACLRevision, access.DocumentPrincipal{Kind: access.DocumentGroup, ID: g.ID}, access.RoleViewer)
			if err != nil {
				t.Fatal(err)
			}
			member := access.Actor{UserID: users[0], TenantID: "a"}
			f.allowed(member, f.ref, access.ActionRead, true)
			f.allowed(member, f.ref, access.ActionWrite, false)
			f.allowed(member, f.ref, access.ActionShare, false)
			g, err = s.UpdateSCIMGroup(f.ctx, "directory", g.ID, g.Revision, []access.SCIMGroupOperation{{Op: "replace", Field: "displayName", Value: "Global Administrators"}})
			if err != nil || g.ID != again.ID {
				t.Fatalf("rename changed identity: %+v %v", g, err)
			}
			var admin bool
			if err := f.db.QueryRow("SELECT is_superuser FROM users WHERE id=$1", users[0]).Scan(&admin); err != nil || admin {
				t.Fatalf("name inferred privilege: %v %v", admin, err)
			}
			g, err = s.UpdateSCIMGroup(f.ctx, "directory", g.ID, g.Revision, []access.SCIMGroupOperation{{Op: "remove", Field: "member", Value: users[0]}})
			if err != nil {
				t.Fatal(err)
			}
			f.allowed(member, f.ref, access.ActionRead, false)
			g, err = s.UpdateSCIMGroup(f.ctx, "directory", g.ID, g.Revision, []access.SCIMGroupOperation{{Op: "add", Field: "members", Members: []string{users[0]}}})
			if err != nil {
				t.Fatal(err)
			}
			f.allowed(member, f.ref, access.ActionRead, true)
			if err := s.DeleteSCIMGroup(f.ctx, "directory", g.ID, g.Revision); err != nil {
				t.Fatal(err)
			}
			if err := s.DeleteSCIMGroup(f.ctx, "directory", g.ID, 0); err != nil {
				t.Fatal(err)
			}
			f.allowed(member, f.ref, access.ActionRead, false)
			if _, err := s.GetSCIMGroup(f.ctx, "directory", g.ID); !errors.Is(err, access.ErrSCIMNotFound) {
				t.Fatalf("deleted group visible: %v", err)
			}
			replacement, created, err := s.CreateSCIMGroup(f.ctx, "directory", g.ExternalID, g.DisplayName, []string{users[0]})
			if err != nil || !created || replacement.ID == g.ID {
				t.Fatalf("recreated group reused principal: %+v %v", replacement, err)
			}
			f.allowed(member, f.ref, access.ActionRead, false)
			var grants int
			if err := f.db.QueryRow("SELECT COUNT(*) FROM document_grants WHERE principal_id=$1", g.ID).Scan(&grants); err != nil || grants != 0 {
				t.Fatalf("retired grants=%d err=%v", grants, err)
			}
			currentResource := f.registered(f.ref, access.DocumentRestricted)
			if currentResource.ACLRevision <= r.ACLRevision {
				t.Fatal("group deletion did not advance document ACL revision")
			}
		})
	}
}

func TestSCIMManagedGroupAtomicityAndAudit(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, s, users := managedDirectoryFixture(t, dialect)
			g, _, err := s.CreateSCIMGroup(f.ctx, "directory", "group", "Group", []string{users[0]})
			if err != nil {
				t.Fatal(err)
			}
			for _, bad := range []string{"foreign", "inactive", "missing", "peer"} {
				if _, err := s.UpdateSCIMGroup(f.ctx, "directory", g.ID, g.Revision, []access.SCIMGroupOperation{{Op: "replace", Field: "displayName", Value: "Changed"}, {Op: "replace", Field: "members", Members: []string{bad}}}); !errors.Is(err, access.ErrSCIMInvalid) {
					t.Fatalf("invalid member %s accepted: %v", bad, err)
				}
				current, err := s.GetSCIMGroup(f.ctx, "directory", g.ID)
				if err != nil || !reflect.DeepEqual(current, g) {
					t.Fatalf("failed operation mutated state: %+v %v", current, err)
				}
			}
			var before int
			if err := f.db.QueryRow("SELECT COUNT(*) FROM scim_provisioning_events").Scan(&before); err != nil {
				t.Fatal(err)
			}
			f.exec("INSERT INTO api_keys(id,key_hash,user_id) VALUES('audit-key','test-only-hash',$1)", users[0])
			epoch, err := access.CurrentCredentialEpoch(f.ctx, f.db, httpapi.Q, users[0])
			if err != nil {
				t.Fatal(err)
			}
			f.exec("ALTER TABLE scim_provisioning_events RENAME TO unavailable_scim_audit")
			if _, err := s.UpdateSCIMGroup(f.ctx, "directory", g.ID, g.Revision, []access.SCIMGroupOperation{{Op: "replace", Field: "members", Members: users}}); err == nil {
				t.Fatal("audit failure reported mutation success")
			}
			if _, _, err := s.ProvisionCreate(f.ctx, access.SCIMUser{Issuer: "directory", ExternalID: "audit-failed-user", Email: "audit-failed@invalid.test", Active: true}); err == nil {
				t.Fatal("user create ignored audit failure")
			}
			if err := s.ProvisionDeactivate(f.ctx, "directory", "alice"); err == nil {
				t.Fatal("deactivation ignored audit failure")
			}
			f.exec("ALTER TABLE unavailable_scim_audit RENAME TO scim_provisioning_events")
			if err := access.ValidateCredential(f.ctx, f.db, httpapi.Q, users[0], epoch); err != nil {
				t.Fatalf("failed deactivation changed active/epoch: %v", err)
			}
			var revoked bool
			if err := f.db.QueryRow("SELECT revoked FROM api_keys WHERE id='audit-key'").Scan(&revoked); err != nil || revoked {
				t.Fatalf("failed deactivation revoked key: %v %v", revoked, err)
			}
			current, err := s.GetSCIMGroup(f.ctx, "directory", g.ID)
			if err != nil || !reflect.DeepEqual(current, g) {
				t.Fatalf("audit rollback lost state: %+v %v", current, err)
			}
			if _, err := s.Lookup(f.ctx, "directory", "audit-failed-user"); !errors.Is(err, access.ErrUserNotFound) {
				t.Fatalf("audit failure left user mapping: %v", err)
			}
			var after int
			if err := f.db.QueryRow("SELECT COUNT(*) FROM scim_provisioning_events").Scan(&after); err != nil || after != before {
				t.Fatalf("audit mismatch %d/%d err=%v", before, after, err)
			}
			events, err := s.ProvisioningEvents(f.ctx, "directory", 200)
			if err != nil || len(events) != after {
				t.Fatalf("durable audit reader: %+v %v", events, err)
			}
			for _, event := range events {
				if event.Issuer != "directory" || event.TenantID != "a" || event.ResourceID == "" || event.Action == "" {
					t.Fatalf("missing audit scope: %+v", event)
				}
			}
			if events, err := s.ProvisioningEvents(f.ctx, "foreign-directory", 200); err != nil || len(events) != 0 {
				t.Fatalf("foreign issuer audit: %+v %v", events, err)
			}
			for range 2 {
				if err := s.EnsureSchema(f.ctx); err != nil {
					t.Fatal(err)
				}
				if err := access.EnsureDirectoryGroupSchema(f.ctx, f.db, httpapi.Q); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestSCIMManagedGroupConcurrentGrantAndDeletion(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, s, users := managedDirectoryFixture(t, dialect)
			for attempt := range 12 {
				g, _, err := s.CreateSCIMGroup(f.ctx, "directory", fmt.Sprintf("delete-%d", attempt), fmt.Sprintf("Delete %d", attempt), users[:1])
				if err != nil {
					t.Fatal(err)
				}
				r := f.registered(f.ref, access.DocumentRestricted)
				start := make(chan struct{})
				grantResult, deleteResult := make(chan error, 1), make(chan error, 1)
				go func() {
					<-start
					_, err := f.p.GrantDocument(f.ctx, f.owner, f.ref, r.ACLRevision, access.DocumentPrincipal{Kind: access.DocumentGroup, ID: g.ID}, access.RoleViewer)
					grantResult <- err
				}()
				go func() {
					<-start
					deleteResult <- s.DeleteSCIMGroup(f.ctx, "directory", g.ID, g.Revision)
				}()
				close(start)
				grantErr, deleteErr := <-grantResult, <-deleteResult
				if deleteErr != nil {
					t.Fatalf("delete failed, grant=%v: %v", grantErr, deleteErr)
				}
				// A concurrent grant may be rejected or commit before deletion. In
				// either ordering the completed deletion must leave no effective grant.
				f.allowed(access.Actor{UserID: users[0], TenantID: "a"}, f.ref, access.ActionRead, false)
				var grants int
				if err := f.db.QueryRow("SELECT COUNT(*) FROM document_grants WHERE principal_id=$1", g.ID).Scan(&grants); err != nil || grants != 0 {
					t.Fatalf("concurrent grant survived deletion: %d %v (grant=%v)", grants, err, grantErr)
				}
				if _, err := s.GetSCIMGroup(f.ctx, "directory", g.ID); !errors.Is(err, access.ErrSCIMNotFound) {
					t.Fatalf("deleted group remains: %v", err)
				}
			}
		})
	}
}

func TestSCIMManagedGroupConcurrentUpdates(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, s, users := managedDirectoryFixture(t, dialect)
			results := make(chan access.SCIMGroup, 4)
			failures := make(chan error, 4)
			var wg sync.WaitGroup
			for range 4 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					g, _, err := s.CreateSCIMGroup(f.ctx, "directory", "same-id", "Concurrent", nil)
					if err != nil {
						failures <- err
					} else {
						results <- g
					}
				}()
			}
			wg.Wait()
			close(results)
			close(failures)
			for err := range failures {
				t.Error(err)
			}
			var group access.SCIMGroup
			for g := range results {
				if group.ID != "" && g.ID != group.ID {
					t.Fatal("duplicate group identities")
				}
				group = g
			}
			if group.ID == "" {
				t.Fatal("no group")
			}
			errCh := make(chan error, 2)
			for _, uid := range users {
				wg.Add(1)
				go func(uid string) {
					defer wg.Done()
					_, err := s.UpdateSCIMGroup(f.ctx, "directory", group.ID, 0, []access.SCIMGroupOperation{{Op: "add", Field: "members", Members: []string{uid}}})
					errCh <- err
				}(uid)
			}
			wg.Wait()
			for range users {
				if err := <-errCh; err != nil {
					t.Fatal(err)
				}
			}
			current, err := s.GetSCIMGroup(f.ctx, "directory", group.ID)
			sort.Strings(users)
			if err != nil || !reflect.DeepEqual(current.Members, users) {
				t.Fatalf("lost concurrent add: %+v %v", current, err)
			}
			if _, err := s.UpdateSCIMGroup(f.ctx, "directory", group.ID, group.Revision, []access.SCIMGroupOperation{{Op: "remove", Field: "members"}}); !errors.Is(err, access.ErrSCIMVersionConflict) {
				t.Fatalf("stale CAS accepted: %v", err)
			}
		})
	}
}

func TestSCIMEnterpriseAtomicFieldsAndTenantManager(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, s, users := managedDirectoryFixture(t, dialect)
			department := "Research"
			manager := users[1]
			u := access.SCIMUser{Issuer: "directory", ExternalID: "alice", Email: "alice@directory.invalid", ActiveUnchanged: true, EnterprisePatch: map[string]*string{"department": &department, "manager": &manager}}
			if err := s.ProvisionUpdate(f.ctx, u, ""); err != nil {
				t.Fatal(err)
			}
			profile, err := s.EnterpriseUser(f.ctx, "directory", "alice")
			if err != nil || profile.Department != department || profile.ManagerID != manager {
				t.Fatalf("metadata=%+v err=%v", profile, err)
			}
			invalidManager := "foreign"
			u.EnterprisePatch = map[string]*string{"manager": &invalidManager}
			u.ActiveUnchanged = false
			u.Active = false
			if err := s.ProvisionUpdate(f.ctx, u, ""); !errors.Is(err, access.ErrSCIMInvalid) {
				t.Fatalf("foreign manager accepted: %v", err)
			}
			active, err := f.p.IsActive(f.ctx, users[0])
			if err != nil || !active {
				t.Fatalf("failed metadata update deactivated user: %v %v", active, err)
			}
			if err := s.ProvisionDeactivate(f.ctx, "directory", "alice"); err != nil {
				t.Fatal(err)
			}
			u.Active = true
			u.ActiveUnchanged = true
			u.EnterprisePatch = map[string]*string{"department": nil}
			if err := s.ProvisionUpdate(f.ctx, u, ""); err != nil {
				t.Fatal(err)
			}
			active, err = f.p.IsActive(f.ctx, users[0])
			if err != nil || active {
				t.Fatal("metadata-only patch revived user from stale active=true")
			}
			profile, err = s.EnterpriseUser(f.ctx, "directory", "alice")
			if err != nil || profile.Department != "" || profile.ManagerID != manager {
				t.Fatalf("clear lost untouched manager: %+v %v", profile, err)
			}
		})
	}
}
