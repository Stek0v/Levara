package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	httpapi "github.com/stek0v/levara/internal/http"
	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/bm25"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/ingest"
	"github.com/stek0v/levara/pkg/workspace"
)

// The application is mounted only on disposable restored resources; this uses
// the existing private proof seam, not a production restore/deployment API.
func TestVerifiedNativeWorkspaceApplication(t *testing.T) {
	for _, provider := range []string{"sqlite", "postgres"} {
		t.Run(provider, func(t *testing.T) {
			ctx := context.Background()
			o := verifiedFixture(t)
			o.Timeout = 3 * time.Minute
			prior := httpapi.GetDBProvider()
			defer httpapi.SetDBProvider(prior)
			defer ingest.SetSQLiteMode(false)
			dbPath := filepath.Join(o.DataDir, "levara.db")
			if err := os.Remove(dbPath); err != nil {
				t.Fatal(err)
			}
			driver, source := "sqlite3", dbPath
			if provider == "postgres" {
				dsn := os.Getenv("LEVARA_TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("isolated test PostgreSQL DSN required")
				}
				admin, err := sql.Open("pgx", dsn)
				if err != nil {
					t.Fatal(err)
				}
				defer admin.Close()
				name := fmt.Sprintf("backup_native_%d", time.Now().UnixNano())
				if _, err := admin.Exec("CREATE DATABASE " + quoteSQL(name)); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if _, err := admin.Exec("DROP DATABASE " + quoteSQL(name) + " WITH (FORCE)"); err != nil {
						t.Errorf("drop fixture: %v", err)
					}
				}()
				u, err := url.Parse(dsn)
				if err != nil {
					t.Fatal(err)
				}
				u.Path = "/" + name
				source = u.String()
				driver = "pgx"
				o.DBProvider, o.PostgresDSN, o.PostgresBinDir = "postgres", source, os.Getenv("LEVARA_TEST_POSTGRES_BIN")
				httpapi.SetDBProvider(httpapi.DBPostgres)
			} else {
				httpapi.SetDBProvider(httpapi.DBSQLite)
			}
			ingest.SetSQLiteMode(provider == "sqlite")
			db, err := sql.Open(driver, source)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			if err := httpapi.MigrateSchema(db); err != nil {
				t.Fatal(err)
			}
			if err := access.EnsureIdentitySchema(ctx, db, httpapi.Q); err != nil {
				t.Fatal(err)
			}
			if err := access.EnsureBrowserSessionSchema(ctx, db, httpapi.Q); err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{
				"INSERT INTO principals(id,type) VALUES('native-owner','user')",
				"INSERT INTO users(id,email,hashed_password) VALUES('native-owner','native@backup.invalid','locked')",
				"INSERT INTO datasets(id,name,owner_id) VALUES('native-project','Native project','native-owner')",
				"INSERT INTO api_keys(id,key_hash,user_id,permissions) VALUES('native-key','native-fixture-hash','native-owner','read-write')",
			} {
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			proof := access.MetadataActor{Actor: access.Actor{UserID: "native-owner", APIKeyPermissions: "read-write", AuthMethod: "api_key"}, Credential: access.MetadataCredential{Kind: "api_key", KeyID: "native-key"}}
			writer := ingest.NewMetadataWriterFromDB(db)
			raw, original, structured := []byte("native backup source evidence"), []byte{0, 1, 255, 0, 13}, []byte(`{"native":"structured evidence"}`)
			results, count, err := writer.IngestAuthorized(ctx, []ingest.Item{{ID: "native-source", Text: string(raw), StructuredArtifact: structured}}, []ingest.Item{{ID: "native-source", Filename: "native.bin", FileData: original}}, o.UploadsPath, nil, proof, "native-project", "Native project")
			closeWriterErr := writer.Close()
			if err != nil || closeWriterErr != nil || count != 1 || len(results) != 1 {
				t.Fatalf("native ingestion: count=%d results=%v err=%v close=%v", count, results, err, closeWriterErr)
			}
			var rawLocation, originalLocation, structuredLocation string
			if err := db.QueryRow("SELECT raw_data_location,original_data_location FROM data WHERE id='native-source'").Scan(&rawLocation, &originalLocation); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow("SELECT storage_location FROM document_structured_artifacts WHERE data_id='native-source' AND state='active'").Scan(&structuredLocation); err != nil {
				t.Fatal(err)
			}
			expectedSource := map[string][]byte{rawLocation: raw, originalLocation: original, structuredLocation: structured}

			embedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Input []string `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					http.Error(w, "invalid embed request", 400)
					return
				}
				data := make([]map[string]any, len(request.Input))
				for i := range request.Input {
					data[i] = map[string]any{"index": i, "embedding": []float32{1, 0}}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			}))
			defer embedServer.Close()
			cm, err := store.NewCollectionManager(2, filepath.Join(o.DataDir, o.NodeID))
			if err != nil {
				t.Fatal(err)
			}
			defer cm.Close()
			lexical := bm25.NewIndexRegistry()
			snapshots := bm25.NewSnapshotStore(filepath.Join(o.DataDir, "bm25"))
			cfg := httpapi.APIConfig{DB: db, StoragePath: o.UploadsPath, WorkspacePath: o.WorkspacePath, Collections: cm, BM25Indexes: lexical, BM25Store: snapshots, EmbedEndpoint: embedServer.URL, EmbedModel: "native-backup", EmbedClient: embed.NewClient(embedServer.URL, "native-backup", 16, 1)}
			app := fiber.New()
			httpapi.RegisterWorkspaceAPI(app, cfg)
			request := func(app *fiber.App, method, path string, payload any) map[string]any {
				t.Helper()
				var body io.Reader
				if payload != nil {
					encoded, err := json.Marshal(payload)
					if err != nil {
						t.Fatal(err)
					}
					body = bytes.NewReader(encoded)
				}
				req := httptest.NewRequest(method, path, body)
				if payload != nil {
					req.Header.Set("Content-Type", "application/json")
				}
				response, err := app.Test(req, 30000)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				encoded, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != 200 {
					t.Fatalf("%s %s status=%d body=%s", method, path, response.StatusCode, encoded)
				}
				var decoded map[string]any
				if err := json.Unmarshal(encoded, &decoded); err != nil {
					t.Fatal(err)
				}
				return decoded
			}
			branchRoot := workspace.ProjectRoot(o.WorkspacePath, "native-project", "main")
			if err := os.MkdirAll(branchRoot, 0700); err != nil {
				t.Fatal(err)
			}
			oldText := "# Old publication\n\nObsolete earlier content.\n"
			writeVerifiedFile(t, filepath.Join(branchRoot, "guide.md"), []byte(oldText))
			request(app, "POST", "/workspace/index", map[string]any{"project_id": "native-project", "branch": "main", "generation": "native-old", "path": "guide.md", "text": oldText, "activate_generation": true, "min_chunk_chars": 1})
			text := "# Restoration\n\nQuasar recovery citation proves restored searchable bytes.\n"
			writeVerifiedFile(t, filepath.Join(branchRoot, "guide.md"), []byte(text))
			writeVerifiedFile(t, filepath.Join(branchRoot, "empty.md"), nil)
			request(app, "POST", "/workspace/reindex", map[string]any{"project_id": "native-project", "branch": "main", "generation": "native-current", "paths": []string{"guide.md", "empty.md"}, "activate_generation": true, "min_chunk_chars": 1})
			manifest, err := workspace.LoadManifest(workspace.ManifestPath(o.WorkspacePath, "native-project", "main"))
			if err != nil {
				t.Fatal(err)
			}
			if manifest.ActiveGeneration != "native-current" || len(manifest.Files["native-current"]) != 2 || manifest.Generations["native-old"].Status != workspace.GenerationGCPending {
				t.Fatalf("native publication missing inventory/history: %+v", manifest)
			}
			if err := snapshots.SaveAll(lexical.Snapshot()); err != nil {
				t.Fatal(err)
			}
			if err := cm.Close(); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			receipt, err := CreateVerifiedBackup(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			for _, root := range []string{o.DataDir, o.WorkspacePath, o.UploadsPath} {
				if err := os.Rename(root, root+"-unavailable"); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatalf("original root remains available: %s %v", root, err)
				}
			}
			if _, err := VerifyArchive(ctx, receipt.Archive, o); err != nil {
				t.Fatal(err)
			}
			opts, err := verifiedDefaults(o)
			if err != nil {
				t.Fatal(err)
			}
			sandbox := t.TempDir()
			archived, _, _, err := extractVerified(ctx, receipt.Archive, sandbox, opts)
			if err != nil {
				t.Fatal(err)
			}
			artifact := filepath.Join(sandbox, "sql", map[bool]string{true: "sqlite.db", false: "postgres.dump"}[provider == "sqlite"])
			restoredDB, closeDB, err := snapshotSQLForProof(ctx, opts, artifact)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := closeDB(); err != nil {
					t.Errorf("close restored SQL: %v", err)
				}
			}()
			if err := rebaseAndCheckReferences(ctx, restoredDB, archived, sandbox, opts.MaxRows); err != nil {
				t.Fatal(err)
			}
			indexes, err := verifyIndexes(ctx, sandbox, archived, opts)
			if err != nil || !sameJSON(indexes, archived.Indexes) {
				t.Fatalf("restored index proof: %v", err)
			}
			for location, want := range expectedSource {
				old := strings.TrimPrefix(location, "file://")
				relative, err := filepath.Rel(o.UploadsPath, old)
				if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					t.Fatalf("source outside uploads: %s %v", location, err)
				}
				got, err := os.ReadFile(filepath.Join(sandbox, "uploads", relative))
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("native source bytes lost: %s %v", location, err)
				}
			}
			restoredCM, err := store.NewCollectionManager(2, filepath.Join(sandbox, "data", o.NodeID))
			if err != nil {
				t.Fatal(err)
			}
			defer restoredCM.Close()
			loaded, err := bm25.NewSnapshotStore(filepath.Join(sandbox, "data", "bm25")).LoadAll()
			if err != nil {
				t.Fatal(err)
			}
			restoredCfg := cfg
			restoredCfg.DB, restoredCfg.Collections = restoredDB, restoredCM
			restoredCfg.WorkspacePath, restoredCfg.StoragePath = filepath.Join(sandbox, "workspace"), filepath.Join(sandbox, "uploads")
			restoredCfg.BM25Indexes = bm25.NewIndexRegistryFrom(loaded)
			restoredCfg.BM25Store = bm25.NewSnapshotStore(filepath.Join(sandbox, "data", "bm25"))
			restoredApp := fiber.New()
			httpapi.RegisterWorkspaceAPI(restoredApp, restoredCfg)
			search := request(restoredApp, "POST", "/workspace/search", map[string]any{"project_id": "native-project", "branch": "main", "search_query": "quasar recovery", "search_type": "BM25", "mode": "full"})
			hits, ok := search["results"].([]any)
			if !ok || len(hits) == 0 || search["generation"] != "native-current" || search["generic_search_status"] != "ok" {
				t.Fatalf("restored search vacuous/wrong generation: %v", search)
			}
			digest := sha256.Sum256([]byte(text))
			expectedDigest := hex.EncodeToString(digest[:])
			for _, item := range hits {
				hit, ok := item.(map[string]any)
				if !ok {
					t.Fatalf("invalid hit: %v", item)
				}
				citation, ok := hit["citation"].(map[string]any)
				if !ok || hit["path"] != "guide.md" || hit["project_id"] != "native-project" || hit["branch"] != "main" || hit["generation"] != "native-current" || hit["file_digest"] != expectedDigest {
					t.Fatalf("restored hit binding: %v", hit)
				}
				for key, want := range map[string]any{"project_id": "native-project", "branch": "main", "generation": "native-current", "path": "guide.md", "file_digest": expectedDigest, "read_tool": "workspace_read"} {
					if citation[key] != want {
						t.Fatalf("citation %s=%v want=%v", key, citation[key], want)
					}
				}
				read := request(restoredApp, "GET", "/workspace/read?project_id=native-project&branch=main&path="+url.QueryEscape(citation["path"].(string)), nil)
				if read["text"] != text || read["file_digest"] != expectedDigest || read["path"] != "guide.md" {
					t.Fatalf("restored exact read: %v", read)
				}
			}
			empty := request(restoredApp, "GET", "/workspace/read?project_id=native-project&branch=main&path=empty.md", nil)
			if empty["text"] != "" {
				t.Fatalf("restored zero-chunk file changed: %v", empty)
			}
		})
	}
}
