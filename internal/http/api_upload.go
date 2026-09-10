// api_upload.go — file upload + OCR endpoints, plus the cognify pipeline
// status helpers shared between cognify/search paths. Split out of api.go
// (T4). Covers:
//
//	POST /add     — multipart file upload (text, image, PDF)
//	POST /ocr     — base64 image → text via vision model
//
// PersistPipelineStatus / CheckPipelineStatus live here because addHandler
// writes pipeline_status on upload and cognifyHandler reads it via
// api_cognify.go; both import this file's symbols.
package http

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/docdetect"
	"github.com/stek0v/levara/pkg/extract"
	"github.com/stek0v/levara/pkg/fetch"
	"github.com/stek0v/levara/pkg/ingest"
	"github.com/stek0v/levara/pkg/structuredextract"
)

// ── U3: File Upload (multipart) ──

func uploadMetadataActor(c *fiber.Ctx, cfg APIConfig, ctx context.Context) accesspkg.MetadataActor {
	ctx = searchEgressContext(c, cfg, ctx)
	e, _ := ctx.Value(searchEgressKey{}).(searchEgress)
	return accesspkg.MetadataActor{Actor: e.actor, TrustedLocal: !cfg.RequireAuth && e.kind == "", Credential: accesspkg.MetadataCredential{Kind: e.kind, KeyID: e.keyID, SessionID: e.sessionID, Epoch: e.epoch, IssuedAt: e.issuedAt, ExpiresAt: e.expiresAt}}
}

func lookupUploadDatasetID(ctx context.Context, db *sql.DB, datasetName, ownerID string) string {
	if db == nil || datasetName == "" {
		return ""
	}
	var datasetID string
	if ownerID != "" {
		db.QueryRowContext(ctx,
			Q("SELECT id FROM datasets WHERE name = $1 AND owner_id = $2 LIMIT 1"), datasetName, ownerID).Scan(&datasetID)
		if datasetID != "" {
			return datasetID
		}
	}
	if ownerID != "" {
		db.QueryRowContext(ctx,
			Q("SELECT id FROM datasets WHERE name = $1 AND (owner_id = '' OR owner_id IS NULL) LIMIT 1"), datasetName).Scan(&datasetID)
		return datasetID
	}
	db.QueryRowContext(ctx,
		Q("SELECT id FROM datasets WHERE name = $1 LIMIT 1"), datasetName).Scan(&datasetID)
	return datasetID
}

func validateUploadDatasetID(ctx context.Context, db *sql.DB, datasetID, ownerID string) (bool, error) {
	return accesspkg.SQLPolicy{DB: db, Q: Q}.CanUseDatasetForUpload(ctx, datasetID, ownerID)
}

func addHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		reqCtx, cancel := apiRequestContext(c)
		defer cancel()

		form, err := uploadMultipartForm(c)
		if form != nil {
			defer form.RemoveAll()
		}
		formValue := func(key string) string {
			if form != nil && len(form.Value[key]) > 0 {
				return form.Value[key][0]
			}
			return ""
		}
		datasetName := firstNonEmptyUpload(formValue("datasetName"), c.Query("datasetName"))
		datasetID := formValue("datasetId")
		if datasetID == "" {
			datasetID = formValue("dataset_id")
		}
		schemaArg := firstNonEmptyUpload(formValue("schema"), formValue("schema_id"), formValue("structured_schema"))
		schemaProvided := strings.TrimSpace(schemaArg) != ""
		replaceDataID := firstNonEmptyUpload(formValue("replaceDataId"), formValue("replace_data_id"))
		var replacement *ingest.SourceCAS
		if replaceDataID != "" {
			revision, parseErr := strconv.ParseInt(firstNonEmptyUpload(formValue("sourceRevision"), formValue("source_revision")), 10, 64)
			hash := strings.ToLower(firstNonEmptyUpload(formValue("rawContentHash"), formValue("raw_content_hash")))
			if datasetID == "" || datasetName == "" || revision <= 0 || len(hash) != 64 {
				return c.Status(400).JSON(fiber.Map{"detail": "replacement requires dataset_id, dataset_name, source_revision and raw_content_hash"})
			}
			if _, err := hex.DecodeString(hash); parseErr != nil || err != nil {
				return c.Status(400).JSON(fiber.Map{"detail": "invalid replacement source version"})
			}
			replacement = &ingest.SourceCAS{Revision: revision, RawContentHash: hash}
		}

		if err != nil {
			if strings.HasPrefix(c.Get("Content-Type"), "multipart/") {
				return fiber.NewError(400, "invalid multipart upload")
			}
			// Try as JSON or text body
			body := c.Body()
			if len(body) > 0 {
				bodyStr := string(body)

				// Parse JSON body for dataset_name
				var tags []string
				mediaType, _, _ := mime.ParseMediaType(c.Get("Content-Type"))
				if mediaType == "application/json" {
					var jsonBody struct {
						Data             string   `json:"data"`
						DatasetName      string   `json:"dataset_name"`
						DatasetID        string   `json:"dataset_id"`
						Schema           string   `json:"schema"`
						SchemaID         string   `json:"schema_id"`
						StructuredSchema string   `json:"structured_schema"`
						Tags             []string `json:"tags"`
					}
					if err := json.Unmarshal(body, &jsonBody); err != nil || strings.TrimSpace(jsonBody.Data) == "" {
						return c.Status(400).JSON(fiber.Map{"detail": "valid JSON with non-empty data is required"})
					}
					bodyStr = jsonBody.Data
					if jsonBody.DatasetName != "" {
						datasetName = jsonBody.DatasetName
					}
					if jsonBody.DatasetID != "" {
						datasetID = jsonBody.DatasetID
					}
					schemaArg = firstNonEmptyUpload(schemaArg, jsonBody.Schema, jsonBody.SchemaID, jsonBody.StructuredSchema)
					schemaProvided = strings.TrimSpace(schemaArg) != ""
					tags = jsonBody.Tags
				}

				if datasetName == "" {
					datasetName = "default"
				}
				// URL detection: fetch content from URL
				if fetch.IsURL(strings.TrimSpace(bodyStr)) {
					var fetchedText string
					var fetchErr error
					if fetch.IsGitHubURL(bodyStr) {
						fetchedText, fetchErr = fetch.FetchGitHub(strings.TrimSpace(bodyStr))
					} else {
						fetchedText, fetchErr = fetch.FetchURL(strings.TrimSpace(bodyStr))
					}
					if fetchErr != nil || strings.TrimSpace(fetchedText) == "" {
						return c.Status(422).JSON(fiber.Map{"detail": "could not extract text from URL"})
					}
					bodyStr = fetchedText
				}
				if _, err := extract.Extract([]byte(bodyStr), "input.txt", "text/plain"); err != nil || strings.TrimSpace(bodyStr) == "" {
					return c.Status(422).JSON(fiber.Map{"detail": "non-empty UTF-8 text is required"})
				}
				ownerID, _ := c.Locals("user_id").(string)
				if ok, err := validateUploadDatasetID(reqCtx, cfg.DB, datasetID, ownerID); err != nil {
					return c.Status(500).JSON(fiber.Map{"detail": "validate dataset: " + err.Error()})
				} else if !ok {
					return c.Status(403).JSON(fiber.Map{"detail": "dataset not accessible"})
				}
				items := []ingest.Item{{Text: bodyStr, DatasetName: datasetName, OwnerID: ownerID, Tags: tags}}
				txtDsID := datasetID
				if txtDsID == "" {
					// Look up existing dataset by name before creating new
					txtDsID = lookupUploadDatasetID(reqCtx, cfg.DB, datasetName, ownerID)
					if txtDsID == "" {
						txtDsID = uuid.New().String()
					}
				}
				results, err := ingestUpload(reqCtx, cfg, items, nil, uploadMetadataActor(c, cfg, reqCtx), txtDsID, datasetName)
				if err != nil {
					return documentHTTPError(err)
				}
				return c.JSON(fiber.Map{
					"status":       "ok",
					"items":        len(results),
					"dataset_id":   txtDsID,
					"dataset_name": datasetName,
					"document_analysis": []docdetect.Analysis{{
						Format:           "text",
						SchemaProvided:   schemaProvided,
						Recommended:      docdetect.PipelinePlainIngest,
						StructuredReady:  false,
						StructuredReason: "text_body_plain_ingest",
					}},
				})
			}
			return c.Status(400).JSON(fiber.Map{"detail": "no data provided"})
		}

		if datasetName == "" {
			datasetName = "default"
		}
		files := form.File["data"]
		if len(files) == 0 {
			return c.Status(400).JSON(fiber.Map{"detail": "no files uploaded"})
		}
		if replacement != nil && len(files) != 1 {
			return c.Status(400).JSON(fiber.Map{"detail": "source replacement requires exactly one file"})
		}

		ownerID, _ := c.Locals("user_id").(string)
		if ok, err := validateUploadDatasetID(reqCtx, cfg.DB, datasetID, ownerID); err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "validate dataset: " + err.Error()})
		} else if !ok {
			return c.Status(403).JSON(fiber.Map{"detail": "dataset not accessible"})
		}
		var items []ingest.Item
		var originals []ingest.Item
		var analyses []docdetect.Analysis
		type uploadPreflight struct {
			data           []byte
			filename, text string
			analysis       docdetect.Analysis
		}
		preflight := make([]uploadPreflight, 0, len(files))
		for _, file := range files {
			f, err := file.Open()
			if err != nil {
				return c.Status(400).JSON(fiber.Map{"detail": "cannot open uploaded file", "filename": file.Filename})
			}
			data, readErr := io.ReadAll(f)
			f.Close()
			if readErr != nil {
				return c.Status(400).JSON(fiber.Map{"detail": "cannot read uploaded file", "filename": file.Filename})
			}
			// Finish every local read/extraction check before any file can leave
			// the process for structured extraction.
			result, err := extract.Extract(data, file.Filename, file.Header.Get("Content-Type"))
			extractedText := ""
			if err == nil {
				extractedText = result.Text
			}
			analysis := docdetect.AnalyzePDFForStructuredExtraction(data, file.Filename, extractedText, schemaProvided)
			analyses = append(analyses, analysis)
			if strings.TrimSpace(extractedText) == "" && !(analysis.StructuredReady && structuredEndpoint(cfg) != "") {
				detail := "no usable text extracted"
				if err != nil {
					detail = err.Error()
				}
				return c.Status(422).JSON(fiber.Map{"detail": detail, "filename": file.Filename, "document_analysis": analyses})
			}
			original := ingest.Item{FileData: data, Filename: file.Filename, OwnerID: ownerID, DatasetName: datasetName}
			if replacement != nil {
				original.ID = replaceDataID
			}
			originals = append(originals, original)
			preflight = append(preflight, uploadPreflight{data: data, filename: file.Filename, text: extractedText, analysis: analysis})
		}

		// Resolve the target before protected bytes are sent to a sidecar. A
		// foreign dataset with the same name is denied here instead of failing
		// after extraction.
		dsID := datasetID
		if dsID == "" {
			dsID = lookupUploadDatasetID(reqCtx, cfg.DB, datasetName, ownerID)
			if dsID == "" && cfg.DB != nil {
				_ = cfg.DB.QueryRowContext(reqCtx, Q("SELECT id FROM datasets WHERE name = $1 LIMIT 1"), datasetName).Scan(&dsID)
			}
			if dsID == "" {
				dsID = uuid.New().String()
			}
		}
		needsStructured := false
		for _, p := range preflight {
			needsStructured = needsStructured || (p.analysis.StructuredReady && structuredEndpoint(cfg) != "")
		}
		releaseStructured := func() {}
		releaseStructuredNow := func() {
			releaseStructured()
			releaseStructured = func() {}
		}
		defer releaseStructuredNow()
		if needsStructured {
			release, err := beginStructuredUploadAccess(reqCtx, cfg, uploadMetadataActor(c, cfg, reqCtx), dsID, datasetName, replaceDataID, replacement)
			if err != nil {
				return documentHTTPError(err)
			}
			releaseStructured = release
		}
		var structuredResults []structuredUploadResult
		for _, p := range preflight {
			structured := maybeRunStructuredExtraction(reqCtx, cfg, p.data, p.filename, schemaArg, p.analysis)
			structuredResults = append(structuredResults, structured)
			if p.analysis.StructuredReady && structuredEndpoint(cfg) != "" && structured.Status != "ok" {
				releaseStructuredNow()
				return c.Status(422).JSON(fiber.Map{"detail": "structured extraction failed", "filename": p.filename, "document_analysis": analyses, "structured_extractions": structuredResults})
			}
			if structured.Status == "ok" && structured.ProjectionText != "" {
				items = append(items, ingest.Item{
					Text:               structured.ProjectionText,
					StructuredArtifact: bytes.Clone(structured.Extraction),
					Filename:           p.filename + ".structured.md",
					DatasetName:        datasetName,
					OwnerID:            ownerID,
					Tags:               []string{"structured_extraction", "lift_structured"},
				})
				continue
			}
			if strings.TrimSpace(p.text) != "" {
				items = append(items, ingest.Item{
					Text:        p.text,
					Filename:    p.filename,
					DatasetName: datasetName,
					OwnerID:     ownerID,
				})
			} else {
				releaseStructuredNow()
				return c.Status(422).JSON(fiber.Map{"detail": "no usable text extracted", "filename": p.filename, "document_analysis": analyses, "structured_extractions": structuredResults})
			}
		}
		releaseStructuredNow()

		var results []ingest.Result
		if replacement != nil {
			results, err = replaceUpload(reqCtx, cfg, items, originals, uploadMetadataActor(c, cfg, reqCtx), dsID, datasetName, *replacement)
		} else {
			results, err = ingestUpload(reqCtx, cfg, items, originals, uploadMetadataActor(c, cfg, reqCtx), dsID, datasetName)
		}
		if err != nil {
			return documentHTTPError(err)
		}
		for i := range structuredResults {
			if i >= len(results) || results[i].StructuredArtifactID == "" {
				continue
			}
			structuredResults[i].ArtifactID = results[i].StructuredArtifactID
			structuredResults[i].ArtifactPath = fmt.Sprintf("%s/api/v1/datasets/%s/data/%s/structured-artifacts/%s",
				c.BaseURL(), url.PathEscape(dsID), url.PathEscape(results[i].ID), url.PathEscape(results[i].StructuredArtifactID))
		}
		response := fiber.Map{
			"status":                 "ok",
			"items":                  len(results),
			"files":                  len(files),
			"dataset_id":             dsID,
			"dataset_name":           datasetName,
			"document_analysis":      analyses,
			"structured_extractions": structuredResults,
		}
		cleanupPending := false
		cleaned := map[string]bool{}
		for _, result := range results {
			if result.ID != "" && !cleaned[result.ID] {
				cleaned[result.ID] = true
				cleanupPending = cleanupRetiredStructuredArtifacts(reqCtx, cfg, result.ID) || cleanupPending
			}
		}
		response["artifact_cleanup_pending"] = cleanupPending
		if replacement != nil {
			response["replaced"] = true
			response["document_id"] = replaceDataID
			response["source_revision"] = results[0].SourceRevision
			response["raw_content_hash"] = results[0].ContentHash
		}
		return c.JSON(response)
	}
}

func replaceUpload(ctx context.Context, cfg APIConfig, items, originals []ingest.Item, actor accesspkg.MetadataActor, datasetID, datasetName string, expected ingest.SourceCAS) ([]ingest.Result, error) {
	if cfg.DB == nil {
		return nil, accesspkg.ErrDocumentInvalid
	}
	w, err := ingest.NewMetadataWriterForStorage(cfg.DB, cfg.FileStorage)
	if err != nil {
		return nil, err
	}
	results, _, err := w.ReplaceAuthorized(ctx, items, originals, cfg.StoragePath, cfg.FileStorage, actor, datasetID, datasetName, expected)
	return results, err
}

// beginStructuredUploadAccess holds the same bounded SQL read fence used for
// protected egress while the sidecar request is in flight.
func beginStructuredUploadAccess(ctx context.Context, cfg APIConfig, actor accesspkg.MetadataActor, datasetID, datasetName, dataID string, expected *ingest.SourceCAS) (func(), error) {
	if cfg.DB == nil {
		if cfg.RequireAuth {
			return nil, accesspkg.ErrDocumentForbidden
		}
		return func() {}, nil
	}
	p, release, err := (accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}).BeginReadFence(ctx, GetDBProvider() == DBSQLite)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (func(), error) { release(); return nil, err }
	if !accesspkg.APIKeyAllows(actor.APIKeyPermissions, accesspkg.ActionWrite) {
		return fail(accesspkg.ErrDocumentForbidden)
	}
	if !actor.TrustedLocal {
		c := actor.Credential
		if err := p.RecheckCredential(ctx, actor.UserID, c.Kind, c.KeyID, actor.APIKeyPermissions, c.SessionID, c.Epoch, c.IssuedAt, c.ExpiresAt); err != nil {
			return fail(err)
		}
	}
	if actor.TenantID != "" {
		member, err := p.IsTenantMember(ctx, actor.UserID, actor.TenantID)
		if err != nil {
			return fail(err)
		}
		if !member {
			return fail(accesspkg.ErrDocumentForbidden)
		}
	}
	decision, err := p.AuthorizeDataset(ctx, actor.Actor, datasetID, accesspkg.ActionWrite)
	if err != nil {
		return fail(err)
	}
	if !decision.Allowed {
		if dataID != "" {
			return fail(accesspkg.ErrDocumentForbidden)
		}
		canCreate, err := p.CanUseDatasetForUpload(ctx, datasetID, actor.UserID)
		if err != nil {
			return fail(err)
		}
		if !canCreate || decision.Reason != "denied" {
			return fail(accesspkg.ErrDocumentForbidden)
		}
	}
	if err := p.ValidateDatasetUploadTarget(ctx, datasetID, datasetName); err != nil {
		return fail(err)
	}
	if dataID != "" {
		ref := accesspkg.DocumentRef{DatasetID: datasetID, DataID: dataID}
		decision, err := p.AuthorizeDocument(ctx, actor.Actor, ref, accesspkg.ActionWrite)
		if err != nil {
			return fail(err)
		}
		if !decision.Allowed {
			return fail(accesspkg.ErrDocumentForbidden)
		}
		if expected == nil {
			return fail(accesspkg.ErrDocumentInvalid)
		}
		if err := p.ValidateSourceReplacement(ctx, ref, datasetName, expected.Revision, expected.RawContentHash); err != nil {
			return fail(err)
		}
	}
	return release, nil
}

func ingestUpload(ctx context.Context, cfg APIConfig, items, originals []ingest.Item, actor accesspkg.MetadataActor, datasetID, datasetName string) ([]ingest.Result, error) {
	if cfg.DB != nil {
		w, err := ingest.NewMetadataWriterForStorage(cfg.DB, cfg.FileStorage)
		if err != nil {
			return nil, err
		}
		results, _, err := w.IngestAuthorized(ctx, items, originals, cfg.StoragePath, cfg.FileStorage, actor, datasetID, datasetName)
		return results, err
	}
	if cfg.RequireAuth {
		return nil, accesspkg.ErrDocumentForbidden
	}
	var sources []ingest.Result
	var err error
	if originals != nil {
		sources, err = ingest.IngestStored(ctx, originals, cfg.StoragePath, cfg.FileStorage)
		if err != nil {
			return nil, err
		}
		for i := range items {
			items[i].ID = sources[i].ID
		}
	}
	results, err := ingest.IngestStored(ctx, items, cfg.StoragePath, cfg.FileStorage)
	if err != nil {
		return nil, err
	}
	for i, source := range sources {
		results[i].OriginalFilePath, results[i].OriginalContentHash, results[i].OriginalFileSize = source.FilePath, source.ContentHash, source.FileSize
		results[i].Name, results[i].Extension, results[i].MimeType = source.Name, source.Extension, source.MimeType
	}
	return results, nil
}

func firstNonEmptyUpload(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

type structuredUploadResult struct {
	Filename       string          `json:"filename,omitempty"`
	Status         string          `json:"status"`
	Reason         string          `json:"reason,omitempty"`
	ArtifactID     string          `json:"artifact_id,omitempty"`
	ArtifactPath   string          `json:"artifact_path,omitempty"`
	Extraction     json.RawMessage `json:"extraction,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	TokenCount     int             `json:"token_count,omitempty"`
	ProjectionText string          `json:"-"`
}

func maybeRunStructuredExtraction(ctx context.Context, cfg APIConfig, data []byte, filename, schema string, analysis docdetect.Analysis) structuredUploadResult {
	out := structuredUploadResult{Filename: filename, Status: "skipped"}
	if !analysis.StructuredReady {
		out.Reason = analysis.StructuredReason
		return out
	}
	endpoint := structuredEndpoint(cfg)
	if endpoint == "" {
		out.Reason = "structured_endpoint_not_configured"
		return out
	}

	timeout := time.Duration(cfg.StructuredExtractTimeoutMs) * time.Millisecond
	client := structuredextract.Client{Endpoint: endpoint, Timeout: timeout}
	res, err := client.Extract(ctx, data, filename, schema, analysis.TableSignal.PagesWithTable)
	if err != nil {
		out.Status = "failed"
		out.Reason = err.Error()
		return out
	}
	if res.Error || len(res.Extraction) == 0 {
		out.Status = "failed"
		out.Reason = "empty_or_error_extraction"
		out.Metadata = res.Metadata
		out.TokenCount = res.TokenCount
		return out
	}

	out.Status = "ok"
	out.Extraction = res.Extraction
	out.Metadata = res.Metadata
	out.TokenCount = res.TokenCount
	out.ProjectionText = structuredextract.ProjectionMarkdown(filename, res.Extraction)
	if out.ProjectionText == "" {
		out.Status = "failed"
		out.Reason = "projection_failed"
	}
	return out
}

func structuredEndpoint(cfg APIConfig) string {
	if strings.TrimSpace(cfg.StructuredExtractEndpoint) != "" {
		return strings.TrimSpace(cfg.StructuredExtractEndpoint)
	}
	if v := strings.TrimSpace(os.Getenv("STRUCTURED_EXTRACT_ENDPOINT")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("LIFT_ENDPOINT"))
}

// The server disables fasthttp's automatic multipart parsing. Retain file
// parts in the already bounded request memory instead of its plaintext temp
// files; this also applies to OCR uploads before storage encryption begins.
func uploadMultipartForm(c *fiber.Ctx) (*multipart.Form, error) {
	media, params, err := mime.ParseMediaType(c.Get("Content-Type"))
	if err != nil || media != "multipart/form-data" || params["boundary"] == "" {
		return nil, fmt.Errorf("multipart form required")
	}
	body := c.Body()
	return multipart.NewReader(bytes.NewReader(body), params["boundary"]).ReadForm(int64(len(body)))
}

// ── U4: Cognify ──
//
// Background run status lives in pkg/runreg. F-4 wave 3j-prep moved the
// package-level sync.Map + pipelineRunStatus type into that package so the
// cognify tools in pkg/mcp can share it without importing internal/http.

type pipelineAttemptSource struct {
	datasetID, dataID string
	sourceRevision    int64
	rawContentHash    string
}

func claimPipelineAttempts(ctx context.Context, db *sql.DB, sources []pipelineAttemptSource, collection, attemptID string) error {
	if db == nil {
		return nil
	}
	if len(sources) == 0 || collection == "" || attemptID == "" {
		return accesspkg.ErrDocumentInvalid
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statusJSON := pipelineStatusJSON("RUNNING", 0, 0, 0, 0)
	for _, source := range sources {
		if source.datasetID == "" || source.dataID == "" || source.sourceRevision <= 0 || len(source.rawContentHash) != 64 {
			return accesspkg.ErrDocumentInvalid
		}
		query, args := QArgs(`INSERT INTO document_pipeline_statuses(
			dataset_id,data_id,collection_name,source_revision,raw_content_hash,attempt_id,pipeline_state,status_json)
			SELECT $1,$2,$3,$4,$5,$6,'RUNNING',$7 FROM data d JOIN dataset_data dd ON dd.data_id=d.id
			WHERE d.id=$2 AND dd.dataset_id=$1 AND d.source_revision=$4 AND LOWER(d.raw_content_hash)=$5
			ON CONFLICT(dataset_id,data_id,collection_name) DO UPDATE SET
			source_revision=EXCLUDED.source_revision,raw_content_hash=EXCLUDED.raw_content_hash,
			attempt_id=EXCLUDED.attempt_id,
			pipeline_state=CASE WHEN document_pipeline_statuses.source_revision=EXCLUDED.source_revision
				AND LOWER(document_pipeline_statuses.raw_content_hash)=EXCLUDED.raw_content_hash
				AND document_pipeline_statuses.pipeline_state='COMPLETED'
				THEN document_pipeline_statuses.pipeline_state ELSE 'RUNNING' END,
			status_json=CASE WHEN document_pipeline_statuses.source_revision=EXCLUDED.source_revision
				AND LOWER(document_pipeline_statuses.raw_content_hash)=EXCLUDED.raw_content_hash
				AND document_pipeline_statuses.pipeline_state='COMPLETED'
				THEN document_pipeline_statuses.status_json ELSE EXCLUDED.status_json END,
			updated_at=CURRENT_TIMESTAMP`, source.datasetID, source.dataID, collection, source.sourceRevision, source.rawContentHash, attemptID, statusJSON)
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return accesspkg.ErrDocumentVersionConflict
		}
	}
	return tx.Commit()
}

func pipelineStatusJSON(status string, chunks, entities, edges int, elapsedMs int64) string {
	raw, _ := json.Marshal(map[string]any{
		"status": status, "chunks": chunks, "entities": entities, "edges": edges,
		"elapsed": elapsedMs, "at": time.Now().UTC().Format(time.RFC3339),
	})
	return string(raw)
}

// PersistPipelineStatus writes operational status for one exact document
// reference, source version and server-issued attempt. data.pipeline_status
// remains a compatibility mirror only for data that belongs to one dataset.
func PersistPipelineStatus(db *sql.DB, datasetID, dataID, collection, status string, sourceRevision int64, rawContentHash string, chunks, entities, edges int, elapsedMs int64, attemptIDs ...string) {
	attemptID := ""
	if len(attemptIDs) > 0 {
		attemptID = attemptIDs[0]
	}
	if err := persistPipelineStatus(db, datasetID, dataID, collection, status, sourceRevision, rawContentHash, chunks, entities, edges, elapsedMs, attemptID); err != nil && !errors.Is(err, accesspkg.ErrDocumentVersionConflict) {
		log.Printf("[pipeline] persist status error: %v", err)
	}
}

func persistPipelineStatus(db *sql.DB, datasetID, dataID, collection, status string, sourceRevision int64, rawContentHash string, chunks, entities, edges int, elapsedMs int64, attemptID string) error {
	if db == nil || datasetID == "" || dataID == "" || collection == "" || sourceRevision <= 0 || len(rawContentHash) != 64 {
		if db == nil {
			return nil
		}
		return accesspkg.ErrDocumentInvalid
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	statusJSON := pipelineStatusJSON(status, chunks, entities, edges, elapsedMs)
	for attempt := 0; attempt < 3; attempt++ {
		var priorRevision int64
		var priorHash, priorAttempt, priorState, priorJSON string
		err := db.QueryRowContext(ctx, Q(`SELECT source_revision,raw_content_hash,attempt_id,pipeline_state,status_json
			FROM document_pipeline_statuses WHERE dataset_id=$1 AND data_id=$2 AND collection_name=$3`), datasetID, dataID, collection).
			Scan(&priorRevision, &priorHash, &priorAttempt, &priorState, &priorJSON)
		if errors.Is(err, sql.ErrNoRows) {
			if attemptID != "" {
				return accesspkg.ErrDocumentVersionConflict
			}
			query, args := QArgs(`INSERT INTO document_pipeline_statuses(
				dataset_id,data_id,collection_name,source_revision,raw_content_hash,attempt_id,pipeline_state,status_json)
				SELECT $1,$2,$3,$4,$5,'',$6,$7 FROM data d JOIN dataset_data dd ON dd.data_id=d.id
				WHERE d.id=$2 AND dd.dataset_id=$1 AND d.source_revision=$4 AND LOWER(d.raw_content_hash)=$5
				ON CONFLICT(dataset_id,data_id,collection_name) DO NOTHING`, datasetID, dataID, collection, sourceRevision, rawContentHash, status, statusJSON)
			result, insertErr := db.ExecContext(ctx, query, args...)
			if insertErr != nil {
				return insertErr
			}
			if n, rowsErr := result.RowsAffected(); rowsErr == nil && n == 1 {
				mirrorSingleDatasetPipelineStatus(ctx, db, datasetID, dataID, collection, []byte(statusJSON), sourceRevision, rawContentHash)
				return nil
			}
			continue
		}
		if err != nil {
			return err
		}
		priorHash = strings.ToLower(priorHash)
		if attemptID != "" && (priorAttempt != attemptID || priorRevision != sourceRevision || priorHash != rawContentHash) {
			return accesspkg.ErrDocumentVersionConflict
		}
		if priorRevision == sourceRevision && priorHash == rawContentHash && priorState == "COMPLETED" && status != "COMPLETED" {
			return nil
		}
		query, args := QArgs(`UPDATE document_pipeline_statuses SET
			source_revision=$1,raw_content_hash=$2,attempt_id=$3,pipeline_state=$4,status_json=$5,updated_at=CURRENT_TIMESTAMP
			WHERE dataset_id=$6 AND data_id=$7 AND collection_name=$8
			AND source_revision=$9 AND LOWER(raw_content_hash)=$10 AND attempt_id=$11 AND pipeline_state=$12 AND status_json=$13
			AND EXISTS (SELECT 1 FROM data d JOIN dataset_data dd ON dd.data_id=d.id
				WHERE d.id=$7 AND dd.dataset_id=$6 AND d.source_revision=$1 AND LOWER(d.raw_content_hash)=$2)`,
			sourceRevision, rawContentHash, attemptID, status, statusJSON, datasetID, dataID, collection,
			priorRevision, priorHash, priorAttempt, priorState, priorJSON)
		result, err := db.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err == nil && n == 1 {
			mirrorSingleDatasetPipelineStatus(ctx, db, datasetID, dataID, collection, []byte(statusJSON), sourceRevision, rawContentHash)
			return nil
		}
	}
	return accesspkg.ErrDocumentVersionConflict
}

func mirrorSingleDatasetPipelineStatus(ctx context.Context, db *sql.DB, datasetID, dataID, collection string, statusJSON []byte, sourceRevision int64, rawContentHash string) {
	var associations int
	if err := db.QueryRowContext(ctx, Q(`SELECT COUNT(*) FROM dataset_data WHERE data_id=$1`), dataID).Scan(&associations); err != nil || associations != 1 {
		return
	}
	for attempt := 0; attempt < 3; attempt++ {
		var current string
		if err := db.QueryRowContext(ctx, Q(`SELECT COALESCE(pipeline_status,'') FROM data
			WHERE id=$1 AND source_revision=$2 AND LOWER(raw_content_hash)=$3`), dataID, sourceRevision, rawContentHash).Scan(&current); err != nil {
			return
		}
		state := map[string]json.RawMessage{}
		if current != "" && current != "{}" && json.Unmarshal([]byte(current), &state) != nil {
			return
		}
		state[collection] = statusJSON
		merged, _ := json.Marshal(state)
		result, err := db.ExecContext(ctx, Q(`UPDATE data SET pipeline_status=$1,updated_at=CURRENT_TIMESTAMP
			WHERE id=$2 AND source_revision=$3 AND LOWER(raw_content_hash)=$4 AND COALESCE(pipeline_status,'')=$5
			AND EXISTS (SELECT 1 FROM dataset_data dd WHERE dd.dataset_id=$6 AND dd.data_id=data.id)`),
			string(merged), dataID, sourceRevision, rawContentHash, current, datasetID)
		if err != nil {
			return
		}
		if n, err := result.RowsAffected(); err == nil && n == 1 {
			return
		}
	}
}

func pipelineStatusForDocument(ctx context.Context, db *sql.DB, datasetID, dataID, _ string) (string, error) {
	rows, err := db.QueryContext(ctx, Q(`SELECT s.collection_name,s.status_json,
		CASE WHEN s.source_revision=d.source_revision AND s.raw_content_hash=LOWER(d.raw_content_hash) THEN 1 ELSE 0 END
		FROM document_pipeline_statuses s
		JOIN dataset_data dd ON dd.dataset_id=s.dataset_id AND dd.data_id=s.data_id
		JOIN data d ON d.id=s.data_id
		WHERE s.dataset_id=$1 AND s.data_id=$2`), datasetID, dataID)
	if err != nil {
		return "", err
	}
	state := map[string]json.RawMessage{}
	for rows.Next() {
		var collection string
		var status string
		var current int
		if err := rows.Scan(&collection, &status, &current); err != nil {
			rows.Close()
			return "", err
		}
		if current == 1 {
			state[collection] = json.RawMessage(status)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	if len(state) > 0 {
		encoded, err := json.Marshal(state)
		return string(encoded), err
	}
	return "{}", nil
}

// CheckPipelineStatus returns true if ALL data items in dataset are already processed for this collection.
// Returns false if any item has empty pipeline_status or missing collection entry.
func CheckPipelineStatus(db *sql.DB, datasetID, collection string) bool {
	if db == nil || datasetID == "" {
		return false
	}
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, Q(`
		SELECT id,pipeline_status FROM data
		WHERE id IN (SELECT data_id FROM dataset_data WHERE dataset_id = $1)
	`), datasetID)
	if err != nil {
		return false
	}
	defer rows.Close()
	type documentStatus struct{ id, legacy string }
	documents := []documentStatus{}
	for rows.Next() {
		var id string
		var raw sql.NullString
		if err := rows.Scan(&id, &raw); err != nil || !raw.Valid {
			rows.Close()
			return false
		}
		documents = append(documents, documentStatus{id: id, legacy: raw.String})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false
	}
	rows.Close()
	for _, document := range documents {
		raw, err := pipelineStatusForDocument(ctx, db, datasetID, document.id, document.legacy)
		if err != nil {
			return false
		}
		var statuses map[string]struct {
			Status string `json:"status"`
		}
		if json.Unmarshal([]byte(raw), &statuses) != nil || statuses[collection].Status != "COMPLETED" {
			return false
		}
	}
	return len(documents) > 0
}

func ocrHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Accept JSON with base64 image or multipart file
		var imgData []byte
		var filename string

		form, err := uploadMultipartForm(c)
		if form != nil {
			defer form.RemoveAll()
		}
		if err == nil {
			files := form.File["image"]
			if len(files) == 0 {
				files = form.File["data"]
			}
			if len(files) > 0 {
				f, err := files[0].Open()
				if err == nil {
					imgData, _ = io.ReadAll(f)
					f.Close()
					filename = files[0].Filename
				}
			}
		}

		if len(imgData) == 0 {
			var req struct {
				Image    string `json:"image"` // base64
				Filename string `json:"filename"`
			}
			if c.BodyParser(&req) == nil && req.Image != "" {
				decoded, decErr := base64Decode(req.Image)
				if decErr != nil {
					return c.Status(400).JSON(fiber.Map{"detail": "invalid base64 image"})
				}
				imgData = decoded
				filename = req.Filename
			}
		}

		if len(imgData) == 0 {
			return c.Status(400).JSON(fiber.Map{"detail": "no image provided (use 'image' field with base64 or multipart 'image'/'data')"})
		}

		result, err := extract.Extract(imgData, filename, "")
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": err.Error()})
		}

		return c.JSON(fiber.Map{
			"text":       result.Text,
			"format":     result.Format,
			"extract_ms": result.ExtractMs,
		})
	}
}

func base64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}
