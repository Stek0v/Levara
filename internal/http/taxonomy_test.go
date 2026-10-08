package http

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/taxonomy"
	"strings"
	"sync"
	"testing"
	"time"
)

const taxonomyNativeSeed = "# Domains\n## auth\nОписание: Identity\nАлиасы: login\n### Collections\n#### sessions\n##### Документ: Rules\nИсточник: visible\n"

func taxonomyNativeAPI(f *documentHTTPFixture) {
	f.cfg.RequireAuth = true
	f.app = fiber.New(fiber.Config{DisableStartupMessage: true})
	f.app.Use(func(c *fiber.Ctx) error {
		user := c.Get("X-Test-User")
		c.Locals("user_id", user)
		if c.Get("X-Test-Unverified") != "true" {
			exp := time.Now().Add(time.Hour).Unix()
			if c.Get("X-Test-Expired") == "true" {
				exp = time.Now().Add(-time.Hour).Unix()
			}
			c.Locals("verified_jwt", jwtPayload{Sub: user, Exp: exp})
		}
		c.Locals("tenant_id", c.Get("X-Tenant-Id"))
		c.Locals("api_key_permissions", c.Get("X-Test-Key"))
		return c.Next()
	})
	RegisterTaxonomyAPI(f.app.Group("/api/v1"), f.cfg)
}
func taxonomyBody(value any) string { b, _ := json.Marshal(value); return string(b) }
func taxonomySnapshot(t *testing.T, f *documentHTTPFixture) string {
	t.Helper()
	result := [][][]any{}
	for _, table := range []string{"knowledge_domains", "knowledge_collections", "knowledge_documents", "knowledge_taxonomy_runs"} {
		rows, err := f.db.Query("SELECT * FROM " + table + " ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		values := [][]any{}
		for rows.Next() {
			row := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range row {
				dest[i] = &row[i]
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			values = append(values, row)
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("snapshot: %v %v", err, closeErr)
		}
		result = append(result, values)
	}
	return taxonomyBody(result)
}
func TestTaxonomyNativeLifecycle(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		taxonomyNativeAPI(f)
		endpoint := "/datasets/alpha/taxonomy"
		empty := f.expect("owner", "GET", endpoint, "", 200, "X-Tenant-Id", "a")
		if string(empty) != "{\"domains\":[]}" {
			t.Fatalf("empty: %s", empty)
		}
		req := taxonomy.ImportRequest{Seed: taxonomyNativeSeed, SourceName: "seed.md", SourceRevision: "v1", RequestID: "first"}
		body := f.expect("owner", "POST", endpoint+"/import", taxonomyBody(req), 200, "X-Tenant-Id", "a")
		var first taxonomy.ImportReport
		if err := json.Unmarshal(body, &first); err != nil {
			t.Fatal(err)
		}
		if first.Created != 3 || first.Domains != 1 || first.Collections != 1 || first.Documents != 1 {
			t.Fatalf("report: %+v", first)
		}
		baseline := taxonomySnapshot(t, f)
		replay := f.expect("owner", "POST", endpoint+"/import", taxonomyBody(req), 200, "X-Tenant-Id", "a")
		if string(replay) != string(body) || taxonomySnapshot(t, f) != baseline {
			t.Fatal("replay changed publication")
		}
		req.Seed = strings.ReplaceAll(req.Seed, "Identity", "Changed")
		f.expect("owner", "POST", endpoint+"/import", taxonomyBody(req), 409, "X-Tenant-Id", "a")
		if taxonomySnapshot(t, f) != baseline {
			t.Fatal("conflict changed publication")
		}
		req.RequestID = "second"
		f.expect("owner", "POST", endpoint+"/import", taxonomyBody(req), 200, "X-Tenant-Id", "a")
		catalogBody := f.expect("owner", "GET", endpoint, "", 200, "X-Tenant-Id", "a")
		var catalog taxonomy.Catalog
		json.Unmarshal(catalogBody, &catalog)
		if len(catalog.Domains) != 1 || catalog.Domains[0].Description != "Changed" || catalog.Domains[0].Collections[0].Documents[0].ID == "visible" {
			t.Fatalf("identity: %s", catalogBody)
		}
		ids := catalog.Domains[0].ID + "/" + catalog.Domains[0].Collections[0].ID + "/" + catalog.Domains[0].Collections[0].Documents[0].ID
		req.RequestID = "third"
		req.Seed = strings.ReplaceAll(req.Seed, "Описание: Changed\n", "")
		f.expect("owner", "POST", endpoint+"/import", taxonomyBody(req), 200, "X-Tenant-Id", "a")
		json.Unmarshal(f.expect("owner", "GET", endpoint, "", 200, "X-Tenant-Id", "a"), &catalog)
		if catalog.Domains[0].Description != "Changed" || ids != catalog.Domains[0].ID+"/"+catalog.Domains[0].Collections[0].ID+"/"+catalog.Domains[0].Collections[0].Documents[0].ID {
			t.Fatal("stable natural key/declared fields lost")
		}
		f.exec("INSERT INTO dataset_shares(id,dataset_id,user_id,role) VALUES('taxonomy-editor','alpha','peer','editor')")
		for _, tc := range []struct {
			name, user, seed, headers string
			status                    int
		}{
			{"second_malformed", "owner", taxonomyNativeSeed + "## broken\nunsupported", "", 400},
			{"viewer", "viewer", taxonomyNativeSeed, "", 403},
			{"foreign", "foreign", taxonomyNativeSeed, "", 403},
			{"foreign_source", "owner", strings.ReplaceAll(taxonomyNativeSeed, "visible", "missing"), "", 403},
			{"source_denied", "peer", strings.ReplaceAll(taxonomyNativeSeed, "visible", "blob"), "", 403},
		} {
			t.Run(tc.name, func(t *testing.T) {
				before := taxonomySnapshot(t, f)
				f.expect(tc.user, "POST", endpoint+"/import", taxonomyBody(taxonomy.ImportRequest{Seed: tc.seed}), tc.status, "X-Tenant-Id", "a")
				if taxonomySnapshot(t, f) != before {
					t.Fatal("rejected import mutated")
				}
			})
		}
		f.expect("peer", "POST", endpoint+"/import", taxonomyBody(taxonomy.ImportRequest{Seed: taxonomyNativeSeed}), 200, "X-Tenant-Id", "a")
		if raw := f.expect("viewer", "GET", endpoint, "", 200, "X-Tenant-Id", "a"); string(raw) != "{\"domains\":[]}" {
			t.Fatalf("shared reader saw owner taxonomy: %s", raw)
		}
		f.expect("owner", "POST", endpoint+"/import", taxonomyBody(map[string]any{"seed": taxonomyNativeSeed, "owner_id": "foreign"}), 400, "X-Tenant-Id", "a")
		f.expect("owner", "GET", endpoint, "", 401, "X-Test-Unverified", "true")
		f.expect("owner", "GET", endpoint, "", 401, "X-Test-Expired", "true")
		f.expect("", "GET", endpoint, "", 401)
		// Tenant-empty is an exact namespace, even for the same verified user.
		if raw := f.expect("owner", "GET", endpoint, "", 200); string(raw) != "{\"domains\":[]}" {
			t.Fatalf("empty tenant leaked: %s", raw)
		}
		f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('owner','b')")
		if raw := f.expect("owner", "GET", endpoint, "", 200, "X-Tenant-Id", "b"); string(raw) != "{\"domains\":[]}" {
			t.Fatalf("other tenant leaked: %s", raw)
		}
		warningSeed := "# Domains\n## second\nАлиасы: LOGIN\n"
		warn := f.expect("owner", "POST", endpoint+"/import", taxonomyBody(taxonomy.ImportRequest{Seed: warningSeed}), 200, "X-Tenant-Id", "a")
		var warning taxonomy.ImportReport
		json.Unmarshal(warn, &warning)
		if len(warning.Warnings) != 1 {
			t.Fatalf("alias warning: %s", warn)
		}
		baseline = taxonomySnapshot(t, f)
		f.expect("owner", "DELETE", endpoint, taxonomyBody(taxonomy.RemoveRequest{Domain: "auth"}), 409, "X-Tenant-Id", "a")
		if taxonomySnapshot(t, f) != baseline {
			t.Fatal("nonempty remove changed publication")
		}
		f.expect("owner", "DELETE", endpoint, taxonomyBody(taxonomy.RemoveRequest{Domain: "auth", Document: "Rules"}), 400, "X-Tenant-Id", "a")
		f.exec("INSERT INTO graph_nodes(id,name,properties,dataset_id) VALUES('taxonomy-safe','Safe','{}','alpha')")
		removed := f.expect("owner", "DELETE", endpoint, taxonomyBody(taxonomy.RemoveRequest{Domain: "AUTH", Force: true, RequestID: "remove"}), 200, "X-Tenant-Id", "a")
		var report taxonomy.RemoveReport
		json.Unmarshal(removed, &report)
		if report.Removed != 3 {
			t.Fatalf("removed: %s", removed)
		}
		if again := f.expect("owner", "DELETE", endpoint, taxonomyBody(taxonomy.RemoveRequest{Domain: "AUTH", Force: true, RequestID: "remove"}), 200, "X-Tenant-Id", "a"); string(again) != string(removed) {
			t.Fatal("remove replay differs")
		}
		var n int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM data WHERE id='visible'").Scan(&n); err != nil || n != 1 {
			t.Fatalf("source removed: %d %v", n, err)
		}
		if err := f.db.QueryRow("SELECT COUNT(*) FROM graph_nodes WHERE id='taxonomy-safe'").Scan(&n); err != nil || n != 1 {
			t.Fatalf("graph removed: %d %v", n, err)
		}
		missing := f.expect("owner", "DELETE", endpoint, taxonomyBody(taxonomy.RemoveRequest{Domain: "missing"}), 200, "X-Tenant-Id", "a")
		json.Unmarshal(missing, &report)
		if report.Removed != 0 {
			t.Fatalf("missing: %s", missing)
		}
		var journal string
		if err := f.db.QueryRow("SELECT report_json FROM knowledge_taxonomy_runs WHERE request_id='first'").Scan(&journal); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(journal, "Identity") || strings.Contains(journal, "Источник") {
			t.Fatal("journal stored seed")
		}
	})
}
func taxonomyReject(t *testing.T, f *documentHTTPFixture, table, operation, condition string) func() {
	t.Helper()
	if GetDBProvider() == DBSQLite {
		f.exec(fmt.Sprintf("CREATE TRIGGER taxonomy_reject BEFORE %s ON %s WHEN %s BEGIN SELECT RAISE(ABORT,'taxonomy rejection'); END", operation, table, condition))
		return func() { f.exec("DROP TRIGGER taxonomy_reject") }
	}
	returned := "NEW"
	if operation == "DELETE" {
		returned = "OLD"
	}
	f.exec(fmt.Sprintf("CREATE FUNCTION taxonomy_reject_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF %s THEN RAISE EXCEPTION 'taxonomy rejection'; END IF; RETURN %s; END $$", condition, returned))
	f.exec(fmt.Sprintf("CREATE TRIGGER taxonomy_reject BEFORE %s ON %s FOR EACH ROW EXECUTE FUNCTION taxonomy_reject_fn()", operation, table))
	return func() {
		f.exec("DROP TRIGGER taxonomy_reject ON " + table)
		f.exec("DROP FUNCTION taxonomy_reject_fn()")
	}
}
func TestTaxonomyNativeRollbackAndConcurrency(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		taxonomyNativeAPI(f)
		endpoint := "/datasets/alpha/taxonomy"
		f.expect("owner", "POST", endpoint+"/import", taxonomyBody(taxonomy.ImportRequest{Seed: taxonomyNativeSeed}), 200, "X-Tenant-Id", "a")
		for _, tc := range []struct{ name, table, op, condition string }{
			{"late_document", "knowledge_documents", "INSERT", "NEW.title='later'"},
			{"audit", "knowledge_taxonomy_runs", "INSERT", "1=1"},
			{"remove", "knowledge_collections", "DELETE", "1=1"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				before := taxonomySnapshot(t, f)
				drop := taxonomyReject(t, f, tc.table, tc.op, tc.condition)
				defer drop()
				if tc.op == "DELETE" {
					f.expect("owner", "DELETE", endpoint, taxonomyBody(taxonomy.RemoveRequest{Domain: "auth", Force: true}), 503, "X-Tenant-Id", "a")
				} else {
					seed := strings.ReplaceAll(taxonomyNativeSeed, "Identity", "modified") + "##### Документ: later\nИсточник: visible\n"
					f.expect("owner", "POST", endpoint+"/import", taxonomyBody(taxonomy.ImportRequest{Seed: seed}), 503, "X-Tenant-Id", "a")
				}
				if taxonomySnapshot(t, f) != before {
					t.Fatal("failed SQL partially committed")
				}
			})
		}
		// Final credential recheck observes a late in-transaction identity revocation.
		beforeLate := taxonomySnapshot(t, f)
		if GetDBProvider() == DBSQLite {
			f.exec("CREATE TRIGGER taxonomy_revoke AFTER INSERT ON knowledge_taxonomy_runs BEGIN UPDATE users SET is_active=0 WHERE id='owner'; END")
		} else {
			f.exec("CREATE FUNCTION taxonomy_revoke_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE users SET is_active=false WHERE id='owner'; RETURN NEW; END $$")
			f.exec("CREATE TRIGGER taxonomy_revoke AFTER INSERT ON knowledge_taxonomy_runs FOR EACH ROW EXECUTE FUNCTION taxonomy_revoke_fn()")
		}
		f.expect("owner", "POST", endpoint+"/import", taxonomyBody(taxonomy.ImportRequest{Seed: "# Domains\n## late-revocation"}), 401, "X-Tenant-Id", "a")
		if taxonomySnapshot(t, f) != beforeLate {
			t.Fatal("late credential failure committed")
		}
		var active bool
		if err := f.db.QueryRow("SELECT is_active FROM users WHERE id='owner'").Scan(&active); err != nil || !active {
			t.Fatalf("revocation failed rollback: %v %v", active, err)
		}
		if GetDBProvider() == DBSQLite {
			f.exec("DROP TRIGGER taxonomy_revoke")
		} else {
			f.exec("DROP TRIGGER taxonomy_revoke ON knowledge_taxonomy_runs")
			f.exec("DROP FUNCTION taxonomy_revoke_fn()")
		}
		// Same-pool SQLite is deliberately pool-serialized; PostgreSQL uses independent connections.
		if GetDBProvider() == DBPostgres {
			f.db.SetMaxOpenConns(3)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		credential := access.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}
		actor := access.MetadataActor{Actor: f.owner, Credential: credential}
		// Use the verified method name supplied by production egress.
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				tx, p, err := f.p.BeginMetadataWrite(ctx, actor, GetDBProvider() == DBSQLite)
				if err != nil {
					errs <- err
					return
				}
				defer tx.Rollback()
				_, err = taxonomy.Import(ctx, tx, p, actor.Actor, "alpha", taxonomy.ImportRequest{Seed: "# Domains\n## concurrent", RequestID: "concurrent"})
				if err == nil {
					err = tx.Commit()
				}
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		var count int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM knowledge_domains WHERE name='concurrent'").Scan(&count); err != nil || count != 1 {
			t.Fatalf("concurrent duplicate: %d %v", count, err)
		}
		if err := f.db.QueryRow("SELECT COUNT(*) FROM knowledge_taxonomy_runs WHERE request_id='concurrent'").Scan(&count); err != nil || count != 1 {
			t.Fatalf("concurrent audit: %d %v", count, err)
		}
		canceled, cancelNow := context.WithTimeout(context.Background(), time.Second)
		cancelNow()
		before := taxonomySnapshot(t, f)
		if tx, _, err := f.p.BeginMetadataWrite(canceled, actor, GetDBProvider() == DBSQLite); err == nil {
			tx.Rollback()
			t.Fatal("canceled write acquired")
		}
		if taxonomySnapshot(t, f) != before {
			t.Fatal("canceled write mutated")
		}
	})
}

func TestTaxonomyEscapedSeedEnvelope(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		taxonomyNativeAPI(f)
		prefix := "# Domains\n## escaped\nОписание: "
		seed := prefix + strings.Repeat("\"", taxonomy.MaxSeedBytes-len(prefix))
		raw := taxonomyBody(taxonomy.ImportRequest{Seed: seed, SourceName: strings.Repeat("x", 1024)})
		if len(raw) <= 2*taxonomy.MaxSeedBytes {
			t.Fatal("fixture does not cross old envelope cap")
		}
		f.expect("owner", "POST", "/datasets/alpha/taxonomy/import", raw, 200, "X-Tenant-Id", "a")
		f.expect("owner", "POST", "/datasets/alpha/taxonomy/import", taxonomyBody(taxonomy.ImportRequest{Seed: seed + "x"}), 400, "X-Tenant-Id", "a")
		f.expect("owner", "POST", "/datasets/alpha/taxonomy/import", taxonomyBody(taxonomy.ImportRequest{Seed: "# Domains\n## nul\x00bad"}), 400, "X-Tenant-Id", "a")
	})
}
