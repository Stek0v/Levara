package access_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stek0v/levara/pkg/access"
)

func TestDocumentGroupsLiveMembershipAndTenantBoundary(t *testing.T) {
	documentDialects(t, func(t *testing.T, f documentFixture) {
		r := f.registered(f.ref, access.DocumentRestricted)
		g, err := f.p.CreateGroup(f.ctx, f.owner, "a", "Readers")
		if err != nil {
			t.Fatal(err)
		}
		again, err := f.p.CreateGroup(f.ctx, f.owner, "a", "Readers")
		if err != nil || again.ID != g.ID {
			t.Fatalf("duplicate group: %+v %v", again, err)
		}
		g, err = f.p.ReplaceGroupMembers(f.ctx, f.owner, g.ID, g.Revision, []string{"peer", "peer"})
		if err != nil || len(g.Members) != 1 {
			t.Fatalf("membership: %+v %v", g, err)
		}
		again, err = f.p.CreateGroup(f.ctx, f.owner, "a", "Readers")
		if err != nil || !reflect.DeepEqual(again, g) {
			t.Fatalf("retry must return current group: got %+v want %+v: %v", again, g, err)
		}
		r, err = f.p.GrantDocument(f.ctx, f.owner, f.ref, r.ACLRevision, access.DocumentPrincipal{Kind: access.DocumentGroup, ID: g.ID}, access.RoleEditor)
		if err != nil {
			t.Fatal(err)
		}
		f.allowed(f.peer, f.ref, access.ActionWrite, true)
		readOnly := f.peer
		readOnly.APIKeyPermissions = "read"
		f.allowed(readOnly, f.ref, access.ActionRead, true)
		f.allowed(readOnly, f.ref, access.ActionWrite, false)
		f.allowed(f.viewer, f.ref, access.ActionRead, false)
		f.exec("UPDATE users SET is_active=FALSE WHERE id='peer'")
		f.allowed(f.peer, f.ref, access.ActionRead, false)
		f.exec("UPDATE users SET is_active=TRUE WHERE id='peer'")
		f.allowed(f.peer, f.ref, access.ActionRead, true)
		g, err = f.p.ReplaceGroupMembers(f.ctx, f.owner, g.ID, g.Revision, []string{})
		if err != nil {
			t.Fatal(err)
		}
		f.allowed(f.peer, f.ref, access.ActionRead, false)
		foreignGroup, err := f.p.CreateGroup(f.ctx, f.foreign, "b", "Readers")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.p.GrantDocument(f.ctx, f.owner, f.ref, r.ACLRevision, access.DocumentPrincipal{Kind: access.DocumentGroup, ID: foreignGroup.ID}, access.RoleViewer); !errors.Is(err, access.ErrDocumentForbidden) {
			t.Fatal(err)
		}
		if _, err := f.p.CreateGroup(f.ctx, f.peer, "a", "Unauthorized"); !errors.Is(err, access.ErrGroupForbidden) {
			t.Fatal(err)
		}
		if _, err := f.p.CreateGroup(f.ctx, f.owner, "b", "Foreign"); !errors.Is(err, access.ErrGroupForbidden) {
			t.Fatal(err)
		}
		g, err = f.p.ReplaceGroupMembers(f.ctx, f.owner, g.ID, g.Revision, []string{"peer"})
		if err != nil {
			t.Fatal(err)
		}
		f.exec("DELETE FROM user_tenant WHERE user_id='peer' AND tenant_id='a'")
		f.allowed(f.peer, f.ref, access.ActionRead, false)
		stored, err := f.p.GetGroup(f.ctx, f.owner, g.ID)
		if err != nil || len(stored.Members) != 0 {
			t.Fatalf("tenant removal must cascade membership: %+v %v", stored, err)
		}
	})
}

func TestDocumentGroupsCASAndRollback(t *testing.T) {
	documentDialects(t, func(t *testing.T, f documentFixture) {
		g, err := f.p.CreateGroup(f.ctx, f.owner, "a", "Editors")
		if err != nil {
			t.Fatal(err)
		}
		g, err = f.p.ReplaceGroupMembers(f.ctx, f.owner, g.ID, g.Revision, []string{"peer"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.p.ReplaceGroupMembers(f.ctx, f.owner, g.ID, g.Revision-1, []string{}); !errors.Is(err, access.ErrGroupVersionConflict) {
			t.Fatal(err)
		}
		for _, id := range []string{"foreign", "inactive", "missing"} {
			if _, err := f.p.ReplaceGroupMembers(f.ctx, f.owner, g.ID, g.Revision, []string{id}); !errors.Is(err, access.ErrDocumentForbidden) {
				t.Fatalf("member=%s err=%v", id, err)
			}
			stored, err := f.p.GetGroup(f.ctx, f.owner, g.ID)
			if err != nil || !reflect.DeepEqual(stored, g) {
				t.Fatalf("failed replacement changed group: %+v %v", stored, err)
			}
		}
		readOnly := f.owner
		readOnly.APIKeyPermissions = "read"
		if _, err := f.p.GetGroup(f.ctx, readOnly, g.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.p.ReplaceGroupMembers(f.ctx, readOnly, g.ID, g.Revision, []string{}); !errors.Is(err, access.ErrGroupForbidden) {
			t.Fatal(err)
		}
		if _, err := f.p.GetGroup(f.ctx, f.peer, g.ID); !errors.Is(err, access.ErrGroupForbidden) {
			t.Fatal(err)
		}
		// Renaming the child table causes DELETE to fail after group revision CAS.
		f.exec("ALTER TABLE access_group_members RENAME TO inaccessible_group_members")
		if _, err := f.p.ReplaceGroupMembers(f.ctx, f.owner, g.ID, g.Revision, []string{}); err == nil {
			t.Fatal("SQL error was hidden")
		}
		f.exec("ALTER TABLE inaccessible_group_members RENAME TO access_group_members")
		stored, err := f.p.GetGroup(f.ctx, f.owner, g.ID)
		if err != nil || !reflect.DeepEqual(stored, g) {
			t.Fatalf("SQL failure lost old members/revision: %+v %v", stored, err)
		}
	})
}

func TestDocumentGroupsActiveManagerAndConcurrentCAS(t *testing.T) {
	documentDialects(t, func(t *testing.T, f documentFixture) {
		g, err := f.p.CreateGroup(f.ctx, f.admin, "a", "Managed")
		if err != nil {
			t.Fatal(err)
		}
		for _, actor := range []access.Actor{{UserID: "missing", TenantID: "a", Superuser: true}, {UserID: "inactive", TenantID: "a", Superuser: true}} {
			if _, err := f.p.GetGroup(f.ctx, actor, g.ID); !errors.Is(err, access.ErrGroupForbidden) {
				t.Fatalf("actor=%+v err=%v", actor, err)
			}
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, id := range []string{"peer", "viewer"} {
			go func(id string) {
				<-start
				_, err := f.p.ReplaceGroupMembers(f.ctx, f.admin, g.ID, g.Revision, []string{id})
				results <- err
			}(id)
		}
		close(start)
		successes, conflicts := 0, 0
		for i := 0; i < 2; i++ {
			err := <-results
			if err == nil {
				successes++
			} else if errors.Is(err, access.ErrGroupVersionConflict) {
				conflicts++
			} else {
				t.Fatal(err)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
		}
		stored, err := f.p.GetGroup(f.ctx, f.admin, g.ID)
		if err != nil || stored.Revision != g.Revision+1 || len(stored.Members) != 1 {
			t.Fatalf("group: %+v %v", stored, err)
		}
		f.exec("UPDATE users SET is_active=FALSE WHERE id='root'")
		if _, err := f.p.ReplaceGroupMembers(f.ctx, f.admin, g.ID, stored.Revision, []string{}); !errors.Is(err, access.ErrGroupForbidden) {
			t.Fatalf("inactive admin: %v", err)
		}
	})
}
