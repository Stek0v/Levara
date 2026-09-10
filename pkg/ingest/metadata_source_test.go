package ingest_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	httpapi "github.com/stek0v/levara/internal/http"
	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/ingest"
)

func metadataSource(t *testing.T, f metadataFixture, ref access.DocumentRef) access.DocumentPublicationLineage {
	t.Helper()
	version, hash, err := f.policy.SourceVersion(f.ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	return access.DocumentPublicationLineage{SourceRevision: version, RawContentHash: hash, SourcesJSON: "[]"}
}
func metadataPublish(f metadataFixture, ref access.DocumentRef, generation string, source access.DocumentPublicationLineage) error {
	ctx, cancel := context.WithTimeout(f.ctx, 4*time.Second)
	defer cancel()
	p, release, err := f.policy.BeginReadFence(ctx, httpapi.GetDBProvider() == httpapi.DBSQLite)
	if err != nil {
		return err
	}
	defer release()
	return p.CommitDocumentIndexVersioned(ctx, f.actor, ref, 0, "docs", generation, source)
}
func TestMetadataSourceRevisionInvalidationAndExactRepeat(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, b := newAuthorizedFixture(t, dialect)
			ref := access.DocumentRef{DatasetID: "owned", DataID: "source"}
			item := ingest.Item{ID: "source", Text: "A", Filename: "source.txt", Room: "docs"}
			add := func(target, name string) ingest.Result {
				t.Helper()
				r, n, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{item}, nil, "", b, f.proof(), target, name)
				if err != nil || n != 1 {
					t.Fatalf("ingest %d %v", n, err)
				}
				return r[0]
			}
			first := add("owned", "Owned")
			initial := metadataSource(t, f, ref)
			if initial.SourceRevision <= 1 {
				t.Fatal(initial)
			}
			if err := metadataPublish(f, ref, "A", initial); err != nil {
				t.Fatal(err)
			}
			before, _, _ := b.snapshot()
			add("owned", "Owned")
			after, _, _ := b.snapshot()
			if after != before || metadataSource(t, f, ref).SourceRevision != initial.SourceRevision || f.count("document_index_publications") != 1 {
				t.Fatal("exact repeat changed source/publication")
			}
			f.exec("UPDATE data SET pipeline_status='{\"docs\":\"COMPLETED\"}',token_count=19 WHERE id='source'")
			add("owned", "Owned")
			if metadataSource(t, f, ref).SourceRevision != initial.SourceRevision || f.count("document_index_publications") != 1 {
				t.Fatal("pipeline-only update changed source")
			}
			item.Text = "B"
			add("owned", "Owned")
			if got := metadataSource(t, f, ref); got.SourceRevision != initial.SourceRevision+1 || got.RawContentHash == initial.RawContentHash {
				t.Fatal(got)
			}
			if f.count("document_index_publications") != 0 {
				t.Fatal("changed bytes retained publication")
			}
			item.Text = "A"
			add("owned", "Owned")
			again := metadataSource(t, f, ref)
			if again.SourceRevision != initial.SourceRevision+2 || again.RawContentHash != initial.RawContentHash {
				t.Fatal(again)
			}
			if err := metadataPublish(f, ref, "late-A", initial); !errors.Is(err, access.ErrDocumentVersionConflict) {
				t.Fatalf("A->B->A revived old source: %v", err)
			}
			add("alias", "Alias")
			other := access.DocumentRef{DatasetID: "alias", DataID: "source"}
			for _, r := range []access.DocumentRef{ref, other} {
				if err := metadataPublish(f, r, "A3", again); err != nil {
					t.Fatal(err)
				}
			}
			if f.count("document_index_publications") != 2 {
				t.Fatal("alias publication missing")
			}
			item.Room = "new processing metadata"
			add("owned", "Owned")
			meta := metadataSource(t, f, ref)
			if meta.SourceRevision != initial.SourceRevision+3 || meta.RawContentHash != initial.RawContentHash || f.count("document_index_publications") != 0 {
				t.Fatal("metadata-only update did not invalidate all aliases", meta)
			}
			var status string
			var tokens int
			if err := f.db.QueryRow("SELECT pipeline_status,token_count FROM data WHERE id='source'").Scan(&status, &tokens); err != nil || status != "{}" || tokens != -1 {
				t.Fatal("metadata update kept pipeline state", err, status, tokens)
			}
			// Trusted compatibility upserts also advance source state and revoke lineage.
			if err := metadataPublish(f, ref, "metadata", meta); err != nil {
				t.Fatal(err)
			}
			result := add("owned", "Owned")
			result.Name = "trusted rename"
			if _, err := f.writer.WriteMetadata(f.ctx, []ingest.Result{result}, "owner", "owned", "Owned"); err != nil {
				t.Fatal(err)
			}
			if metadataSource(t, f, ref).SourceRevision != initial.SourceRevision+4 || f.count("document_index_publications") != 0 {
				t.Fatal("trusted upsert failed invalidation")
			}
			current := metadataSource(t, f, ref)
			if err := metadataPublish(f, ref, "trusted", current); err != nil {
				t.Fatal(err)
			}
			if _, err := f.writer.WriteMetadata(f.ctx, []ingest.Result{result}, "owner", "owned", "Owned"); err != nil {
				t.Fatal(err)
			}
			if metadataSource(t, f, ref).SourceRevision != initial.SourceRevision+4 || f.count("document_index_publications") != 1 {
				t.Fatal("trusted exact repeat invalidated")
			}
			// The first published blob is immutable even when source metadata advances.
			_, _, objects := b.snapshot()
			if string(objects[strings.TrimPrefix(first.FilePath, "storage://")]) != "A" {
				t.Fatal("old source blob overwritten")
			}
		})
	}
}
func TestMetadataSourceInvalidationRollback(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, b := newAuthorizedFixture(t, dialect)
			ref := access.DocumentRef{DatasetID: "owned", DataID: "source"}
			if _, _, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{{ID: "source", Text: "A"}}, nil, "", b, f.proof(), "owned", "Owned"); err != nil {
				t.Fatal(err)
			}
			initial := metadataSource(t, f, ref)
			if err := metadataPublish(f, ref, "A", initial); err != nil {
				t.Fatal(err)
			}
			if dialect == "sqlite" {
				f.exec(`CREATE TRIGGER reject_source_link BEFORE INSERT ON dataset_data WHEN NEW.dataset_id='reject' BEGIN SELECT RAISE(ABORT,'reject source link'); END`)
			} else {
				f.exec(`CREATE FUNCTION reject_source_link() RETURNS TRIGGER AS $$ BEGIN IF NEW.dataset_id='reject' THEN RAISE EXCEPTION 'reject source link'; END IF; RETURN NEW; END; $$ LANGUAGE plpgsql`)
				f.exec(`CREATE TRIGGER reject_source_link BEFORE INSERT ON dataset_data FOR EACH ROW EXECUTE FUNCTION reject_source_link()`)
			}
			if _, _, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{{ID: "source", Text: "B"}}, nil, "", b, f.proof(), "reject", "Rejected"); err == nil {
				t.Fatal("failed association accepted")
			}
			if got := metadataSource(t, f, ref); got != initial {
				t.Fatal("rolled-back update advanced source", got)
			}
			if ok, err := f.policy.DocumentIndexPublished(f.ctx, ref, 0, "docs", "A"); err != nil || !ok {
				t.Fatal("rolled-back update invalidated publication", err)
			}
			if f.count("datasets") != 2 || f.count("ingest_pending_uploads") != 0 {
				t.Fatal("failed update left partial SQL")
			}
		})
	}
}
func TestMetadataSourcePublicationUpdateSerialization(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, order := range []string{"publish-first", "update-first"} {
				t.Run(order, func(t *testing.T) {
					f, b := newAuthorizedFixture(t, dialect)
					f.db.SetMaxOpenConns(3)
					ref := access.DocumentRef{DatasetID: "owned", DataID: "source"}
					if _, _, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{{ID: "source", Text: "A"}}, nil, "", b, f.proof(), "owned", "Owned"); err != nil {
						t.Fatal(err)
					}
					old := metadataSource(t, f, ref)
					update := func() error {
						_, _, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{{ID: "source", Text: "B"}}, nil, "", b, f.proof(), "owned", "Owned")
						return err
					}
					if order == "publish-first" {
						ctx, cancel := context.WithTimeout(f.ctx, 3*time.Second)
						defer cancel()
						p, release, err := f.policy.BeginReadFence(ctx, dialect == "sqlite")
						if err != nil {
							t.Fatal(err)
						}
						defer release()
						done := make(chan error, 1)
						go func() { done <- update() }()
						select {
						case err := <-done:
							t.Fatalf("update crossed publication fence %v", err)
						case <-time.After(70 * time.Millisecond):
						}
						if err := p.CommitDocumentIndexVersioned(ctx, f.actor, ref, 0, "docs", "A", old); err != nil {
							t.Fatal(err)
						}
						if err := <-done; err != nil {
							t.Fatal(err)
						}
					} else {
						started, release := make(chan struct{}), make(chan struct{})
						b.saveHook = func(context.Context, string) error { close(started); <-release; return nil }
						updated := make(chan error, 1)
						go func() { updated <- update() }()
						select {
						case <-started:
						case <-time.After(2 * time.Second):
							t.Fatal("update not started")
						}
						published := make(chan error, 1)
						go func() { published <- metadataPublish(f, ref, "late-A", old) }()
						select {
						case err := <-published:
							close(release)
							<-updated
							t.Fatalf("publication crossed metadata fence %v", err)
						case <-time.After(70 * time.Millisecond):
						}
						close(release)
						if err := <-updated; err != nil {
							t.Fatal(err)
						}
						if err := <-published; !errors.Is(err, access.ErrDocumentVersionConflict) {
							t.Fatalf("stale concurrent publication %v", err)
						}
					}
					if metadataSource(t, f, ref).SourceRevision != old.SourceRevision+1 || f.count("document_index_publications") != 0 {
						t.Fatal("update did not invalidate generation")
					}
				})
			}
		})
	}
}

func TestMetadataSourceCounterSurvivesDeleteRecreate(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, b := newAuthorizedFixture(t, dialect)
			ref := access.DocumentRef{DatasetID: "owned", DataID: "source"}
			items := []ingest.Item{{ID: "source", Text: "identical bytes"}}
			if _, _, err := f.writer.IngestAuthorized(f.ctx, items, nil, "", b, f.proof(), "owned", "Owned"); err != nil {
				t.Fatal(err)
			}
			old := metadataSource(t, f, ref)
			if err := metadataPublish(f, ref, "old-incarnation", old); err != nil {
				t.Fatal(err)
			}
			if err := f.policy.DeleteDocumentAssociation(f.ctx, f.actor, ref, 0, 0); err != nil {
				t.Fatal(err)
			}
			if f.count("data") != 0 || f.count("document_index_publications") != 0 {
				t.Fatal("delete did not remove old association/source/publication")
			}
			var before int64
			if err := f.db.QueryRow("SELECT value FROM source_revision_counter WHERE id=1").Scan(&before); err != nil {
				t.Fatal(err)
			}
			// Startup migration after pruning must preserve the counter's high watermark.
			if err := httpapi.MigrateSchema(f.db); err != nil {
				t.Fatal(err)
			}
			if _, _, err := f.writer.IngestAuthorized(f.ctx, items, nil, "", b, f.proof(), "owned", "Owned"); err != nil {
				t.Fatal(err)
			}
			current := metadataSource(t, f, ref)
			if current.SourceRevision <= before || current.SourceRevision <= old.SourceRevision || current.RawContentHash != old.RawContentHash {
				t.Fatalf("recreated source reused identity old=%+v new=%+v", old, current)
			}
			if err := metadataPublish(f, ref, "late-old-incarnation", old); !errors.Is(err, access.ErrDocumentVersionConflict) {
				t.Fatalf("old incarnation published: %v", err)
			}
			if err := metadataPublish(f, ref, "new-incarnation", current); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestMetadataSourceCounterExactRepeatAndFaultRollback(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, fault := range []string{"exact-repeat", "overflow", "missing-row", "missing-table"} {
				t.Run(fault, func(t *testing.T) {
					f, b := newAuthorizedFixture(t, dialect)
					ref := access.DocumentRef{DatasetID: "owned", DataID: "source"}
					items := []ingest.Item{{ID: "source", Text: "A"}}
					if _, _, err := f.writer.IngestAuthorized(f.ctx, items, nil, "", b, f.proof(), "owned", "Owned"); err != nil {
						t.Fatal(err)
					}
					initial := metadataSource(t, f, ref)
					if err := metadataPublish(f, ref, "A", initial); err != nil {
						t.Fatal(err)
					}
					switch fault {
					case "overflow":
						f.exec("UPDATE source_revision_counter SET value=9223372036854775807 WHERE id=1")
					case "missing-row":
						f.exec("DELETE FROM source_revision_counter")
					case "missing-table":
						f.exec("ALTER TABLE source_revision_counter RENAME TO unavailable_counter")
					}
					if fault != "exact-repeat" {
						items[0].Text = "B"
					}
					_, _, err := f.writer.IngestAuthorized(f.ctx, items, nil, "", b, f.proof(), "owned", "Owned")
					if (fault == "exact-repeat") != (err == nil) {
						t.Fatalf("fault outcome %v", err)
					}
					if current := metadataSource(t, f, ref); current != initial {
						t.Fatalf("failed/repeated ingest changed source %+v", current)
					}
					if ok, err := f.policy.DocumentIndexPublished(f.ctx, ref, 0, "docs", "A"); err != nil || !ok {
						t.Fatal("failed/repeated ingest invalidated old generation", err)
					}
					if f.count("ingest_pending_uploads") != 0 {
						t.Fatal("returned counter fault left pending blobs")
					}
					if fault == "exact-repeat" {
						var value int64
						if err := f.db.QueryRow("SELECT value FROM source_revision_counter WHERE id=1").Scan(&value); err != nil || value != initial.SourceRevision {
							t.Fatal("exact repeat allocated version", err, value)
						}
					}
				})
			}
		})
	}
}
func TestMetadataSourceCounterConcurrentDocumentsUnique(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f, b := newAuthorizedFixture(t, dialect)
			f.db.SetMaxOpenConns(3)
			start := make(chan struct{})
			done := make(chan error, 2)
			for _, id := range []string{"a", "b"} {
				go func(id string) {
					<-start
					_, _, err := f.writer.IngestAuthorized(f.ctx, []ingest.Item{{ID: id, Text: "same content"}}, nil, "", b, f.proof(), "owned", "Owned")
					done <- err
				}(id)
			}
			close(start)
			for i := 0; i < 2; i++ {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			a := metadataSource(t, f, access.DocumentRef{DatasetID: "owned", DataID: "a"})
			bVersion := metadataSource(t, f, access.DocumentRef{DatasetID: "owned", DataID: "b"})
			if a.SourceRevision <= 1 || bVersion.SourceRevision <= 1 || a.SourceRevision == bVersion.SourceRevision {
				t.Fatalf("global versions not unique %+v %+v", a, bVersion)
			}
		})
	}
}
