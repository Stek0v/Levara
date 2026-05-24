package http

import "github.com/stek0v/levara/internal/contract"

// RouteSpec is the stable REST route inventory used by architecture tests and
// generated docs. Keep this list in sync with RegisterAPI and main.go protected
// vector routes when adding or removing public API surface.
type RouteSpec = contract.RESTRoute

// APIStatus describes the architectural role of a REST endpoint.
type APIStatus = contract.Status

const (
	APICanonical  = contract.StatusCanonical
	APILegacy     = contract.StatusLegacy
	APIAlias      = contract.StatusAlias
	APIOps        = contract.StatusOps
	APIDeprecated = contract.StatusDeprecated
)

// RESTRouteInventory returns the canonical REST contract for /api/v1 routes
// registered by RegisterAPI plus the protected legacy vector compatibility
// endpoints registered in main.go.
func RESTRouteInventory() []RouteSpec {
	return []RouteSpec{
		{Method: "GET", Path: "/datasets", Status: APICanonical, Group: "datasets"},
		{Method: "POST", Path: "/datasets", Status: APICanonical, Group: "datasets"},
		{Method: "DELETE", Path: "/datasets/:id", Status: APICanonical, Group: "datasets"},
		{Method: "GET", Path: "/datasets/:id/data", Status: APICanonical, Group: "datasets"},
		{Method: "DELETE", Path: "/datasets/:id/data/:dataId", Status: APICanonical, Group: "datasets"},
		{Method: "GET", Path: "/datasets/:id/data/:dataId/raw", Status: APICanonical, Group: "datasets"},
		{Method: "GET", Path: "/datasets/:id/data/:dataId/raw/url", Status: APICanonical, Group: "datasets"},
		{Method: "GET", Path: "/datasets/status", Status: APICanonical, Group: "datasets"},
		{Method: "POST", Path: "/add", Status: APICanonical, Group: "ingest"},
		{Method: "POST", Path: "/ocr", Status: APICanonical, Group: "ingest"},
		{Method: "POST", Path: "/cognify", Status: APICanonical, Group: "cognify"},
		{Method: "GET", Path: "/cognify/:runId/status", Status: APICanonical, Group: "cognify"},
		{Method: "GET", Path: "/cognify/:runId/stream", Status: APICanonical, Group: "cognify"},
		{Method: "POST", Path: "/memify", Status: APICanonical, Group: "memify"},
		{Method: "GET", Path: "/memify/:runId/status", Status: APICanonical, Group: "memify"},
		{Method: "GET", Path: "/memify/:runId/stream", Status: APICanonical, Group: "memify"},
		{Method: "GET", Path: "/users/me", Status: APICanonical, Group: "users"},
		{Method: "GET", Path: "/users", Status: APICanonical, Group: "users"},
		{Method: "PUT", Path: "/users/me", Status: APICanonical, Group: "users"},
		{Method: "PUT", Path: "/users/me/password", Status: APICanonical, Group: "users"},
		{Method: "GET", Path: "/settings", Status: APICanonical, Group: "settings"},
		{Method: "PUT", Path: "/settings", Status: APICanonical, Group: "settings"},
		{Method: "GET", Path: "/collections", Status: APICanonical, Group: "collections"},
		{Method: "POST", Path: "/collections", Status: APICanonical, Group: "collections"},
		{Method: "DELETE", Path: "/collections/:name", Status: APICanonical, Group: "collections"},
		{Method: "GET", Path: "/collections/:name/meta", Status: APICanonical, Group: "collections"},
		{Method: "PUT", Path: "/collections/:name/meta", Status: APICanonical, Group: "collections"},
		{Method: "GET", Path: "/models/rerank", Status: APICanonical, Group: "models"},
		{Method: "POST", Path: "/reembed", Status: APICanonical, Group: "collections"},
		{Method: "GET", Path: "/reembed/:runId/status", Status: APICanonical, Group: "collections"},
		{Method: "POST", Path: "/search/dual", Status: APICanonical, Group: "search"},
		{Method: "POST", Path: "/prune/data", Status: APIOps, Group: "admin"},
		{Method: "POST", Path: "/prune/system", Status: APIOps, Group: "admin"},
		{Method: "PATCH", Path: "/datasets/:id/data/:dataId", Status: APICanonical, Group: "datasets"},
		{Method: "POST", Path: "/tenants", Status: APICanonical, Group: "tenants"},
		{Method: "GET", Path: "/tenants", Status: APICanonical, Group: "tenants"},
		{Method: "GET", Path: "/tenants/mine", Status: APICanonical, Group: "tenants"},
		{Method: "POST", Path: "/tenants/select", Status: APICanonical, Group: "tenants"},
		{Method: "POST", Path: "/tenants/:id/users", Status: APICanonical, Group: "tenants"},
		{Method: "DELETE", Path: "/tenants/:id/users/:uid", Status: APICanonical, Group: "tenants"},
		{Method: "POST", Path: "/acl", Status: APICanonical, Group: "rbac"},
		{Method: "GET", Path: "/acl/check", Status: APICanonical, Group: "rbac"},
		{Method: "POST", Path: "/interactions", Status: APICanonical, Group: "sessions"},
		{Method: "GET", Path: "/interactions", Status: APICanonical, Group: "sessions"},
		{Method: "GET", Path: "/interactions/:sessionId", Status: APICanonical, Group: "sessions"},
		{Method: "POST", Path: "/memories", Status: APICanonical, Group: "memory"},
		{Method: "GET", Path: "/memories", Status: APICanonical, Group: "memory"},
		{Method: "GET", Path: "/memories/stream", Status: APICanonical, Group: "memory"},
		{Method: "GET", Path: "/memories/:key", Status: APICanonical, Group: "memory"},
		{Method: "DELETE", Path: "/memories/:key", Status: APICanonical, Group: "memory"},
		{Method: "GET", Path: "/sync/manifest", Status: APICanonical, Group: "sync"},
		{Method: "GET", Path: "/sync/export/memories", Status: APICanonical, Group: "sync"},
		{Method: "POST", Path: "/sync/import/memories", Status: APICanonical, Group: "sync"},
		{Method: "GET", Path: "/sync/export/interactions", Status: APICanonical, Group: "sync"},
		{Method: "POST", Path: "/sync/import/interactions", Status: APICanonical, Group: "sync"},
		{Method: "GET", Path: "/sync/export/graph", Status: APICanonical, Group: "sync"},
		{Method: "POST", Path: "/sync/import/graph", Status: APICanonical, Group: "sync"},
		{Method: "GET", Path: "/sync/export/collection/:name", Status: APICanonical, Group: "sync"},
		{Method: "POST", Path: "/sync/import/collection", Status: APICanonical, Group: "sync"},
		{Method: "GET", Path: "/sync/import/collection/:runId/status", Status: APICanonical, Group: "sync"},
		{Method: "GET", Path: "/workspace/context", Status: APICanonical, Group: "workspace"},
		{Method: "POST", Path: "/workspace/access/check", Status: APICanonical, Group: "workspace"},
		{Method: "GET", Path: "/workspace/audit", Status: APICanonical, Group: "workspace"},
		{Method: "GET", Path: "/workspace/ops/status", Status: APICanonical, Group: "workspace"},
		{Method: "GET", Path: "/workspace/context/artifacts", Status: APICanonical, Group: "workspace"},
		{Method: "POST", Path: "/workspace/context/artifacts/reindex", Status: APICanonical, Group: "workspace"},
		{Method: "GET", Path: "/workspace/conflicts", Status: APICanonical, Group: "workspace"},
		{Method: "POST", Path: "/workspace/index", Status: APICanonical, Group: "workspace"},
		{Method: "POST", Path: "/workspace/delete", Status: APICanonical, Group: "workspace"},
		{Method: "POST", Path: "/workspace/gc", Status: APICanonical, Group: "workspace"},
		{Method: "GET", Path: "/workspace/manifest", Status: APICanonical, Group: "workspace"},
		{Method: "GET", Path: "/workspace/read", Status: APICanonical, Group: "workspace"},
		{Method: "POST", Path: "/workspace/write", Status: APICanonical, Group: "workspace"},
		{Method: "POST", Path: "/workspace/reindex", Status: APICanonical, Group: "workspace"},
		{Method: "POST", Path: "/workspace/reconcile", Status: APICanonical, Group: "workspace"},
		{Method: "GET", Path: "/workspace/jobs", Status: APICanonical, Group: "workspace"},
		{Method: "POST", Path: "/workspace/jobs/enqueue", Status: APICanonical, Group: "workspace"},
		{Method: "POST", Path: "/workspace/jobs/retry", Status: APICanonical, Group: "workspace"},
		{Method: "GET", Path: "/workspace/watch/status", Status: APICanonical, Group: "workspace"},
		{Method: "POST", Path: "/workspace/runs/start", Status: APICanonical, Group: "workspace"},
		{Method: "GET", Path: "/workspace/runs/get", Status: APICanonical, Group: "workspace"},
		{Method: "POST", Path: "/workspace/commit", Status: APICanonical, Group: "workspace"},
		{Method: "GET", Path: "/workspace/log", Status: APICanonical, Group: "workspace"},
		{Method: "POST", Path: "/workspace/revert", Status: APICanonical, Group: "workspace"},
		{Method: "POST", Path: "/feedback", Status: APICanonical, Group: "feedback"},
		{Method: "GET", Path: "/feedback/stats", Status: APICanonical, Group: "feedback"},
		{Method: "GET", Path: "/feedback", Status: APICanonical, Group: "feedback"},
		{Method: "POST", Path: "/ontologies", Status: APICanonical, Group: "ontology"},
		{Method: "GET", Path: "/ontologies", Status: APICanonical, Group: "ontology"},
		{Method: "DELETE", Path: "/ontologies/:id", Status: APICanonical, Group: "ontology"},
		{Method: "GET", Path: "/datasets/:id/shares", Status: APICanonical, Group: "rbac"},
		{Method: "POST", Path: "/datasets/:id/shares", Status: APICanonical, Group: "rbac"},
		{Method: "DELETE", Path: "/datasets/:id/shares/:shareId", Status: APICanonical, Group: "rbac"},
		{Method: "GET", Path: "/permissions/me", Status: APICanonical, Group: "rbac"},
		{Method: "GET", Path: "/notebooks", Status: APICanonical, Group: "notebooks"},
		{Method: "POST", Path: "/notebooks", Status: APICanonical, Group: "notebooks"},
		{Method: "GET", Path: "/notebooks/:id", Status: APICanonical, Group: "notebooks"},
		{Method: "PUT", Path: "/notebooks/:id", Status: APICanonical, Group: "notebooks"},
		{Method: "DELETE", Path: "/notebooks/:id", Status: APICanonical, Group: "notebooks"},
		{Method: "POST", Path: "/notebooks/:id/cells", Status: APICanonical, Group: "notebooks"},
		{Method: "PUT", Path: "/notebooks/:id/cells/:cellId", Status: APICanonical, Group: "notebooks"},
		{Method: "DELETE", Path: "/notebooks/:id/cells/:cellId", Status: APICanonical, Group: "notebooks"},
		{Method: "POST", Path: "/notebooks/:id/cells/:cellId/run", Status: APICanonical, Group: "notebooks"},
		{Method: "POST", Path: "/notebooks/:id/:cellId/run", Status: APIAlias, Group: "notebooks"},
		{Method: "POST", Path: "/search/text", Status: APICanonical, Group: "search"},
		{Method: "POST", Path: "/search/", Status: APIAlias, Group: "search"},
		{Method: "POST", Path: "/search", Status: APIAlias, Group: "search"},
		{Method: "GET", Path: "/heartbeats", Status: APIOps, Group: "ops"},
		{Method: "GET", Path: "/graph/path", Status: APICanonical, Group: "graph"},
		{Method: "POST", Path: "/insert", Status: APILegacy, Group: "vector"},
		{Method: "POST", Path: "/batch_insert", Status: APILegacy, Group: "vector"},
		{Method: "POST", Path: "/delete", Status: APILegacy, Group: "vector"},
	}
}
