package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	vectorhttp "github.com/stek0v/levara/internal/http"
	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/bm25"
)

func TestWorkspaceCLIFullCycleWriteSearchCommitRevert(t *testing.T) {
	cleanup := newWorkspaceCLITestServer(t)
	defer cleanup()

	withStdin(t, "# CLI\n\nOriginal CLI anchor.", func() {
		cmdWorkspaceWrite([]string{
			"docs/cli.md",
			"--project=payments",
			"--generation=cli-gen-1",
			"--collection=cli_gen_1",
			"--chunk-strategy=paragraph",
			"--min-chunk-chars=1",
			"--activate",
		})
	})

	originalSearch := captureStdout(t, func() {
		cmdSearch([]string{
			"original",
			"cli",
			"anchor",
			"--type=CHUNKS_LEXICAL",
			"--collection=cli_gen_1",
			"--top-k=5",
		})
	})
	if !strings.Contains(originalSearch, "Original CLI anchor") {
		t.Fatalf("original search missing indexed text:\n%s", originalSearch)
	}

	commitOutput := captureStdout(t, func() {
		cmdWorkspaceCommit([]string{
			"--project=payments",
			"--message=cli-original",
		})
	})
	commitID := parseCommitID(t, commitOutput)

	withStdin(t, "# CLI\n\nChanged CLI anchor.", func() {
		cmdWorkspaceWrite([]string{
			"docs/cli.md",
			"--project=payments",
			"--generation=cli-gen-2",
			"--collection=cli_gen_2",
			"--chunk-strategy=paragraph",
			"--min-chunk-chars=1",
			"--activate",
		})
	})

	changedSearch := captureStdout(t, func() {
		cmdSearch([]string{
			"changed",
			"cli",
			"anchor",
			"--type=CHUNKS_LEXICAL",
			"--collection=cli_gen_2",
			"--top-k=5",
		})
	})
	if !strings.Contains(changedSearch, "Changed CLI anchor") {
		t.Fatalf("changed search missing indexed text:\n%s", changedSearch)
	}

	cmdWorkspaceRevert([]string{
		commitID,
		"--project=payments",
		"--reindex",
		"--generation=cli-gen-3",
		"--collection=cli_gen_3",
		"--chunk-strategy=paragraph",
		"--min-chunk-chars=1",
	})

	revertedSearch := captureStdout(t, func() {
		cmdSearch([]string{
			"original",
			"cli",
			"anchor",
			"--type=CHUNKS_LEXICAL",
			"--collection=cli_gen_3",
			"--top-k=5",
		})
	})
	if !strings.Contains(revertedSearch, "Original CLI anchor") {
		t.Fatalf("reverted search missing original text:\n%s", revertedSearch)
	}
	if strings.Contains(revertedSearch, "Changed CLI anchor") {
		t.Fatalf("reverted search leaked changed text:\n%s", revertedSearch)
	}

	watchStatus := captureStdout(t, func() {
		cmdWorkspaceWatchStatus(nil)
	})
	if !strings.Contains(watchStatus, `"enabled"`) {
		t.Fatalf("watch-status output missing enabled field:\n%s", watchStatus)
	}
}

func newWorkspaceCLITestServer(t *testing.T) func() {
	t.Helper()

	dir := t.TempDir()
	cm, err := store.NewCollectionManager(2, filepath.Join(dir, "vectors"))
	if err != nil {
		t.Fatal(err)
	}
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		resp := struct {
			Data []item `json:"data"`
		}{Data: make([]item, len(req.Input))}
		for i, text := range req.Input {
			resp.Data[i] = item{Index: i, Embedding: []float32{float32(len(text)), float32(i + 1)}}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))

	app := fiber.New()
	api := app.Group("/api/v1")
	vectorhttp.RegisterAPI(api, vectorhttp.APIConfig{
		StoragePath:      filepath.Join(dir, "uploads"),
		WorkspacePath:    filepath.Join(dir, "workspace"),
		WorkspaceWatcher: vectorhttp.NewWorkspaceWatchState(),
		EmbedEndpoint:    embedSrv.URL,
		EmbedModel:       "test-embed",
		Collections:      cm,
		BM25Indexes:      map[string]*bm25.Index{},
	})
	srv := httptest.NewServer(adaptor.FiberApp(app))

	oldBaseURL := baseURL
	oldToken := token
	baseURL = srv.URL + "/api/v1"
	token = ""

	return func() {
		baseURL = oldBaseURL
		token = oldToken
		srv.Close()
		embedSrv.Close()
		_ = cm.Close()
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	fn()

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldStdout

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func withStdin(t *testing.T, input string, fn func()) {
	t.Helper()

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, input); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	defer func() {
		os.Stdin = oldStdin
		_ = r.Close()
	}()

	fn()
}

func parseCommitID(t *testing.T, output string) string {
	t.Helper()
	fields := strings.Fields(output)
	for i, field := range fields {
		if field == "commit" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	t.Fatalf("commit id not found in output:\n%s", output)
	return ""
}
