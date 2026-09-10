package http

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/memoryindex"
)

func TestMCPLatestDeleteMemoryAdvertisesAndUsesExactID(t *testing.T) {
	db := newMCPMemoryBehaviorDB(t)
	outbox, err := memoryindex.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES('selected','dup','value','','one')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES('control','selected','control','','two')`); err != nil {
		t.Fatal(err)
	}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	h := &mcpHandler{cfg: APIConfig{DB: db, MemoryIndexOutbox: outbox}, sessions: mcp.NewSessionStore()}
	app.Post(latestMCPPath, h.handleLatestRPC)

	list := latestMCPPost(t, app, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":`+latestMCPMeta()+`}`, latestMCPHeaders("tools/list"))
	var listed struct {
		Result struct {
			Tools []struct {
				Name        string         `json:"name"`
				InputSchema map[string]any `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.NewDecoder(list.Body).Decode(&listed); err != nil {
		list.Body.Close()
		t.Fatal(err)
	}
	list.Body.Close()
	advertised := false
	for _, tool := range listed.Result.Tools {
		if tool.Name != "delete_memory" {
			continue
		}
		properties, _ := tool.InputSchema["properties"].(map[string]any)
		advertised = properties["memory_id"] != nil
	}
	if !advertised {
		t.Fatal("HTTP tools/list did not advertise delete_memory.memory_id")
	}

	headers := latestMCPHeaders("tools/call")
	headers["Mcp-Name"] = "delete_memory"
	call := latestMCPPost(t, app, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"delete_memory","arguments":{"memory_id":"selected"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"memory-delete-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, headers)
	defer call.Body.Close()
	if call.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(call.Body)
		t.Fatalf("status=%d body=%s", call.StatusCode, body)
	}
	var payload struct {
		Result mcp.ToolResult `json:"result"`
	}
	if err := json.NewDecoder(call.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Result.IsError {
		t.Fatalf("tool result=%+v", payload.Result)
	}
	structured, _ := payload.Result.StructuredContent.(map[string]any)
	if structured["ok"] != true {
		t.Fatalf("structured result=%+v", structured)
	}
	var selected, control, jobs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories WHERE id='selected'`).Scan(&selected); err != nil || selected != 0 {
		t.Fatalf("selected=%d err=%v", selected, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories WHERE id='control'`).Scan(&control); err != nil || control != 1 {
		t.Fatalf("control=%d err=%v", control, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM memory_index_jobs WHERE memory_id='selected' AND operation='delete_vector'`).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("jobs=%d err=%v", jobs, err)
	}
}
