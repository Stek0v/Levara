package fetch

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// T-9b smoke for URL classifiers + FetchURL.

func TestIsURL(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"https://example.com", true},
		{"http://localhost:8080/path", true},
		{"  https://leading-space.com  ", true},
		{"ftp://example.com", false},
		{"example.com", false},
		{"", false},
		{"file:///etc/passwd", false},
	}
	for _, c := range cases {
		if got := IsURL(c.s); got != c.want {
			t.Errorf("IsURL(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}

func TestIsGitHubURL(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"https://github.com/foo/bar", true},
		{"http://github.com/x/y", true},
		{"https://raw.githubusercontent.com/x/y/main/r.md", false},
		{"https://gitlab.com/foo/bar", false},
		{"https://example.com", false},
	}
	for _, c := range cases {
		if got := IsGitHubURL(c.s); got != c.want {
			t.Errorf("IsGitHubURL(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}

func TestFetchURL_PlainText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "hello plain world")
	}))
	defer srv.Close()

	got, err := fetchURLWithClient(srv.URL, srv.Client(), true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello plain world" {
		t.Errorf("got %q", got)
	}
}

func TestFetchURL_HTMLExtractsText(t *testing.T) {
	html := `<html><head><title>t</title><script>alert(1)</script></head>
		<body><h1>Title</h1><p>First para.</p><p>Second para.</p>
		<nav>SHOULD BE STRIPPED</nav></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, html)
	}))
	defer srv.Close()

	got, err := fetchURLWithClient(srv.URL, srv.Client(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "# Title") {
		t.Errorf("missing markdown header: %q", got)
	}
	if !strings.Contains(got, "First para.") || !strings.Contains(got, "Second para.") {
		t.Errorf("missing paragraphs: %q", got)
	}
	if strings.Contains(got, "alert(1)") {
		t.Errorf("script not stripped: %q", got)
	}
	if strings.Contains(got, "SHOULD BE STRIPPED") {
		t.Errorf("nav not stripped: %q", got)
	}
}

func TestFetchURL_Non200Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := fetchURLWithClient(srv.URL, srv.Client(), true)
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err should mention status: %v", err)
	}
}

func TestFetchURL_JSONPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"k":"v"}`)
	}))
	defer srv.Close()

	got, err := fetchURLWithClient(srv.URL, srv.Client(), true)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"k":"v"}` {
		t.Errorf("JSON should be passed through verbatim, got %q", got)
	}
}

func TestFetchMultipleURLs_ConcurrentNoLeak(t *testing.T) {
	// Returns map with successful fetches; failures (here: bad URLs) drop out.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, r.URL.Path)
	}))
	defer srv.Close()

	urls := []string{srv.URL + "/a", srv.URL + "/b", srv.URL + "/c"}
	got := fetchMultipleURLs(urls, func(rawURL string) (string, error) {
		return fetchURLWithClient(rawURL, srv.Client(), true)
	})
	if len(got) != 3 {
		t.Errorf("got %d results, want 3", len(got))
	}
	for _, u := range urls {
		if got[u] == "" {
			t.Errorf("missing result for %s", u)
		}
	}
}

func TestFetchURLRejectsPrivateAddresses(t *testing.T) {
	for _, rawURL := range []string{
		"http://127.0.0.1/secret",
		"http://[::1]/secret",
		"http://169.254.169.254/latest/meta-data",
		"http://10.0.0.1/internal",
		"file:///etc/passwd",
	} {
		if _, err := FetchURL(rawURL); err == nil {
			t.Fatalf("FetchURL(%q) succeeded, want SSRF rejection", rawURL)
		}
	}
}

func TestFetchURLRejectsOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", fmt.Sprint(maxURLResponseBytes+1))
		_, _ = io.WriteString(w, strings.Repeat("x", int(maxURLResponseBytes+1)))
	}))
	defer srv.Close()

	if _, err := fetchURLWithClient(srv.URL, srv.Client(), true); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized response err=%v, want size-limit error", err)
	}
}
