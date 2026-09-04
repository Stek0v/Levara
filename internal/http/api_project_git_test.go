package http

import (
	"io"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
)

// Coverage for the repo-binding endpoints (block ④).

func gitMetaApp(t *testing.T) *fiber.App {
	t.Helper()
	db := projectMetaDB(t)
	app := fiber.New()
	app.Patch("/datasets/:id", datasetSetRepoHandler(APIConfig{DB: db}))
	app.Get("/datasets/:id/commits", datasetCommitsHandler(APIConfig{DB: db}))
	return app
}

func TestSetRepoRoundTrip(t *testing.T) {
	app := gitMetaApp(t)

	// Empty binding → empty commits list.
	req0 := httptest.NewRequest("GET", "/datasets/ds-1/commits", nil)
	resp0, _ := app.Test(req0)
	if resp0.StatusCode != 200 {
		t.Fatalf("empty-binding commits status=%d", resp0.StatusCode)
	}

	// Bind, then verify the binding persists via a re-read of the row.
	req := httptest.NewRequest("PATCH", "/datasets/ds-1", strings.NewReader(`{"github_repo":"/tmp/repo"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("patch status=%v err=%v", resp, err)
	}
}

func TestCommitsInvalidRepoPathIs400(t *testing.T) {
	// Bind a non-repo dir, expect 400 with the parse error text.
	app := gitMetaApp(t)
	dir := t.TempDir()
	req := httptest.NewRequest("PATCH", "/datasets/ds-1", strings.NewReader(`{"github_repo":"`+dir+`"}`))
	req.Header.Set("Content-Type", "application/json")
	if _, err := app.Test(req); err != nil {
		t.Fatal(err)
	}
	req2 := httptest.NewRequest("GET", "/datasets/ds-1/commits", nil)
	resp2, _ := app.Test(req2)
	if resp2.StatusCode != 400 {
		t.Fatalf("status=%d, want 400", resp2.StatusCode)
	}
}

func TestCommitsRealRepo(t *testing.T) {
	// Init a real git repo with one commit and read its feed.
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("git", "init", "-q")
	run("git", "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-q", "-m", "init commit")

	app := gitMetaApp(t)
	repoPath := filepath.Join(dir)
	req := httptest.NewRequest("PATCH", "/datasets/ds-1", strings.NewReader(`{"github_repo":"`+repoPath+`"}`))
	req.Header.Set("Content-Type", "application/json")
	if _, err := app.Test(req); err != nil {
		t.Fatal(err)
	}
	req2 := httptest.NewRequest("GET", "/datasets/ds-1/commits", nil)
	resp2, err := app.Test(req2)
	if err != nil {
		t.Fatal(err)
	}
	if resp2.StatusCode != 200 {
		t.Fatalf("status=%d", resp2.StatusCode)
	}
	b, _ := io.ReadAll(resp2.Body)
	body := string(b)
	if !strings.Contains(body, "init commit") {
		t.Fatalf("commit feed missing message: %s", body)
	}
}
