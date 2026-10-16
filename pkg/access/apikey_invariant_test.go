package access

import (
	"context"
	"testing"
)

// Phase-1 B3 invariant (MODERNIZATION_BRIEF.md §5, owner decision 2026-10-16):
// API-key permissions may only NARROW what the role allows — never widen it.
// The decision model is a conjunction:
//
//	Decision.Allowed == RoleAllows(role, action) && APIKeyAllows(perms, action)
//
// The tests below pin (1) the semantics of APIKeyAllows itself, (2) the
// widening direction on the SQLPolicy facade — a narrow role must stay denied
// even with a maximally wide key — and (3) the full cross-product against the
// conjunction oracle on the dataset resource.

// invariantPerms covers: no key, single grants, combined grants, case
// variants, and an unknown string that must narrow to nothing.
var invariantPerms = []string{"", "read", "write", "admin", "read write", "READ", "bogus"}

func TestAPIKeyAllowsSemantics(t *testing.T) {
	cases := []struct {
		perms  string
		action string
		want   bool
	}{
		{"", ActionRead, true},
		{"", ActionWrite, true},
		{"", ActionDelete, true},
		{"", ActionShare, true},
		{"read", ActionRead, true},
		{"read", ActionWrite, false},
		{"read", ActionDelete, false},
		{"read", ActionShare, false},
		{"write", ActionRead, true}, // write implies read at key level
		{"write", ActionWrite, true},
		{"write", ActionShare, true},
		{"admin", ActionShare, true},
		{"READ", ActionRead, true}, // case-insensitive
		{"READ", ActionWrite, false},
		{"bogus", ActionRead, false}, // unknown grants nothing
		{"bogus", ActionWrite, false},
	}
	for _, tc := range cases {
		if got := APIKeyAllows(tc.perms, tc.action); got != tc.want {
			t.Errorf("APIKeyAllows(%q, %q) = %v, want %v", tc.perms, tc.action, got, tc.want)
		}
	}
}

// TestAuthorizeAPIKeyNeverWidens is the core B3 direction: a role that denies
// an action stays denied no matter how wide the key is, while a role that
// allows keeps allowing unless the key itself rules the action out.
func TestAuthorizeAPIKeyNeverWidens(t *testing.T) {
	db := newPolicyTestDB(t)
	// Editor share so the fixture covers the middle role of the matrix.
	if _, err := db.Exec(`INSERT INTO dataset_shares(id, dataset_id, user_id, role) VALUES ('share-c', 'payments', 'user-c', 'editor')`); err != nil {
		t.Fatal(err)
	}
	policy := SQLPolicy{DB: db, Q: sqliteQ}
	ctx := context.Background()

	cases := []struct {
		name    string
		kind    ResourceKind
		userID  string
		perms   string
		action  string
		allowed bool
	}{
		// Widening attempts: wide key, narrow role — must stay denied.
		{"viewer write with admin key stays denied", ResourceDataset, "user-b", "admin", ActionWrite, false},
		{"viewer delete with admin key stays denied", ResourceDataset, "user-b", "admin", ActionDelete, false},
		{"viewer share with admin key stays denied", ResourceDataset, "user-b", "admin", ActionShare, false},
		{"viewer write with write key stays denied", ResourceWorkspace, "user-b", "write", ActionWrite, false},
		// normalizeAction folds delete/share into the write class (finding M25),
		// so RoleAllows(editor, delete|share) is true — the editor rows below
		// pin the narrowing direction instead: a read-only key must still deny.
		{"editor delete with read key denied", ResourceDataset, "user-c", "read", ActionDelete, false},
		{"editor share with read key denied", ResourceDataset, "user-c", "read", ActionShare, false},
		{"no-role read with admin key stays denied", ResourceDataset, "user-d", "admin", ActionRead, false},

		// Narrowing: wide role, key rules the action out — must deny.
		{"owner write with read key denied", ResourceDataset, "user-a", "read", ActionWrite, false},
		{"owner write with READ key denied", ResourceWorkspace, "user-a", "READ", ActionWrite, false},
		{"editor write with read key denied", ResourceDataset, "user-c", "read", ActionWrite, false},

		// Neutral: key must not break access the role legitimately grants.
		{"owner write with empty key allowed", ResourceDataset, "user-a", "", ActionWrite, true},
		{"owner write with admin key allowed", ResourceWorkspace, "user-a", "admin", ActionWrite, true},
		{"viewer read with admin key still allowed", ResourceDataset, "user-b", "admin", ActionRead, true},
		{"editor write with admin key still allowed", ResourceDataset, "user-c", "admin", ActionWrite, true},

		// Unknown permission string narrows everything, even for admin.
		{"owner read with bogus key denied", ResourceDataset, "user-a", "bogus", ActionRead, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := policy.Authorize(ctx, Actor{UserID: tc.userID, APIKeyPermissions: tc.perms},
				Resource{Kind: tc.kind, ID: "payments"}, tc.action)
			if err != nil {
				t.Fatal(err)
			}
			if d.Allowed != tc.allowed {
				t.Fatalf("Allowed = %v (role=%q reason=%q apiKeyAllowed=%v), want %v",
					d.Allowed, d.Role, d.Reason, d.APIKeyAllowed, tc.allowed)
			}
		})
	}
}

// TestAuthorizeAPIKeyConjunctionExhaustive walks the full cross-product of
// fixture roles × permission strings × actions on the dataset resource and
// checks the decision against the conjunction oracle. This is the machine-
// checkable form of the invariant: the key modulates only by AND.
func TestAuthorizeAPIKeyConjunctionExhaustive(t *testing.T) {
	db := newPolicyTestDB(t)
	if _, err := db.Exec(`INSERT INTO dataset_shares(id, dataset_id, user_id, role) VALUES ('share-c', 'payments', 'user-c', 'editor')`); err != nil {
		t.Fatal(err)
	}
	policy := SQLPolicy{DB: db, Q: sqliteQ}
	ctx := context.Background()

	roles := map[string]string{
		"user-a": RoleAdmin,   // owner
		"user-b": RoleViewer,  // shared viewer
		"user-c": RoleEditor,  // shared editor
		"user-d": "",          // no relation
	}
	actions := []string{ActionRead, ActionWrite, ActionDelete, ActionShare}

	for userID, role := range roles {
		for _, perms := range invariantPerms {
			for _, action := range actions {
				want := RoleAllows(role, action) && APIKeyAllows(perms, action)
				d, err := policy.Authorize(ctx, Actor{UserID: userID, APIKeyPermissions: perms},
					Resource{Kind: ResourceDataset, ID: "payments"}, action)
				if err != nil {
					t.Fatal(err)
				}
				if d.Allowed != want {
					t.Errorf("user=%s role=%q perms=%q action=%q: Allowed=%v (reason=%q), oracle says %v",
						userID, role, perms, action, d.Allowed, d.Reason, want)
				}
			}
		}
	}
}
