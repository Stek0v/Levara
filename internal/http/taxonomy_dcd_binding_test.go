package http

import (
	"context"
	"testing"

	"github.com/stek0v/levara/pkg/vsamemory"
)

func TestDCDNativeSourceBindingRequiresExactDatasetAndSource(t *testing.T) {
	routes := []dcdRouteCandidate{{DatasetID: "dataset", DocumentID: "taxonomy-id", SourceDocumentID: "source-id", Confidence: 1}}
	for _, c := range []vsamemory.Candidate{
		{DatasetID: "foreign", SourceDocumentID: "source-id"},
		{SourceDocumentID: "source-id"},
		{DatasetID: "dataset", SourceDocumentID: "other"},
		{DatasetID: "dataset", DocumentID: "taxonomy-id"},
	} {
		if got := dcdRouteCandidateBoost(c, routes); got != 0 {
			t.Fatalf("foreign or missing native identity boosted: %+v => %v", c, got)
		}
	}
	if got := dcdRouteCandidateBoost(vsamemory.Candidate{DatasetID: "dataset", SourceDocumentID: "source-id"}, routes); got != .35 {
		t.Fatalf("native binding boost = %v", got)
	}
	items := []graphContextItem{
		{Provider: graphContextProviderSQL, DatasetID: "dataset", DocumentID: "other", TargetName: "baseline"},
		{Provider: graphContextProviderSQL, DatasetID: "dataset", DocumentID: "source-id", TargetName: "bound"},
	}
	if rerankNativeGraphItemsByDCDRoute(items, nil)[0].TargetName != "baseline" {
		t.Fatal("off/observe changed order")
	}
	got := rerankNativeGraphItemsByDCDRoute(items, routes)
	if got[0].TargetName != "bound" || items[0].TargetName != "baseline" || len(got) != len(items) {
		t.Fatal("binding lift changed input or result count")
	}
}

func TestDCDAuthenticatedEmptyTenantIsExact(t *testing.T) {
	withSQLiteProvider(t)
	db := newDCDRouteSchemaDB(t)
	t.Cleanup(func() { _ = db.Close() })
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	seedDCDRouteResolverRows(t, db)
	rows, err := loadDCDRouteRows(context.Background(), db, dcdRouteScope{OwnerID: "alice", ExactTenant: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("empty selected tenant leaked nonempty tenant: %+v", rows)
	}
}
