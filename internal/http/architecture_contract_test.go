package http

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestRESTRouteInventoryMatchesRegisterAPI(t *testing.T) {
	// Retired notebook routes remain absent even with an old enabled setting.
	t.Setenv("LEVARA_NOTEBOOKS", "1")
	app := fiber.New()
	db, err := sql.Open("sqlite3", t.TempDir()+"/routes.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	RegisterAPI(app, APIConfig{DB: db, StoragePath: t.TempDir(), WorkspacePath: t.TempDir()})

	registered := map[string]bool{}
	for _, routes := range app.Stack() {
		for _, r := range routes {
			if r.Method == "HEAD" {
				continue
			}
			if r.Path == "/workspace" || r.Path == "/sync" {
				continue // app.Use middleware mounts are not endpoints.
			}
			key := routeKey(r.Method, r.Path)
			if registered[key] {
				t.Fatalf("runtime route registered twice: %s", key)
			}
			registered[key] = true
		}
	}

	inventory := map[string]RouteSpec{}
	for _, r := range RESTRouteInventory() {
		if r.Group == "vector" {
			continue // legacy vector routes are registered in main.go.
		}
		key := routeKey(r.Method, r.Path)
		if _, exists := inventory[key]; exists {
			t.Fatalf("RESTRouteInventory contains duplicate: %s", key)
		}
		inventory[key] = r
	}

	for key := range registered {
		if _, ok := inventory[key]; !ok {
			t.Fatalf("registered route missing from RESTRouteInventory: %s", key)
		}
	}
	for key, spec := range inventory {
		if spec.Status == APILegacy {
			continue
		}
		if !registered[key] {
			t.Fatalf("RESTRouteInventory route not registered by RegisterAPI: %s", key)
		}
	}
}

func TestRESTRouteInventoryClassifiesLegacyVectorCompatibility(t *testing.T) {
	for _, want := range []string{
		routeKey("POST", "/insert"),
		routeKey("POST", "/batch_insert"),
		routeKey("POST", "/search"),
		routeKey("POST", "/delete"),
	} {
		found := false
		for _, r := range RESTRouteInventory() {
			if routeKey(r.Method, r.Path) == want {
				found = true
				if r.Status != APILegacy {
					t.Fatalf("%s status = %s, want %s", want, r.Status, APILegacy)
				}
			}
		}
		if !found {
			t.Fatalf("legacy vector route missing from inventory: %s", want)
		}
	}
}

func TestSchemaInventoryCoversCoreTables(t *testing.T) {
	byProvider := map[DBProvider]map[string]bool{}
	for _, obj := range SchemaInventory() {
		if obj.Kind != SchemaTable {
			continue
		}
		p := DBProvider(obj.Provider)
		if byProvider[p] == nil {
			byProvider[p] = map[string]bool{}
		}
		byProvider[p][obj.Name] = true
	}

	coreTables := []string{
		"users",
		"datasets",
		"data",
		"dataset_data",
		"graph_nodes",
		"graph_edges",
		"knowledge_domains",
		"knowledge_collections",
		"knowledge_documents",
		"memories",
		"interactions",
		"search_feedback",
	}
	for _, provider := range []DBProvider{DBPostgres, DBSQLite} {
		for _, table := range coreTables {
			if !byProvider[provider][table] {
				t.Fatalf("schema inventory missing %s table %q", provider, table)
			}
		}
	}
}

func routeKey(method, path string) string {
	return fmt.Sprintf("%s %s", strings.ToUpper(method), path)
}
