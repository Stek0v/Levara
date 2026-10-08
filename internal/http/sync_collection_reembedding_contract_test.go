package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/embcontract"
)

// This tests receiver-local re-embedding, not independently isolated run status.
// Native SQL supplies live instance-admin authorization; vector roots are private.
func TestSyncCollectionNativeReembeddingContract(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		for _, tc := range []struct {
			name, model string
			dim         int
			deny        bool
		}{
			{"different_model_and_dimension", "receiver-v2", 3, false},
			{"same_contract", "source-v1", 2, false},
			{"existing_target_wrong_encoder_same_dimension", "receiver-v2", 2, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				sourceRoot, targetRoot := t.TempDir(), t.TempDir()
				sourceContract := embcontract.Contract{Encoder: "source-v1", Tokenizer: "native-fixture-tokenizer", Pooling: "mean", Normalization: "l2", Dim: 2, Metric: "cosine"}
				targetContract := sourceContract
				targetContract.Encoder, targetContract.Dim = tc.model, tc.dim
				receiverContract := targetContract
				if tc.deny {
					targetContract = sourceContract
				}
				newManager := func(root string, contract embcontract.Contract) *store.CollectionManager {
					t.Helper()
					cm, err := store.NewCollectionManager(contract.Dim, root)
					if err != nil {
						t.Fatal(err)
					}
					cm.SetDefaultModel(contract.Encoder)
					cm.SetDefaultEmbeddingContract(contract)
					return cm
				}
				source := newManager(sourceRoot, sourceContract)
				defer func() { _ = source.Close() }()
				target := newManager(targetRoot, targetContract)
				defer func() { _ = target.Close() }()
				if err := source.CreateWithDim("docs", 2, sourceContract.Encoder, "cosine"); err != nil {
					t.Fatal(err)
				}
				// Native insertion alone generates both embedding metadata fields.
				if err := source.Insert("docs", "source-record", []float32{1, 0}, map[string]any{"text": "short business document", "custom": "business-field", "nested": map[string]any{"kept": true}}); err != nil {
					t.Fatal(err)
				}
				beforeIDs, beforeVectors, beforeMetadata, err := source.AllRecords("docs")
				if err != nil {
					t.Fatal(err)
				}
				if len(beforeIDs) != 1 {
					t.Fatalf("source count=%d", len(beforeIDs))
				}
				beforeSourceCollection, err := json.Marshal(source.GetMeta("docs"))
				if err != nil {
					t.Fatal(err)
				}
				var stamped map[string]any
				if err := json.Unmarshal(beforeMetadata[0], &stamped); err != nil {
					t.Fatal(err)
				}
				if embcontract.VersionFromMetadata(stamped) != sourceContract.Fingerprint() {
					t.Fatal("native source did not stamp its actual contract")
				}

				var calls atomic.Int32
				receiverVector := make([]float32, tc.dim)
				receiverVector[1] = 1
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req struct {
						Model string   `json:"model"`
						Input []string `json:"input"`
					}
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model != tc.model || len(req.Input) == 0 {
						http.Error(w, "invalid receiver-local embedding request", http.StatusBadRequest)
						return
					}
					for _, text := range req.Input {
						if text != "short business document" {
							http.Error(w, "source text changed", 400)
							return
						}
					}
					calls.Add(1)
					data := make([]map[string]any, len(req.Input))
					for i := range data {
						data[i] = map[string]any{"index": i, "embedding": receiverVector}
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
				}))
				defer provider.Close()
				appFor := func(cm *store.CollectionManager, model, endpoint string) *fiber.App {
					app := fiber.New(fiber.Config{DisableStartupMessage: true})
					api := app.Group("/api/v1", func(c *fiber.Ctx) error { c.Locals("auth_db", &DBRef{DB: f.db}); return c.Next() }, JWTMiddleware(syncTestSecret, true), APIKeyPermissionMiddleware())
					RegisterSyncAPI(api, APIConfig{DB: f.db, RequireAuth: true, Collections: cm, EmbedModel: model, EmbedEndpoint: endpoint})
					return app
				}
				sourceApp := appFor(source, sourceContract.Encoder, "")
				targetApp := appFor(target, tc.model, provider.URL)
				request := func(app *fiber.App, method, path string, body []byte) []byte {
					t.Helper()
					req := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(body))
					req.Header.Set("Authorization", "Bearer "+createJWT("root", "root@test.invalid", syncTestSecret))
					req.Header.Set("Content-Type", "application/json")
					resp, err := app.Test(req)
					if err != nil {
						t.Fatal(err)
					}
					raw, readErr := io.ReadAll(resp.Body)
					closeErr := resp.Body.Close()
					if readErr != nil || closeErr != nil {
						t.Fatalf("read=%v close=%v", readErr, closeErr)
					}
					if resp.StatusCode != http.StatusOK {
						t.Fatalf("%s %s: status=%d body=%s", method, path, resp.StatusCode, raw)
					}
					return raw
				}
				exported := request(sourceApp, "GET", "/sync/export/collection/docs", nil)
				var payload syncCollectionExport
				if err := json.Unmarshal(exported, &payload); err != nil {
					t.Fatal(err)
				}
				if payload.SourceModel != sourceContract.Encoder || payload.SourceDim != 2 || len(payload.Records) != 1 || !bytes.Equal(payload.Records[0].Metadata, beforeMetadata[0]) {
					t.Fatalf("export lost native source binding: %+v", payload)
				}

				// Precreate with the receiver's real contract, so dropping the validator
				// cannot silently make a foreign native stamp look compatible.
				if err := target.CreateWithDim("docs", tc.dim, targetContract.Encoder, "cosine"); err != nil {
					t.Fatal(err)
				}
				if tc.deny {
					if err := target.Insert("docs", "sentinel", []float32{1, 0}, map[string]any{"text": "retained sentinel", "custom": "untouched"}); err != nil {
						t.Fatal(err)
					}
					// The configured receiver encoder changes; the existing collection
					// remains in its original, source-compatible native vector space.
					target.SetDefaultModel(receiverContract.Encoder)
					target.SetDefaultEmbeddingContract(receiverContract)
				}
				priorTargetIDs, priorTargetVectors, priorTargetMetadata, err := target.AllRecords("docs")
				if err != nil {
					t.Fatal(err)
				}
				priorTargetCollection, err := json.Marshal(target.GetMeta("docs"))
				if err != nil {
					t.Fatal(err)
				}
				if tc.deny {
					var sentinelMeta map[string]any
					if len(priorTargetMetadata) != 1 {
						t.Fatalf("target sentinel count=%d", len(priorTargetMetadata))
					}
					if err := json.Unmarshal(priorTargetMetadata[0], &sentinelMeta); err != nil {
						t.Fatal(err)
					}
					if embcontract.VersionFromMetadata(sentinelMeta) != embcontract.VersionFromMetadata(stamped) || target.GetMeta("docs").EmbeddingVersion != sourceContract.Fingerprint() {
						t.Fatal("denial fixture requires genuinely matching native source and existing target stamps")
					}
				}
				if !tc.deny && tc.model != sourceContract.Encoder {
					err := target.Insert("docs", "foreign-contract-control", receiverVector, json.RawMessage(beforeMetadata[0]))
					if !errors.Is(err, store.ErrEmbeddingContractMismatch) {
						t.Fatalf("foreign native contract must remain rejected: %v", err)
					}
				}
				var started struct {
					RunID string `json:"run_id"`
				}
				if err := json.Unmarshal(request(targetApp, "POST", "/sync/import/collection", exported), &started); err != nil {
					t.Fatal(err)
				}
				if started.RunID == "" {
					t.Fatal("import did not start")
				}
				deadline := time.Now().Add(5 * time.Second)
				var status syncCollectionImportStatus
				for {
					if err := json.Unmarshal(request(targetApp, "GET", fmt.Sprintf("/sync/import/collection/%s/status", started.RunID), nil), &status); err != nil {
						t.Fatal(err)
					}
					if status.Status != "RUNNING" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("native import never reached terminal status")
					}
					time.Sleep(5 * time.Millisecond)
				}
				if tc.deny {
					assertUnchanged := func() {
						t.Helper()
						ids, vectors, metadata, err := target.AllRecords("docs")
						if err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(ids, priorTargetIDs) || !reflect.DeepEqual(vectors, priorTargetVectors) || !reflect.DeepEqual(metadata, priorTargetMetadata) {
							t.Error("refused import changed target cardinality, vectors or raw metadata")
						}
						meta, err := json.Marshal(target.GetMeta("docs"))
						if err != nil {
							t.Fatal(err)
						}
						if !bytes.Equal(meta, priorTargetCollection) {
							t.Error("refused import changed target native collection contract/count")
						}
						ids, vectors, metadata, err = source.AllRecords("docs")
						if err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(ids, beforeIDs) || !reflect.DeepEqual(vectors, beforeVectors) || !reflect.DeepEqual(metadata, beforeMetadata) {
							t.Error("refused import changed source records")
						}
						meta, err = json.Marshal(source.GetMeta("docs"))
						if err != nil {
							t.Fatal(err)
						}
						if !bytes.Equal(meta, beforeSourceCollection) {
							t.Error("refused import changed source native collection metadata")
						}
					}
					// Source and existing target contracts match. Old behavior can
					// therefore accept newly embedded receiver-v2 vectors as source-v1.
					if status.Status != "FAILED" || status.Processed != 0 {
						t.Errorf("wrong existing target contract must refuse import: %+v", status)
					}
					assertUnchanged()
					if err := target.Close(); err != nil {
						t.Fatal(err)
					}
					if err := source.Close(); err != nil {
						t.Fatal(err)
					}
					target = newManager(targetRoot, receiverContract)
					source = newManager(sourceRoot, sourceContract)
					assertUnchanged()
					if f.db.Stats().InUse != 0 {
						t.Fatalf("SQL admission leaked: %+v", f.db.Stats())
					}
					return
				}
				if status.Status != "COMPLETED" || status.Total != 1 || status.Processed != 1 || status.Failed != 0 || status.Skipped != 0 {
					t.Fatalf("receiver re-embedding failed: %+v", status)
				}
				if calls.Load() < 2 {
					t.Fatalf("receiver probe and native re-embedding missing: calls=%d", calls.Load())
				}
				assertTarget := func(cm *store.CollectionManager) {
					t.Helper()
					ids, vectors, metadata, err := cm.AllRecords("docs")
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(ids, beforeIDs) || len(vectors) != 1 || !reflect.DeepEqual(vectors[0], receiverVector) {
						t.Fatalf("target records/vectors: %v %v", ids, vectors)
					}
					var actual map[string]any
					if err := json.Unmarshal(metadata[0], &actual); err != nil {
						t.Fatal(err)
					}
					if embcontract.VersionFromMetadata(actual) != targetContract.Fingerprint() {
						t.Fatalf("target retained foreign embedding version: %v", actual)
					}
					var actualContract embcontract.Contract
					contractJSON, err := json.Marshal(actual[embcontract.MetadataContractKey])
					if err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(contractJSON, &actualContract); err != nil {
						t.Fatal(err)
					}
					if actualContract.Normalized() != targetContract.Normalized() {
						t.Fatalf("target contract=%+v", actualContract)
					}
					for key, value := range stamped {
						if key != embcontract.MetadataVersionKey && key != embcontract.MetadataContractKey && !reflect.DeepEqual(actual[key], value) {
							t.Fatalf("business metadata %s changed", key)
						}
					}
					meta := cm.GetMeta("docs")
					if meta == nil || meta.EmbeddingVersion != targetContract.Fingerprint() || meta.EmbeddingDim != tc.dim || meta.EmbeddingModel != tc.model {
						t.Fatalf("target collection contract=%+v", meta)
					}
				}
				assertSource := func(cm *store.CollectionManager) {
					t.Helper()
					ids, vectors, metadata, err := cm.AllRecords("docs")
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(ids, beforeIDs) || !reflect.DeepEqual(vectors, beforeVectors) || !reflect.DeepEqual(metadata, beforeMetadata) {
						t.Fatal("export/import modified source records")
					}
				}
				assertTarget(target)
				assertSource(source)
				if err := target.Close(); err != nil {
					t.Fatal(err)
				}
				if err := source.Close(); err != nil {
					t.Fatal(err)
				}
				target = newManager(targetRoot, targetContract)
				source = newManager(sourceRoot, sourceContract)
				assertTarget(target)
				assertSource(source)
				if f.db.Stats().InUse != 0 {
					t.Fatalf("SQL admission leaked: %+v", f.db.Stats())
				}
			})
		}
	})
}

func TestSyncCollectionReembeddingMetadataAndAutoCreation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata json.RawMessage
		pass     bool
	}{
		{"missing", nil, true}, {"null", json.RawMessage("null"), true},
		{"object", json.RawMessage(`{"business":"retained"}`), true},
		{"array", json.RawMessage("[]"), false}, {"scalar", json.RawMessage(`"value"`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			cm, err := store.NewCollectionManager(2, root)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cm.Close() }()
			cm.SetDefaultModel("receiver")
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[0,1]}]}`)
			}))
			defer provider.Close()
			prior := append([]byte(nil), tc.metadata...)
			result, code := startSyncCollectionImport(APIConfig{Collections: cm, EmbedModel: "receiver", EmbedEndpoint: provider.URL}, syncCollectionExport{Collection: "docs", Records: []syncCollectionRecord{{ID: "r1", Text: "text", Metadata: tc.metadata}}})
			if code != 200 {
				t.Fatalf("code=%d result=%v", code, result)
			}
			runID, _ := result["run_id"].(string)
			deadline := time.Now().Add(5 * time.Second)
			var status *syncCollectionImportStatus
			for {
				var ok bool
				status, ok = syncImportRuns.Load(runID)
				if ok && status.Status != "RUNNING" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("import did not terminate")
				}
				time.Sleep(time.Millisecond)
			}
			if !bytes.Equal(prior, tc.metadata) {
				t.Fatal("import mutated source metadata")
			}
			expected := embcontract.FromEnv("receiver", 2, "cosine")
			verify := func() {
				t.Helper()
				ids, _, metadata, err := cm.AllRecords("docs")
				if err != nil {
					t.Fatal(err)
				}
				if !tc.pass {
					if status.Status != "FAILED" || status.Processed != 0 || status.Failed != 1 || len(ids) != 0 {
						t.Fatalf("nonobject import had effect: %+v ids=%v", status, ids)
					}
					return
				}
				if status.Status != "COMPLETED" || status.Processed != 1 || status.Failed != 0 || len(ids) != 1 {
					t.Fatalf("empty/object metadata import failed: %+v ids=%v", status, ids)
				}
				var actual map[string]any
				if err := json.Unmarshal(metadata[0], &actual); err != nil {
					t.Fatal(err)
				}
				if embcontract.VersionFromMetadata(actual) != expected.Fingerprint() {
					t.Fatal("fresh contract missing")
				}
				var bound struct {
					Contract embcontract.Contract `json:"embedding_contract"`
				}
				if err := json.Unmarshal(metadata[0], &bound); err != nil || bound.Contract.Fingerprint() != expected.Fingerprint() {
					t.Fatalf("full fresh contract missing: %v", err)
				}
				if tc.name == "object" && actual["business"] != "retained" {
					t.Fatal("business metadata lost")
				}
				if cm.GetMeta("docs").EmbeddingVersion != expected.Fingerprint() {
					t.Fatal("auto-created target differs from receiver")
				}
			}
			verify()
			if err := cm.Close(); err != nil {
				t.Fatal(err)
			}
			cm, err = store.NewCollectionManager(2, root)
			if err != nil {
				t.Fatal(err)
			}
			verify()
		})
	}
}

func TestSyncCollectionChunkReplayAndAllSkippedTotals(t *testing.T) {
	for _, name := range []string{"long_null", "long_object", "all_oversized_whitespace"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			cm, err := store.NewCollectionManager(2, root)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cm.Close() }()
			cm.SetDefaultModel("receiver")
			if err := cm.CreateWithDim("docs", 2, "receiver", "cosine"); err != nil {
				t.Fatal(err)
			}
			if err := cm.Insert("docs", "sentinel", []float32{1, 0}, map[string]any{"text": "untouched"}); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Input []string `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					http.Error(w, "bad input", 400)
					return
				}
				calls.Add(1)
				data := make([]map[string]any, len(req.Input))
				for i := range data {
					data[i] = map[string]any{"index": i, "embedding": []float32{0, 1}}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			}))
			defer provider.Close()
			text := strings.Repeat("word ", 1000)
			meta := json.RawMessage("null")
			if name == "long_object" {
				meta = json.RawMessage(`{"content":"original full document","business":"retained"}`)
			}
			if name == "all_oversized_whitespace" {
				text = strings.Repeat(" ", reembedMaxRunes+1)
			}
			var firstIDs []string
			for attempt := 0; attempt < 2; attempt++ {
				result, code := startSyncCollectionImport(APIConfig{Collections: cm, EmbedModel: "receiver", EmbedEndpoint: provider.URL}, syncCollectionExport{Collection: "docs", Records: []syncCollectionRecord{{ID: "r1", Text: text, Metadata: meta}}})
				if code != 200 {
					t.Fatalf("code=%d result=%v", code, result)
				}
				runID := result["run_id"].(string)
				deadline := time.Now().Add(5 * time.Second)
				var status *syncCollectionImportStatus
				for {
					var ok bool
					status, ok = syncImportRuns.Load(runID)
					if ok && status.Status != "RUNNING" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("job never terminated")
					}
					time.Sleep(time.Millisecond)
				}
				ids, vectors, metadata, err := cm.AllRecords("docs")
				if err != nil {
					t.Fatal(err)
				}
				if status.Status != "COMPLETED" || status.Failed != 0 {
					t.Fatalf("expanded import failed: %+v", status)
				}
				sortedIDs := append([]string(nil), ids...)
				sort.Strings(sortedIDs)
				if attempt == 0 {
					firstIDs = sortedIDs
				} else if !reflect.DeepEqual(sortedIDs, firstIDs) {
					t.Fatal("repeated chunk import changed physical identities/cardinality")
				}
				if name == "all_oversized_whitespace" {
					if status.Total != 0 || status.Processed != 0 || status.Skipped != 1 || len(ids) != 1 || calls.Load() != 0 {
						t.Fatalf("zero units misreported or mutated: %+v ids=%v calls=%d", status, ids, calls.Load())
					}
					continue
				}
				if len(ids) < 3 || status.Total != len(ids)-1 || status.Processed != status.Total || status.Skipped != 0 {
					t.Fatalf("expanded counts differ from committed units: %+v ids=%v", status, ids)
				}
				for i, id := range ids {
					var actual map[string]any
					if err := json.Unmarshal(metadata[i], &actual); err != nil {
						t.Fatal(err)
					}
					if id == "sentinel" {
						if actual["text"] != "untouched" || !reflect.DeepEqual(vectors[i], []float32{1, 0}) {
							t.Fatal("unrelated record changed")
						}
						continue
					}
					if actual["_source_id"] != "r1" || actual["_chunk_total"] != float64(status.Total) {
						t.Fatalf("chunk lineage lost: %v", actual)
					}
					field := "text"
					if name == "long_object" {
						field = "content"
						if actual["business"] != "retained" {
							t.Fatal("business metadata lost")
						}
					}
					value, ok := actual[field].(string)
					if !ok || value == "" || len([]rune(value)) > reembedMaxRunes {
						t.Fatal("chunk metadata does not describe bounded embedded text")
					}
					if embcontract.VersionFromMetadata(actual) != embcontract.FromEnv("receiver", 2, "cosine").Fingerprint() || !reflect.DeepEqual(vectors[i], []float32{0, 1}) {
						t.Fatal("chunk receiver vector/contract lost")
					}
				}
				type persistedRecord struct {
					Vector   []float32
					Metadata string
				}
				beforeReopen := make(map[string]persistedRecord, len(ids))
				for i, id := range ids {
					beforeReopen[id] = persistedRecord{vectors[i], string(metadata[i])}
				}
				if err := cm.Close(); err != nil {
					t.Fatal(err)
				}
				cm, err = store.NewCollectionManager(2, root)
				if err != nil {
					t.Fatal(err)
				}
				cm.SetDefaultModel("receiver")
				reopenedIDs, reopenedVectors, reopenedMetadata, err := cm.AllRecords("docs")
				if err != nil {
					t.Fatal(err)
				}
				afterReopen := make(map[string]persistedRecord, len(reopenedIDs))
				for i, id := range reopenedIDs {
					afterReopen[id] = persistedRecord{reopenedVectors[i], string(reopenedMetadata[i])}
				}
				if !reflect.DeepEqual(beforeReopen, afterReopen) {
					t.Fatal("native chunk data changed immediately after reopen")
				}
			}
		})
	}
}
