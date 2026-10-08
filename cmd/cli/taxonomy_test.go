package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTaxonomyImportRequest(t *testing.T) {
	const dataset = "Каталог /?+#"
	const seed = "\ufeff# Domains\r\n## auth\r\nОписание: sessions\r\n"
	file := filepath.Join(t.TempDir(), "seed with spaces.md")
	if err := os.WriteFile(file, []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []string{"", "version ё/2"} {
		t.Run(revision, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				expectedPath := "/api/v1/datasets/" + url.PathEscape(dataset) + "/taxonomy/import"
				if r.Method != http.MethodPost || r.URL.EscapedPath() != expectedPath || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer isolated-cli-token" || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("import request: %s %s headers=%v", r.Method, r.URL, r.Header)
				}
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				want := map[string]any{"seed": seed, "source_name": filepath.Base(file)}
				if revision != "" {
					want["source_revision"] = revision
				}
				if !reflect.DeepEqual(payload, want) {
					t.Errorf("payload=%v want=%v", payload, want)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"run_id":"run-import","domains":1,"collections":0,"documents":0,"created":1,"updated":0,"warnings":["shared alias"]}`))
			}))
			defer server.Close()
			args := []string{"taxonomy", "import", file, "--dataset", dataset}
			if revision != "" {
				args = append(args, "--revision="+revision)
			}
			out, err := runCLICommand(t, server.URL, args...)
			var report map[string]any
			if err != nil || json.Unmarshal([]byte(out), &report) != nil || report["run_id"] != "run-import" || !strings.Contains(out, "shared alias") || calls.Load() != 1 {
				t.Fatalf("import failed: %v calls=%d output=%s", err, calls.Load(), out)
			}
		})
	}
}

func TestTaxonomyListAndRemovalRequests(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		method   string
		payload  map[string]any
		response string
	}{
		{"list", []string{"list", "--dataset=dataset"}, http.MethodGet, nil, `{"domains":[]}`},
		{"domain", []string{"remove", "auth", "--dataset", "dataset"}, http.MethodDelete, map[string]any{"domain": "auth", "force": false}, `{"run_id":"remove-domain","removed":0}`},
		{"collection", []string{"remove", "auth", "--dataset=dataset", "--collection", "sessions", "--force"}, http.MethodDelete, map[string]any{"domain": "auth", "collection": "sessions", "force": true}, `{"run_id":"remove-collection","removed":3}`},
		{"document", []string{"remove", "auth", "--dataset", "dataset", "--collection=sessions", "--document", "Session policy ё"}, http.MethodDelete, map[string]any{"domain": "auth", "collection": "sessions", "document": "Session policy ё", "force": false}, `{"run_id":"remove-document","removed":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tc.method || r.URL.Path != "/api/v1/datasets/dataset/taxonomy" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer isolated-cli-token" {
					t.Errorf("request: %s %s headers=%v", r.Method, r.URL, r.Header)
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if tc.payload == nil {
					if len(raw) != 0 {
						t.Errorf("list sent body %s", raw)
					}
				} else {
					var payload map[string]any
					if r.Header.Get("Content-Type") != "application/json" || json.Unmarshal(raw, &payload) != nil || !reflect.DeepEqual(payload, tc.payload) {
						t.Errorf("remove payload=%s want=%v", raw, tc.payload)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer server.Close()
			out, err := runCLICommand(t, server.URL, append([]string{"taxonomy"}, tc.args...)...)
			var got, want map[string]any
			if err != nil || json.Unmarshal([]byte(out), &got) != nil || json.Unmarshal([]byte(tc.response), &want) != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("command failed: %v output=%s", err, out)
			}
		})
	}
}

func TestTaxonomyArgumentErrorsExitBeforeRequest(t *testing.T) {
	file := filepath.Join(t.TempDir(), "seed.md")
	if err := os.WriteFile(file, []byte("# Domains\n## auth\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{}, {"unknown"}, {"list"}, {"list", "--dataset"}, {"list", "--dataset="},
		{"list", "--dataset", "dataset", "extra"}, {"list", "--dataset=dataset", "--revision=v1"},
		{"list", "--dataset=dataset", "--owner=root"}, {"list", "--dataset=dataset", "--dataset=other"},
		{"import", "--dataset=dataset"}, {"import", file, "extra", "--dataset=dataset"},
		{"import", file, "--dataset=dataset", "--revision"}, {"import", file, "--dataset=dataset", "--force"},
		{"remove", "--dataset=dataset"}, {"remove", "auth", "--dataset=dataset", "--document=title"},
		{"remove", "auth", "--dataset=dataset", "--collection="}, {"remove", "auth", "--dataset=dataset", "--force=true"},
		{"remove", "auth", "--dataset=dataset", "--unknown=x"}, {"list", "-d", "dataset"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				http.Error(w, "unexpected", 500)
			}))
			defer server.Close()
			out, err := runCLICommand(t, server.URL, append([]string{"taxonomy"}, args...)...)
			if err == nil || calls.Load() != 0 || !strings.Contains(out, "error:") {
				t.Fatalf("invalid arguments accepted: %v calls=%d output=%s", err, calls.Load(), out)
			}
		})
	}
}

func TestTaxonomySeedBoundsAndReadFailures(t *testing.T) {
	for _, size := range []int{taxonomyMaxSeedBytes, taxonomyMaxSeedBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "seed.md")
			raw := []byte(strings.Repeat("x", size))
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			seed, err := readTaxonomySeed(file)
			if size == taxonomyMaxSeedBytes {
				if err != nil || seed != string(raw) {
					t.Fatalf("exact bound rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("oversized seed accepted")
			}
		})
	}
	for _, mode := range []string{"missing", "oversize", "invalid-utf8", "directory"} {
		t.Run(mode, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "seed.md")
			switch mode {
			case "oversize":
				if err := os.WriteFile(file, []byte(strings.Repeat("x", taxonomyMaxSeedBytes+1)), 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid-utf8":
				if err := os.WriteFile(file, []byte{0xff}, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(file, 0700); err != nil {
					t.Fatal(err)
				}
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "unexpected", 500) }))
			defer server.Close()
			out, err := runCLICommand(t, server.URL, "taxonomy", "import", file, "--dataset=dataset")
			if err == nil || calls.Load() != 0 {
				t.Fatalf("invalid file reached server: %v calls=%d output=%s", err, calls.Load(), out)
			}
		})
	}
}

func TestTaxonomyHTTPFailuresExitUnsuccessfully(t *testing.T) {
	file := filepath.Join(t.TempDir(), "seed.md")
	if err := os.WriteFile(file, []byte("# Domains\n## auth\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{302, 400, 401, 403, 409, 503} {
		for _, operation := range []string{"import", "list", "remove"} {
			t.Run(fmt.Sprintf("%s/%d", operation, status), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Location", "/unexpected-redirect")
					http.Error(w, "taxonomy rejected", status)
				}))
				defer server.Close()
				args := []string{"taxonomy", operation}
				if operation == "import" {
					args = append(args, file)
				}
				if operation == "remove" {
					args = append(args, "auth")
				}
				args = append(args, "--dataset=dataset")
				out, err := runCLICommand(t, server.URL, args...)
				if err == nil || calls.Load() != 1 || !strings.Contains(out, fmt.Sprintf("failed (%d)", status)) {
					t.Fatalf("HTTP failure hidden: %v calls=%d output=%s", err, calls.Load(), out)
				}
			})
		}
	}
}

func TestTaxonomyExistingGlobalConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/custom/datasets/dataset/taxonomy" || r.Header.Get("Authorization") != "Bearer explicit-token" {
			t.Errorf("global configuration ignored: %s %s headers=%v", r.Method, r.URL, r.Header)
		}
		_, _ = w.Write([]byte(`{"domains":[]}`))
	}))
	defer server.Close()
	out, err := runCLICommand(t, server.URL, "--url="+server.URL+"/custom", "--token=explicit-token", "taxonomy", "list", "--dataset", "dataset")
	if err != nil || !strings.Contains(out, "\"domains\": []") {
		t.Fatalf("global configuration request failed: %v output=%s", err, out)
	}
}
