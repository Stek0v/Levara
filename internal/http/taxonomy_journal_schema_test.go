package http

import "testing"

func TestTaxonomyJournalSchemaMigration(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		f.exec("INSERT INTO knowledge_domains(id,owner_id,team_id,dataset_id,name) VALUES('legacy-taxonomy','alice','','ds','Auth')")
		f.exec("DROP TABLE knowledge_taxonomy_runs")
		for i := 0; i < 2; i++ {
			if err := MigrateSchema(f.db); err != nil {
				t.Fatal(err)
			}
			var name string
			if err := f.db.QueryRow("SELECT name FROM knowledge_domains WHERE id='legacy-taxonomy'").Scan(&name); err != nil || name != "Auth" {
				t.Fatalf("legacy catalog changed: name=%q err=%v", name, err)
			}
		}
		f.exec("INSERT INTO knowledge_taxonomy_runs(id,owner_id,team_id,dataset_id,request_id,action,created_at) VALUES('run-a','alice','','ds','request','import','2026-10-06T00:00:00Z')")
		if _, err := f.db.Exec("INSERT INTO knowledge_taxonomy_runs(id,owner_id,team_id,dataset_id,request_id,action,created_at) VALUES('duplicate','alice','','ds','request','remove','2026-10-06T00:00:00Z')"); err == nil {
			t.Fatal("duplicate scoped request accepted")
		}
		for _, tc := range []struct{ id, owner, tenant, dataset string }{
			{"other-owner", "bob", "", "ds"}, {"other-tenant", "alice", "tenant", "ds"}, {"other-dataset", "alice", "", "other"},
		} {
			f.exec("INSERT INTO knowledge_taxonomy_runs(id,owner_id,team_id,dataset_id,request_id,action,created_at) VALUES($1,$2,$3,$4,$5,'import',$6)", tc.id, tc.owner, tc.tenant, tc.dataset, "request", "2026-10-06T00:00:00Z")
		}
		if _, err := f.db.Exec("INSERT INTO knowledge_taxonomy_runs(id,action,created_at) VALUES('bad-action','apply','2026-10-06T00:00:00Z')"); err == nil {
			t.Fatal("unknown journal action accepted")
		}
		if err := MigrateSchema(f.db); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM knowledge_taxonomy_runs").Scan(&count); err != nil || count != 4 {
			t.Fatalf("journal changed: count=%d err=%v", count, err)
		}
	})
}
