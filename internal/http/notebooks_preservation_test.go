package http

import (
	"database/sql"
	"fmt"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

func TestNotebookSunsetPreservesSQLHistory(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		created := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
		updated := created.Add(time.Hour)
		for i, owner := range []string{"owner", "foreign", ""} {
			id := fmt.Sprintf("retained-%d", i)
			f.exec("INSERT INTO notebooks(id,title,owner_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5)",
				id, "История "+id, owner, created, updated)
			for j, kind := range []string{"code", "markdown", "search"} {
				f.exec("INSERT INTO notebook_cells(id,notebook_id,cell_type,source,output,position,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)",
					fmt.Sprintf("%s-cell-%d", id, j), id, kind, "source\nλ", "{\"result\":\"saved\"}", 10-j, created, updated)
			}
		}
		beforeNotebooks := notebookHistoryRows(t, f.db, "SELECT id,title,owner_id,created_at,updated_at FROM notebooks ORDER BY id")
		beforeCells := notebookHistoryRows(t, f.db, "SELECT id,notebook_id,cell_type,source,output,position,created_at,updated_at FROM notebook_cells ORDER BY id")
		for attempt := 0; attempt < 2; attempt++ {
			if err := MigrateSchema(f.db); err != nil {
				t.Fatal(err)
			}
			t.Setenv("LEVARA_NOTEBOOKS", "1")
			app := fiber.New()
			RegisterAPI(app, f.cfg)
			for _, probe := range notebooksFlagProbe() {
				resp, err := app.Test(httptest.NewRequest(probe.method, probe.path, nil))
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != fiber.StatusNotFound {
					t.Fatalf("%s %s = %d, want 404", probe.method, probe.path, resp.StatusCode)
				}
			}
			if got := notebookHistoryRows(t, f.db, "SELECT id,title,owner_id,created_at,updated_at FROM notebooks ORDER BY id"); !reflect.DeepEqual(got, beforeNotebooks) {
				t.Fatalf("notebook history changed: got %v, want %v", got, beforeNotebooks)
			}
			if got := notebookHistoryRows(t, f.db, "SELECT id,notebook_id,cell_type,source,output,position,created_at,updated_at FROM notebook_cells ORDER BY id"); !reflect.DeepEqual(got, beforeCells) {
				t.Fatalf("cell history changed: got %v, want %v", got, beforeCells)
			}
		}
		indexQuery := "SELECT name FROM sqlite_master WHERE type='index' AND name IN ('idx_notebooks_owner','idx_notebook_cells_notebook') ORDER BY name"
		if GetDBProvider() == DBPostgres {
			indexQuery = "SELECT indexname FROM pg_indexes WHERE schemaname=current_schema() AND indexname IN ('idx_notebooks_owner','idx_notebook_cells_notebook') ORDER BY indexname"
		}
		if got := notebookHistoryRows(t, f.db, indexQuery); !reflect.DeepEqual(got, [][]string{{"idx_notebook_cells_notebook"}, {"idx_notebooks_owner"}}) {
			t.Fatalf("retained indexes = %v", got)
		}
		if _, err := f.db.Exec(Q("INSERT INTO notebook_cells(id,notebook_id) VALUES($1,$2)"), "orphan", "missing-notebook"); err == nil {
			t.Fatal("retained notebook foreign key accepted an orphan cell")
		}
	})
}

func notebookHistoryRows(t *testing.T, db *sql.DB, query string) [][]string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]string
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		row := make([]string, len(values))
		for i, value := range values {
			if bytes, ok := value.([]byte); ok {
				row[i] = string(bytes)
			} else {
				row[i] = fmt.Sprint(value)
			}
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
