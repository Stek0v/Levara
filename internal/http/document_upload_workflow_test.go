package http

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/ingest"
)

func documentWorkflowApp(t *testing.T) (*fiber.App, APIConfig) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "docs.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	SetDBProvider(DBSQLite)
	ingest.SetSQLiteMode(true)
	t.Cleanup(func() { db.Close(); SetDBProvider(DBPostgres); ingest.SetSQLiteMode(false) })
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", "alice"); return c.Next() })
	cfg := APIConfig{DB: db, StoragePath: t.TempDir()}
	RegisterAPI(app, cfg)
	return app, cfg
}

func uploadDocumentFixture(t *testing.T, app *fiber.App, files map[string][]byte) (int, []byte) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("datasetName", "documents"); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		part, err := w.CreateFormFile("data", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/add", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := app.Test(req, -1)
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

func TestUploadDoesNotIndexExtractionFailuresAsBinaryText(t *testing.T) {
	t.Setenv("WHISPER_ENDPOINT", "")
	for _, fixture := range []struct {
		name string
		data []byte
	}{
		{"broken.pdf", []byte("%PDF-1.4\ninvalid document")},
		{"voice.wav", []byte("RIFF invalid audio")},
		{"empty.txt", nil},
		{"binary.txt", []byte{0xff, 0x00, 0x01}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			app, cfg := documentWorkflowApp(t)
			status, raw := uploadDocumentFixture(t, app, map[string][]byte{"good.txt": []byte("valid document"), fixture.name: fixture.data})
			if status != 422 {
				t.Errorf("status=%d, want422: %s", status, raw)
			}
			var count int
			if err := cfg.DB.QueryRow("SELECT COUNT(*) FROM data").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Errorf("failed batch saved %d documents", count)
			}
		})
	}
}

func TestUploadJSONMediaTypeAndMetadataFailure(t *testing.T) {
	app, cfg := documentWorkflowApp(t)
	req := httptest.NewRequest("POST", "/add", strings.NewReader(`{"data":"Отчёт за сентябрь","dataset_name":"reports"}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || out["dataset_name"] != "reports" {
		t.Fatalf("JSON upload: status=%d body=%v", resp.StatusCode, out)
	}
	var location string
	if err := cfg.DB.QueryRow("SELECT raw_data_location FROM data").Scan(&location); err != nil {
		t.Fatal(err)
	}
	data, err := loadRawDataByLocation(t.Context(), cfg, location)
	if err != nil || string(data) != "Отчёт за сентябрь" {
		t.Fatalf("stored bytes=%q err=%v", data, err)
	}
	if _, err := cfg.DB.Exec("CREATE TRIGGER reject_upload BEFORE INSERT ON data BEGIN SELECT RAISE(ABORT,'forced metadata failure'); END"); err != nil {
		t.Fatal(err)
	}
	status, raw := uploadDocumentFixture(t, app, map[string][]byte{"new.txt": []byte("new upload must fail")})
	if status != 500 {
		t.Fatalf("metadata failure reported as status=%d: %s", status, raw)
	}
}

func TestPipelineStatusRequiresEveryDocumentInCollection(t *testing.T) {
	_, cfg := documentWorkflowApp(t)
	if _, err := cfg.DB.Exec(`INSERT INTO datasets(id,name) VALUES('ds','ds');
	INSERT INTO data(id,pipeline_status) VALUES('first','{"docs":{"status":"COMPLETED"}}'),('second','{"docs":{"status":"FAILED"}}');
	INSERT INTO dataset_data(dataset_id,data_id) VALUES('ds','first'),('ds','second');`); err != nil {
		t.Fatal(err)
	}
	if CheckPipelineStatus(cfg.DB, "ds", "docs") {
		t.Error("partly failed dataset was treated as complete")
	}
	if _, err := cfg.DB.Exec(`UPDATE data SET pipeline_status='{"other":{"status":"COMPLETED"}}' WHERE id='second'`); err != nil {
		t.Fatal(err)
	}
	if CheckPipelineStatus(cfg.DB, "ds", "docs") {
		t.Error("another collection's status hid unprocessed document")
	}
	if _, err := cfg.DB.Exec(`UPDATE data SET pipeline_status='{"docs":{"status":"COMPLETED"}}' WHERE id='second'`); err != nil {
		t.Fatal(err)
	}
	if !CheckPipelineStatus(cfg.DB, "ds", "docs") {
		t.Error("fully processed dataset should be complete")
	}
}

func TestUploadPreservesOriginalAndExtractedText(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "remote"}[remote], func(t *testing.T) {
			app, cfg := documentWorkflowApp(t)
			if remote {
				cfg.FileStorage = newMemStorage()
				app = fiber.New()
				app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", "alice"); return c.Next() })
				RegisterAPI(app, cfg)
			}
			original := []byte("<!doctype html><html><body><h1>Quarterly report</h1><p>Revenue is 123.</p></body></html>")
			status, raw := uploadDocumentFixture(t, app, map[string][]byte{"report.html": original})
			if status != 200 {
				t.Fatalf("upload status=%d: %s", status, raw)
			}
			var id, datasetID, derivedLocation, originalLocation string
			if err := cfg.DB.QueryRow(`SELECT d.id,dd.dataset_id,d.raw_data_location,d.original_data_location FROM data d JOIN dataset_data dd ON dd.data_id=d.id`).Scan(&id, &datasetID, &derivedLocation, &originalLocation); err != nil {
				t.Fatal(err)
			}
			derived, err := loadRawDataByLocation(t.Context(), cfg, derivedLocation)
			if err != nil || !strings.Contains(string(derived), "Revenue is 123.") || bytes.Contains(derived, []byte("<html>")) {
				t.Fatalf("derived=%q err=%v", derived, err)
			}
			savedOriginal, err := loadRawDataByLocation(t.Context(), cfg, originalLocation)
			if err != nil || !bytes.Equal(savedOriginal, original) {
				t.Errorf("original not preserved: %q err=%v", savedOriginal, err)
			}
			for _, mode := range []string{"", "?original=true"} {
				resp, err := app.Test(httptest.NewRequest("GET", "/datasets/"+datasetID+"/data/"+id+"/raw"+mode, nil), -1)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				want := derived
				if mode != "" {
					want = original
				}
				if resp.StatusCode != 200 || !bytes.Equal(body, want) || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
					t.Errorf("download mode=%q status=%d body=%q", mode, resp.StatusCode, body)
				}
			}
		})
	}
}

func TestUploadChangedExtractionInvalidatesProcessing(t *testing.T) {
	app, cfg := documentWorkflowApp(t)
	source := []byte("<html><body><p>Revenue is 123.</p></body></html>")
	status, raw := uploadDocumentFixture(t, app, map[string][]byte{"report.txt": source})
	if status != 200 {
		t.Fatalf("first upload=%d %s", status, raw)
	}
	if _, err := cfg.DB.Exec(`UPDATE data SET pipeline_status='{"documents":{"status":"COMPLETED"}}',token_count=42`); err != nil {
		t.Fatal(err)
	}
	status, raw = uploadDocumentFixture(t, app, map[string][]byte{"report.html": source})
	if status != 200 {
		t.Fatalf("reupload=%d %s", status, raw)
	}
	var count, tokens int
	var pipeline, rawPath, ext string
	if err := cfg.DB.QueryRow("SELECT COUNT(*) FROM data").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := cfg.DB.QueryRow("SELECT pipeline_status,token_count,raw_data_location,extension FROM data").Scan(&pipeline, &tokens, &rawPath, &ext); err != nil {
		t.Fatal(err)
	}
	if count != 1 || pipeline != "{}" || tokens != -1 {
		t.Fatalf("count=%d status=%s tokens=%d", count, pipeline, tokens)
	}
	got, err := loadRawDataByLocation(t.Context(), cfg, rawPath)
	if err != nil || string(got) != "Revenue is 123." {
		t.Fatalf("text=%q err=%v", got, err)
	}
	if ext != ".html" {
		t.Fatalf("extension stale: %s", ext)
	}
}
