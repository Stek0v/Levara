package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

func TestMCPResourceMemoriesAreOwnerScopedAndActive(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		for _, row := range []struct{ id, key, owner, superseded, validUntil string }{
			{"own", "own", "owner", "", ""},
			{"shared", "shared", "", "", ""},
			{"foreign", "foreign", "foreign", "", ""},
			{"superseded", "superseded", "owner", "replacement", ""},
			{"expired", "expired", "owner", "", "2026-10-08T00:00:00Z"},
		} {
			f.exec(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,superseded_by,valid_until) VALUES($1,$2,'value','project',$3,'levara',$4,$5)`, row.id, row.key, row.owner, row.superseded, nullableString(row.validUntil))
		}
		h := &mcpHandler{cfg: f.cfg}
		var got []map[string]string
		if err := json.Unmarshal([]byte(h.resourceMemories(context.Background(), "owner", "project", "levara")), &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0]["key"] != "shared" && got[1]["key"] != "shared" {
			t.Fatalf("resources=%+v", got)
		}
		seen := map[string]bool{}
		for _, row := range got {
			seen[row["key"]] = true
		}
		if !seen["own"] || !seen["shared"] || seen["foreign"] || seen["superseded"] || seen["expired"] {
			t.Fatalf("resources=%+v", got)
		}
	})
}

func TestMCPResourceReadTransportUsesVerifiedOwner(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		for _, row := range []struct{ id, owner, validUntil string }{
			{"own", "owner", ""},
			{"shared", "", ""},
			{"foreign", "foreign", ""},
			{"retired", "owner", "2026-10-08T00:00:00Z"},
		} {
			f.exec(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,valid_until) VALUES($1,$1,'value','project',$2,'levara',$3)`, row.id, row.owner, nullableString(row.validUntil))
		}
		f.cfg.RequireAuth = true
		h := &mcpHandler{cfg: f.cfg}
		app := fiber.New()
		app.Post("/mcp", func(c *fiber.Ctx) error {
			c.Locals("verified_jwt", jwtPayload{Sub: "owner", Exp: time.Now().Add(time.Hour).Unix()})
			var req jsonRPCRequest
			if err := c.BodyParser(&req); err != nil {
				return err
			}
			return h.handleResourcesRead(c, req)
		})
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"levara://memories/project/levara"}}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.Contains(text, `\"key\":\"own\"`) || !strings.Contains(text, `\"key\":\"shared\"`) || strings.Contains(text, `\"key\":\"foreign\"`) || strings.Contains(text, `\"key\":\"retired\"`) {
			t.Fatalf("response leaked wrong memory scope: %s", text)
		}
	})
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
