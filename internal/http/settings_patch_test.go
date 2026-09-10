package http

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func settingsPatchDB(t *testing.T, dialect string) (*sql.DB, *sql.DB) {
	t.Helper()
	previous := GetDBProvider()
	t.Cleanup(func() { SetDBProvider(previous) })
	var db, other *sql.DB
	if dialect == "postgres" {
		dsn := os.Getenv("LEVARA_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("LEVARA_TEST_POSTGRES_DSN not set")
		}
		config, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		schema := fmt.Sprintf("settings_patch_%d", time.Now().UnixNano())
		config.RuntimeParams["search_path"] = schema
		db, other = stdlib.OpenDB(*config), stdlib.OpenDB(*config)
		t.Cleanup(func() { other.Close(); db.Exec("DROP SCHEMA " + schema + " CASCADE"); db.Close() })
		if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
			t.Fatal(err)
		}
		SetDBProvider(DBPostgres)
	} else {
		dsn := "file:" + filepath.Join(t.TempDir(), "settings.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
		var err error
		db, err = sql.Open("sqlite3", dsn)
		if err != nil {
			t.Fatal(err)
		}
		other, err = sql.Open("sqlite3", dsn)
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() { other.Close(); db.Close() })
		SetDBProvider(DBSQLite)
	}
	db.SetMaxOpenConns(1)
	other.SetMaxOpenConns(1)
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { userSettings.Delete("settings-alice"); userSettings.Delete("settings-bob") })
	return db, other
}

func settingsPatchApp(db *sql.DB) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user_id", c.Get("X-Fixture-User", "settings-alice"))
		return c.Next()
	})
	cfg := APIConfig{DB: db}
	app.Get("/settings", settingsGetHandler(cfg))
	app.Put("/settings", settingsPutHandler(cfg))
	return app
}

func settingsPatchRequest(t *testing.T, app *fiber.App, method, body string, want int, owner ...string) SettingsDTO {
	t.Helper()
	req := httptest.NewRequest(method, "/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if len(owner) > 0 {
		req.Header.Set("X-Fixture-User", owner[0])
	}
	resp, err := app.Test(req, 10000)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, body, resp.StatusCode, want, raw)
	}
	var got SettingsDTO
	if want == 200 {
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
	}
	return got
}

func TestSettingsPartialUpdate(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres", "no-db"} {
		t.Run(dialect, func(t *testing.T) {
			var db *sql.DB
			if dialect != "no-db" {
				db, _ = settingsPatchDB(t, dialect)
			}
			t.Cleanup(func() { userSettings.Delete("settings-alice"); userSettings.Delete("settings-bob") })
			app := settingsPatchApp(db)
			for _, tc := range []struct{ name, patch, theme, locale, collection string }{
				{"collection", `{"default_collection":"project-b"}`, "dark", "en", "project-b"},
				{"theme", `{"theme":"light"}`, "light", "en", "project-a"},
				{"clear", `{"default_collection":""}`, "dark", "en", ""},
				{"empty", `{}`, "dark", "en", "project-a"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					settingsPatchRequest(t, app, "PUT", `{"theme":"dark","locale":"en","default_collection":"project-a","llm_api_key":"fixture-secret"}`, 200)
					bob := settingsPatchRequest(t, app, "PUT", `{"theme":"light","locale":"ru","default_collection":"bob"}`, 200, "settings-bob")
					got := settingsPatchRequest(t, app, "PUT", tc.patch, 200)
					if db != nil {
						userSettings.Delete("settings-alice")
					}
					persisted := settingsPatchRequest(t, app, "GET", "", 200)
					if got != persisted || persisted.Theme != tc.theme || persisted.Locale != tc.locale || persisted.DefaultCollection != tc.collection || persisted.LLMAPIKey != "fixture-secret" {
						t.Errorf("partial settings result=%+v persisted=%+v, expected %s/%s/%s with prior secret retained", got, persisted, tc.theme, tc.locale, tc.collection)
					}
					if settingsPatchRequest(t, app, "GET", "", 200, "settings-bob") != bob {
						t.Error("other user's settings changed")
					}
				})
			}
			t.Run("sequence", func(t *testing.T) {
				settingsPatchRequest(t, app, "PUT", `{"theme":"dark","locale":"en","default_collection":"project-a"}`, 200)
				settingsPatchRequest(t, app, "PUT", `{"default_collection":"project-b"}`, 200)
				got := settingsPatchRequest(t, app, "PUT", `{"theme":"light"}`, 200)
				if got.Theme != "light" || got.Locale != "en" || got.DefaultCollection != "project-b" {
					t.Errorf("sequence lost state: %+v", got)
				}
			})
		})
	}
}

func TestSettingsPatchValidation(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres", "no-db"} {
		t.Run(dialect, func(t *testing.T) {
			var db *sql.DB
			if dialect != "no-db" {
				db, _ = settingsPatchDB(t, dialect)
			}
			t.Cleanup(func() { userSettings.Delete("settings-alice") })
			app := settingsPatchApp(db)
			before := settingsPatchRequest(t, app, "PUT", `{"theme":"dark","locale":"en","default_collection":"project-a"}`, 200)
			for _, invalid := range []string{`null`, `[]`, `"x"`, `{`, `{} {}`, `{"theme":null}`, `{"theme":"unknown"}`, `{"theme":""}`, `{"locale":"unknown"}`, `{"default_collection":null}`, `{"theme":1}`, `{"embedding_dimension":0}`, `{"chunk_size":-1}`, `{"chunk_size":1.5}`, `{"chunk_size":"42"}`, `{"not_a_setting":true}`, `{"theme":"light","locale":null}`} {
				t.Run(invalid, func(t *testing.T) {
					settingsPatchRequest(t, app, "PUT", `{"theme":"dark","locale":"en","default_collection":"project-a"}`, 200)
					settingsPatchRequest(t, app, "PUT", invalid, 400)
					if got := settingsPatchRequest(t, app, "GET", "", 200); got != before {
						t.Errorf("invalid update changed settings: %+v", got)
					}
				})
			}
		})
	}
}

func TestSettingsPatchStoredState(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db, other := settingsPatchDB(t, dialect)
			app := settingsPatchApp(db)
			if _, err := db.Exec(Q(`INSERT INTO user_settings(user_id,settings) VALUES($1,$2)`), "settings-alice", `{"theme":"dark","locale":"en","future":{"enabled":true}}`); err != nil {
				t.Fatal(err)
			}
			settingsPatchRequest(t, app, "PUT", `{"default_collection":"a"}`, 200)
			var raw string
			if err := db.QueryRow(`SELECT settings FROM user_settings WHERE user_id='settings-alice'`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(raw), &fields); err != nil {
				t.Fatal(err)
			}
			var future map[string]bool
			if json.Unmarshal(fields["future"], &future) != nil || !future["enabled"] {
				t.Errorf("unknown stored field lost: %s", raw)
			}
			userSettings.Store("settings-alice", &SettingsDTO{Theme: "stale"})
			if _, err := other.Exec(Q(`UPDATE user_settings SET settings=$1 WHERE user_id='settings-alice'`), `{"theme":"light"}`); err != nil {
				t.Fatal(err)
			}
			if got := settingsPatchRequest(t, app, "GET", "", 200); got.Theme != "light" {
				t.Errorf("DB GET trusted stale process cache: %+v", got)
			}
			brokenValues := []string{`[]`, `null`, `{"theme":1}`, `{"theme":"neon"}`, `{"locale":"de"}`, `{"chunk_size":0}`, `{"embedding_dimension":-1}`, `{"default_collection":null}`, `{"llm_api_key":null}`}
			if dialect == "sqlite" {
				brokenValues = append(brokenValues, `{"broken"`)
			}
			for _, broken := range brokenValues {
				if _, err := other.Exec(Q(`UPDATE user_settings SET settings=$1 WHERE user_id='settings-alice'`), broken); err != nil {
					t.Fatal(err)
				}
				settingsPatchRequest(t, app, "GET", "", 500)
				settingsPatchRequest(t, app, "PUT", `{"default_collection":"repair-control"}`, 500)
				if err := db.QueryRow(`SELECT settings FROM user_settings WHERE user_id='settings-alice'`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var a, b any
				json.Unmarshal([]byte(raw), &a)
				json.Unmarshal([]byte(broken), &b)
				if fmt.Sprint(a) != fmt.Sprint(b) {
					t.Errorf("corrupt state overwritten: %s", raw)
				}
			}
			if _, err := other.Exec(`DROP TABLE user_settings`); err != nil {
				t.Fatal(err)
			}
			settingsPatchRequest(t, app, "PUT", `{"theme":"dark"}`, 500)
			settingsPatchRequest(t, app, "GET", "", 500)
		})
	}
}

func TestSettingsPersistenceRollback(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, failure := range []string{"statement", "commit"} {
				t.Run(failure, func(t *testing.T) {
					db, _ := settingsPatchDB(t, dialect)
					app := settingsPatchApp(db)
					before := settingsPatchRequest(t, app, "PUT", `{"theme":"dark","locale":"en","default_collection":"retained"}`, 200)
					ddl := []string{
						`CREATE TABLE settings_valid_refs(id INTEGER PRIMARY KEY)`,
						`CREATE TABLE settings_failure(ref INTEGER REFERENCES settings_valid_refs(id) DEFERRABLE INITIALLY DEFERRED)`,
					}
					if failure == "statement" {
						ddl[1] = `CREATE TABLE settings_failure(ref INTEGER CHECK(ref > 0))`
					}
					if dialect == "sqlite" {
						ddl = append(ddl, `CREATE TRIGGER settings_fail AFTER UPDATE ON user_settings BEGIN INSERT INTO settings_failure(ref) VALUES(-1); END`)
					} else {
						ddl = append(ddl, `CREATE FUNCTION fail_settings_write() RETURNS trigger AS $$ BEGIN INSERT INTO settings_failure(ref) VALUES(-1); RETURN NEW; END $$ LANGUAGE plpgsql`,
							`CREATE TRIGGER settings_fail AFTER UPDATE ON user_settings FOR EACH ROW EXECUTE FUNCTION fail_settings_write()`)
					}
					for _, q := range ddl {
						if _, err := db.Exec(q); err != nil {
							t.Fatal(err)
						}
					}
					// Establish where the injected failure occurs, without the handler.
					tx, err := db.Begin()
					if err != nil {
						t.Fatal(err)
					}
					_, err = tx.Exec(`UPDATE user_settings SET user_id=user_id`)
					if failure == "commit" {
						if err != nil {
							tx.Rollback()
							t.Fatalf("expected deferred commit failure, got statement error: %v", err)
						}
						if err = tx.Commit(); err == nil {
							t.Fatal("commit-failure control committed")
						}
					} else {
						if err == nil {
							tx.Rollback()
							t.Fatal("statement-failure control succeeded")
						}
						tx.Rollback()
					}
					settingsPatchRequest(t, app, "PUT", `{"theme":"light","default_collection":"lost"}`, 500)
					if got := settingsPatchRequest(t, app, "GET", "", 200); got != before {
						t.Errorf("failed transaction changed committed settings: %+v", got)
					}
					settingsPatchRequest(t, app, "PUT", `{"theme":"light"}`, 500, "settings-new")
					var count int
					if err := db.QueryRow(`SELECT COUNT(*) FROM user_settings WHERE user_id='settings-new'`).Scan(&count); err != nil || count != 0 {
						t.Fatalf("failed first PUT left a row: count=%d err=%v", count, err)
					}
					if err := db.QueryRow(`SELECT COUNT(*) FROM settings_failure`).Scan(&count); err != nil || count != 0 {
						t.Fatalf("failed transaction left trigger effects: count=%d err=%v", count, err)
					}
					if _, exists := userSettings.Load("settings-new"); exists {
						t.Error("failed first PUT populated process cache")
					}
				})
			}
		})
	}
}

func TestSettingsConcurrentPatches(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres", "no-db"} {
		t.Run(dialect, func(t *testing.T) {
			var db, other *sql.DB
			if dialect != "no-db" {
				db, other = settingsPatchDB(t, dialect)
			}
			t.Cleanup(func() { userSettings.Delete("settings-alice") })
			for _, first := range []bool{true, false} {
				for _, sameField := range []bool{false, true} {
					t.Run(fmt.Sprintf("first=%t/same-field=%t", first, sameField), func(t *testing.T) {
						userSettings.Delete("settings-alice")
						if db != nil {
							if _, err := db.Exec(`DELETE FROM user_settings`); err != nil {
								t.Fatal(err)
							}
						}
						base := settingsPatchApp(db)
						if !first {
							settingsPatchRequest(t, base, "PUT", `{"theme":"system","locale":"ru","default_collection":"a"}`, 200)
						}
						var ready sync.WaitGroup
						ready.Add(2)
						start := make(chan struct{})
						apps := []*fiber.App{}
						for _, handle := range []*sql.DB{db, other} {
							app := fiber.New(fiber.Config{DisableStartupMessage: true})
							app.Use(func(c *fiber.Ctx) error {
								c.Locals("user_id", "settings-alice")
								ready.Done()
								<-start
								return c.Next()
							})
							app.Put("/settings", settingsPutHandler(APIConfig{DB: handle}))
							apps = append(apps, app)
						}
						patches := []string{`{"theme":"dark"}`, `{"locale":"en"}`}
						if sameField {
							patches[1] = `{"theme":"light"}`
						}
						type result struct {
							status int
							raw    []byte
							err    error
						}
						results := make(chan result, 2)
						for i := range apps {
							go func(i int) {
								req := httptest.NewRequest("PUT", "/settings", strings.NewReader(patches[i]))
								req.Header.Set("Content-Type", "application/json")
								resp, err := apps[i].Test(req, 10000)
								if err != nil {
									results <- result{err: err}
									return
								}
								b, err := io.ReadAll(resp.Body)
								resp.Body.Close()
								results <- result{resp.StatusCode, b, err}
							}(i)
						}
						ready.Wait()
						close(start)
						for range 2 {
							r := <-results
							if r.err != nil || r.status != 200 {
								t.Fatalf("concurrent PUT status=%d err=%v body=%s", r.status, r.err, r.raw)
							}
						}
						got := settingsPatchRequest(t, base, "GET", "", 200)
						if sameField {
							if got.Theme != "light" && got.Theme != "dark" {
								t.Errorf("same-field result=%+v", got)
							}
						} else if got.Theme != "dark" || got.Locale != "en" {
							t.Errorf("independent updates lost: %+v", got)
						}
						if !first && got.DefaultCollection != "a" {
							t.Errorf("unrelated setting lost: %+v", got)
						}
					})
				}
			}
		})
	}
}
