package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise main/fatalf in a child so failures must produce an actual nonzero exit.
func TestCLICommandProcess(t *testing.T) {
	if os.Getenv("LEVARA_CLI_TEST_PROCESS") != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("LEVARA_CLI_TEST_ARGS")), &args); err != nil {
		t.Fatal(err)
	}
	os.Args = append([]string{"levara"}, args...)
	main()
	os.Exit(0)
}

func runCLICommand(t *testing.T, server string, args ...string) (string, error) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestCLICommandProcess$")
	cmd.Env = append(os.Environ(), "LEVARA_CLI_TEST_PROCESS=1", "LEVARA_CLI_TEST_ARGS="+string(encoded), "LEVARA_URL="+server+"/api/v1", "LEVARA_TOKEN=isolated-cli-token")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("CLI did not terminate: %v\n%s", ctx.Err(), out)
	}
	return string(out), err
}

func writeAddSuccess(w http.ResponseWriter, dataset string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "items": 1, "dataset_name": dataset, "dataset_id": "dataset-123"})
}

func TestCLIAddTextTargetsDatasetJSON(t *testing.T) {
	const dataset = "Отдел & audit +/#?"
	for _, input := range []string{"{\"report\":\"literal JSON\"}\nSecond line", "https://example.invalid/a?x=1&y=2"} {
		t.Run(input, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/add" || r.Header.Get("Authorization") != "Bearer isolated-cli-token" || r.Header.Get("Content-Type") != "application/json" || r.URL.RawQuery != "" {
					t.Errorf("unexpected request: %s %s headers=%v", r.Method, r.URL, r.Header)
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["data"] != input || body["dataset_name"] != dataset {
					t.Errorf("body=%v decode=%v", body, err)
					http.Error(w, "invalid ingestion payload", 400)
					return
				}
				writeAddSuccess(w, dataset)
			}))
			defer server.Close()
			out, err := runCLICommand(t, server.URL, "add", input, "--dataset="+dataset)
			if err != nil || !strings.Contains(out, dataset) {
				t.Fatalf("add failed: %v\n%s", err, out)
			}
		})
	}
}

func TestCLIAddFilePreservesBytesAndDataset(t *testing.T) {
	contents := []byte("# Файл\n\nExact bytes \x00 and newline\n")
	file := filepath.Join(t.TempDir(), "report with spaces.md")
	if err := os.WriteFile(file, contents, 0600); err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			const dataset = "Research & reports/2026"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer isolated-cli-token" || r.URL.Path != "/api/v1/add" {
					t.Errorf("bad request auth/path: %v", r)
				}
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
					return
				}
				defer r.MultipartForm.RemoveAll()
				if got := r.FormValue("datasetName"); got != dataset {
					t.Errorf("dataset=%q, want %q", got, dataset)
				}
				f, header, err := r.FormFile("data")
				if err != nil {
					t.Error(err)
					return
				}
				defer f.Close()
				got, err := io.ReadAll(f)
				if err != nil || !bytes.Equal(got, contents) || header.Filename != filepath.Base(file) {
					t.Errorf("file=%q bytes=%q error=%v", header.Filename, got, err)
				}
				writeAddSuccess(w, dataset)
			}))
			defer server.Close()
			input := file
			if explicit {
				input = "--file=" + file
			}
			out, err := runCLICommand(t, server.URL, "add", input, "--dataset="+dataset)
			if err != nil || !strings.Contains(out, "report with spaces.md") {
				t.Fatalf("file add failed: %v\n%s", err, out)
			}
		})
	}
}

func TestCLIAddRejectsFailedResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		truncated  bool
	}{
		{"server error", `{"detail":"failed extraction"}`, 422, false},
		{"redirect", `{"status":"ok","items":1,"dataset_name":"default","dataset_id":"id"}`, 302, false},
		{"invalid JSON", `<html>proxy login</html>`, 200, false},
		{"missing fields", `{}`, 200, false},
		{"application error", `{"status":"error","items":1,"dataset_name":"default","dataset_id":"id"}`, 200, false},
		{"wrong dataset", `{"status":"ok","items":1,"dataset_name":"different","dataset_id":"id"}`, 200, false},
		{"empty dataset ID", `{"status":"ok","items":1,"dataset_name":"default","dataset_id":""}`, 200, false},
		{"zero items", `{"status":"ok","items":0,"dataset_name":"default","dataset_id":"id"}`, 200, false},
		{"truncated body", `{"status":"ok","items":1,"dataset_name":"default","dataset_id":"id"}`, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.truncated {
					w.Header().Set("Content-Length", "1000")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			out, err := runCLICommand(t, server.URL, "add", "plain input")
			if err == nil || strings.Contains(out, "OK") {
				t.Fatalf("failure reported success: err=%v output=%q", err, out)
			}
		})
	}
}

func TestCLIAddRejectsInvalidFilesBeforeRequest(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); writeAddSuccess(w, "default") }))
	defer server.Close()
	for _, input := range []string{dir, "--file=" + filepath.Join(dir, "missing.pdf"), "--file="} {
		out, err := runCLICommand(t, server.URL, "add", input)
		if err == nil {
			t.Errorf("invalid file succeeded: %s %q", input, out)
		}
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("invalid files made %d requests", got)
	}
}

func TestCLICognifyResolvesDatasetAndWaits(t *testing.T) {
	for _, input := range []string{"reports & notes", "dataset-id"} {
		t.Run(input, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer isolated-cli-token" {
					t.Error("missing auth")
				}
				switch r.URL.Path {
				case "/api/v1/datasets":
					_, _ = io.WriteString(w, `[{"id":"dataset-id","name":"reports & notes"}]`)
				case "/api/v1/cognify":
					var body struct {
						Datasets   []string `json:"datasets"`
						Collection string   `json:"collection"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Datasets) != 1 || body.Datasets[0] != "dataset-id" || body.Collection != "research" {
						t.Errorf("bad cognify payload: %+v error=%v", body, err)
						http.Error(w, "expected dataset ID", 400)
						return
					}
					_, _ = io.WriteString(w, `{"pipeline_run_id":"run-id"}`)
				case "/api/v1/cognify/run-id/status":
					_, _ = io.WriteString(w, `{"status":"COMPLETED","chunks_created":2}`)
				default:
					t.Errorf("unexpected path: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			out, err := runCLICommand(t, server.URL, "cognify", "--dataset="+input, "--collection=research", "--wait")
			if err != nil || !strings.Contains(out, "COMPLETED") {
				t.Fatalf("cognify failed: %v\n%s", err, out)
			}
		})
	}
}

func TestCLIAddRejectsRedirectReplay(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		writeAddSuccess(w, "default")
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/add", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	out, err := runCLICommand(t, server.URL, "add", "private test document")
	if err == nil || redirected.Load() != 0 || !strings.Contains(out, "307") {
		t.Fatalf("redirect replayed: calls=%d err=%v output=%q", redirected.Load(), err, out)
	}
}

func TestCLIAddInvalidRequestAndArguments(t *testing.T) {
	out, err := runCLICommand(t, "http://[invalid", "add", "plain text")
	if err == nil || !strings.Contains(out, "create request") || strings.Contains(out, "panic:") {
		t.Fatalf("invalid URL not reported cleanly: %v %q", err, out)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeAddSuccess(w, "default")
	}))
	defer server.Close()
	for _, args := range [][]string{
		{"add", "  "}, {"add", "input", "--dataset="}, {"add", "first", "silently-lost-second"},
		{"add", "input", "--unknown=ignored"}, {"add", "input", "--file=/missing"},
	} {
		if out, err := runCLICommand(t, server.URL, args...); err == nil {
			t.Errorf("invalid arguments succeeded: %v %q", args, out)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid arguments issued %d requests", calls.Load())
	}
}

func TestCLICognifyRejectsUnresolvedDataset(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"not found", `[]`, 200},
		{"ambiguous", `[{"id":"one","name":"reports"},{"id":"two","name":"reports"}]`, 200},
		{"missing id", `[{"name":"reports"}]`, 200},
		{"malformed", `{"detail":"login page"}`, 200},
		{"no access", `{"detail":"forbidden"}`, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var started atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/cognify" {
					started.Add(1)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			out, err := runCLICommand(t, server.URL, "cognify", "--dataset=reports")
			if err == nil || started.Load() != 0 || strings.Contains(out, "Pipeline started") {
				t.Fatalf("unresolved dataset started: %v calls=%d output=%q", err, started.Load(), out)
			}
		})
	}
}

func TestCLICognifyDatasetIDTakesPrecedenceOverName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/datasets" {
			_, _ = io.WriteString(w, `[{"id":"other","name":"dataset-id"},{"id":"dataset-id","name":"reports"}]`)
			return
		}
		var body struct {
			Datasets []string `json:"datasets"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Datasets) != 1 || body.Datasets[0] != "dataset-id" {
			t.Errorf("ID changed meaning: %+v %v", body, err)
		}
		_, _ = io.WriteString(w, `{"pipeline_run_id":"run-id"}`)
	}))
	defer server.Close()
	if out, err := runCLICommand(t, server.URL, "cognify", "--dataset=dataset-id"); err != nil {
		t.Fatalf("%v %q", err, out)
	}
}

func TestCLICognifyRejectsInvalidRunAndStatus(t *testing.T) {
	for _, tc := range []struct{ name, start, poll string }{
		{"invalid start", `not JSON`, ``},
		{"missing run ID", `{}`, ``},
		{"invalid status", `{"pipeline_run_id":"run-id"}`, `not JSON`},
		{"missing status", `{"pipeline_run_id":"run-id"}`, `{}`},
		{"unknown status", `{"pipeline_run_id":"run-id"}`, `{"status":"UNKNOWN"}`},
		{"failed status", `{"pipeline_run_id":"run-id"}`, `{"status":"FAILED","message":"extraction failed"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/datasets":
					_, _ = io.WriteString(w, `[{"id":"id","name":"reports"}]`)
				case "/api/v1/cognify":
					_, _ = io.WriteString(w, tc.start)
				default:
					_, _ = io.WriteString(w, tc.poll)
				}
			}))
			defer server.Close()
			out, err := runCLICommand(t, server.URL, "cognify", "--dataset=reports", "--wait")
			if err == nil || strings.Contains(out, "COMPLETED") {
				t.Fatalf("failure reported success: %v %q", err, out)
			}
		})
	}
}

func TestCLICognifyAlreadyProcessedDoesNotPoll(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/datasets":
			_, _ = io.WriteString(w, `[{"id":"id","name":"reports"}]`)
		case "/api/v1/cognify":
			_, _ = io.WriteString(w, `{"status":"already_processed","message":"Dataset already processed"}`)
		default:
			t.Errorf("unexpected poll: %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	out, err := runCLICommand(t, server.URL, "cognify", "--dataset=reports", "--wait")
	if err != nil || !strings.Contains(out, "Already processed") {
		t.Fatalf("no-op failed: %v %q", err, out)
	}
}
