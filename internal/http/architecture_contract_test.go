package http

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestRESTRouteInventoryMatchesRegisterAPI(t *testing.T) {
	app := fiber.New()
	RegisterAPI(app, APIConfig{})

	registered := map[string]bool{}
	for _, routes := range app.Stack() {
		for _, r := range routes {
			if r.Method == "HEAD" {
				continue
			}
			if r.Path == "/workspace" {
				continue // middleware mount from app.Use("/workspace", ...), not an endpoint.
			}
			registered[routeKey(r.Method, r.Path)] = true
		}
	}

	inventory := map[string]RouteSpec{}
	for _, r := range RESTRouteInventory() {
		if r.Group == "vector" {
			continue // legacy vector routes are registered in main.go.
		}
		inventory[routeKey(r.Method, r.Path)] = r
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
