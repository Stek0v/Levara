package main

import (
	"sort"

	"github.com/stek0v/levara/internal/contract"
	grpcsvc "github.com/stek0v/levara/internal/grpc"
	httpsvc "github.com/stek0v/levara/internal/http"
	"github.com/stek0v/levara/pkg/mcp"
)

func collect(gitRev, generatedAt string) contract.Contract {
	rest := append([]contract.RESTRoute(nil), httpsvc.RESTRouteInventory()...)
	sort.Sort(contract.ByRESTRoute(rest))

	schema := append([]contract.SchemaObject(nil), httpsvc.SchemaInventory()...)
	sort.Sort(contract.BySchemaObject(schema))

	grpc := grpcsvc.GRPCInventory()
	tools := mcp.MCPInventory()

	return contract.Contract{
		GeneratedAt: generatedAt,
		GitRev:      gitRev,
		REST:        rest,
		GRPC:        grpc,
		MCP:         tools,
		Schema:      schema,
	}
}
