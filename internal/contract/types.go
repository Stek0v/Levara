// Package contract holds the shared types produced by every inventory
// (REST, gRPC, MCP, DB schema). cmd/contract composes them into the
// canonical artefacts under docs/.
package contract

type Status string

const (
	StatusCanonical  Status = "canonical"
	StatusLegacy     Status = "legacy_compat"
	StatusAlias      Status = "alias"
	StatusOps        Status = "ops"
	StatusDeprecated Status = "deprecated"
)

type Contract struct {
	GeneratedAt string         `json:"generated_at"`
	GitRev      string         `json:"git_rev"`
	REST        []RESTRoute    `json:"rest"`
	GRPC        []GRPCMethod   `json:"grpc"`
	MCP         []MCPTool      `json:"mcp"`
	Schema      []SchemaObject `json:"schema"`
}

type RESTRoute struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Status Status `json:"status"`
	Group  string `json:"group,omitempty"`
	Note   string `json:"note,omitempty"`
}

type GRPCMethod struct {
	Service string `json:"service"`
	Method  string `json:"method"`
	Status  Status `json:"status"`
	Note    string `json:"note,omitempty"`
}

type MCPTool struct {
	Name   string `json:"name"`
	Group  string `json:"group,omitempty"`
	Status Status `json:"status"`
	Note   string `json:"note,omitempty"`
}

type SchemaObject struct {
	Provider string `json:"provider"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Note     string `json:"note,omitempty"`
}

type ByRESTRoute []RESTRoute

func (s ByRESTRoute) Len() int      { return len(s) }
func (s ByRESTRoute) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s ByRESTRoute) Less(i, j int) bool {
	if s[i].Path != s[j].Path {
		return s[i].Path < s[j].Path
	}
	return s[i].Method < s[j].Method
}

type ByGRPCMethod []GRPCMethod

func (s ByGRPCMethod) Len() int      { return len(s) }
func (s ByGRPCMethod) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s ByGRPCMethod) Less(i, j int) bool {
	if s[i].Service != s[j].Service {
		return s[i].Service < s[j].Service
	}
	return s[i].Method < s[j].Method
}

type ByMCPTool []MCPTool

func (s ByMCPTool) Len() int           { return len(s) }
func (s ByMCPTool) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s ByMCPTool) Less(i, j int) bool { return s[i].Name < s[j].Name }

type BySchemaObject []SchemaObject

func (s BySchemaObject) Len() int      { return len(s) }
func (s BySchemaObject) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s BySchemaObject) Less(i, j int) bool {
	if s[i].Provider != s[j].Provider {
		return s[i].Provider < s[j].Provider
	}
	if s[i].Kind != s[j].Kind {
		return s[i].Kind < s[j].Kind
	}
	return s[i].Name < s[j].Name
}
