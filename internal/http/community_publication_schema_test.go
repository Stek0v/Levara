package http

import "testing"

func TestCommunityPublicationSchemaMigration(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		f.exec("INSERT INTO graph_communities(id,summary,member_count) VALUES('legacy','original private summary',3)")
		check := func() {
			t.Helper()
			var summary, generation, sources string
			var verified int
			if err := f.db.QueryRow("SELECT summary,generation,sources_json,lineage_verified FROM graph_communities WHERE id='legacy'").Scan(&summary, &generation, &sources, &verified); err != nil {
				t.Fatal(err)
			}
			if summary != "original private summary" || generation != "" || sources != "[]" || verified != 0 {
				t.Fatalf("legacy publication adopted or changed: summary=%q generation=%q sources=%q verified=%d", summary, generation, sources, verified)
			}
		}
		check() // Fresh schema defaults.
		for _, column := range []string{"generation", "sources_json", "lineage_verified"} {
			f.exec("ALTER TABLE graph_communities DROP COLUMN " + column)
		}
		for i := 0; i < 2; i++ {
			if err := MigrateSchema(f.db); err != nil {
				t.Fatal(err)
			}
			check() // Populated old schema, then idempotent migration.
		}
		if _, err := f.db.Exec("UPDATE graph_communities SET lineage_verified=2 WHERE id='legacy'"); err == nil {
			t.Fatal("invalid verification state accepted")
		}
	})
}
