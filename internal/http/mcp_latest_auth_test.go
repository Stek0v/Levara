package http

import (
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/stek0v/levara/pkg/mcp"
)

// The stateless 2026-07-28 transport must enforce RequireAuth exactly like the
// legacy session transport: tools/call and resources/* fail closed (404) for
// unauthenticated callers, while discovery and tools/list stay public for
// capability negotiation. Regression test for the auth-bypass finding (2026-09-03).

func latestMCPTestAppWithAuth(secret string) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	h := &mcpHandler{cfg: APIConfig{RequireAuth: true, JWTSecret: secret}, sessions: mcp.NewSessionStore()}
	app.Post(latestMCPPath, h.handleLatestRPC)
	app.Get(latestMCPPath, methodNotAllowed)
	app.Delete(latestMCPPath, methodNotAllowed)
	return app
}

// latestMCPMetaParams returns the _meta object as params content, e.g. to
// merge with extra params like "name".
func latestMCPMetaParams() string {
	return `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"contract-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`
}

func TestMCPLatestRequiresAuthForToolCall(t *testing.T) {
	const secret = "latest-transport-secret"
	app := latestMCPTestAppWithAuth(secret)

	headers := latestMCPHeaders("tools/call")
	headers["Mcp-Name"] = "levara_instructions"
	resp := latestMCPPost(t, app, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"levara_instructions","arguments":{},`+latestMCPMetaParams()+`}}`, headers)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unauthenticated tools/call status=%d, want 404", resp.StatusCode)
	}
}

func TestMCPLatestRequiresAuthForResources(t *testing.T) {
	const secret = "latest-transport-secret"
	app := latestMCPTestAppWithAuth(secret)

	headers := latestMCPHeaders("resources/list")
	resp := latestMCPPost(t, app, `{"jsonrpc":"2.0","id":1,"method":"resources/list","params":`+latestMCPMeta()+`}`, headers)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unauthenticated resources/list status=%d, want 404", resp.StatusCode)
	}

	readHeaders := latestMCPHeaders("resources/read")
	readHeaders["Mcp-Name"] = "levara://collections"
	resp = latestMCPPost(t, app, `{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"levara://collections",`+latestMCPMetaParams()+`}}`, readHeaders)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unauthenticated resources/read status=%d, want 404", resp.StatusCode)
	}
}

func TestMCPLatestAllowsAuthenticatedToolCall(t *testing.T) {
	const secret = "latest-transport-secret"
	app := latestMCPTestAppWithAuth(secret)

	headers := latestMCPHeaders("tools/call")
	headers["Mcp-Name"] = "levara_instructions"
	headers["Authorization"] = "Bearer " + createJWT("alice", "alice@example.com", secret)
	resp := latestMCPPost(t, app, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"levara_instructions","arguments":{},`+latestMCPMetaParams()+`}}`, headers)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated tools/call status=%d, want 200", resp.StatusCode)
	}
}

func TestMCPLatestDiscoveryAndToolsListStayPublic(t *testing.T) {
	const secret = "latest-transport-secret"
	app := latestMCPTestAppWithAuth(secret)

	for _, method := range []string{"server/discover", "tools/list"} {
		resp := latestMCPPost(t, app, `{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":`+latestMCPMeta()+`}`, latestMCPHeaders(method))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s unauthenticated status=%d, want 200 (must stay public)", method, resp.StatusCode)
		}
	}
}
