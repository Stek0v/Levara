package access

import "testing"

func TestAPIKeyIdentityValid(t *testing.T) {
	if (APIKeyIdentity{}).Valid() {
		t.Fatal("zero-value identity must be invalid")
	}
	if !(APIKeyIdentity{UserID: "user-a"}).Valid() {
		t.Fatal("identity with user id must be valid")
	}
}

func TestAPIKeyIdentityActor(t *testing.T) {
	actor := APIKeyIdentity{UserID: "user-a", Permissions: "read"}.Actor()
	if actor.UserID != "user-a" || actor.APIKeyPermissions != "read" || actor.AuthMethod != "api_key" {
		t.Fatalf("actor=%+v, want user-a/read/api_key projection", actor)
	}

	// The projected actor must carry the same access verdict the raw
	// permissions imply, so policy code can consume it interchangeably.
	if APIKeyAllows(actor.APIKeyPermissions, ActionWrite) {
		t.Fatal("read-only api key must not allow write via projected actor")
	}
	if !APIKeyAllows(actor.APIKeyPermissions, ActionRead) {
		t.Fatal("read api key must allow read via projected actor")
	}
}
