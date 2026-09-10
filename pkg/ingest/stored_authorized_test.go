package ingest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	httpapi "github.com/stek0v/levara/internal/http"
	"github.com/stek0v/levara/pkg/access"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/ingest"
	"github.com/stek0v/levara/pkg/storage"
)

type authorizedObjects struct {
	storage.Storage
	mu             sync.Mutex
	objects        map[string][]byte
	saves, deletes int
	saveHook       func(context.Context, string) error
	deleteFail     bool
}

func (s *authorizedObjects) Save(ctx context.Context, key string, r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.saves++
	s.objects[key] = b
	s.mu.Unlock()
	if s.saveHook != nil {
		return s.saveHook(ctx, key)
	}
	return nil
}
func (s *authorizedObjects) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes++
	if s.deleteFail {
		return errors.New("cleanup unavailable")
	}
	delete(s.objects, key)
	return nil
}
func TestIngestAuthorizedHoldBeforeSave(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f := newMetadataFixture(t, dialect)
			f.register()
			f.exec("UPDATE document_resources SET hold=true WHERE data_id='blob'")
			backend := &authorizedObjects{objects: map[string][]byte{}}
			if err := ingest.EnsureIngestJournalSchema(f.ctx, f.db); err != nil {
				t.Fatal(err)
			}
			f.writer.SetStorageIdentity("test-fixture")
			_, _, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{{ID: "blob", Text: "changed", OwnerID: "owner"}}, nil, t.TempDir(), backend, f.proof(), "owned", "Owned")
			if err == nil {
				t.Fatal("held document accepted")
			}
			if backend.saves != 0 {
				t.Fatalf("held document reached backend: saves=%d", backend.saves)
			}
		})
	}
}

func TestStructuredArtifactPublishesAtomically(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, backend := newAuthorizedFixture(t, dialect)
			results, _, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{{
				ID: "structured", Text: "projection", StructuredArtifact: []byte(`{"value":"ok"}`),
			}}, nil, t.TempDir(), backend, f.proof(), "owned", "Owned")
			if err != nil || len(results) != 1 || results[0].StructuredArtifactID == "" {
				t.Fatalf("publish structured artifact: results=%+v err=%v", results, err)
			}
			var revision int64
			var rawHash, artifactHash, location, state string
			var size int64
			if err := f.db.QueryRow(httpapi.Q(`SELECT source_revision,raw_content_hash,artifact_sha256,byte_size,storage_location,state
				FROM document_structured_artifacts WHERE id=$1`), results[0].StructuredArtifactID).
				Scan(&revision, &rawHash, &artifactHash, &size, &location, &state); err != nil {
				t.Fatal(err)
			}
			if revision != results[0].SourceRevision || rawHash != results[0].ContentHash || artifactHash != results[0].StructuredArtifactHash || size != int64(len(`{"value":"ok"}`)) || location != results[0].StructuredArtifactPath || state != "active" {
				t.Fatalf("artifact lineage mismatch: revision=%d raw=%q hash=%q size=%d location=%q state=%q result=%+v", revision, rawHash, artifactHash, size, location, state, results[0])
			}

			failed, broken := newAuthorizedFixture(t, dialect)
			broken.saveHook = func(_ context.Context, key string) error {
				if strings.HasSuffix(key, "-structured") {
					return errors.New("structured storage unavailable")
				}
				return nil
			}
			_, _, err = failed.writer.IngestAuthorized(failed.ctx, []ingest.Item{{
				ID: "broken", Text: "projection", StructuredArtifact: []byte(`{"value":"lost"}`),
			}}, nil, t.TempDir(), broken, failed.proof(), "owned", "Owned")
			if err == nil {
				t.Fatal("structured artifact save failure published metadata")
			}
			_, _, objects := broken.snapshot()
			if failed.count("data") != 0 || failed.count("document_structured_artifacts") != 0 || failed.count("ingest_pending_uploads") != 0 || len(objects) != 0 {
				t.Fatalf("failed artifact publish left state: data=%d artifacts=%d journal=%d objects=%d", failed.count("data"), failed.count("document_structured_artifacts"), failed.count("ingest_pending_uploads"), len(objects))
			}

			rejected, stored := newAuthorizedFixture(t, dialect)
			if dialect == "sqlite" {
				rejected.exec(`CREATE TRIGGER reject_structured_artifact BEFORE INSERT ON document_structured_artifacts
					BEGIN SELECT RAISE(ABORT, 'artifact inventory unavailable'); END`)
			} else {
				rejected.exec(`CREATE FUNCTION reject_structured_artifact() RETURNS trigger LANGUAGE plpgsql AS $$
					BEGIN RAISE EXCEPTION 'artifact inventory unavailable'; END $$`)
				rejected.exec(`CREATE TRIGGER reject_structured_artifact BEFORE INSERT ON document_structured_artifacts
					FOR EACH ROW EXECUTE FUNCTION reject_structured_artifact()`)
			}
			_, _, err = rejected.writer.IngestAuthorized(rejected.ctx, []ingest.Item{{
				ID: "rejected", Text: "projection", StructuredArtifact: []byte(`{"value":"rejected"}`),
			}}, nil, t.TempDir(), stored, rejected.proof(), "owned", "Owned")
			if err == nil {
				t.Fatal("structured inventory failure published metadata")
			}
			_, _, objects = stored.snapshot()
			if rejected.count("data") != 0 || rejected.count("document_structured_artifacts") != 0 || rejected.count("ingest_pending_uploads") != 0 || len(objects) != 0 {
				t.Fatalf("failed inventory publish left state: data=%d artifacts=%d journal=%d objects=%d", rejected.count("data"), rejected.count("document_structured_artifacts"), rejected.count("ingest_pending_uploads"), len(objects))
			}
		})
	}
}

func TestStructuredArtifactReplacementNeverReusesLineage(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, backend := newAuthorizedFixture(t, dialect)
			publish := func(text, artifact string, expected *ingest.SourceCAS) ingest.Result {
				t.Helper()
				item := ingest.Item{ID: "source", Text: text, StructuredArtifact: []byte(artifact)}
				var results []ingest.Result
				var err error
				if expected == nil {
					results, _, err = f.writer.IngestAuthorized(f.ctx, []ingest.Item{item}, nil, t.TempDir(), backend, f.proof(), "owned", "Owned")
				} else {
					results, _, err = f.writer.ReplaceAuthorized(f.ctx, []ingest.Item{item}, nil, t.TempDir(), backend, f.proof(), "owned", "Owned", *expected)
				}
				if err != nil || len(results) != 1 {
					t.Fatalf("publish %q: results=%+v err=%v", text, results, err)
				}
				return results[0]
			}
			a := publish("A", `{"value":"A"}`, nil)
			repeated := publish("A", `{"value":"A"}`, nil)
			if repeated.StructuredArtifactID != a.StructuredArtifactID || repeated.SourceRevision != a.SourceRevision || f.count("document_structured_artifacts") != 1 {
				t.Fatalf("exact repeat duplicated artifact: first=%+v repeated=%+v count=%d", a, repeated, f.count("document_structured_artifacts"))
			}
			unchanged := make(chan struct {
				result ingest.Result
				err    error
			}, 2)
			for range 2 {
				go func() {
					results, _, err := f.writer.ReplaceAuthorized(f.ctx, []ingest.Item{{ID: "source", Text: "A", StructuredArtifact: []byte(`{"value":"A"}`)}}, nil, t.TempDir(), backend, f.proof(), "owned", "Owned", ingest.SourceCAS{Revision: a.SourceRevision, RawContentHash: a.ContentHash})
					var result ingest.Result
					if len(results) == 1 {
						result = results[0]
					}
					unchanged <- struct {
						result ingest.Result
						err    error
					}{result, err}
				}()
			}
			for range 2 {
				got := <-unchanged
				if got.err != nil || got.result.StructuredArtifactID != a.StructuredArtifactID || got.result.SourceRevision != a.SourceRevision {
					t.Fatalf("unchanged concurrent replacement: result=%+v err=%v", got.result, got.err)
				}
			}
			if f.count("document_structured_artifacts") != 1 {
				t.Fatal("unchanged replacement changed artifact inventory")
			}
			b := publish("B", `{"value":"B"}`, &ingest.SourceCAS{Revision: a.SourceRevision, RawContentHash: a.ContentHash})
			a2 := publish("A", `{"value":"A"}`, &ingest.SourceCAS{Revision: b.SourceRevision, RawContentHash: b.ContentHash})
			if !(a.SourceRevision < b.SourceRevision && b.SourceRevision < a2.SourceRevision) || a.StructuredArtifactID == b.StructuredArtifactID || a.StructuredArtifactID == a2.StructuredArtifactID || b.StructuredArtifactID == a2.StructuredArtifactID {
				t.Fatalf("lineage reused: A=%+v B=%+v A2=%+v", a, b, a2)
			}
			rows, err := f.db.Query("SELECT id,state FROM document_structured_artifacts ORDER BY source_revision")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var states []string
			for rows.Next() {
				var id, state string
				if err := rows.Scan(&id, &state); err != nil {
					t.Fatal(err)
				}
				states = append(states, state)
			}
			if !reflect.DeepEqual(states, []string{"retired", "retired", "active"}) {
				t.Fatalf("artifact states=%v", states)
			}
		})
	}
}

func TestStructuredArtifactOrdinaryReingestRetiresSuperseded(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, backend := newAuthorizedFixture(t, dialect)
			publish := func(text, artifact string) ingest.Result {
				t.Helper()
				results, _, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{{ID: "source", Text: text, StructuredArtifact: []byte(artifact)}}, nil, t.TempDir(), backend, f.proof(), "owned", "Owned")
				if err != nil || len(results) != 1 {
					t.Fatalf("publish: results=%+v err=%v", results, err)
				}
				return results[0]
			}
			a := publish("same projection", `{"a":1,"b":2}`)
			b := publish("same projection", `{"b":2,"a":1}`)
			if b.SourceRevision <= a.SourceRevision || a.StructuredArtifactID == b.StructuredArtifactID {
				t.Fatalf("artifact-only change lineage: A=%+v B=%+v", a, b)
			}
			var oldState, newState string
			if err := f.db.QueryRow(httpapi.Q("SELECT state FROM document_structured_artifacts WHERE id=$1"), a.StructuredArtifactID).Scan(&oldState); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueryRow(httpapi.Q("SELECT state FROM document_structured_artifacts WHERE id=$1"), b.StructuredArtifactID).Scan(&newState); err != nil {
				t.Fatal(err)
			}
			if oldState != "retired" || newState != "active" {
				t.Fatalf("artifact-only states old=%q new=%q", oldState, newState)
			}
			c := publish("changed projection", `{"value":"C"}`)
			if c.SourceRevision <= b.SourceRevision {
				t.Fatalf("source change did not advance: B=%+v C=%+v", b, c)
			}
			if err := f.db.QueryRow(httpapi.Q("SELECT state FROM document_structured_artifacts WHERE id=$1"), b.StructuredArtifactID).Scan(&oldState); err != nil || oldState != "retired" {
				t.Fatalf("source change kept prior artifact active: state=%q err=%v", oldState, err)
			}
		})
	}
}

func TestStructuredArtifactOnlyReplacementUsesSourceCAS(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, backend := newAuthorizedFixture(t, dialect)
			storagePath := t.TempDir()
			initial, _, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{{ID: "source", Text: "projection", StructuredArtifact: []byte(`{"value":"A"}`)}}, nil, storagePath, backend, f.proof(), "owned", "Owned")
			if err != nil || len(initial) != 1 {
				t.Fatalf("initial: results=%+v err=%v", initial, err)
			}
			ref := access.DocumentRef{DatasetID: "owned", DataID: "source"}
			resource, err := f.policy.RegisterDocument(f.ctx, f.actor, ref, "a", access.DocumentRestricted)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{{ID: "source", Text: "projection", StructuredArtifact: []byte(`{"value":"without-cas"}`)}}, nil, storagePath, backend, f.proof(), "owned", "Owned"); !errors.Is(err, access.ErrDocumentVersionConflict) {
				t.Fatalf("registered artifact-only update without CAS: %v", err)
			}
			type outcome struct {
				result ingest.Result
				err    error
			}
			start := make(chan struct{})
			done := make(chan outcome, 2)
			for _, artifact := range []string{`{"value":"B"}`, `{"value":"C"}`} {
				go func(artifact string) {
					<-start
					results, _, err := f.writer.ReplaceAuthorized(f.ctx, []ingest.Item{{ID: "source", Text: "projection", StructuredArtifact: []byte(artifact)}}, nil, storagePath, backend, f.proof(), "owned", "Owned", ingest.SourceCAS{Revision: initial[0].SourceRevision, RawContentHash: initial[0].ContentHash})
					var result ingest.Result
					if len(results) == 1 {
						result = results[0]
					}
					done <- outcome{result, err}
				}(artifact)
			}
			close(start)
			var winner ingest.Result
			success, conflict := 0, 0
			for range 2 {
				got := <-done
				switch {
				case got.err == nil:
					success++
					winner = got.result
				case errors.Is(got.err, access.ErrDocumentVersionConflict):
					conflict++
				default:
					t.Fatalf("artifact-only replacement: %v", got.err)
				}
			}
			currentRevision, currentHash, err := f.policy.SourceVersion(f.ctx, ref)
			if err != nil || success != 1 || conflict != 1 || winner.SourceRevision <= initial[0].SourceRevision || currentRevision != winner.SourceRevision || currentHash != winner.ContentHash {
				t.Fatalf("CAS outcome success=%d conflict=%d initial=%+v winner=%+v current=%d/%q err=%v", success, conflict, initial[0], winner, currentRevision, currentHash, err)
			}
			updated, err := f.policy.GetDocumentResource(f.ctx, ref)
			if err != nil || updated.ContentRevision != resource.ContentRevision+1 {
				t.Fatalf("content revision=%+v err=%v", updated, err)
			}
			var activeID string
			if err := f.db.QueryRow(httpapi.Q("SELECT id FROM document_structured_artifacts WHERE data_id=$1 AND state='active'"), "source").Scan(&activeID); err != nil || activeID != winner.StructuredArtifactID {
				t.Fatalf("active artifact=%q winner=%q err=%v", activeID, winner.StructuredArtifactID, err)
			}
			saves, _, objects := backend.snapshot()
			if saves != 3 || len(objects) != 3 || f.count("ingest_pending_uploads") != 0 {
				t.Fatalf("loser left effects: saves=%d objects=%d journal=%d", saves, len(objects), f.count("ingest_pending_uploads"))
			}
		})
	}
}

func TestStructuredArtifactDuplicateBatch(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		for _, destination := range []string{"local", "remote"} {
			t.Run(dialect+"/"+destination, func(t *testing.T) {
				f, remote := newAuthorizedFixture(t, dialect)
				var backend storage.Storage
				if destination == "remote" {
					backend = remote
				}
				item := ingest.Item{ID: "duplicate", Text: "projection", StructuredArtifact: []byte(`{"value":"same"}`)}
				results, n, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{item, item}, nil, t.TempDir(), backend, f.proof(), "owned", "Owned")
				if err != nil || n != 2 || len(results) != 2 {
					t.Fatalf("duplicate batch: results=%+v written=%d err=%v", results, n, err)
				}
				if results[0].FilePath != results[1].FilePath || results[0].StructuredArtifactID == "" || results[0].StructuredArtifactID != results[1].StructuredArtifactID || results[0].StructuredArtifactPath != results[1].StructuredArtifactPath || f.count("document_structured_artifacts") != 1 {
					t.Fatalf("duplicate artifact diverged: results=%+v inventory=%d", results, f.count("document_structured_artifacts"))
				}
			})
		}
	}
}

func TestStructuredArtifactCleanupRejectsForeignLocation(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, backend := newAuthorizedFixture(t, dialect)
			backend.objects["unrelated"] = []byte("keep")
			f.exec(`INSERT INTO document_structured_artifacts
				(id,data_id,source_revision,raw_content_hash,artifact_sha256,byte_size,storage_location,state)
				VALUES('tampered','source',1,'source','artifact',4,'storage://unrelated','retired')`)
			if err := f.writer.CleanupRetiredStructuredArtifacts(f.ctx, "source", t.TempDir(), backend); err == nil {
				t.Fatal("foreign inventory location was deleted")
			}
			if string(backend.objects["unrelated"]) != "keep" || f.count("document_structured_artifacts") != 1 {
				t.Fatal("foreign inventory cleanup changed object or row")
			}
		})
	}
}

func newAuthorizedFixture(t *testing.T, dialect string) (metadataFixture, *authorizedObjects) {
	f := newMetadataFixture(t, dialect)
	if err := ingest.EnsureIngestJournalSchema(f.ctx, f.db); err != nil {
		t.Fatal(err)
	}
	if err := ingest.EnsureIngestJournalSchema(f.ctx, f.db); err != nil {
		t.Fatal(err)
	}
	f.writer.SetStorageIdentity("test-fixture")
	return f, &authorizedObjects{objects: map[string][]byte{}}
}
func (s *authorizedObjects) snapshot() (int, int, map[string][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]byte{}
	for k, v := range s.objects {
		out[k] = append([]byte(nil), v...)
	}
	return s.saves, s.deletes, out
}
func TestIngestAuthorizedPreflight(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, tc := range []string{"invalid-second", "invalid-id", "original-count", "empty-original", "duplicate-conflict", "missing-proof", "revoked", "expired", "foreign-owner", "foreign-dataset", "reserved-name", "registered-change", "cross-alias-hold", "cross-alias-tombstone", "cross-alias-tenant", "orphan-held"} {
				t.Run(tc, func(t *testing.T) {
					f, b := newAuthorizedFixture(t, dialect)
					actor := f.proof()
					items := []ingest.Item{{ID: "new", Text: "valid"}}
					var originals []ingest.Item
					ds, name := "owned", "Owned"
					switch tc {
					case "invalid-second":
						items = append(items, ingest.Item{})
					case "invalid-id":
						items = append(items, ingest.Item{ID: "../bad", Text: "x"})
					case "original-count":
						originals = []ingest.Item{}
					case "empty-original":
						originals = []ingest.Item{{}}
					case "duplicate-conflict":
						items = append(items, ingest.Item{ID: "new", Text: "different"})
					case "missing-proof":
						actor.Credential = access.MetadataCredential{}
					case "revoked":
						f.exec("UPDATE users SET is_active=false WHERE id='owner'")
					case "expired":
						actor.Credential.ExpiresAt = 1
					case "foreign-owner":
						f.exec("INSERT INTO data(id,name,owner_id) VALUES('new','foreign','foreign')")
					case "foreign-dataset":
						ds, name = "foreign", "Foreign"
					case "reserved-name":
						ds, name = "new-dataset", "Foreign"
					case "registered-change":
						f.register()
						items[0].ID = "blob"
					default:
						f.register()
						items[0].ID = "blob"
						f.exec("INSERT INTO datasets(id,name,owner_id) VALUES('alias','Alias','owner')")
						tenant := "a"
						hold, tombstone := false, false
						if tc == "cross-alias-tenant" {
							tenant = "b"
						}
						if tc == "cross-alias-hold" || tc == "orphan-held" {
							hold = true
						}
						if tc == "cross-alias-tombstone" {
							tombstone = true
						}
						f.exec("INSERT INTO document_resources(dataset_id,data_id,tenant_id,mode,acl_revision,content_revision,hold,tombstoned) VALUES('alias','blob',$1,'inherit',1,1,$2,$3)", tenant, hold, tombstone)
						if tc != "orphan-held" {
							f.exec("INSERT INTO dataset_data(dataset_id,data_id) VALUES('alias','blob')")
						}
					}
					before := f.count("data")
					_, n, err := f.writer.IngestAuthorized(f.ctx, items, originals, t.TempDir(), b, actor, ds, name)
					saves, deletes, _ := b.snapshot()
					if err == nil || n != 0 || saves != 0 || deletes != 0 || f.count("data") != before || f.count("ingest_pending_uploads") != 0 {
						t.Fatalf("preflight accepted/effected: n=%d err=%v saves=%d deletes=%d", n, err, saves, deletes)
					}
				})
			}
		})
	}
}
func TestIngestAuthorizedOriginalRepeatAndImmutableReplacement(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, b := newAuthorizedFixture(t, dialect)
			dir := filepath.Join(t.TempDir(), "no-plaintext")
			items := []ingest.Item{{Text: "Извлечённый текст", Filename: "derived.txt", OwnerID: "foreign", Tags: []string{"タグ"}, Room: "docs"}}
			originals := []ingest.Item{{ID: "source", FileData: []byte("%PDF-1.7 original binary\x00"), Filename: "原文.pdf", OwnerID: "foreign"}}
			first, n, err := f.writer.IngestAuthorized(f.ctx, items, originals, dir, b, f.proof(), "owned", "Owned")
			if err != nil || n != 1 {
				t.Fatalf("first: %v %d", err, n)
			}
			if first[0].ID != "source" || first[0].Name != "原文.pdf" || first[0].MimeType != "application/pdf" {
				t.Fatal(first)
			}
			saves, _, objects := b.snapshot()
			if saves != 2 || string(objects[strings.TrimPrefix(first[0].FilePath, "storage://")]) != items[0].Text || !bytes.Equal(objects[strings.TrimPrefix(first[0].OriginalFilePath, "storage://")], originals[0].FileData) {
				t.Fatal("original/derived bytes mismatch")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("remote plaintext staging: %v", err)
			}
			var owner string
			if err := f.db.QueryRow("SELECT owner_id FROM data WHERE id='source'").Scan(&owner); err != nil || owner != "owner" {
				t.Fatalf("owner override %q %v", owner, err)
			}
			f.exec("UPDATE data SET pipeline_status='{\"collection\":\"COMPLETED\"}',token_count=17 WHERE id='source'")
			again, n, err := f.writer.IngestAuthorized(f.ctx, items, originals, dir, b, f.proof(), "second", "Second")
			nowSaves, _, _ := b.snapshot()
			if err != nil || n != 1 || nowSaves != saves || again[0].FilePath != first[0].FilePath || !again[0].AlreadyExists {
				t.Fatalf("repeat %v %+v saves=%d", err, again, nowSaves)
			}
			var status string
			var tokens int
			if err := f.db.QueryRow("SELECT pipeline_status,token_count FROM data WHERE id='source'").Scan(&status, &tokens); err != nil || tokens != 17 || !strings.Contains(status, "COMPLETED") {
				t.Fatal("repeat reset status", err, status, tokens)
			}
			// Unregistered changes use fresh private keys and never overwrite old refs.
			items[0].Text = "changed derived"
			changed, _, err := f.writer.IngestAuthorized(f.ctx, items, originals, dir, b, f.proof(), "owned", "Owned")
			if err != nil {
				t.Fatal(err)
			}
			_, _, after := b.snapshot()
			if changed[0].FilePath == first[0].FilePath || !bytes.Equal(after[strings.TrimPrefix(first[0].FilePath, "storage://")], []byte("Извлечённый текст")) {
				t.Fatal("published key overwritten")
			}
			ref := access.DocumentRef{DatasetID: "owned", DataID: "source"}
			if _, err := f.policy.RegisterDocument(f.ctx, f.actor, ref, "a", access.DocumentRestricted); err != nil {
				t.Fatal(err)
			}
			beforeRepeat, _, _ := b.snapshot()
			if _, _, err := f.writer.IngestAuthorized(f.ctx, items, originals, dir, b, f.proof(), "owned", "Owned"); err != nil {
				t.Fatal("registered exact repeat", err)
			}
			items[0].Room = "changed metadata"
			if _, _, err := f.writer.IngestAuthorized(f.ctx, items, originals, dir, b, f.proof(), "owned", "Owned"); !errors.Is(err, access.ErrDocumentVersionConflict) {
				t.Fatalf("registered mutation %v", err)
			}
			afterRepeat, _, _ := b.snapshot()
			if afterRepeat != beforeRepeat {
				t.Fatal("registered repeat/conflict reached Save")
			}
			if f.count("ingest_pending_uploads") != 0 {
				t.Fatal("successful journal retained")
			}
		})
	}
}

func TestReplaceAuthorizedSourceCAS(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, backend := newAuthorizedFixture(t, dialect)
			ref := access.DocumentRef{DatasetID: "owned", DataID: "source"}
			_, n, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{{ID: "source", Text: "A", Filename: "source.txt"}}, nil, "", backend, f.proof(), "owned", "Owned")
			if err != nil || n != 1 {
				t.Fatalf("initial ingest: n=%d err=%v", n, err)
			}
			initialRevision, initialHash, err := f.policy.SourceVersion(f.ctx, ref)
			if err != nil {
				t.Fatal(err)
			}
			resource, err := f.policy.RegisterDocument(f.ctx, f.actor, ref, "a", access.DocumentRestricted)
			if err != nil {
				t.Fatal(err)
			}
			p, release, err := f.policy.BeginReadFence(f.ctx, dialect == "sqlite")
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if err := p.CommitDocumentIndexVersioned(f.ctx, f.actor, ref, resource.ContentRevision, "docs", "A", access.DocumentPublicationLineage{SourceRevision: initialRevision, RawContentHash: initialHash, SourcesJSON: "[]"}); err != nil {
				t.Fatal(err)
			}

			replaced, n, err := f.writer.ReplaceAuthorized(f.ctx, []ingest.Item{{ID: "source", Text: "B", Filename: "source.txt"}}, nil, "", backend, f.proof(), "owned", "Owned", ingest.SourceCAS{Revision: initialRevision, RawContentHash: strings.ToUpper(initialHash)})
			if err != nil || n != 1 {
				t.Fatalf("replace: n=%d err=%v", n, err)
			}
			currentRevision, currentHash, err := f.policy.SourceVersion(f.ctx, ref)
			if err != nil || currentRevision <= initialRevision || currentHash == initialHash {
				t.Fatalf("source after replace: revision=%d hash=%q err=%v", currentRevision, currentHash, err)
			}
			_, _, objects := backend.snapshot()
			if got := objects[strings.TrimPrefix(replaced[0].FilePath, "storage://")]; string(got) != "B" {
				t.Fatalf("replacement bytes=%q", got)
			}
			updated, err := f.policy.GetDocumentResource(f.ctx, ref)
			if err != nil || updated.ContentRevision != resource.ContentRevision+1 || f.count("document_index_publications") != 0 {
				t.Fatalf("replacement revision/publication: resource=%+v publications=%d err=%v", updated, f.count("document_index_publications"), err)
			}

			beforeSaves, beforeDeletes, beforeObjects := backend.snapshot()
			_, n, err = f.writer.ReplaceAuthorized(f.ctx, []ingest.Item{{ID: "source", Text: "late", Filename: "source.txt"}}, nil, "", backend, f.proof(), "owned", "Owned", ingest.SourceCAS{Revision: initialRevision, RawContentHash: initialHash})
			afterSaves, afterDeletes, afterObjects := backend.snapshot()
			if !errors.Is(err, access.ErrDocumentVersionConflict) || n != 0 || beforeSaves != afterSaves || beforeDeletes != afterDeletes || !reflect.DeepEqual(beforeObjects, afterObjects) {
				t.Fatalf("stale replacement changed state: n=%d err=%v saves=%d/%d deletes=%d/%d", n, err, beforeSaves, afterSaves, beforeDeletes, afterDeletes)
			}

			backend.saveHook = func(context.Context, string) error { return errors.New("replacement storage unavailable") }
			_, n, err = f.writer.ReplaceAuthorized(f.ctx, []ingest.Item{{ID: "source", Text: "C", Filename: "source.txt"}}, nil, "", backend, f.proof(), "owned", "Owned", ingest.SourceCAS{Revision: currentRevision, RawContentHash: currentHash})
			backend.saveHook = nil
			if err == nil || n != 0 || f.count("ingest_pending_uploads") != 0 {
				t.Fatalf("failed replacement: n=%d err=%v journal=%d", n, err, f.count("ingest_pending_uploads"))
			}
			afterFailureRevision, afterFailureHash, err := f.policy.SourceVersion(f.ctx, ref)
			if err != nil || afterFailureRevision != currentRevision || afterFailureHash != currentHash {
				t.Fatalf("failed replacement changed source: revision=%d hash=%q err=%v", afterFailureRevision, afterFailureHash, err)
			}
			_, _, afterFailureObjects := backend.snapshot()
			if !reflect.DeepEqual(beforeObjects, afterFailureObjects) {
				t.Fatal("failed replacement leaked an attempt object")
			}

			f.exec("INSERT INTO datasets(id,name,owner_id) VALUES('alias','Alias','owner'); INSERT INTO dataset_data(dataset_id,data_id) VALUES('alias','source')")
			_, n, err = f.writer.ReplaceAuthorized(f.ctx, []ingest.Item{{ID: "source", Text: "D", Filename: "source.txt"}}, nil, "", backend, f.proof(), "owned", "Owned", ingest.SourceCAS{Revision: currentRevision, RawContentHash: currentHash})
			if !errors.Is(err, access.ErrDocumentSharedMetadata) || n != 0 {
				t.Fatalf("shared source replacement: n=%d err=%v", n, err)
			}
		})
	}
}

func TestReplaceAuthorizedConcurrentCAS(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, backend := newAuthorizedFixture(t, dialect)
			f.db.SetMaxOpenConns(3)
			ref := access.DocumentRef{DatasetID: "owned", DataID: "source"}
			if _, _, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{{ID: "source", Text: "A"}}, nil, "", backend, f.proof(), "owned", "Owned"); err != nil {
				t.Fatal(err)
			}
			revision, hash, err := f.policy.SourceVersion(f.ctx, ref)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.policy.RegisterDocument(f.ctx, f.actor, ref, "a", access.DocumentRestricted); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			done := make(chan error, 2)
			for _, text := range []string{"B", "C"} {
				go func(text string) {
					<-start
					_, _, err := f.writer.ReplaceAuthorized(f.ctx, []ingest.Item{{ID: "source", Text: text}}, nil, "", backend, f.proof(), "owned", "Owned", ingest.SourceCAS{Revision: revision, RawContentHash: hash})
					done <- err
				}(text)
			}
			close(start)
			success, conflict := 0, 0
			for i := 0; i < 2; i++ {
				err := <-done
				switch {
				case err == nil:
					success++
				case errors.Is(err, access.ErrDocumentVersionConflict):
					conflict++
				default:
					t.Fatalf("concurrent replacement: %v", err)
				}
			}
			currentRevision, currentHash, err := f.policy.SourceVersion(f.ctx, ref)
			if err != nil || success != 1 || conflict != 1 || currentRevision <= revision || currentHash == hash || f.count("ingest_pending_uploads") != 0 {
				t.Fatalf("concurrent CAS: success=%d conflict=%d revision=%d hash=%q err=%v journal=%d", success, conflict, currentRevision, currentHash, err, f.count("ingest_pending_uploads"))
			}
		})
	}
}
func TestIngestAuthorizedDuplicateAndLocalPrivacy(t *testing.T) {
	f, _ := newAuthorizedFixture(t, "sqlite")
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "uploads")
	items := []ingest.Item{{Text: "same"}, {Text: "same"}}
	r, n, err := f.writer.IngestAuthorized(f.ctx, items, nil, dir, nil, f.proof(), "owned", "Owned")
	if err != nil || n != 2 || r[0].FilePath != r[1].FilePath {
		t.Fatalf("duplicate %+v %d %v", r, n, err)
	}
	path := strings.TrimPrefix(r[0].FilePath, "file://")
	for p, mode := range map[string]os.FileMode{path: 0600, filepath.Dir(path): 0700, filepath.Dir(filepath.Dir(path)): 0700} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("mode %s %v %v", p, info, err)
		}
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "same" {
		t.Fatal(err, string(body))
	}
	if !filepath.IsAbs(path) || !strings.HasPrefix(path, dir+string(filepath.Separator)) {
		t.Fatal(path)
	}
	// A supplied plain LocalStorage keeps legacy storagePath semantics.
	other := t.TempDir()
	plain := storage.NewLocalStorage(other)
	if _, _, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{{Text: "other"}}, nil, dir, plain, f.proof(), "owned", "Owned"); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(other)
	if len(entries) != 0 {
		t.Fatal("wrong local root used")
	}
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{{Text: "symlink"}}, nil, link, nil, f.proof(), "owned", "Owned"); err == nil {
		t.Fatal("symlink root accepted")
	}
}
func TestIngestAuthorizedFailuresAndRecovery(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, kind := range []string{"partial-save", "cleanup-failure", "cancel", "credential-expiry"} {
				t.Run(kind, func(t *testing.T) {
					f, b := newAuthorizedFixture(t, dialect)
					actor := f.proof()
					ctx, cancel := context.WithCancel(f.ctx)
					defer cancel()
					if kind == "credential-expiry" {
						actor.Credential.ExpiresAt = time.Now().Unix() + 1
					}
					calls := 0
					b.saveHook = func(ctx context.Context, key string) error {
						calls++
						if kind == "cancel" {
							cancel()
							return ctx.Err()
						}
						if kind == "credential-expiry" {
							time.Sleep(time.Until(time.Unix(actor.Credential.ExpiresAt, 0)) + 20*time.Millisecond)
							return nil
						}
						if calls == 2 {
							return errors.New("partial backend failure")
						}
						return nil
					}
					b.deleteFail = kind == "cleanup-failure"
					_, n, err := f.writer.IngestAuthorized(ctx, []ingest.Item{{Text: "first"}, {Text: "second"}}, nil, t.TempDir(), b, actor, "new", "New")
					if err == nil || n != 0 || f.count("data") != 0 || f.count("dataset_data") != 0 || f.count("datasets") != 2 {
						t.Fatalf("partial metadata after error: %v", err)
					}
					_, _, objects := b.snapshot()
					if kind == "cleanup-failure" {
						if f.count("ingest_pending_uploads") != 1 || len(objects) != 2 || !strings.Contains(err.Error(), "cleanup pending") {
							t.Fatal("cleanup failure lost journal", err)
						}
						b.deleteFail = false
						if n, err := f.writer.RecoverPendingIngestOffline(f.ctx, "", b); err != nil || n != 0 {
							t.Fatalf("unexpired recovery %d %v", n, err)
						}
						f.exec("UPDATE ingest_pending_uploads SET expires_at=1")
						if n, err := f.writer.RecoverPendingIngestOffline(f.ctx, "", b); err != nil || n != 1 {
							t.Fatalf("offline recovery %d %v", n, err)
						}
					} else if f.count("ingest_pending_uploads") != 0 || len(objects) != 0 {
						t.Fatal("returned failure left owned bytes/journal")
					}
				})
			}
		})
	}
}
func TestIngestAuthorizedNonCooperativeSaveWaitsBeforeCleanup(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, b := newAuthorizedFixture(t, dialect)
			ctx, cancel := context.WithTimeout(f.ctx, 300*time.Millisecond)
			defer cancel()
			started, release := make(chan struct{}), make(chan struct{})
			b.saveHook = func(context.Context, string) error { close(started); <-release; return nil }
			done := make(chan error, 1)
			go func() {
				_, _, err := f.writer.IngestAuthorized(ctx, []ingest.Item{{Text: "delayed"}}, nil, "", b, f.proof(), "owned", "Owned")
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("Save not reached")
			}
			<-ctx.Done()
			time.Sleep(30 * time.Millisecond)
			_, deletes, _ := b.snapshot()
			if deletes != 0 {
				t.Fatal("cleanup raced unfinished Save")
			}
			select {
			case err := <-done:
				t.Fatalf("returned before backend: %v", err)
			default:
			}
			close(release)
			if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline %v", err)
			}
			_, _, objects := b.snapshot()
			if len(objects) != 0 || f.count("data") != 0 || f.count("ingest_pending_uploads") != 0 {
				t.Fatal("late Save published or not cleaned")
			}
		})
	}
}

func TestIngestAuthorizedRevokeAndConcurrentRepeat(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			t.Run("revoke-serialized", func(t *testing.T) {
				f, b := newAuthorizedFixture(t, dialect)
				f.db.SetMaxOpenConns(3)
				entered, release := make(chan struct{}), make(chan struct{})
				b.saveHook = func(context.Context, string) error { close(entered); <-release; return nil }
				done := make(chan error, 1)
				go func() {
					_, _, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{{Text: "write before revoke"}}, nil, "", b, f.proof(), "owned", "Owned")
					done <- err
				}()
				select {
				case <-entered:
				case <-time.After(2 * time.Second):
					t.Fatal("Save not entered")
				}
				revoke := make(chan error, 1)
				go func() {
					_, err := f.db.ExecContext(f.ctx, "UPDATE users SET is_active=false WHERE id='owner'")
					revoke <- err
				}()
				select {
				case err := <-revoke:
					close(release)
					<-done
					t.Fatalf("revocation crossed active Save fence: %v", err)
				case <-time.After(80 * time.Millisecond):
				}
				close(release)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if err := <-revoke; err != nil {
					t.Fatal(err)
				}
				saves, _, _ := b.snapshot()
				b.saveHook = nil
				if _, _, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{{Text: "after revoke"}}, nil, "", b, f.proof(), "owned", "Owned"); err == nil {
					t.Fatal("revocation bypassed")
				}
				after, _, _ := b.snapshot()
				if saves != after {
					t.Fatal("revoked principal reached Save")
				}
			})
			t.Run("concurrent-repeat", func(t *testing.T) {
				f, b := newAuthorizedFixture(t, dialect)
				f.db.SetMaxOpenConns(3)
				start := make(chan struct{})
				done := make(chan error, 2)
				for i := 0; i < 2; i++ {
					go func() {
						<-start
						_, _, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{{Text: "same concurrent bytes"}}, nil, "", b, f.proof(), "owned", "Owned")
						done <- err
					}()
				}
				close(start)
				for i := 0; i < 2; i++ {
					if err := <-done; err != nil {
						t.Fatal(err)
					}
				}
				saves, _, _ := b.snapshot()
				if saves != 1 || f.count("data") != 1 || f.count("dataset_data") != 1 || f.count("ingest_pending_uploads") != 0 {
					t.Fatalf("concurrent repeat duplicated saves=%d", saves)
				}
			})
		})
	}
}
func TestIngestAuthorizedReservationExpiry(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, b := newAuthorizedFixture(t, dialect)
			if dialect == "sqlite" {
				f.exec(`CREATE TRIGGER expire_ingest AFTER INSERT ON ingest_pending_uploads BEGIN UPDATE ingest_pending_uploads SET expires_at=1 WHERE attempt_id=NEW.attempt_id; END`)
			} else {
				f.exec(`CREATE FUNCTION expire_ingest() RETURNS TRIGGER AS $$ BEGIN NEW.expires_at:=1; RETURN NEW; END; $$ LANGUAGE plpgsql`)
				f.exec(`CREATE TRIGGER expire_ingest BEFORE INSERT ON ingest_pending_uploads FOR EACH ROW EXECUTE FUNCTION expire_ingest()`)
			}
			_, n, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{{Text: "expired reservation"}}, nil, "", b, f.proof(), "owned", "Owned")
			saves, _, _ := b.snapshot()
			if err == nil || n != 0 || saves != 0 || f.count("data") != 0 || f.count("ingest_pending_uploads") != 0 {
				t.Fatalf("expired attempt saved: %d %v saves=%d", n, err, saves)
			}
		})
	}
}
func TestIngestAuthorizedOfflineRecoveryConfinement(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, kind := range []string{"crash", "wrong-destination", "foreign-key", "traversal", "published", "invalid-uuid", "null-keys"} {
				t.Run(kind, func(t *testing.T) {
					f, b := newAuthorizedFixture(t, dialect)
					id := uuid.NewString()
					key := "ingest-authorized/" + id + "/0-text"
					location := "storage://" + key
					identity := "backend:test-fixture"
					b.objects[key] = []byte("pending")
					b.objects["foreign/keep"] = []byte("immutable foreign")
					switch kind {
					case "wrong-destination":
						identity = "backend:wrong-bucket"
					case "foreign-key":
						location = "storage://foreign/keep"
					case "traversal":
						location = "storage://ingest-authorized/" + id + "/../foreign/keep"
					case "published":
						f.exec("INSERT INTO data(id,name,owner_id,raw_data_location,original_data_location) VALUES('published','Published','owner',$1,$2)", location, location)
					case "invalid-uuid":
						id = "../bad"
					}
					encoded, _ := json.Marshal([]string{location})
					if kind == "null-keys" {
						encoded = []byte("null")
					}
					f.exec("INSERT INTO ingest_pending_uploads(attempt_id,destination,keys_json,expires_at) VALUES($1,$2,$3,1)", id, identity, string(encoded))
					n, err := f.writer.RecoverPendingIngestOffline(f.ctx, "", b)
					_, deletes, objects := b.snapshot()
					if kind == "crash" {
						if err != nil || n != 1 || deletes != 1 || f.count("ingest_pending_uploads") != 0 {
							t.Fatalf("crash cleanup %d %v", n, err)
						}
					} else if err == nil || n != 0 || deletes != 0 || f.count("ingest_pending_uploads") != 1 {
						t.Fatalf("unsafe cleanup %d %v deletes=%d", n, err, deletes)
					}
					if string(objects["foreign/keep"]) != "immutable foreign" {
						t.Fatal("foreign path altered")
					}
				})
			}
			t.Run("wrong-destination-before-any-delete", func(t *testing.T) {
				f, b := newAuthorizedFixture(t, dialect)
				ids := []string{"00000000-0000-4000-8000-000000000001", "ffffffff-ffff-4fff-8fff-ffffffffffff"}
				for i, id := range ids {
					identity := "backend:test-fixture"
					if i == 1 {
						identity = "backend:other"
					}
					key := "ingest-authorized/" + id + "/0-text"
					b.objects[key] = []byte("pending")
					encoded, _ := json.Marshal([]string{"storage://" + key})
					f.exec("INSERT INTO ingest_pending_uploads(attempt_id,destination,keys_json,expires_at) VALUES($1,$2,$3,1)", id, identity, string(encoded))
				}
				if _, err := f.writer.RecoverPendingIngestOffline(f.ctx, "", b); err == nil {
					t.Fatal("mixed destinations accepted")
				}
				_, deletes, _ := b.snapshot()
				if deletes != 0 {
					t.Fatal("wrong destination detected after deletion")
				}
			})
		})
	}
}
func TestIngestAuthorizedLocalCrashRecoveryAndWrongRoot(t *testing.T) {
	f, _ := newAuthorizedFixture(t, "sqlite")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	path := filepath.Join(root, "ingest-authorized", id, "0-text")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal([]string{"file://" + path})
	f.exec("INSERT INTO ingest_pending_uploads(attempt_id,destination,keys_json,expires_at) VALUES($1,$2,$3,1)", id, "local:"+root, string(encoded))
	if _, err := f.writer.RecoverPendingIngestOffline(f.ctx, t.TempDir(), nil); err == nil {
		t.Fatal("wrong root accepted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("wrong-root recovery deleted old path")
	}
	if n, err := f.writer.RecoverPendingIngestOffline(f.ctx, root, nil); err != nil || n != 1 {
		t.Fatalf("local crash %d %v", n, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("orphan remains", err)
	}
}

// The KMS fixture wraps fresh random DEKs; it never contacts an external service.
type ingestKMS struct {
	storage.KMS
	keys map[string][]byte
	arn  string
}

func (k *ingestKMS) EncryptDataKey(_ context.Context, r storage.EncryptDataKeyRequest) (storage.EncryptDataKeyResponse, error) {
	id := uuid.NewString()
	k.keys[id] = bytes.Clone(r.Plaintext)
	return storage.EncryptDataKeyResponse{CiphertextKeyRef: id, KeyRef: k.arn}, nil
}
func (k *ingestKMS) DecryptDataKey(_ context.Context, r storage.DecryptDataKeyRequest) (storage.DecryptDataKeyResponse, error) {
	return storage.DecryptDataKeyResponse{Plaintext: bytes.Clone(k.keys[r.CiphertextKeyRef]), KeyRef: k.arn}, nil
}
func TestIngestAuthorizedEncryptedLocalNeverStagesPlaintext(t *testing.T) {
	f, _ := newAuthorizedFixture(t, "sqlite")
	root := t.TempDir()
	kms := &ingestKMS{keys: map[string][]byte{}, arn: "arn:aws:kms:us-east-1:123456789012:key/12345678-1234-1234-1234-123456789012"}
	encrypted, err := storage.NewEncryptedStorage(storage.NewLocalStorage(root), kms, storage.EncryptedStorageConfig{WriteKeyARN: kms.arn, SpoolDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "never-plaintext")
	secret := []byte("Unique highly private original bytes")
	result, _, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{{FileData: secret}}, nil, dir, encrypted, f.proof(), "owned", "Owned")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result[0].FilePath, "storage://") {
		t.Fatal(result)
	}
	key := strings.TrimPrefix(result[0].FilePath, "storage://")
	cipher, err := os.ReadFile(filepath.Join(root, key))
	if err != nil || bytes.Contains(cipher, secret) || !bytes.HasPrefix(cipher, []byte("LEVARAE1")) {
		t.Fatal("not encrypted", err)
	}
	reader, err := encrypted.Load(f.ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || !bytes.Equal(body, secret) {
		t.Fatal("decrypted bytes", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("plaintext staging", err)
	}
}
func TestIngestAuthorizedMissingDestinationAndJournalFailClosed(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, b := newAuthorizedFixture(t, dialect)
			items := []ingest.Item{{Text: "test"}}
			f.writer.SetStorageIdentity("")
			if _, _, err := f.writer.IngestAuthorized(f.ctx, items, nil, "", b, f.proof(), "owned", "Owned"); err == nil {
				t.Fatal("missing backend identity accepted")
			}
			f.writer.SetStorageIdentity("test-fixture")
			f.exec("ALTER TABLE ingest_pending_uploads RENAME TO offline_journal")
			if _, _, err := f.writer.IngestAuthorized(f.ctx, items, nil, "", b, f.proof(), "owned", "Owned"); err == nil {
				t.Fatal("missing durable journal accepted")
			}
			saves, _, _ := b.snapshot()
			if saves != 0 || f.count("data") != 0 {
				t.Fatal("journal failure reached Save")
			}
		})
	}
}

func TestIngestAuthorizedCommitFailureRetainsRecoveryJournal(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, b := newAuthorizedFixture(t, dialect)
			f.exec(`CREATE TABLE ingest_commit_guard (user_id TEXT REFERENCES users(id) DEFERRABLE INITIALLY DEFERRED)`)
			if dialect == "sqlite" {
				f.exec(`CREATE TRIGGER fail_ingest_commit AFTER INSERT ON data BEGIN INSERT INTO ingest_commit_guard(user_id) VALUES('does-not-exist'); END`)
			} else {
				f.exec(`CREATE FUNCTION fail_ingest_commit() RETURNS TRIGGER AS $$ BEGIN INSERT INTO ingest_commit_guard(user_id) VALUES('does-not-exist'); RETURN NEW; END; $$ LANGUAGE plpgsql`)
				f.exec(`CREATE TRIGGER fail_ingest_commit AFTER INSERT ON data FOR EACH ROW EXECUTE FUNCTION fail_ingest_commit()`)
			}
			_, n, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{{Text: "commit uncertainty"}}, nil, "", b, f.proof(), "owned", "Owned")
			saves, deletes, objects := b.snapshot()
			if err == nil || n != 0 || !strings.Contains(err.Error(), "outcome uncertain") || saves != 1 || deletes != 0 || len(objects) != 1 || f.count("data") != 0 || f.count("ingest_pending_uploads") != 1 {
				t.Fatalf("uncertain commit cleanup: %v saves=%d deletes=%d objects=%d", err, saves, deletes, len(objects))
			}
			f.exec("UPDATE ingest_pending_uploads SET expires_at=1")
			if n, err := f.writer.RecoverPendingIngestOffline(f.ctx, "", b); err != nil || n != 1 {
				t.Fatalf("uncertain recovery %d %v", n, err)
			}
		})
	}
}
