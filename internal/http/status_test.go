// status_test.go — A1 /status endpoint: memory reporting guarantees.
//
// The endpoint must report the real OS resident set (not MemStats.Sys),
// the GOMEMLIMIT-derived budget, and pressure consistent with it.
package http

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestMemoryPressureBands(t *testing.T) {
	const GiB = uint64(1) << 30
	cases := []struct {
		rss    uint64
		budget uint64
		want   string
	}{
		// Budget set: bands relative to budget (governor thresholds).
		{(19 * GiB) / 2, 10 * GiB, "critical"},
		{(17 * GiB) / 2, 10 * GiB, "elevated"},
		{6 * GiB, 10 * GiB, "normal"},
		{4 * GiB, 10 * GiB, "low"},
		// No budget: absolute bands for a 16 GB host.
		{13 * GiB, 0, "critical"},
		{9 * GiB, 0, "elevated"},
		{5 * GiB, 0, "normal"},
		{3 * GiB, 0, "low"},
	}
	for _, c := range cases {
		if got := memoryPressure(c.rss, c.budget); got != c.want {
			t.Errorf("memoryPressure(%d, %d) = %q, want %q", c.rss, c.budget, got, c.want)
		}
	}
}

func TestStatusEndpointReportsRealRSS(t *testing.T) {
	app := fiber.New()
	RegisterStatusAPI(app, APIConfig{Version: "test"})

	resp, err := app.Test(httptest.NewRequest("GET", "/status", nil), -1)
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("GET /status status=%d, want 200", resp.StatusCode)
	}

	var body struct {
		Memory struct {
			RSSBytes    uint64 `json:"rss_bytes"`
			BudgetBytes uint64 `json:"budget_bytes"`
			Pressure    string `json:"pressure"`
		} `json:"memory"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /status body: %v", err)
	}
	if body.Memory.RSSBytes == 0 {
		t.Fatal("memory.rss_bytes = 0 — must report the real OS resident set")
	}
	switch body.Memory.Pressure {
	case "low", "normal", "elevated", "critical":
	default:
		t.Fatalf("memory.pressure = %q, want a known band", body.Memory.Pressure)
	}
}

func TestStatusEndpointReportsBudgetFromGOMEMLIMIT(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "8GiB")
	app := fiber.New()
	RegisterStatusAPI(app, APIConfig{Version: "test"})

	resp, err := app.Test(httptest.NewRequest("GET", "/status", nil), -1)
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	var body struct {
		Memory struct {
			BudgetBytes uint64 `json:"budget_bytes"`
		} `json:"memory"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /status body: %v", err)
	}
	if want := uint64(8) << 30; body.Memory.BudgetBytes != want {
		t.Fatalf("memory.budget_bytes = %d, want %d (GOMEMLIMIT=8GiB)", body.Memory.BudgetBytes, want)
	}
}
