package http

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// notebooksFlagProbe retains both historical run aliases and CRUD operations.
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

// TestNotebooksRoutesRetiredForAllFlags prevents old deployment settings from
// resurrecting notebook execution after the sunset.
func TestNotebooksRoutesRetiredForAllFlags(t *testing.T) {
	for _, value := range []string{"", "0", "false", "1", "true", "yes", "on", "enabled", " TRUE ", "maybe"} {
		t.Run("flag="+value, func(t *testing.T) {
			t.Setenv("LEVARA_NOTEBOOKS", value)
			app := fiber.New()
			RegisterAPI(app, APIConfig{StoragePath: t.TempDir()})
			for _, probe := range notebooksFlagProbe() {
				resp, err := app.Test(httptest.NewRequest(probe.method, probe.path, nil))
				if err != nil {
					t.Fatalf("%s %s: %v", probe.method, probe.path, err)
				}
				resp.Body.Close()
				if resp.StatusCode != fiber.StatusNotFound {
					t.Errorf("%s %s = %d, want 404", probe.method, probe.path, resp.StatusCode)
				}
			}
		})
	}
}
