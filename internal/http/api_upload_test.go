package http

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/ingest"
	"github.com/stek0v/levara/pkg/structuredextract"
)

type artifactDeleteStorage struct {
	*memStorage
	fail atomic.Bool
}

func (s *artifactDeleteStorage) Delete(ctx context.Context, key string) error {
	if s.fail.Load() {
		return errors.New("delete unavailable")
	}
	return s.memStorage.Delete(ctx, key)
}

func uploadDatasetDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, "upload.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		os.RemoveAll(dir)
	})
	if _, err := db.Exec(`CREATE TABLE datasets (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		owner_id TEXT
	)`); err != nil {
		t.Fatalf("create datasets: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE dataset_shares (
		id TEXT PRIMARY KEY,
		dataset_id TEXT,
		user_id TEXT,
		role TEXT
	)`); err != nil {
		t.Fatalf("create dataset_shares: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE users(id TEXT PRIMARY KEY, is_active BOOLEAN NOT NULL DEFAULT true); INSERT INTO users(id) VALUES ('alice'), ('bob')`); err != nil {
		t.Fatal(err)
	}
	SetDBProvider(DBSQLite)
	t.Cleanup(func() { SetDBProvider(DBPostgres) })
	return db
}

func TestLookupUploadDatasetID_PrefersOwnerMatch(t *testing.T) {
	db := uploadDatasetDB(t)
	if _, err := db.Exec(`INSERT INTO datasets(id, name, owner_id) VALUES
		('legacy-default', 'default', ''),
		('alice-default', 'default', 'alice')`); err != nil {
		t.Fatalf("seed datasets: %v", err)
	}

	got := lookupUploadDatasetID(context.Background(), db, "default", "alice")
	if got != "alice-default" {
		t.Fatalf("lookupUploadDatasetID = %q, want alice-default", got)
	}
}

func TestLookupUploadDatasetID_FallsBackForNoAuth(t *testing.T) {
	db := uploadDatasetDB(t)
	if _, err := db.Exec(`INSERT INTO datasets(id, name, owner_id) VALUES ('legacy-default', 'default', '')`); err != nil {
		t.Fatalf("seed datasets: %v", err)
	}

	got := lookupUploadDatasetID(context.Background(), db, "default", "")
	if got != "legacy-default" {
		t.Fatalf("lookupUploadDatasetID = %q, want legacy-default", got)
	}
}

func TestLookupUploadDatasetID_DoesNotFallbackToOtherOwner(t *testing.T) {
	db := uploadDatasetDB(t)
	if _, err := db.Exec(`INSERT INTO datasets(id, name, owner_id) VALUES ('bob-default', 'default', 'bob')`); err != nil {
		t.Fatalf("seed datasets: %v", err)
	}

	got := lookupUploadDatasetID(context.Background(), db, "default", "alice")
	if got != "" {
		t.Fatalf("lookupUploadDatasetID = %q, want empty for another owner's dataset", got)
	}
}

func TestLookupUploadDatasetID_AuthenticatedUserCanUsePublicFallback(t *testing.T) {
	db := uploadDatasetDB(t)
	if _, err := db.Exec(`INSERT INTO datasets(id, name, owner_id) VALUES ('public-default', 'default', '')`); err != nil {
		t.Fatalf("seed datasets: %v", err)
	}

	got := lookupUploadDatasetID(context.Background(), db, "default", "alice")
	if got != "public-default" {
		t.Fatalf("lookupUploadDatasetID = %q, want public-default", got)
	}
}

func TestValidateUploadDatasetID_DeniesOtherOwner(t *testing.T) {
	db := uploadDatasetDB(t)
	if _, err := db.Exec(`INSERT INTO datasets(id, name, owner_id) VALUES ('bob-ds', 'bob-data', 'bob')`); err != nil {
		t.Fatalf("seed datasets: %v", err)
	}

	ok, err := validateUploadDatasetID(context.Background(), db, "bob-ds", "alice")
	if err != nil {
		t.Fatalf("validateUploadDatasetID: %v", err)
	}
	if ok {
		t.Fatal("validateUploadDatasetID allowed another owner's dataset")
	}
}

func TestValidateUploadDatasetID_AllowsSharedDataset(t *testing.T) {
	db := uploadDatasetDB(t)
	if _, err := db.Exec(`INSERT INTO datasets(id, name, owner_id) VALUES ('bob-ds', 'bob-data', 'bob')`); err != nil {
		t.Fatalf("seed datasets: %v", err)
	}
	// Editors hold write access to the shared dataset.
	if _, err := db.Exec(`INSERT INTO dataset_shares(id, dataset_id, user_id, role) VALUES ('share-1', 'bob-ds', 'alice', 'editor')`); err != nil {
		t.Fatalf("seed share: %v", err)
	}

	ok, err := validateUploadDatasetID(context.Background(), db, "bob-ds", "alice")
	if err != nil {
		t.Fatalf("validateUploadDatasetID: %v", err)
	}
	if !ok {
		t.Fatal("validateUploadDatasetID denied editor-shared dataset")
	}
}

func TestValidateUploadDatasetID_DeniesViewerSharedDataset(t *testing.T) {
	db := uploadDatasetDB(t)
	if _, err := db.Exec(`INSERT INTO datasets(id, name, owner_id) VALUES ('bob-ds', 'bob-data', 'bob')`); err != nil {
		t.Fatalf("seed datasets: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO dataset_shares(id, dataset_id, user_id, role) VALUES ('share-1', 'bob-ds', 'alice', 'viewer')`); err != nil {
		t.Fatalf("seed share: %v", err)
	}

	ok, err := validateUploadDatasetID(context.Background(), db, "bob-ds", "alice")
	if err != nil {
		t.Fatalf("validateUploadDatasetID: %v", err)
	}
	if ok {
		t.Fatal("validateUploadDatasetID allowed upload with viewer share")
	}
}

func TestValidateUploadDatasetID_AllowsMissingDatasetForCreate(t *testing.T) {
	db := uploadDatasetDB(t)

	ok, err := validateUploadDatasetID(context.Background(), db, "new-ds", "alice")
	if err != nil {
		t.Fatalf("validateUploadDatasetID: %v", err)
	}
	if !ok {
		t.Fatal("validateUploadDatasetID denied missing dataset id")
	}
}

func TestAddHandler_JSONBodyReturnsDocumentAnalysis(t *testing.T) {
	app := fiber.New()
	RegisterAPI(app, APIConfig{StoragePath: t.TempDir()})

	body := []byte(`{"data":"plain text for ingest","schema_id":"invoice-v1"}`)
	req := httptest.NewRequest("POST", "/add", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var payload struct {
		DocumentAnalysis []struct {
			Format           string `json:"format"`
			SchemaProvided   bool   `json:"schema_provided"`
			Recommended      string `json:"recommended_pipeline"`
			StructuredReady  bool   `json:"structured_ready"`
			StructuredReason string `json:"structured_reason"`
		} `json:"document_analysis"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.DocumentAnalysis) != 1 {
		t.Fatalf("document_analysis len = %d, want 1", len(payload.DocumentAnalysis))
	}
	got := payload.DocumentAnalysis[0]
	if got.Format != "text" {
		t.Fatalf("format = %q, want text", got.Format)
	}
	if !got.SchemaProvided {
		t.Fatal("schema_provided = false, want true")
	}
	if got.Recommended != "plain_ingest" {
		t.Fatalf("recommended_pipeline = %q, want plain_ingest", got.Recommended)
	}
	if got.StructuredReady {
		t.Fatal("structured_ready = true, want false for text body")
	}
}

func TestAddHandler_PDFTableRunsStructuredExtraction(t *testing.T) {
	pdfPath := generateUploadPDFWithReportLab(t)
	pdfData, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatal(err)
	}

	var sidecarReq structuredextract.Request
	sidecar := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sidecarReq); err != nil {
			t.Fatalf("decode sidecar request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"extraction":{"invoice_number":"INV-42","total":155.5,"line_items":[{"description":"Compute","amount":120.0}]},"token_count":33}`))
	}))
	t.Cleanup(sidecar.Close)

	storage := t.TempDir()
	app := fiber.New()
	RegisterAPI(app, APIConfig{
		StoragePath:               storage,
		StructuredExtractEndpoint: sidecar.URL,
	})

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("schema", `{"type":"object","properties":{"invoice_number":{"type":"string"},"total":{"type":"number"}}}`); err != nil {
		t.Fatal(err)
	}
	part, err := mw.CreateFormFile("data", "invoice.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(part, bytes.NewReader(pdfData)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/add", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, raw)
	}

	var payload struct {
		DocumentAnalysis []struct {
			Recommended     string `json:"recommended_pipeline"`
			StructuredReady bool   `json:"structured_ready"`
		} `json:"document_analysis"`
		StructuredExtractions []struct {
			Status       string          `json:"status"`
			Reason       string          `json:"reason"`
			ArtifactPath string          `json:"artifact_path"`
			Extraction   json.RawMessage `json:"extraction"`
		} `json:"structured_extractions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.DocumentAnalysis) != 1 || !payload.DocumentAnalysis[0].StructuredReady {
		t.Fatalf("document_analysis = %+v, want structured ready", payload.DocumentAnalysis)
	}
	if payload.DocumentAnalysis[0].Recommended != "lift_structured" {
		t.Fatalf("recommended = %q, want lift_structured", payload.DocumentAnalysis[0].Recommended)
	}
	if len(payload.StructuredExtractions) != 1 {
		t.Fatalf("structured_extractions len = %d, want 1", len(payload.StructuredExtractions))
	}
	got := payload.StructuredExtractions[0]
	if got.Status != "ok" {
		t.Fatalf("structured status = %q reason=%q", got.Status, got.Reason)
	}
	if got.ArtifactPath != "" {
		t.Fatalf("database-less upload returned unmanaged artifact path %q", got.ArtifactPath)
	}
	if !bytes.Contains(got.Extraction, []byte("INV-42")) {
		t.Fatalf("response extraction missing invoice number: %s", got.Extraction)
	}
	if sidecarReq.Filename != "invoice.pdf" || sidecarReq.Schema == "" {
		t.Fatalf("bad sidecar request: %+v", sidecarReq)
	}
	if matches, _ := filepath.Glob(filepath.Join(storage, "structured_extractions", "*.json")); len(matches) != 0 {
		t.Fatalf("database-less upload wrote unmanaged structured artifacts: %v", matches)
	}
}

func TestAddHandlerValidatesBatchBeforeStructuredExtraction(t *testing.T) {
	called := 0
	sidecar := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		called++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"extraction":{"value":"ok"}}`))
	}))
	defer sidecar.Close()

	app := fiber.New()
	RegisterAPI(app, APIConfig{StoragePath: t.TempDir(), StructuredExtractEndpoint: sidecar.URL})
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("schema", `{"type":"object"}`); err != nil {
		t.Fatal(err)
	}
	for _, file := range []struct {
		name string
		data []byte
	}{{"scan.pdf", []byte("%PDF-1.4\n")}, {"empty.txt", nil}} {
		part, err := mw.CreateFormFile("data", file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(file.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/add", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 422 {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d, want 422: %s", resp.StatusCode, raw)
	}
	if called != 0 {
		t.Fatalf("structured sidecar called %d times before full batch validation", called)
	}
}

func TestBeginStructuredUploadAccessRechecksCredential(t *testing.T) {
	_, cfg := documentWorkflowApp(t)
	actor := accesspkg.MetadataActor{
		Actor:      accesspkg.Actor{UserID: "alice"},
		Credential: accesspkg.MetadataCredential{Kind: "jwt", Epoch: 0, ExpiresAt: time.Now().Add(time.Hour).Unix()},
	}
	if _, err := cfg.DB.Exec("UPDATE users SET is_active=false WHERE id='alice'"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if release, err := beginStructuredUploadAccess(ctx, cfg, actor, "new-dataset", "new-dataset", "", nil); !errors.Is(err, accesspkg.ErrRevokedCredential) {
		if release != nil {
			release()
		}
		t.Fatalf("revoked credential reached structured transfer boundary: %v", err)
	}
}

func structuredUploadRequest(t *testing.T, app *fiber.App, names ...string) (int, []byte) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("schema", `{"type":"object"}`); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("datasetName", "structured"); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		part, err := mw.CreateFormFile("data", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte("%PDF-1.4\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/add", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
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

func structuredFileRequest(t *testing.T, app *fiber.App, fields map[string]string, data []byte) (int, []byte) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("schema", `{"type":"object"}`); err != nil {
		t.Fatal(err)
	}
	for key, value := range fields {
		if err := mw.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := mw.CreateFormFile("data", "report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/add", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
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

func TestStructuredUploadPartialFailurePublishesNothing(t *testing.T) {
	_, cfg := documentWorkflowApp(t)
	sidecar := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		var req structuredextract.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Filename == "second.pdf" {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"extraction":{"value":"ok"}}`))
	}))
	defer sidecar.Close()
	cfg.StructuredExtractEndpoint = sidecar.URL
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", "alice"); return c.Next() })
	RegisterAPI(app, cfg)
	status, raw := structuredUploadRequest(t, app, "first.pdf", "second.pdf")
	if status != 422 {
		t.Fatalf("partial structured failure=%d: %s", status, raw)
	}
	for _, table := range []string{"data", "dataset_data", "ingest_pending_uploads"} {
		var count int
		if err := cfg.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(cfg.StoragePath, "structured_extractions", "*.json")); len(matches) != 0 {
		t.Fatalf("partial failure left structured artifacts: %v", matches)
	}
}

func TestStructuredUploadFailureWithLocalTextPublishesNothing(t *testing.T) {
	_, cfg := documentWorkflowApp(t)
	pdf, err := os.ReadFile(filepath.Join("..", "..", "pkg", "extract", "testdata", "report.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	sidecar := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) { w.WriteHeader(500) }))
	defer sidecar.Close()
	cfg.StructuredExtractEndpoint = sidecar.URL
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", "alice"); return c.Next() })
	RegisterAPI(app, cfg)
	status, raw := structuredFileRequest(t, app, map[string]string{"datasetName": "structured"}, pdf)
	if status != 422 {
		t.Fatalf("structured failure with local text=%d: %s", status, raw)
	}
	for _, table := range []string{"data", "dataset_data", "ingest_pending_uploads"} {
		var count int
		if err := cfg.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
}

func TestBeginStructuredUploadAccessRequiresDatasetAndDocumentWrite(t *testing.T) {
	_, cfg := documentWorkflowApp(t)
	hash := strings.Repeat("a", 64)
	if _, err := cfg.DB.Exec(`INSERT INTO principals(id) VALUES('bob');
		INSERT INTO users(id,email,hashed_password,is_active) VALUES('bob','bob@example.test','unused',true);
		INSERT INTO tenants(id,name,owner_id) VALUES('a','A','alice');
		INSERT INTO user_tenant(user_id,tenant_id) VALUES('alice','a'),('bob','a');
		INSERT INTO datasets(id,name,owner_id) VALUES('owned','Owned','alice');
		INSERT INTO data(id,name,owner_id,source_revision,raw_content_hash) VALUES('source','source','alice',1,'` + hash + `');
		INSERT INTO dataset_data(dataset_id,data_id) VALUES('owned','source');
		INSERT INTO document_resources(dataset_id,data_id,tenant_id,mode) VALUES('owned','source','a','restricted');
		INSERT INTO document_grants(dataset_id,data_id,principal_kind,principal_id,role,granted_by) VALUES('owned','source','user','bob','editor','alice')`); err != nil {
		t.Fatal(err)
	}
	actor := accesspkg.MetadataActor{Actor: accesspkg.Actor{UserID: "bob", TenantID: "a"}, TrustedLocal: true}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if release, err := beginStructuredUploadAccess(ctx, cfg, actor, "owned", "Owned", "source", &ingest.SourceCAS{Revision: 1, RawContentHash: hash}); !errors.Is(err, accesspkg.ErrDocumentForbidden) {
		if release != nil {
			release()
		}
		t.Fatalf("document grant bypassed dataset write denial: %v", err)
	}
}

func TestStructuredReplacementConflictsAreRejectedBeforeSidecar(t *testing.T) {
	pdf, err := os.ReadFile(filepath.Join("..", "..", "pkg", "extract", "testdata", "report.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"stale", "dataset-name", "shared"} {
		t.Run(scenario, func(t *testing.T) {
			app, cfg := documentWorkflowApp(t)
			status, raw := uploadDocumentFixture(t, app, map[string][]byte{"source.txt": []byte("version A")})
			if status != 200 {
				t.Fatalf("initial upload=%d: %s", status, raw)
			}
			var datasetID, dataID, datasetName, hash string
			var revision int64
			if err := cfg.DB.QueryRow(`SELECT ds.id,d.id,ds.name,d.source_revision,d.raw_content_hash FROM data d JOIN dataset_data dd ON dd.data_id=d.id JOIN datasets ds ON ds.id=dd.dataset_id`).Scan(&datasetID, &dataID, &datasetName, &revision, &hash); err != nil {
				t.Fatal(err)
			}
			if scenario == "stale" {
				revision++
			}
			if scenario == "dataset-name" {
				datasetName = "wrong"
			}
			if scenario == "shared" {
				if _, err := cfg.DB.Exec("INSERT INTO datasets(id,name,owner_id) VALUES('alias','Alias','alice')"); err != nil {
					t.Fatal(err)
				}
				if _, err := cfg.DB.Exec("INSERT INTO dataset_data(dataset_id,data_id) VALUES('alias',$1)", dataID); err != nil {
					t.Fatal(err)
				}
			}
			var calls atomic.Int32
			sidecar := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
				calls.Add(1)
				w.WriteHeader(200)
			}))
			defer sidecar.Close()
			cfg.StructuredExtractEndpoint = sidecar.URL
			app = fiber.New()
			app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", "alice"); return c.Next() })
			RegisterAPI(app, cfg)
			status, raw = structuredFileRequest(t, app, map[string]string{
				"datasetId": datasetID, "datasetName": datasetName, "replace_data_id": dataID,
				"source_revision": strconv.FormatInt(revision, 10), "raw_content_hash": hash,
			}, pdf)
			if status != 409 || calls.Load() != 0 {
				t.Fatalf("%s status=%d sidecar_calls=%d body=%s", scenario, status, calls.Load(), raw)
			}
		})
	}
}

func TestStructuredUploadDatasetIdentityConflictsAreRejectedBeforeSidecar(t *testing.T) {
	pdf, err := os.ReadFile(filepath.Join("..", "..", "pkg", "extract", "testdata", "report.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"existing-id-wrong-name", "new-id-reserved-name"} {
		t.Run(scenario, func(t *testing.T) {
			app, cfg := documentWorkflowApp(t)
			status, raw := uploadDocumentFixture(t, app, map[string][]byte{"source.txt": []byte("version A")})
			if status != 200 {
				t.Fatalf("initial upload=%d: %s", status, raw)
			}
			var datasetID, datasetName string
			if err := cfg.DB.QueryRow("SELECT id,name FROM datasets").Scan(&datasetID, &datasetName); err != nil {
				t.Fatal(err)
			}
			if scenario == "existing-id-wrong-name" {
				datasetName = "wrong"
			} else {
				datasetID = "new-id"
			}
			var calls atomic.Int32
			sidecar := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
				calls.Add(1)
				w.WriteHeader(200)
			}))
			defer sidecar.Close()
			cfg.StructuredExtractEndpoint = sidecar.URL
			app = fiber.New()
			app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", "alice"); return c.Next() })
			RegisterAPI(app, cfg)
			status, raw = structuredFileRequest(t, app, map[string]string{"datasetId": datasetID, "datasetName": datasetName}, pdf)
			if status != 409 || calls.Load() != 0 {
				t.Fatalf("%s status=%d sidecar_calls=%d body=%s", scenario, status, calls.Load(), raw)
			}
		})
	}
}

func TestStructuredArtifactIsInventoriedAndPathSafe(t *testing.T) {
	_, cfg := documentWorkflowApp(t)
	pdf, err := os.ReadFile(filepath.Join("..", "..", "pkg", "extract", "testdata", "report.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	sidecar := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"extraction":{"value":"inventoried"}}`))
	}))
	defer sidecar.Close()
	cfg.StructuredExtractEndpoint = sidecar.URL
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", "alice"); return c.Next() })
	RegisterAPI(app, cfg)
	status, raw := structuredFileRequest(t, app, map[string]string{"datasetName": "structured"}, pdf)
	if status != 200 {
		t.Fatalf("structured upload=%d: %s", status, raw)
	}
	var response struct {
		Structured []struct {
			ArtifactPath string `json:"artifact_path"`
		} `json:"structured_extractions"`
	}
	if err := json.Unmarshal(raw, &response); err != nil || len(response.Structured) != 1 {
		t.Fatalf("decode response: %v body=%s", err, raw)
	}
	path := response.Structured[0].ArtifactPath
	if path == "" || strings.HasPrefix(path, "file://") || strings.HasPrefix(path, "storage://") {
		t.Fatalf("internal artifact location exposed: %q", path)
	}
	var count int
	if err := cfg.DB.QueryRow("SELECT COUNT(*) FROM document_structured_artifacts").Scan(&count); err != nil || count != 1 {
		t.Fatalf("artifact inventory count=%d err=%v", count, err)
	}
}

func TestStructuredArtifactReadGrantAndReplacementLifecycle(t *testing.T) {
	_, cfg := documentWorkflowApp(t)
	if _, err := cfg.DB.Exec(`INSERT INTO principals(id) VALUES('bob');
		INSERT INTO users(id,email,hashed_password,is_active) VALUES('bob','bob@example.test','unused',true);
		INSERT INTO tenants(id,name,owner_id) VALUES('a','A','alice');
		INSERT INTO user_tenant(user_id,tenant_id) VALUES('alice','a'),('bob','a')`); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	sidecar := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		value := "first"
		if calls.Add(1) > 2 {
			value = "second"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"extraction":{"value":"` + value + `"}}`))
	}))
	defer sidecar.Close()
	cfg.StructuredExtractEndpoint = sidecar.URL
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		user := c.Get("X-Test-User")
		if user == "" {
			user = "alice"
		}
		c.Locals("user_id", user)
		c.Locals("tenant_id", "a")
		return c.Next()
	})
	RegisterAPI(app, cfg)

	type extractionRef struct {
		ArtifactID   string `json:"artifact_id"`
		ArtifactPath string `json:"artifact_path"`
	}
	type uploadResponse struct {
		DatasetID  string          `json:"dataset_id"`
		Structured []extractionRef `json:"structured_extractions"`
	}
	pdf, err := os.ReadFile(filepath.Join("..", "..", "pkg", "extract", "testdata", "report.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	status, raw := structuredFileRequest(t, app, map[string]string{"datasetName": "structured"}, pdf)
	var first uploadResponse
	if status != 200 || json.Unmarshal(raw, &first) != nil || len(first.Structured) != 1 {
		t.Fatalf("first upload=%d: %s", status, raw)
	}
	if strings.Contains(first.Structured[0].ArtifactPath, "file://") || strings.Contains(first.Structured[0].ArtifactPath, "storage://") {
		t.Fatalf("internal artifact location exposed: %s", first.Structured[0].ArtifactPath)
	}
	var dataID, rawHash, oldLocation string
	var sourceRevision int64
	if err := cfg.DB.QueryRow(`SELECT d.id,d.source_revision,d.raw_content_hash,a.storage_location FROM data d
		JOIN document_structured_artifacts a ON a.data_id=d.id WHERE a.id=?`, first.Structured[0].ArtifactID).
		Scan(&dataID, &sourceRevision, &rawHash, &oldLocation); err != nil {
		t.Fatal(err)
	}
	artifactPath := "/datasets/" + first.DatasetID + "/data/" + dataID + "/structured-artifacts/" + first.Structured[0].ArtifactID
	get := func(user, path string) (int, []byte) {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Test-User", user)
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}
	if status, body := get("alice", artifactPath); status != 200 || string(body) != `{"value":"first"}` {
		t.Fatalf("owner read=%d: %s", status, body)
	}
	status, raw = structuredFileRequest(t, app, map[string]string{
		"datasetId": first.DatasetID, "datasetName": "structured", "replace_data_id": dataID,
		"source_revision": strconv.FormatInt(sourceRevision, 10), "raw_content_hash": rawHash,
	}, pdf)
	var unchanged uploadResponse
	if status != 200 || json.Unmarshal(raw, &unchanged) != nil || len(unchanged.Structured) != 1 || unchanged.Structured[0].ArtifactID != first.Structured[0].ArtifactID {
		t.Fatalf("unchanged replacement=%d: %s", status, raw)
	}
	if status, body := get("alice", artifactPath); status != 200 || string(body) != `{"value":"first"}` {
		t.Fatalf("unchanged replacement invalidated artifact=%d: %s", status, body)
	}

	policy := accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}
	ref := accesspkg.DocumentRef{DatasetID: first.DatasetID, DataID: dataID}
	resource, err := policy.RegisterDocument(t.Context(), accesspkg.Actor{UserID: "alice", TenantID: "a"}, ref, "a", accesspkg.DocumentRestricted)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := get("bob", artifactPath); status != 403 {
		t.Fatalf("ungranted read=%d, want 403", status)
	}
	resource, err = policy.GrantDocument(t.Context(), accesspkg.Actor{UserID: "alice", TenantID: "a"}, ref, resource.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "bob"}, accesspkg.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	if status, body := get("bob", artifactPath); status != 200 || string(body) != `{"value":"first"}` {
		t.Fatalf("granted read=%d: %s", status, body)
	}
	resource, err = policy.RevokeDocument(t.Context(), accesspkg.Actor{UserID: "alice", TenantID: "a"}, ref, resource.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := get("bob", artifactPath); status != 403 {
		t.Fatalf("revoked read=%d, want 403", status)
	}
	group, err := policy.CreateGroup(t.Context(), accesspkg.Actor{UserID: "alice", TenantID: "a"}, "a", "Artifact readers")
	if err != nil {
		t.Fatal(err)
	}
	group, err = policy.ReplaceGroupMembers(t.Context(), accesspkg.Actor{UserID: "alice", TenantID: "a"}, group.ID, group.Revision, []string{"bob"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err = policy.GrantDocument(t.Context(), accesspkg.Actor{UserID: "alice", TenantID: "a"}, ref, resource.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentGroup, ID: group.ID}, accesspkg.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	if status, body := get("bob", artifactPath); status != 200 || string(body) != `{"value":"first"}` {
		t.Fatalf("group-granted read=%d: %s", status, body)
	}
	if _, err = policy.ReplaceGroupMembers(t.Context(), accesspkg.Actor{UserID: "alice", TenantID: "a"}, group.ID, group.Revision, nil); err != nil {
		t.Fatal(err)
	}
	if status, _ := get("bob", artifactPath); status != 403 {
		t.Fatalf("removed group member read=%d, want 403", status)
	}

	status, raw = structuredFileRequest(t, app, map[string]string{
		"datasetId": first.DatasetID, "datasetName": "structured", "replace_data_id": dataID,
		"source_revision": strconv.FormatInt(sourceRevision, 10), "raw_content_hash": rawHash,
	}, pdf)
	var second uploadResponse
	if status != 200 || json.Unmarshal(raw, &second) != nil || len(second.Structured) != 1 {
		t.Fatalf("replacement=%d: %s", status, raw)
	}
	if status, _ := get("alice", artifactPath); status != 404 {
		t.Fatalf("superseded artifact read=%d, want 404", status)
	}
	newPath := "/datasets/" + first.DatasetID + "/data/" + dataID + "/structured-artifacts/" + second.Structured[0].ArtifactID
	if status, body := get("alice", newPath); status != 200 || string(body) != `{"value":"second"}` {
		t.Fatalf("replacement artifact read=%d: %s", status, body)
	}
	if _, err := os.Stat(strings.TrimPrefix(oldLocation, "file://")); !os.IsNotExist(err) {
		t.Fatalf("superseded artifact was not deleted: %v", err)
	}
	var retired int
	if err := cfg.DB.QueryRow("SELECT COUNT(*) FROM document_structured_artifacts WHERE state='retired'").Scan(&retired); err != nil || retired != 0 {
		t.Fatalf("retired cleanup rows=%d err=%v", retired, err)
	}
	var newLocation string
	if err := cfg.DB.QueryRow("SELECT storage_location FROM document_structured_artifacts WHERE id=?", second.Structured[0].ArtifactID).Scan(&newLocation); err != nil {
		t.Fatal(err)
	}
	resource, err = policy.GetDocumentResource(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	resource, err = policy.SetDocumentHold(t.Context(), accesspkg.Actor{UserID: "alice", TenantID: "a"}, ref, resource.ACLRevision, true)
	if err != nil {
		t.Fatal(err)
	}
	deleteDocument := func(etag string) (int, []byte) {
		t.Helper()
		req := httptest.NewRequest("DELETE", "/datasets/"+first.DatasetID+"/data/"+dataID, nil)
		req.Header.Set("X-Test-User", "alice")
		req.Header.Set("If-Match", etag)
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}
	if status, _ := deleteDocument(documentETag(resource)); status != 403 {
		t.Fatalf("held delete=%d, want 403", status)
	}
	if _, err := os.Stat(strings.TrimPrefix(newLocation, "file://")); err != nil {
		t.Fatalf("held delete removed artifact: %v", err)
	}
	resource, err = policy.SetDocumentHold(t.Context(), accesspkg.Actor{UserID: "alice", TenantID: "a"}, ref, resource.ACLRevision, false)
	if err != nil {
		t.Fatal(err)
	}
	if status, body := deleteDocument(documentETag(resource)); status != 200 || !bytes.Contains(body, []byte(`"artifact_cleanup_pending":false`)) {
		t.Fatalf("document delete=%d: %s", status, body)
	}
	if _, err := os.Stat(strings.TrimPrefix(newLocation, "file://")); !os.IsNotExist(err) {
		t.Fatalf("document delete left artifact: %v", err)
	}
	if err := cfg.DB.QueryRow("SELECT COUNT(*) FROM document_structured_artifacts").Scan(&retired); err != nil || retired != 0 {
		t.Fatalf("document delete inventory=%d err=%v", retired, err)
	}
}

func TestStructuredArtifactOrdinaryReingestReplacesEqualProjection(t *testing.T) {
	_, cfg := documentWorkflowApp(t)
	var calls atomic.Int32
	sidecar := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		switch calls.Add(1) {
		case 1:
			_, _ = w.Write([]byte(`{"extraction":{"a":1,"b":2}}`))
		case 2:
			_, _ = w.Write([]byte(`{"extraction":{"b":2,"a":1}}`))
		default:
			_, _ = w.Write([]byte(`{"extraction": { "a":1, "b":2 }}`))
		}
	}))
	defer sidecar.Close()
	cfg.StructuredExtractEndpoint = sidecar.URL
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", "alice"); return c.Next() })
	RegisterAPI(app, cfg)
	type uploadResponse struct {
		DatasetID      string `json:"dataset_id"`
		CleanupPending bool   `json:"artifact_cleanup_pending"`
		SourceRevision int64  `json:"source_revision"`
		Structured     []struct {
			ArtifactID string `json:"artifact_id"`
		} `json:"structured_extractions"`
	}
	pdf, err := os.ReadFile(filepath.Join("..", "..", "pkg", "extract", "testdata", "report.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	status, raw := structuredFileRequest(t, app, map[string]string{"datasetName": "structured"}, pdf)
	var first uploadResponse
	if status != 200 || json.Unmarshal(raw, &first) != nil || len(first.Structured) != 1 {
		t.Fatalf("first upload=%d: %s", status, raw)
	}
	var dataID, oldLocation string
	if err := cfg.DB.QueryRow("SELECT data_id,storage_location FROM document_structured_artifacts WHERE id=?", first.Structured[0].ArtifactID).Scan(&dataID, &oldLocation); err != nil {
		t.Fatal(err)
	}
	status, raw = structuredFileRequest(t, app, map[string]string{"datasetId": first.DatasetID, "datasetName": "structured"}, pdf)
	var second uploadResponse
	if status != 200 || json.Unmarshal(raw, &second) != nil || len(second.Structured) != 1 || second.Structured[0].ArtifactID == first.Structured[0].ArtifactID || second.CleanupPending {
		t.Fatalf("second upload=%d: %s", status, raw)
	}
	get := func(id string) (int, []byte) {
		t.Helper()
		resp, err := app.Test(httptest.NewRequest("GET", "/datasets/"+first.DatasetID+"/data/"+dataID+"/structured-artifacts/"+id, nil), -1)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}
	if status, _ := get(first.Structured[0].ArtifactID); status != 404 {
		t.Fatalf("superseded artifact read=%d, want 404", status)
	}
	if status, body := get(second.Structured[0].ArtifactID); status != 200 || string(body) != `{"b":2,"a":1}` {
		t.Fatalf("current artifact read=%d: %s", status, body)
	}
	if _, err := os.Stat(strings.TrimPrefix(oldLocation, "file://")); !os.IsNotExist(err) {
		t.Fatalf("superseded object remains: %v", err)
	}
	var count int
	if err := cfg.DB.QueryRow("SELECT COUNT(*) FROM document_structured_artifacts").Scan(&count); err != nil || count != 1 {
		t.Fatalf("artifact inventory=%d err=%v", count, err)
	}
	var revision int64
	var rawHash string
	if err := cfg.DB.QueryRow("SELECT source_revision,raw_content_hash FROM data WHERE id=?", dataID).Scan(&revision, &rawHash); err != nil {
		t.Fatal(err)
	}
	replacement := map[string]string{
		"datasetId": first.DatasetID, "datasetName": "structured", "replace_data_id": dataID,
		"source_revision": strconv.FormatInt(revision, 10), "raw_content_hash": rawHash,
	}
	status, raw = structuredFileRequest(t, app, replacement, pdf)
	var winner uploadResponse
	if status != 200 || json.Unmarshal(raw, &winner) != nil || len(winner.Structured) != 1 || winner.SourceRevision <= revision {
		t.Fatalf("artifact-only replacement=%d: %s", status, raw)
	}
	status, raw = structuredFileRequest(t, app, replacement, pdf)
	if status != 409 || calls.Load() != 3 {
		t.Fatalf("stale artifact-only replacement=%d calls=%d: %s", status, calls.Load(), raw)
	}
	if status, _ := get(second.Structured[0].ArtifactID); status != 404 {
		t.Fatalf("artifact-only superseded read=%d, want 404", status)
	}
	if status, _ := get(winner.Structured[0].ArtifactID); status != 200 {
		t.Fatalf("artifact-only winner read=%d, want 200", status)
	}
}

func TestStructuredArtifactRemoteCleanupCanRetry(t *testing.T) {
	_, cfg := documentWorkflowApp(t)
	backend := &artifactDeleteStorage{memStorage: newMemStorage()}
	cfg.FileStorage = backend
	var calls atomic.Int32
	sidecar := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		value := calls.Add(1)
		_, _ = fmt.Fprintf(w, `{"extraction":{"version":%d}}`, value)
	}))
	defer sidecar.Close()
	cfg.StructuredExtractEndpoint = sidecar.URL
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", "alice"); return c.Next() })
	RegisterAPI(app, cfg)
	pdf, err := os.ReadFile(filepath.Join("..", "..", "pkg", "extract", "testdata", "report.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	type response struct {
		DatasetID  string `json:"dataset_id"`
		Structured []struct {
			ArtifactID string `json:"artifact_id"`
		} `json:"structured_extractions"`
	}
	status, raw := structuredFileRequest(t, app, map[string]string{"datasetName": "structured"}, pdf)
	var first response
	if status != 200 || json.Unmarshal(raw, &first) != nil || len(first.Structured) != 1 {
		t.Fatalf("remote upload=%d: %s", status, raw)
	}
	var dataID, rawHash, oldLocation string
	var revision int64
	if err := cfg.DB.QueryRow(`SELECT d.id,d.source_revision,d.raw_content_hash,a.storage_location FROM data d
		JOIN document_structured_artifacts a ON a.data_id=d.id WHERE a.id=?`, first.Structured[0].ArtifactID).
		Scan(&dataID, &revision, &rawHash, &oldLocation); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(oldLocation, "storage://") {
		t.Fatalf("remote artifact location=%q", oldLocation)
	}
	backend.fail.Store(true)
	status, raw = structuredFileRequest(t, app, map[string]string{
		"datasetId": first.DatasetID, "datasetName": "structured", "replace_data_id": dataID,
		"source_revision": strconv.FormatInt(revision, 10), "raw_content_hash": rawHash,
	}, pdf)
	if status != 200 || !bytes.Contains(raw, []byte(`"artifact_cleanup_pending":true`)) {
		t.Fatalf("replacement with pending cleanup=%d: %s", status, raw)
	}
	var retired int
	if err := cfg.DB.QueryRow("SELECT COUNT(*) FROM document_structured_artifacts WHERE state='retired'").Scan(&retired); err != nil || retired != 1 {
		t.Fatalf("pending cleanup rows=%d err=%v", retired, err)
	}
	oldKey := strings.TrimPrefix(oldLocation, "storage://")
	if _, ok := backend.objects[oldKey]; !ok {
		t.Fatal("failed cleanup lost old artifact")
	}
	backend.fail.Store(false)
	if cleanupRetiredStructuredArtifacts(t.Context(), cfg, dataID) {
		t.Fatal("cleanup retry still pending")
	}
	if _, ok := backend.objects[oldKey]; ok {
		t.Fatal("cleanup retry left old artifact")
	}
	if err := cfg.DB.QueryRow("SELECT COUNT(*) FROM document_structured_artifacts WHERE state='retired'").Scan(&retired); err != nil || retired != 0 {
		t.Fatalf("cleanup retry rows=%d err=%v", retired, err)
	}
}

func TestStructuredUploadPublicDatasetIsDeniedBeforeSidecar(t *testing.T) {
	_, cfg := documentWorkflowApp(t)
	if _, err := cfg.DB.Exec("INSERT INTO datasets(id,name,owner_id) VALUES('public','structured','')"); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	sidecar := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		calls.Add(1)
		w.WriteHeader(200)
	}))
	defer sidecar.Close()
	cfg.StructuredExtractEndpoint = sidecar.URL
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", "alice"); return c.Next() })
	RegisterAPI(app, cfg)
	status, raw := structuredUploadRequest(t, app, "private.pdf")
	if status != 403 || calls.Load() != 0 {
		t.Fatalf("public dataset upload status=%d sidecar_calls=%d body=%s", status, calls.Load(), raw)
	}
}

func TestStructuredUploadTimeoutCanRetryCleanly(t *testing.T) {
	_, cfg := documentWorkflowApp(t)
	var calls atomic.Int32
	sidecar := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		if calls.Add(1) == 1 {
			time.Sleep(100 * time.Millisecond)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"extraction":{"value":"retry-ok"}}`))
	}))
	defer sidecar.Close()
	cfg.StructuredExtractEndpoint = sidecar.URL
	cfg.StructuredExtractTimeoutMs = 20
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", "alice"); return c.Next() })
	RegisterAPI(app, cfg)
	if status, raw := structuredUploadRequest(t, app, "retry.pdf"); status != 422 {
		t.Fatalf("timeout=%d: %s", status, raw)
	}
	var count int
	if err := cfg.DB.QueryRow("SELECT COUNT(*) FROM data").Scan(&count); err != nil || count != 0 {
		t.Fatalf("timeout published data=%d err=%v", count, err)
	}
	if status, raw := structuredUploadRequest(t, app, "retry.pdf"); status != 200 {
		t.Fatalf("retry=%d: %s", status, raw)
	}
	if err := cfg.DB.QueryRow("SELECT COUNT(*) FROM data").Scan(&count); err != nil || count != 1 {
		t.Fatalf("retry data=%d err=%v", count, err)
	}
}

func generateUploadPDFWithReportLab(t *testing.T) string {
	t.Helper()
	python := os.Getenv("LEVARA_TEST_PYTHON")
	if python == "" {
		var err error
		python, err = exec.LookPath("python3")
		if err != nil {
			t.Skip("python3 not available for real PDF fixture generation")
		}
	}

	out := filepath.Join(t.TempDir(), "invoice.pdf")
	script := `
import sys
try:
    from reportlab.lib import colors
    from reportlab.lib.pagesizes import letter
    from reportlab.platypus import SimpleDocTemplate, Paragraph, Spacer, Table, TableStyle
    from reportlab.lib.styles import getSampleStyleSheet
except Exception as exc:
    print(exc, file=sys.stderr)
    sys.exit(17)
doc = SimpleDocTemplate(sys.argv[1], pagesize=letter)
styles = getSampleStyleSheet()
rows = [["Item", "Qty", "Amount"], ["Compute", "2", "120.00"], ["Storage", "1", "35.50"], ["Total", "", "155.50"]]
table = Table(rows, colWidths=[220, 80, 100])
table.setStyle(TableStyle([
    ("GRID", (0, 0), (-1, -1), 1, colors.black),
    ("BACKGROUND", (0, 0), (-1, 0), colors.lightgrey),
    ("ALIGN", (1, 1), (-1, -1), "RIGHT"),
]))
doc.build([Paragraph("Invoice fixture", styles["Title"]), Spacer(1, 12), table])
`
	cmd := exec.Command(python, "-c", script, out)
	if output, err := cmd.CombinedOutput(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 17 {
			t.Skipf("reportlab not available for real PDF fixture generation: %s", output)
		}
		t.Fatalf("generate pdf: %v\n%s", err, output)
	}
	return out
}
