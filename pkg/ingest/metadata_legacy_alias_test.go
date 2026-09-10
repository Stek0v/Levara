package ingest_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/ingest"
)

func TestMetadataLegacyAliases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, transport := range []string{"coordinator", "metadata"} {
				t.Run(transport, func(t *testing.T) {
					for _, scenario := range []struct {
						name, restriction           string
						registered, repeat, allowed bool
					}{
						{name: "legacy-editor", allowed: true},
						{name: "legacy-exact-repeat", repeat: true, allowed: true},
						{name: "legacy-revoked", restriction: "revoked"},
						{name: "legacy-repeat-revoked", restriction: "revoked", repeat: true},
						{name: "legacy-viewer", restriction: "viewer"},
						{name: "legacy-public", restriction: "public"},
						{name: "mixed-exact-repeat", registered: true, repeat: true, allowed: true},
						{name: "mixed-revoked", registered: true, repeat: true, restriction: "revoked"},
						{name: "mixed-hold", registered: true, repeat: true, restriction: "hold"},
						{name: "mixed-tombstone", registered: true, repeat: true, restriction: "tombstone"},
					} {
						t.Run(scenario.name, func(t *testing.T) {
							f, backend := newAuthorizedFixture(t, dialect)
							f.exec("INSERT INTO datasets(id,name,owner_id) VALUES('shared','Shared','editor')")
							f.exec("INSERT INTO dataset_shares(id,dataset_id,user_id,role) VALUES('legacy-share','shared','owner','editor')")
							items := []ingest.Item{{Text: "original extraction", Filename: "report.txt"}}
							originals := []ingest.Item{{ID: "source", FileData: []byte("immutable original source"), Filename: "report.txt"}}
							first, n, err := f.writer.IngestAuthorized(f.ctx, items, originals, "", backend, f.proof(), "shared", "Shared")
							if err != nil || n != 1 {
								t.Fatalf("initial editor upload: n=%d err=%v", n, err)
							}
							if _, _, err := f.writer.IngestAuthorized(f.ctx, items, originals, "", backend, f.proof(), "owned", "Owned"); err != nil {
								t.Fatal("own alias exact repeat", err)
							}
							// The existing PK makes a repeated inclusion idempotent.
							f.exec("INSERT INTO dataset_data(dataset_id,data_id) VALUES('shared','source') ON CONFLICT DO NOTHING")
							if scenario.registered {
								if _, err := f.policy.RegisterDocument(f.ctx, f.actor, access.DocumentRef{DatasetID: "owned", DataID: "source"}, "a", access.DocumentRestricted); err != nil {
									t.Fatal(err)
								}
							}
							switch scenario.restriction {
							case "revoked":
								f.exec("DELETE FROM dataset_shares WHERE id='legacy-share'")
							case "viewer":
								f.exec("UPDATE dataset_shares SET role='viewer' WHERE id='legacy-share'")
							case "public":
								f.exec("UPDATE datasets SET owner_id='' WHERE id='shared'")
							case "hold":
								f.exec("UPDATE document_resources SET hold=true WHERE data_id='source'")
							case "tombstone":
								f.exec("UPDATE document_resources SET tombstoned=true WHERE data_id='source'")
							}
							f.exec("UPDATE data SET pipeline_status='{\"collection\":\"COMPLETED\"}',token_count=42 WHERE id='source'")
							before := legacyAliasState(t, f)
							beforeSaves, beforeDeletes, beforeObjects := backend.snapshot()
							if !scenario.repeat {
								items[0].Text = "different extraction of same original"
								first[0].Name = "changed metadata"
							}
							if transport == "coordinator" {
								if !scenario.allowed {
									// Even the earlier valid item must not reach storage.
									items = append([]ingest.Item{{Text: "valid new batch member"}}, items...)
									originals = append([]ingest.Item{{ID: "new", FileData: []byte("new original")}}, originals...)
								}
								_, n, err = f.writer.IngestAuthorized(f.ctx, items, originals, "", backend, f.proof(), "owned", "Owned")
							} else {
								if !scenario.allowed {
									valid := first[0]
									valid.ID = "new"
									first = append([]ingest.Result{valid}, first...)
								}
								n, err = f.writer.WriteMetadataAuthorized(f.ctx, first, f.proof(), "owned", "Owned")
							}
							if scenario.allowed {
								if err != nil || n != 1 {
									t.Fatalf("allowed write denied: n=%d err=%v", n, err)
								}
								if scenario.repeat && legacyAliasState(t, f) != before {
									t.Fatal("exact repeat changed source metadata/version/processing")
								}
								if !scenario.repeat && legacyAliasState(t, f).revision <= before.revision {
									t.Fatal("authorized change did not advance shared source version")
								}
							} else {
								if !errors.Is(err, access.ErrDocumentForbidden) || n != 0 {
									t.Errorf("alias without write permission accepted: n=%d err=%v", n, err)
								}
								if after := legacyAliasState(t, f); after != before {
									t.Errorf("denied batch changed source/associations: before=%+v after=%+v", before, after)
								}
							}
							if !scenario.allowed || scenario.repeat {
								saves, deletes, objects := backend.snapshot()
								if saves != beforeSaves || deletes != beforeDeletes || !reflect.DeepEqual(objects, beforeObjects) || f.count("ingest_pending_uploads") != 0 {
									t.Error("denied/repeated batch affected storage or cleanup journal")
								}
							}
						})
					}
				})
			}
		})
	}
}

type legacyAliasSnapshot struct {
	name, raw, original, hash, pipeline string
	tokens, revision                    int64
	dataRows, aliases, publications     int
}

func legacyAliasState(t *testing.T, f metadataFixture) legacyAliasSnapshot {
	t.Helper()
	var out legacyAliasSnapshot
	if err := f.db.QueryRow("SELECT name,raw_data_location,original_data_location,raw_content_hash,pipeline_status,token_count,source_revision FROM data WHERE id='source'").Scan(&out.name, &out.raw, &out.original, &out.hash, &out.pipeline, &out.tokens, &out.revision); err != nil {
		t.Fatal(err)
	}
	out.dataRows, out.aliases, out.publications = f.count("data"), f.count("dataset_data"), f.count("document_index_publications")
	return out
}
