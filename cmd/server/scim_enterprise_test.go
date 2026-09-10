package main

import (
	"context"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/access"
)

// The existing read seam reproduces a deactivation after PATCH has read the
// user, before the mutation starts; no scheduling assumption is required.
type deactivateAfterSCIMRead struct {
	scimQuerier
	store *access.SCIMStore
	fired bool
}

func (q *deactivateAfterSCIMRead) ByID(ctx context.Context, issuer, id string) (email string, active bool, externalID string, err error) {
	email, active, externalID, err = q.scimQuerier.ByID(ctx, issuer, id)
	if err == nil && !q.fired {
		q.fired = true
		err = q.store.ProvisionDeactivate(ctx, issuer, externalID)
	}
	return
}

func TestSCIMEnterprisePatchCannotReviveConcurrentDeactivation(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			app, store := newManagedSCIMTestApp(t, dialect)
			id := managedSCIMRequest(t, app, "POST", "/scim/v2/Users", `{"userName":"race@test.invalid","externalId":"race","active":true}`, 201)["id"].(string)
			store.TenantID = "tenant-a"
			q := &deactivateAfterSCIMRead{scimQuerier: pgSCIMQuery{DB: store.DB, Q: store.Q}, store: store}
			raced := fiber.New()
			if err := SCIMRoutes(raced, *store, q, nil); err != nil {
				t.Fatal(err)
			}
			out := managedSCIMRequest(t, raced, "PATCH", "/scim/v2/Users/"+id, `{"Operations":[{"op":"replace","path":"`+scimEnterpriseURN+`:department","value":"After read"}]}`, 200)
			if !q.fired || out["active"] != false || out[scimEnterpriseURN].(map[string]any)["department"] != "After read" {
				t.Fatalf("metadata patch revived deactivated user: %v", out)
			}
			if err := access.ValidateCredential(context.Background(), store.DB, store.Q, id, 0); err == nil {
				t.Fatal("credential from before deactivation became valid")
			}
		})
	}
}

func TestSCIMEnterpriseFieldsRoundTrip(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			app, _ := newManagedSCIMTestApp(t, dialect)
			out := managedSCIMRequest(t, app, "POST", "/scim/v2/Users", `{"userName":"enterprise@test.invalid","externalId":"enterprise-subject","urn:ietf:params:scim:schemas:extension:enterprise:2.0:User":{"employeeNumber":"E-42","organization":"Admin","department":"Readers"}}`, 201)
			extension, ok := out["urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"].(map[string]any)
			if !ok || extension["employeeNumber"] != "E-42" || extension["organization"] != "Admin" {
				t.Fatalf("enterprise attributes lost: %v", out)
			}
		})
	}
}

func TestSCIMEnterpriseHTTPPatchAndManagerBoundary(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			app, store := newManagedSCIMTestApp(t, dialect)
			manager := managedSCIMRequest(t, app, "POST", "/scim/v2/Users", `{"userName":"manager@test.invalid","externalId":"manager"}`, 201)["id"].(string)
			if _, err := store.DB.Exec(store.Q("INSERT INTO user_tenant(user_id,tenant_id) VALUES($1,'tenant-a')"), manager); err != nil {
				t.Fatal(err)
			}
			user := managedSCIMRequest(t, app, "POST", "/scim/v2/Users", `{"userName":"profile@test.invalid","externalId":"profile","urn:ietf:params:scim:schemas:extension:enterprise:2.0:User":{"department":"Research","manager":{"value":"`+manager+`"}}}`, 201)
			id := user["id"].(string)
			path := "/scim/v2/Users/" + id
			for _, route := range []string{path, "/scim/v2/Users?filter=externalId%20eq%20%22profile%22"} {
				out := managedSCIMRequest(t, app, "GET", route, "", 200)
				if route != path {
					out = out["Resources"].([]any)[0].(map[string]any)
				}
				extension, ok := out[scimEnterpriseURN].(map[string]any)
				if !ok || extension["department"] != "Research" {
					t.Fatalf("read lost extension: %v", out)
				}
			}
			managedSCIMRequest(t, app, "PATCH", path, `{"Operations":[{"op":"replace","path":"active","value":false},{"op":"replace","path":"`+scimEnterpriseURN+`:manager","value":{"value":"unknown"}}]}`, 400)
			out := managedSCIMRequest(t, app, "GET", path, "", 200)
			if out["active"] != true {
				t.Fatal("failed extension changed active")
			}
			out = managedSCIMRequest(t, app, "PATCH", path, `{"Operations":[{"op":"remove","path":"`+scimEnterpriseURN+`:department"}]}`, 200)
			extension := out[scimEnterpriseURN].(map[string]any)
			if _, ok := extension["department"]; ok {
				t.Fatalf("remove did not clear department: %v", extension)
			}
			if extension["manager"] == nil {
				t.Fatal("remove cleared unrelated manager")
			}
			managedSCIMRequest(t, app, "PATCH", path, `{"Operations":[{"op":"replace","path":"active","value":null}]}`, 400)
			managedSCIMRequest(t, app, "POST", "/scim/v2/Users", `{"userName":"missing-id@test.invalid"}`, 400)
		})
	}
}
