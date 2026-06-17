package http

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestSettingsPreserveThemeAndLocale(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "settings.db")+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	SetDBProvider(DBSQLite)
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	cfg := APIConfig{DB: db}
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user_id", "user-1")
		return c.Next()
	})
	app.Get("/settings", settingsGetHandler(cfg))
	app.Put("/settings", settingsPutHandler(cfg))

	putSettings(t, app, map[string]string{"theme": "dark", "locale": "en"})
	req := httptest.NewRequest("GET", "/settings", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("GET settings: %v", err)
	}
	defer resp.Body.Close()
	var got SettingsDTO
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if got.Theme != "dark" || got.Locale != "en" {
		t.Fatalf("settings theme=%q locale=%q, want dark/en", got.Theme, got.Locale)
	}
}

func putSettings(t *testing.T, app *fiber.App, body any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest("PUT", "/settings", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("PUT settings: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("PUT settings status=%d, want 200", resp.StatusCode)
	}
}
