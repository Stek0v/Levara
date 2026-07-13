package main

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestHealthDetailsHandlesUnavailablePostgresAndDisabledGRPC(t *testing.T) {
	t.Setenv("LLM_ENDPOINT", "")
	t.Setenv("WHISPER_ENDPOINT", "")
	t.Setenv("VISION_ENDPOINT", "")

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	registerHealthDetails(app, healthDeps{dbProvider: "postgres", pgDB: nil, grpcPort: 0})

	resp, err := app.Test(httptest.NewRequest("GET", "/health/details", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Services map[string]map[string]any `json:"services"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if got := body.Services["postgres"]["status"]; got != "unavailable" {
		t.Fatalf("postgres status=%v, want unavailable", got)
	}
	if got := body.Services["grpc"]["status"]; got != "disabled" {
		t.Fatalf("grpc status=%v, want disabled", got)
	}
	if got := body.Services["collections"]["status"]; got != "unavailable" {
		t.Fatalf("collections status=%v, want unavailable", got)
	}
}

func TestHealthDetailsDoesNotReportSQLiteAsPostgres(t *testing.T) {
	t.Setenv("LLM_ENDPOINT", "")
	t.Setenv("WHISPER_ENDPOINT", "")
	db, err := sql.Open("sqlite3", "file:"+t.TempDir()+"/health.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	registerHealthDetails(app, healthDeps{dbProvider: "sqlite", pgDB: db})
	resp, err := app.Test(httptest.NewRequest("GET", "/health/details", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Services map[string]map[string]any `json:"services"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if got := body.Services["database"]["status"]; got != "connected" {
		t.Fatalf("database status=%v, want connected", got)
	}
	if got := body.Services["postgres"]["status"]; got != "not_configured" {
		t.Fatalf("postgres status=%v, want not_configured", got)
	}
}
