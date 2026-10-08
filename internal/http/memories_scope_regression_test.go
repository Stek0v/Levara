package http

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/memoryindex"
)

func memoryScopeTestApp(f *documentHTTPFixture) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user_id", "alice")
		return c.Next()
	})
	app.Get("/memories/:key", getMemoryHandler(f.cfg))
	app.Post("/memories", saveMemoryHandler(f.cfg))
	return app
}

func TestMemorySaveRollsBackWhenIndexIntentFails(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		outbox, err := memoryindex.NewStore(f.db)
		if err != nil {
			t.Fatal(err)
		}
		cfg := f.cfg
		cfg.MemoryIndexOutbox = outbox
		cfg.EmbedEndpoint = "http://embed.invalid"
		app := fiber.New(fiber.Config{DisableStartupMessage: true})
		app.Use(func(c *fiber.Ctx) error {
			c.Locals("user_id", "alice")
			return c.Next()
		})
		app.Post("/memories", saveMemoryHandler(cfg))
		f.exec(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,verification_status,source_task_id,source_receipt_ids,created_at,updated_at)
			VALUES('canonical','atomic','original','project','alice','main','memory','decision','receipt-validated','task-1','["receipt-1"]','2026-10-08','2026-10-08')`)
		f.exec("DROP TABLE memory_index_jobs")
		body := `{"key":"atomic","value":"replacement","type":"project","collection_name":"main","room":"memory","hall":"decision"}`
		status, raw := memoryScopeRequest(t, app, http.MethodPost, "/memories", body)
		if status != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", status, raw)
		}
		var value, verification, task, receipts string
		if err := f.db.QueryRow(Q(`SELECT value,verification_status,source_task_id,source_receipt_ids FROM memories WHERE id='canonical'`)).
			Scan(&value, &verification, &task, &receipts); err != nil {
			t.Fatal(err)
		}
		if value != "original" || verification != "receipt-validated" || task != "task-1" || receipts != `["receipt-1"]` {
			t.Fatalf("failed index intent committed SQL: %q/%q/%q/%q", value, verification, task, receipts)
		}
	})
}

func memoryScopeRequest(t *testing.T, app *fiber.App, method, path, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
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
	return resp.StatusCode, raw
}

func TestMemoryGetUsesExactCollectionAndActiveRow(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		app := memoryScopeTestApp(f)
		for _, row := range []struct {
			id, key, value, owner, collection, superseded string
		}{
			{"personal-main", "same", "personal-main-value", "alice", "main", ""},
			{"shared-main", "same", "shared-main-value", "", "main", ""},
			{"personal-other", "same", "personal-other-value", "alice", "other", ""},
			{"retired-default", "retired", "must-not-return", "alice", "", "replacement"},
		} {
			f.exec(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,superseded_by,created_at,updated_at)
				VALUES($1,$2,$3,'project',$4,$5,'memory','fact',$6,'2026-10-08','2026-10-08')`,
				row.id, row.key, row.value, row.owner, row.collection, row.superseded)
		}
		f.exec(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,valid_until,created_at,updated_at)
			VALUES('expired-default','expired','must-not-return','project','alice','','memory','fact','2026-10-08','2026-10-07','2026-10-07')`)

		status, raw := memoryScopeRequest(t, app, http.MethodGet, "/memories/same?collection=main", "")
		if status != http.StatusOK {
			t.Fatalf("main status=%d body=%s", status, raw)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got["id"] != "personal-main" || got["value"] != "personal-main-value" {
			t.Fatalf("personal row must win over shared row: %+v", got)
		}

		status, raw = memoryScopeRequest(t, app, http.MethodGet, "/memories/same?collection=other", "")
		if status != http.StatusOK || !strings.Contains(string(raw), "personal-other-value") {
			t.Fatalf("other collection status=%d body=%s", status, raw)
		}
		status, raw = memoryScopeRequest(t, app, http.MethodGet, "/memories/same", "")
		if status != http.StatusConflict {
			t.Fatalf("omitted ambiguous collection status=%d body=%s", status, raw)
		}
		status, raw = memoryScopeRequest(t, app, http.MethodGet, "/memories/retired", "")
		if status != http.StatusNotFound {
			t.Fatalf("retired row returned: status=%d body=%s", status, raw)
		}
		status, raw = memoryScopeRequest(t, app, http.MethodGet, "/memories/expired", "")
		if status != http.StatusNotFound {
			t.Fatalf("validity-retired row returned: status=%d body=%s", status, raw)
		}
	})
}

func TestMemoryOverwriteResetsChangedEvidenceOnly(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		app := memoryScopeTestApp(f)
		f.exec(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,verification_status,source_task_id,source_receipt_ids,created_at,updated_at)
			VALUES('canonical','evidence','original','project','alice','main','memory','decision','receipt-validated','task-1','["receipt-1"]','2026-10-08','2026-10-08')`)

		post := func(value string) {
			t.Helper()
			body, err := json.Marshal(map[string]any{
				"key": "evidence", "value": value, "type": "project", "collection_name": "main", "room": "memory", "hall": "decision",
			})
			if err != nil {
				t.Fatal(err)
			}
			status, raw := memoryScopeRequest(t, app, http.MethodPost, "/memories", string(body))
			if status != http.StatusCreated || !strings.Contains(string(raw), `"id":"canonical"`) {
				t.Fatalf("save status=%d body=%s", status, raw)
			}
		}
		assertEvidence := func(value, verification, task, receipts string) {
			t.Helper()
			var gotValue, gotVerification, gotTask, gotReceipts string
			if err := f.db.QueryRow(Q(`SELECT value,verification_status,source_task_id,source_receipt_ids FROM memories WHERE id='canonical'`)).
				Scan(&gotValue, &gotVerification, &gotTask, &gotReceipts); err != nil {
				t.Fatal(err)
			}
			if gotValue != value || gotVerification != verification || gotTask != task || gotReceipts != receipts {
				t.Fatalf("memory/evidence=%q/%q/%q/%q", gotValue, gotVerification, gotTask, gotReceipts)
			}
		}

		post("original")
		assertEvidence("original", "receipt-validated", "task-1", `["receipt-1"]`)
		post("replacement")
		assertEvidence("replacement", "unverified", "", `[]`)
	})
}

func TestMemorySaveRejectsUnknownHall(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		status, raw := memoryScopeRequest(t, memoryScopeTestApp(f), http.MethodPost, "/memories", `{"key":"hall","value":"value","hall":"bogus"}`)
		if status != http.StatusBadRequest || !strings.Contains(string(raw), "invalid hall") {
			t.Fatalf("status=%d body=%s", status, raw)
		}
		var count int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM memories WHERE key='hall'").Scan(&count); err != nil || count != 0 {
			t.Fatalf("invalid hall persisted rows=%d err=%v", count, err)
		}
	})
}
