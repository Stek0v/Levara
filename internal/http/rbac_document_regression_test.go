package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

func documentACLHTTPFixture(t *testing.T, dialect ...string) (*fiber.App, *sql.DB) {
	t.Helper()
	var db *sql.DB
	if len(dialect) > 0 && dialect[0] == "postgres" {
		dsn := os.Getenv("LEVARA_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("LEVARA_TEST_POSTGRES_DSN is not set")
		}
		var err error
		db, err = sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		schema := fmt.Sprintf("document_acl_%d", time.Now().UnixNano())
		if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
			db.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Exec("DROP SCHEMA " + schema + " CASCADE"); db.Close(); SetDBProvider(DBPostgres) })
		if _, err := db.Exec("SET search_path TO " + schema); err != nil {
			t.Fatal(err)
		}
		SetDBProvider(DBPostgres)
		if err := MigrateSchema(db); err != nil {
			t.Fatal(err)
		}
	} else {
		db = newMCPMemoryBehaviorDB(t)
	}
	if err := accesspkg.EnsureIdentitySchema(context.Background(), db, Q); err != nil {
		t.Fatal(err)
	}
	if err := accesspkg.EnsureBrowserSessionSchema(context.Background(), db, Q); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO principals(id) VALUES('alice'),('bob')`,
		`INSERT INTO users(id,email,hashed_password) VALUES('alice','alice@example.test','x'),('bob','bob@example.test','x')`,
		`INSERT INTO datasets(id,name,owner_id) VALUES('a','alice-docs','alice'),('b','bob-docs','bob')`,
		`INSERT INTO dataset_shares(id,dataset_id,user_id,role,granted_by) VALUES('share-b','b','alice','viewer','bob')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", c.Get("X-Test-User")); return c.Next() })
	RegisterRBACAPI(app, APIConfig{DB: db})
	return app, db
}

func TestDocumentACLShareBindingAndUpsert(t *testing.T) {
	app, db := documentACLHTTPFixture(t)
	checkDocumentACLShareBindingAndUpsert(t, app, db)
}

func checkDocumentACLShareBindingAndUpsert(t *testing.T, app *fiber.App, db *sql.DB) {
	t.Helper()
	req := httptest.NewRequest("DELETE", "/datasets/a/shares/share-b", nil)
	req.Header.Set("X-Test-User", "alice")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM dataset_shares WHERE id='share-b'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("foreign share deleted, rows=%d err=%v", n, err)
	}
	var ids []string
	for _, role := range []string{"viewer", "editor"} {
		req = httptest.NewRequest("POST", "/datasets/a/shares", strings.NewReader(`{"user_id":"bob","role":"`+role+`"}`))
		req.Header.Set("X-Test-User", "alice")
		req.Header.Set("Content-Type", "application/json")
		resp, err = app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		var out ShareDTO
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 201 {
			t.Fatalf("grant status=%d err=%v", resp.StatusCode, err)
		}
		ids = append(ids, out.ID)
	}
	if ids[0] != ids[1] {
		t.Errorf("upsert returned invented share ID: %v", ids)
	}
	req = httptest.NewRequest("DELETE", "/datasets/a/shares/"+ids[1], nil)
	req.Header.Set("X-Test-User", "alice")
	resp, err = app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := db.QueryRow(`SELECT COUNT(*) FROM dataset_shares WHERE dataset_id='a'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("returned ID cannot revoke persisted share: %d %v", n, err)
	}
}
