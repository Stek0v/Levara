package structuredextract

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientExtract(t *testing.T) {
	var seen Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"extraction":{"invoice_number":"INV-1","total":42.5},"token_count":17}`))
	}))
	t.Cleanup(srv.Close)

	res, err := (Client{Endpoint: srv.URL}).Extract(context.Background(), []byte("pdf"), "invoice.pdf", `{"type":"object"}`, []int{1})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if seen.Filename != "invoice.pdf" {
		t.Fatalf("filename = %q", seen.Filename)
	}
	raw, _ := base64.StdEncoding.DecodeString(seen.ContentBase64)
	if string(raw) != "pdf" {
		t.Fatalf("content = %q, want pdf", raw)
	}
	if res.TokenCount != 17 {
		t.Fatalf("TokenCount = %d, want 17", res.TokenCount)
	}
	if !strings.Contains(string(res.Extraction), "INV-1") {
		t.Fatalf("extraction missing invoice_number: %s", res.Extraction)
	}
}

func TestProjectionMarkdown(t *testing.T) {
	md := ProjectionMarkdown("invoice.pdf", json.RawMessage(`{"total":42.5,"invoice_number":"INV-1","line_items":[{"amount":10,"description":"Compute"}]}`))
	for _, want := range []string{"# Structured extraction: invoice.pdf", "invoice_number: INV-1", "total: 42.5", "line_items", "description: Compute"} {
		if !strings.Contains(md, want) {
			t.Fatalf("projection missing %q:\n%s", want, md)
		}
	}
}
