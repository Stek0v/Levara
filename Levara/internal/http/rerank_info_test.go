package http

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestRerankInfo_Disabled(t *testing.T) {
	app := fiber.New()
	cfg := APIConfig{RerankEndpoint: "", RerankModel: "", RerankBudgetMs: 1500}
	app.Get("/api/v1/models/rerank", rerankInfoHandler(cfg))

	req := httptest.NewRequest("GET", "/api/v1/models/rerank", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["enabled"] != false {
		t.Fatalf("enabled=%v want false", got["enabled"])
	}
	if got["model"] != "" {
		t.Fatalf("model=%v want empty", got["model"])
	}
}

func TestRerankInfo_Enabled(t *testing.T) {
	app := fiber.New()
	cfg := APIConfig{
		RerankEndpoint: "http://sidecar:9100/rerank",
		RerankModel:    "mmini-L12-int8",
		RerankBudgetMs: 1500,
	}
	app.Get("/api/v1/models/rerank", rerankInfoHandler(cfg))

	req := httptest.NewRequest("GET", "/api/v1/models/rerank", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["enabled"] != true {
		t.Fatalf("enabled=%v want true", got["enabled"])
	}
	if got["model"] != "mmini-L12-int8" {
		t.Fatalf("model=%v", got["model"])
	}
	if got["endpoint"] != "http://sidecar:9100/rerank" {
		t.Fatalf("endpoint=%v", got["endpoint"])
	}
	if got["budget_ms"].(float64) != 1500 {
		t.Fatalf("budget_ms=%v", got["budget_ms"])
	}
}
