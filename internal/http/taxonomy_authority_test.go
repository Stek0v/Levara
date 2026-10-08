package http

import (
	"context"
	"errors"
	"fmt"
	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/taxonomy"
	"io"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTaxonomyLegacyHierarchyConflicts(t *testing.T) {
	for _, shape := range []string{"domain-duplicate", "collection-duplicate", "document-duplicate", "orphan-collection", "mismatched-document"} {
		t.Run(shape, func(t *testing.T) {
			documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
				taxonomyNativeAPI(f)
				f.exec("INSERT INTO knowledge_domains(id,owner_id,team_id,dataset_id,name) VALUES('d','owner','a','alpha','ЁЖ'),('other','owner','a','alpha','other')")
				f.exec("INSERT INTO knowledge_collections(id,domain_id,owner_id,team_id,dataset_id,name) VALUES('c','d','owner','a','alpha','sessions')")
				switch shape {
				case "domain-duplicate":
					f.exec("INSERT INTO knowledge_domains(id,owner_id,team_id,dataset_id,name) VALUES('duplicate','owner','a','alpha','ёж')")
				case "collection-duplicate":
					f.exec("INSERT INTO knowledge_collections(id,domain_id,owner_id,team_id,dataset_id,name) VALUES('duplicate','d','owner','a','alpha','SESSIONS')")
				case "document-duplicate":
					f.exec("INSERT INTO knowledge_documents(id,domain_id,collection_id,owner_id,team_id,dataset_id,title) VALUES('one','d','c','owner','a','alpha','Rules'),('two','d','c','owner','a','alpha','RULES')")
				case "orphan-collection":
					f.exec("UPDATE knowledge_collections SET domain_id='missing' WHERE id='c'")
				case "mismatched-document":
					f.exec("INSERT INTO knowledge_documents(id,domain_id,collection_id,owner_id,team_id,dataset_id,title) VALUES('one','other','c','owner','a','alpha','Rules')")
				}
				before := taxonomySnapshot(t, f)
				f.expect("owner", "POST", "/datasets/alpha/taxonomy/import", taxonomyBody(taxonomy.ImportRequest{Seed: "# Domains\n## next"}), 409, "X-Tenant-Id", "a")
				f.expect("owner", "DELETE", "/datasets/alpha/taxonomy", taxonomyBody(taxonomy.RemoveRequest{Domain: "ЁЖ", Force: true}), 409, "X-Tenant-Id", "a")
				if taxonomySnapshot(t, f) != before {
					t.Fatal("ambiguous legacy hierarchy mutated")
				}
			})
		})
	}
}
func TestTaxonomySelectedTenantDatasetOwner(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		taxonomyNativeAPI(f)
		f.exec("INSERT INTO datasets(id,name,owner_id) VALUES('foreign-catalog','Foreign catalog','foreign')")
		f.exec("INSERT INTO dataset_shares(id,dataset_id,user_id,role) VALUES('foreign-editor','foreign-catalog','peer','editor'),('native-editor','alpha','peer','editor')")
		before := taxonomySnapshot(t, f)
		path := "/datasets/foreign-catalog/taxonomy"
		f.expect("peer", "POST", path+"/import", taxonomyBody(taxonomy.ImportRequest{Seed: "# Domains\n## no-doc"}), 403, "X-Tenant-Id", "a")
		f.expect("peer", "GET", path, "", 403, "X-Tenant-Id", "a")
		f.expect("peer", "DELETE", path, taxonomyBody(taxonomy.RemoveRequest{Domain: "no-doc", Force: true}), 403, "X-Tenant-Id", "a")
		if taxonomySnapshot(t, f) != before {
			t.Fatal("cross-tenant editor grant mutated")
		}
		f.expect("peer", "POST", "/datasets/alpha/taxonomy/import", taxonomyBody(taxonomy.ImportRequest{Seed: "# Domains\n## allowed"}), 200, "X-Tenant-Id", "a")
		// Empty tenant remains an exact catalog namespace; it does not invent an owner tenant.
		f.expect("peer", "POST", path+"/import", taxonomyBody(taxonomy.ImportRequest{Seed: "# Domains\n## empty-tenant"}), 200)
	})
}
func TestTaxonomyReadOnlyVerifiedKey(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.cfg.RequireAuth = true
		f.exec("INSERT INTO api_keys(id,key_hash,user_id,permissions,revoked) VALUES('taxonomy-read-key','fixture-hash','owner','read',false)")
		f.app = fiber.New(fiber.Config{DisableStartupMessage: true})
		f.app.Use(func(c *fiber.Ctx) error {
			c.Locals("user_id", "owner")
			c.Locals("tenant_id", "a")
			c.Locals("api_key_permissions", "read")
			c.Locals("verified_api_key", access.APIKeyIdentity{KeyID: "taxonomy-read-key", UserID: "owner", Permissions: "read"})
			return c.Next()
		})
		RegisterTaxonomyAPI(f.app.Group("/api/v1"), f.cfg)
		before := taxonomySnapshot(t, f)
		path := "/datasets/alpha/taxonomy"
		f.expect("owner", "GET", path, "", 200)
		f.expect("owner", "POST", path+"/import", taxonomyBody(taxonomy.ImportRequest{Seed: "# Domains\n## denied"}), 403)
		f.expect("owner", "DELETE", path, taxonomyBody(taxonomy.RemoveRequest{Domain: "missing"}), 403)
		if taxonomySnapshot(t, f) != before {
			t.Fatal("readonly key mutated")
		}
		f.exec("UPDATE api_keys SET revoked=true WHERE id='taxonomy-read-key'")
		f.expect("owner", "GET", path, "", 401)
		if f.db.Stats().InUse != 0 {
			t.Fatal("revoked listing leaked connection")
		}
	})
}
func TestTaxonomyListingActualClose(t *testing.T) {
	for _, boundary := range []string{"request-deadline", "credential-expiry"} {
		t.Run(boundary, func(t *testing.T) {
			documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
				f.db.SetMaxOpenConns(1)
				f.cfg.RequireAuth = true
				f.exec("INSERT INTO knowledge_domains(id,owner_id,team_id,dataset_id,name,description) VALUES('retained','owner','a','alpha','retained','private taxonomy summary')")
				f.app = fiber.New(fiber.Config{DisableStartupMessage: true})
				f.app.Get("/api/v1/datasets/:id/taxonomy", func(c *fiber.Ctx) error {
					deadline := time.Now().Add(250 * time.Millisecond)
					expiry := time.Now().Add(time.Hour).Unix()
					if boundary == "credential-expiry" {
						deadline = time.Now().Add(5 * time.Second)
						expiry = time.Now().Unix() + 2
					}
					parent, cancel := context.WithDeadline(context.Background(), deadline)
					defer cancel()
					c.SetUserContext(parent)
					c.Locals("user_id", "owner")
					c.Locals("tenant_id", "a")
					c.Locals("verified_jwt", jwtPayload{Sub: "owner", Exp: expiry})
					if err := taxonomyListHandler(f.cfg)(c); err != nil {
						return err
					}
					stream, ok := c.Response().BodyStream().(*fencedResponse)
					if !ok {
						return errors.New("listing missing retained stream")
					}
					defer stream.Close()
					if f.db.Stats().InUse != 1 {
						return errors.New("listing SQL released before Close")
					}
					if n, err := stream.Read(make([]byte, 1)); n != 1 || err != nil {
						return fmt.Errorf("partial listing: %d %v", n, err)
					}
					cancel()
					if n, err := stream.Read(make([]byte, 1)); n != 1 || err != nil {
						return fmt.Errorf("handler cancellation reached completed listing: %d %v", n, err)
					}
					select {
					case <-stream.ctx.Done():
					case <-time.After(3 * time.Second):
						return errors.New("listing observer failed to expire")
					}
					if n, err := stream.Read(make([]byte, 1)); n != 0 || !errors.Is(err, context.DeadlineExceeded) {
						return fmt.Errorf("expired listing still readable: %d %v", n, err)
					}
					if f.db.Stats().InUse != 1 {
						return errors.New("expiry released listing SQL before Close")
					}
					if err := stream.Close(); err != nil {
						return err
					}
					if err := stream.Close(); err != nil {
						return err
					}
					if f.db.Stats().InUse != 0 {
						return errors.New("listing actual Close leaked SQL")
					}
					return c.SendString("checked")
				})
				f.expect("owner", "GET", "/datasets/alpha/taxonomy", "", 200)
			})
		})
	}
}
func TestTaxonomyQueuedCredentialExpiry(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		f.cfg.RequireAuth = true
		held, err := f.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer held.Close()
		f.app = fiber.New(fiber.Config{DisableStartupMessage: true})
		expiry := time.Now().Unix() + 2
		f.app.Use(func(c *fiber.Ctx) error {
			c.Locals("user_id", "owner")
			c.Locals("tenant_id", "a")
			c.Locals("verified_jwt", jwtPayload{Sub: "owner", Exp: expiry})
			return c.Next()
		})
		RegisterTaxonomyAPI(f.app.Group("/api/v1"), f.cfg)
		// WaitCount proves the credential was live on entry and the handler queued for SQL.
		before := f.db.Stats().WaitCount
		type result struct {
			status int
			body   string
		}
		done := make(chan result, 1)
		go func() {
			response, err := f.app.Test(httptest.NewRequest("GET", "/api/v1/datasets/alpha/taxonomy", nil), -1)
			if err != nil {
				done <- result{0, err.Error()}
				return
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				done <- result{response.StatusCode, err.Error()}
				return
			}
			done <- result{response.StatusCode, string(body)}
		}()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
	waiting:
		for {
			if f.db.Stats().WaitCount > before {
				break waiting
			}
			select {
			case got := <-done:
				t.Fatalf("request did not queue: %+v", got)
			case <-ticker.C:
			case <-timer.C:
				held.Close()
				<-done
				t.Fatal("listing did not queue")
			}
		}
		select {
		case got := <-done:
			if got.status != 401 {
				t.Fatalf("expired queued credential: %+v", got)
			}
		case <-time.After(3 * time.Second):
			held.Close()
			<-done
			t.Fatal("expiry waited for held SQL")
		}
		if f.db.Stats().InUse != 1 {
			t.Fatal("failed acquisition touched held connection")
		}
		if err := held.Close(); err != nil {
			t.Fatal(err)
		}
		if f.db.Stats().InUse != 0 {
			t.Fatal("failed acquisition leaked SQL")
		}
	})
}
