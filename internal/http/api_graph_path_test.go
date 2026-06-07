package http

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func graphPathTestApp(cfg APIConfig) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/graph/path", graphPathHandler(cfg))
	return app
}

// When Neo4j is unconfigured the handler should answer 503 immediately rather
// than try to dial; this protects deployments running without a graph store.
func TestGraphPath_Neo4jUnconfigured(t *testing.T) {
	app := graphPathTestApp(APIConfig{})
	req := httptest.NewRequest("GET", "/graph/path?from=a&to=b", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
}

// Required-arg validation runs before the Neo4j connect attempt, so this
// branch is reachable even without a configured graph store — but only by
// the empty-config 503 above. To exercise 400 we set a URL so the 503 guard
// passes; the connect attempt then fails after the arg check returns 400.
func TestGraphPath_MissingArgs(t *testing.T) {
	cfg := APIConfig{Neo4jCfg: GraphVisualizationConfig{Neo4jURL: "bolt://does-not-resolve.test:7687"}}
	app := graphPathTestApp(cfg)

	cases := []string{
		"/graph/path",
		"/graph/path?from=a",
		"/graph/path?to=b",
	}
	for _, url := range cases {
		req := httptest.NewRequest("GET", url, nil)
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("Test %q: %v", url, err)
		}
		if resp.StatusCode != fiber.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", url, resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		if parsed["error"] == nil {
			t.Errorf("%s: expected error field, got %s", url, string(body))
		}
	}
}

func TestParseIntDefault(t *testing.T) {
	if parseIntDefault("", 7) != 7 {
		t.Error("empty should yield default")
	}
	if parseIntDefault("nope", 5) != 5 {
		t.Error("garbage should yield default")
	}
	if parseIntDefault("42", 0) != 42 {
		t.Error("number should parse")
	}
}

func TestParseInt64Default(t *testing.T) {
	if parseInt64Default("", 9) != 9 {
		t.Error("empty should yield default")
	}
	if parseInt64Default("123456789012", 0) != 123456789012 {
		t.Error("large int should parse")
	}
}
