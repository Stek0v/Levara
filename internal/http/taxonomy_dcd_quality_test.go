package http

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/vsamemory"
)

// Frozen two-source native fixture: no model/corpus quality claim is inferred.
func TestTaxonomyDCDImportedNativeGraphQuality(t *testing.T) {
	t.Setenv("LLM_ENDPOINT", "")
	t.Setenv("LEVARA_GRAPH_CONTEXT_ORDER", graphContextOrderSQLOnly)
	t.Setenv("LEVARA_GRAPH_CONTEXT_LIMIT", "2")
	t.Setenv("LEVARA_DCD_ROUTE_MIN_CONFIDENCE", "0.01")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg, closeFn := newWorkspaceTestConfig(t)
		defer closeFn()
		cfg.DB = f.db
		cfg.RequireAuth = true
		cfg.EmbedClient = embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, 4, 1)
		RegisterTaxonomyAPI(f.app.Group("/api/v1"), cfg)
		f.app.Post("/api/v1/search", searchHandler(cfg))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		f.exec("INSERT INTO dataset_shares(id,dataset_id,user_id,role) VALUES('peer-quality-share','alpha','peer','viewer')")
		for _, id := range []string{"blob", "visible"} {
			ref := accesspkg.DocumentRef{DatasetID: "alpha", DataID: id}
			resource := f.r
			if id == "visible" {
				var err error
				resource, err = f.p.RegisterDocument(ctx, f.owner, ref, "a", accesspkg.DocumentRestricted)
				if err != nil {
					t.Fatal(err)
				}
			}
			if id == "visible" {
				var err error
				resource, err = f.p.GrantDocument(ctx, f.owner, ref, resource.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "peer"}, accesspkg.RoleViewer)
				if err != nil {
					t.Fatal(err)
				}
			}
			rev, hash, err := f.p.SourceVersion(ctx, ref)
			if err != nil {
				t.Fatal(err)
			}
			locked, release, err := f.p.BeginReadFence(ctx, GetDBProvider() == DBSQLite)
			if err != nil {
				t.Fatal(err)
			}
			err = locked.CommitDocumentIndexVersioned(ctx, f.owner, ref, resource.ContentRevision, "docs", "native-generation", accesspkg.DocumentPublicationLineage{SourceRevision: rev, RawContentHash: hash, SourcesJSON: "[]"})
			release()
			if err != nil {
				t.Fatal(err)
			}
			properties, _ := json.Marshal(map[string]any{"document_id": id, "content_revision": resource.ContentRevision, "collection": "docs", "generation": "native-generation"})
			prefix, target := "a", "Baseline"
			if id == "blob" {
				prefix, target = "b", "Bound"
			}
			f.exec("INSERT INTO graph_nodes(id,name,dataset_id,properties) VALUES($1,'Subject','alpha',$2),($3,$4,'alpha',$2)", prefix+"-source", string(properties), prefix+"-target", target)
			f.exec("INSERT INTO graph_edges(id,source_id,target_id,relationship_name,dataset_id,properties) VALUES($1,$2,$3,'uses','alpha',$4)", prefix+"-edge", prefix+"-source", prefix+"-target", string(properties))
		}
		seed := "# Domains\n## Native\n### Collections\n#### Policies\n##### Документ: Bound policy\nИсточник: blob\n"
		body, _ := json.Marshal(map[string]any{"seed": seed})
		f.expect("owner", "POST", "/datasets/alpha/taxonomy/import", string(body), 200, "X-Tenant-ID", "a")
		var taxonomyID, sourceID string
		if err := f.db.QueryRow(Q("SELECT id,source_document_id FROM knowledge_documents WHERE owner_id='owner' AND team_id='a' AND dataset_id='alpha'")).Scan(&taxonomyID, &sourceID); err != nil {
			t.Fatal(err)
		}
		if sourceID != "blob" || taxonomyID == sourceID {
			t.Fatalf("identities conflated: %s %s", taxonomyID, sourceID)
		}
		if err := cfg.Collections.Create("native-entities"); err != nil {
			t.Fatal(err)
		}
		if err := cfg.Collections.Insert("native-entities", "subject-vector", []float32{5, 1}, map[string]any{"name": "Subject", "dataset_id": "alpha", "document_id": "visible", "content_revision": 1, "collection": "docs", "generation": "native-generation"}); err != nil {
			t.Fatal(err)
		}
		query := `{"query_text":"Bound policy","query_type":"GRAPH_COMPLETION","collection":"native-entities","top_k":5,"include_debug":true}`
		results := map[string][]string{}
		for _, mode := range []string{"off", "observe", "boost"} {
			t.Setenv("LEVARA_DCD_ROUTER", mode)
			raw := f.expect("owner", "POST", "/search", query, 200, "X-Tenant-ID", "a")
			var response struct {
				Context []string `json:"context"`
				Debug   struct {
					DCD struct {
						Count int `json:"candidate_count"`
					} `json:"dcd_route"`
				} `json:"debug"`
			}
			if err := json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Context) != 2 {
				t.Fatalf("%s native context: %s", mode, raw)
			}
			if mode != "off" && response.Debug.DCD.Count == 0 {
				t.Fatalf("%s bypassed DCD: %s", mode, raw)
			}
			results[mode] = response.Context
		}
		if !reflect.DeepEqual(results["off"], results["observe"]) {
			t.Fatal("observe changed native order")
		}
		if !strings.Contains(results["off"][0], "Baseline") || !strings.Contains(results["boost"][0], "Bound") {
			t.Fatalf("no binding lift: %+v", results)
		}
		zeroResults := map[string]int{}
		for mode, lines := range results {
			if len(lines) == 0 {
				zeroResults[mode]++
			} else {
				zeroResults[mode] = 0
			}
		}
		t.Logf("frozen native GRAPH_COMPLETION: zero_result_counts=%v; bound_rank off=2 observe=2 boost=1; authorized_results=2", zeroResults)
		// Peer can read the baseline source only, despite dataset read access.
		peerRaw := f.expect("peer", "POST", "/search", query, 200, "X-Tenant-ID", "a")
		var peer struct {
			Context []string `json:"context"`
		}
		if err := json.Unmarshal(peerRaw, &peer); err != nil {
			t.Fatal(err)
		}
		if len(peer.Context) != 1 || !strings.Contains(peer.Context[0], "Baseline") {
			t.Fatalf("denied source changed peer baseline: %s", peerRaw)
		}
		for _, line := range peer.Context {
			if strings.Contains(line, "Bound") {
				t.Fatalf("binding admitted denied source: %s", peerRaw)
			}
		}
		t.Log("native peer deny check: denied_bound=0 authorized_baseline=1")
		// Authoritative SQL retirement wins over the imported binding.
		f.exec("UPDATE document_resources SET tombstoned=true WHERE dataset_id='alpha' AND data_id='blob'")
		raw := f.expect("owner", "POST", "/search", query, 200, "X-Tenant-ID", "a")
		var retired struct {
			Context []string `json:"context"`
		}
		json.Unmarshal(raw, &retired)
		for _, line := range retired.Context {
			if strings.Contains(line, "Bound") {
				t.Fatalf("retired binding admitted: %s", raw)
			}
		}
		if len(retired.Context) != 1 {
			t.Fatalf("retirement changed unrelated result: %s", raw)
		}
	})
}

func TestTaxonomyDCDNativeResolverAndVSAMetadataDialects(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.exec("INSERT INTO knowledge_domains(id,owner_id,team_id,dataset_id,name) VALUES('native-domain','owner','a','alpha','Native')")
		f.exec("INSERT INTO knowledge_collections(id,domain_id,owner_id,team_id,dataset_id,name) VALUES('native-collection','native-domain','owner','a','alpha','Policies')")
		f.exec("INSERT INTO knowledge_documents(id,collection_id,domain_id,owner_id,team_id,dataset_id,title,source_document_id) VALUES('taxonomy-id','native-collection','native-domain','owner','a','alpha','Bound policy','blob')")
		candidates, err := resolveDCDRouteCandidates(context.Background(), f.db, "Bound policy", dcdRouteScope{OwnerID: "owner", TeamID: "a", ExactTenant: true, AllowedDatasetIDs: []string{"alpha"}}, dcdRoutePolicy{MaxCandidates: 3})
		if err != nil || len(candidates) != 1 || candidates[0].SourceDocumentID != "blob" || candidates[0].DocumentID != "taxonomy-id" {
			t.Fatalf("resolver identity: %+v %v", candidates, err)
		}
		for _, scope := range []dcdRouteScope{
			{OwnerID: "owner", ExactTenant: true, AllowedDatasetIDs: []string{"alpha"}},
			{OwnerID: "owner", TeamID: "b", ExactTenant: true, AllowedDatasetIDs: []string{"alpha"}},
			{OwnerID: "foreign", TeamID: "a", ExactTenant: true, AllowedDatasetIDs: []string{"alpha"}},
			{OwnerID: "owner", TeamID: "a", ExactTenant: true, AllowedDatasetIDs: []string{"beta"}},
			{OwnerID: "owner", TeamID: "a", ExactTenant: true, AllowedDatasetIDs: []string{}},
		} {
			rows, err := loadDCDRouteRows(context.Background(), f.db, scope)
			if err != nil || len(rows) != 0 {
				t.Fatalf("scope leaked native bindings: %+v => %+v %v", scope, rows, err)
			}
		}
		f.exec("INSERT INTO graph_nodes(id,name,dataset_id) VALUES('s','Subject','alpha'),('t','Target','alpha')")
		f.exec("INSERT INTO graph_edges(id,source_id,target_id,relationship_name,dataset_id,properties) VALUES('e','s','t','uses','alpha',$1)", `{"document_id":"blob"}`)
		dialect := vsamemory.DialectPostgres
		if GetDBProvider() == DBSQLite {
			dialect = vsamemory.DialectSQLite
		}
		f.db.SetMaxOpenConns(1)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		store := vsamemory.NewStore(f.db, vsamemory.Config{Dialect: dialect, Dim: 128})
		if err := store.RebuildFromGraph(ctx, "alpha"); err != nil {
			t.Fatal(err)
		}
		got, err := store.QueryObject(ctx, "alpha", "s", "uses", 3)
		if err != nil || len(got) != 1 || got[0].SourceDocumentID != "blob" || got[0].DocumentID != "" {
			t.Fatalf("SQL native metadata: %+v %v", got, err)
		}
	})
}
