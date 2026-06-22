package http

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"mime/multipart"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stek0v/levara/pkg/structuredextract"
)

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
	if _, err := db.Exec(`INSERT INTO dataset_shares(id, dataset_id, user_id, role) VALUES ('share-1', 'bob-ds', 'alice', 'viewer')`); err != nil {
		t.Fatalf("seed share: %v", err)
	}

	ok, err := validateUploadDatasetID(context.Background(), db, "bob-ds", "alice")
	if err != nil {
		t.Fatalf("validateUploadDatasetID: %v", err)
	}
	if !ok {
		t.Fatal("validateUploadDatasetID denied shared dataset")
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
	if got.ArtifactPath == "" {
		t.Fatal("artifact_path is empty")
	}
	if !bytes.Contains(got.Extraction, []byte("INV-42")) {
		t.Fatalf("response extraction missing invoice number: %s", got.Extraction)
	}
	if sidecarReq.Filename != "invoice.pdf" || sidecarReq.Schema == "" {
		t.Fatalf("bad sidecar request: %+v", sidecarReq)
	}
	if matches, _ := filepath.Glob(filepath.Join(storage, "structured_extractions", "*.json")); len(matches) != 1 {
		t.Fatalf("structured artifact count = %d, want 1 (%v)", len(matches), matches)
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
