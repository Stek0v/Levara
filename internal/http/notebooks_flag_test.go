package http

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// notebooksFlagProbe covers every route the flag gates, so a future route
// added outside the guard is caught here.
func notebooksFlagProbe() []struct{ method, path string } {
	return []struct{ method, path string }{
		{"GET", "/notebooks"},
		{"POST", "/notebooks"},
		{"GET", "/notebooks/nb1"},
		{"PUT", "/notebooks/nb1"},
		{"DELETE", "/notebooks/nb1"},
		{"POST", "/notebooks/nb1/cells"},
		{"PUT", "/notebooks/nb1/cells/c1"},
		{"DELETE", "/notebooks/nb1/cells/c1"},
		{"POST", "/notebooks/nb1/cells/c1/run"},
		{"POST", "/notebooks/nb1/c1/run"},
	}
}

// TestNotebooksRoutesDisabledByDefault pins the Р2 decision: the notebooks
// surface is absent (404) unless explicitly re-enabled via LEVARA_NOTEBOOKS.
func TestNotebooksRoutesDisabledByDefault(t *testing.T) {
	t.Setenv("LEVARA_NOTEBOOKS", "")
	app := fiber.New()
	RegisterAPI(app, APIConfig{StoragePath: t.TempDir()})

	for _, probe := range notebooksFlagProbe() {
		resp, err := app.Test(httptest.NewRequest(probe.method, probe.path, nil))
		if err != nil {
			t.Fatalf("%s %s: %v", probe.method, probe.path, err)
		}
		if resp.StatusCode != fiber.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", probe.method, probe.path, resp.StatusCode)
		}
	}
}

// TestNotebooksInvalidFlagValueIsOff: an unrecognized value must not
// accidentally re-enable the cut surface (warning + off, never fail startup).
func TestNotebooksInvalidFlagValueIsOff(t *testing.T) {
	t.Setenv("LEVARA_NOTEBOOKS", "maybe")
	app := fiber.New()
	RegisterAPI(app, APIConfig{StoragePath: t.TempDir()})

	resp, err := app.Test(httptest.NewRequest("GET", "/notebooks", nil))
	if err != nil {
		t.Fatalf("GET /notebooks: %v", err)
	}
	if resp.StatusCode != fiber.StatusNotFound {
		t.Errorf("GET /notebooks = %d, want 404 for invalid flag value", resp.StatusCode)
	}
}

// TestNotebooksRoutesEnabledByFlag: the escape hatch still works until the
// Ф2 removal. DB-less handlers return their zero-value success shapes.
func TestNotebooksRoutesEnabledByFlag(t *testing.T) {
	t.Setenv("LEVARA_NOTEBOOKS", "1")
	app := fiber.New()
	RegisterAPI(app, APIConfig{StoragePath: t.TempDir()})

	checks := []struct {
		method, path string
		want         int
	}{
		{"GET", "/notebooks", fiber.StatusOK},
		{"POST", "/notebooks", fiber.StatusCreated},
		{"DELETE", "/notebooks/nb1", fiber.StatusOK},
	}
	for _, check := range checks {
		resp, err := app.Test(httptest.NewRequest(check.method, check.path, nil))
		if err != nil {
			t.Fatalf("%s %s: %v", check.method, check.path, err)
		}
		if resp.StatusCode != check.want {
			t.Errorf("%s %s = %d, want %d", check.method, check.path, resp.StatusCode, check.want)
		}
	}
}
