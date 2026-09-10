package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	httpapi "github.com/stek0v/levara/internal/http"
	"github.com/stek0v/levara/pkg/access"
)

func TestCLIAddRealAPI(t *testing.T) {
	t.Setenv("LEVARA_TENANT_ENFORCED", "0")
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			server, db, _ := teamIntegrationServer(t, dialect)
			documentCLIExec(t, db, "INSERT INTO principals(id,type) VALUES('owner','user')")
			documentCLIExec(t, db, "INSERT INTO users(id,email,hashed_password,is_active) VALUES('owner','owner@test.invalid','locked',TRUE)")
			documentCLIExec(t, db, "INSERT INTO tenants(id,name,owner_id) VALUES('a','A','owner')")
			documentCLIExec(t, db, "INSERT INTO user_tenant(user_id,tenant_id) VALUES('owner','a')")
			token, err := httpapi.IssueSessionJWT(context.Background(), db, "owner", "owner@test.invalid", "isolated-team-cli-session-secret")
			if err != nil {
				t.Fatal(err)
			}

			const dataset = "CLI real upload"
			fileBytes := []byte("# Exact upload\n\nПривет из CLI.\n")
			file := filepath.Join(t.TempDir(), "exact upload.md")
			if err := os.WriteFile(file, fileBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"add", "--file=" + file, "--dataset=" + dataset}, {"add", "second exact text", "--dataset=" + dataset}} {
				out, err := runCLICommandWithToken(t, server.URL, token, args...)
				if err != nil || !strings.Contains(out, "OK") || !strings.Contains(out, "ingested") {
					t.Fatalf("real add %v: %v\n%s", args, err, out)
				}
			}

			var datasetID string
			if err := db.QueryRow(httpapi.Q("SELECT id FROM datasets WHERE name=$1 AND owner_id='owner'"), dataset).Scan(&datasetID); err != nil {
				t.Fatal(err)
			}
			var location string
			if err := db.QueryRow(httpapi.Q(`SELECT d.original_data_location FROM data d JOIN dataset_data dd ON dd.data_id=d.id
				WHERE dd.dataset_id=$1 AND d.name=$2`), datasetID, filepath.Base(file)).Scan(&location); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(strings.TrimPrefix(location, "file://"))
			if err != nil || string(got) != string(fileBytes) {
				t.Fatalf("stored bytes=%q err=%v location=%q", got, err, location)
			}
			var rows int
			if err := db.QueryRow(httpapi.Q("SELECT COUNT(*) FROM dataset_data WHERE dataset_id=$1"), datasetID).Scan(&rows); err != nil || rows != 2 {
				t.Fatalf("dataset rows=%d err=%v", rows, err)
			}
		})
	}
}

func documentCLIExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	query, args = httpapi.QArgs(query, args...)
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestDocumentCLIRealAPI(t *testing.T) {
	t.Setenv("LEVARA_TENANT_ENFORCED", "0")
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			server, db, _ := teamIntegrationServer(t, dialect)
			for _, id := range []string{"owner", "peer"} {
				documentCLIExec(t, db, "INSERT INTO principals(id,type) VALUES($1,'user')", id)
				documentCLIExec(t, db, "INSERT INTO users(id,email,hashed_password,is_active) VALUES($1,$2,'locked',TRUE)", id, id+"@test.invalid")
			}
			documentCLIExec(t, db, "INSERT INTO tenants(id,name,owner_id) VALUES('a','A','owner')")
			documentCLIExec(t, db, "INSERT INTO user_tenant(user_id,tenant_id) VALUES('owner','a'),('peer','a')")
			documentCLIExec(t, db, "INSERT INTO datasets(id,name,owner_id) VALUES('alpha','Alpha','owner')")
			documentCLIExec(t, db, "INSERT INTO data(id,name,owner_id) VALUES('blob','Blob','owner'),('visible','Visible','owner')")
			documentCLIExec(t, db, "INSERT INTO dataset_data(dataset_id,data_id) VALUES('alpha','blob'),('alpha','visible')")
			policy := access.SQLPolicy{DB: db, Q: httpapi.Q, QA: httpapi.QArgs}
			if _, err := policy.RegisterDocument(context.Background(), access.Actor{UserID: "owner", TenantID: "a"}, access.DocumentRef{DatasetID: "alpha", DataID: "blob"}, "a", access.DocumentRestricted); err != nil {
				t.Fatal(err)
			}
			ownerToken, err := httpapi.IssueSessionJWT(context.Background(), db, "owner", "owner@test.invalid", "isolated-team-cli-session-secret")
			if err != nil {
				t.Fatal(err)
			}
			peerToken, err := httpapi.IssueSessionJWT(context.Background(), db, "peer", "peer@test.invalid", "isolated-team-cli-session-secret")
			if err != nil {
				t.Fatal(err)
			}
			run := func(token string, args ...string) string {
				t.Helper()
				out, err := runCLICommandWithToken(t, server.URL, token, args...)
				if err != nil {
					t.Fatalf("documents %v: %v\n%s", args, err, out)
				}
				return out
			}

			if out := run(ownerToken, "documents", "policy", "alpha", "blob"); !strings.Contains(out, `"mode": "restricted"`) {
				t.Fatalf("policy output: %s", out)
			}
			if out := run(ownerToken, "documents", "recipients", "alpha", "blob"); !strings.Contains(out, "peer@test.invalid") {
				t.Fatalf("recipients output: %s", out)
			}
			run(ownerToken, "documents", "grant", "alpha", "blob", "user", "peer", "editor")
			if out := run(peerToken, "documents", "shared"); !strings.Contains(out, `"data_id": "blob"`) || !strings.Contains(out, `"role": "editor"`) {
				t.Fatalf("shared output: %s", out)
			}
			run(ownerToken, "documents", "revoke", "alpha", "blob", "user", "peer")
			if out := run(peerToken, "documents", "shared"); strings.Contains(out, `"data_id": "blob"`) {
				t.Fatalf("revoked document remains: %s", out)
			}
			if out := run(ownerToken, "documents", "register", "alpha", "visible"); !strings.Contains(out, `"tenant_id": "a"`) {
				t.Fatalf("registration did not default tenant: %s", out)
			}
			groupJSON := run(ownerToken, "documents", "group-create", "Review", "Team")
			var group struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal([]byte(groupJSON), &group); err != nil || group.ID == "" {
				t.Fatalf("group output: %v %s", err, groupJSON)
			}
			run(ownerToken, "documents", "group-members", group.ID, "--members=peer")
			var count int
			if err := db.QueryRow(httpapi.Q("SELECT COUNT(*) FROM access_group_members WHERE group_id=$1 AND user_id='peer'"), group.ID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("group members count=%d err=%v", count, err)
			}
			run(ownerToken, "documents", "group-members", group.ID)
			if err := db.QueryRow(httpapi.Q("SELECT COUNT(*) FROM access_group_members WHERE group_id=$1"), group.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("empty group members count=%d err=%v", count, err)
			}
		})
	}
}

func TestDocumentCLIRejectsConflictAndRedirect(t *testing.T) {
	var redirected bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/policy") {
			_, _ = w.Write([]byte(`{"acl_revision":1}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/grants") {
			http.Error(w, "policy version conflict", http.StatusConflict)
			return
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	if out, err := runCLICommand(t, server.URL, "documents", "grant", "alpha", "blob", "user", "peer", "viewer"); err == nil || !strings.Contains(out, "409") {
		t.Fatalf("conflict reported as success: %v %s", err, out)
	}
	if out, err := runCLICommand(t, server.URL, "documents", "shared"); err == nil || !strings.Contains(out, "307") || redirected {
		t.Fatalf("redirect followed: redirected=%v err=%v output=%s", redirected, err, out)
	}
}
