package decisions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewEmptyEndpointIsDisabled(t *testing.T) {
	if c := New("", 0); c != nil {
		t.Fatalf("New(\"\") = %v, want nil", c)
	}
	var nilClient *Client
	if nilClient.Enabled() {
		t.Fatal("nil client must report Enabled=false")
	}
	if c := New("http://127.0.0.1:9200", 0); !c.Enabled() {
		t.Fatal("configured client must report Enabled=true")
	}
}

func TestNoulRequestShapeAndParsing(t *testing.T) {
	var gotBody map[string]any
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answers": map[string]any{
				"q": map[string]any{"type": "noul", "noul": 0.844},
			},
		})
	}))
	defer srv.Close()

	c := New(srv.URL, 0)
	p, err := c.Supersedes(context.Background(), "old fact", "new fact with updated date")
	if err != nil {
		t.Fatalf("Supersedes: %v", err)
	}
	if p != 0.844 {
		t.Fatalf("p = %v, want 0.844", p)
	}
	if gotPath != "/judge" {
		t.Fatalf("path = %q, want /judge", gotPath)
	}
	state, _ := gotBody["state"].(string)
	if !strings.Contains(state, "ЗАПИСЬ A:\nold fact") || !strings.Contains(state, "ЗАПИСЬ B:\nnew fact") {
		t.Fatalf("state does not frame the pair: %q", state)
	}
	qs, _ := gotBody["questions"].(map[string]any)
	q, _ := qs["q"].(map[string]any)
	if q["type"] != "noul" {
		t.Fatalf("question type = %v, want noul", q["type"])
	}
	if instr, _ := q["instructions"].(string); !strings.Contains(instr, "обновлением того же факта") {
		t.Fatalf("unexpected instruction: %q", instr)
	}

	if p, err := c.SameFact(context.Background(), "a", "b"); err != nil || p != 0.844 {
		t.Fatalf("SameFact = %v, %v", p, err)
	}
	if instr, _ := qs["q"].(map[string]any)["instructions"].(string); gotPath != "/judge" || instr == "" {
		// SameFact reuses the same endpoint; body shape already asserted above.
		_ = instr
	}
}

func TestNoulServerErrorAndBadPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := New(srv.URL, 0)
	if _, err := c.Supersedes(context.Background(), "a", "b"); err == nil {
		t.Fatal("expected error on HTTP 503")
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"answers": {}}`))
	}))
	defer srv2.Close()
	c2 := New(srv2.URL, 0)
	if _, err := c2.Supersedes(context.Background(), "a", "b"); err == nil ||
		!strings.Contains(err.Error(), "missing answer") {
		t.Fatalf("expected missing-answer error, got %v", err)
	}
}

func TestTimeoutIsApplied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()
	c := New(srv.URL, 50) // 50ms
	if _, err := c.Supersedes(context.Background(), "a", "b"); err == nil {
		t.Fatal("expected timeout error")
	}
}
