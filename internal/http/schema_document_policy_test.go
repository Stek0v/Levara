package http

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestDocumentPolicySchemaMigration(t *testing.T) {
	for _, dialect := range []DBProvider{DBSQLite, DBPostgres} {
		t.Run(string(dialect), func(t *testing.T) {
			previous := GetDBProvider()
			SetDBProvider(dialect)
			t.Cleanup(func() { SetDBProvider(previous) })
			var db *sql.DB
			if dialect == DBSQLite {
				var err error
				db, err = sql.Open("sqlite3", t.TempDir()+"/documents.db")
				if err != nil {
					t.Fatal(err)
				}
				db.SetMaxOpenConns(1)
				t.Cleanup(func() { _ = db.Close() })
			} else {
				dsn := os.Getenv("LEVARA_TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("LEVARA_TEST_POSTGRES_DSN is not set")
				}
				cfg, err := pgx.ParseConfig(dsn)
				if err != nil {
					t.Fatal(err)
				}
				schema := fmt.Sprintf("document_schema_%d", time.Now().UnixNano())
				cfg.RuntimeParams["search_path"] = schema
				db = stdlib.OpenDB(*cfg)
				t.Cleanup(func() { _ = db.Close() })
				if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _, _ = db.Exec("DROP SCHEMA " + schema + " CASCADE") })
			}
			for i := 0; i < 2; i++ {
				if err := MigrateSchema(db); err != nil {
					t.Fatal(err)
				}
			}
			for _, query := range []string{
				"SELECT dataset_id, data_id, tenant_id, mode, acl_revision, content_revision, tombstoned, hold FROM document_resources LIMIT 0",
				"SELECT dataset_id, data_id, principal_kind, principal_id, role FROM document_grants LIMIT 0",
				"SELECT id, tenant_id, name, revision FROM access_groups LIMIT 0",
				"SELECT group_id, tenant_id, user_id FROM access_group_members LIMIT 0",
				"SELECT id, data_id, source_revision, raw_content_hash, artifact_sha256, byte_size, storage_location, destination, state FROM document_structured_artifacts LIMIT 0",
			} {
				rows, err := db.Query(query)
				if err != nil {
					t.Fatal(err)
				}
				_ = rows.Close()
			}
			for _, query := range []string{
				"INSERT INTO principals(id,type) VALUES('owner','user'),('foreign','user'),('group','group')",
				"INSERT INTO users(id,email,hashed_password) VALUES('owner','owner@test.invalid','locked'),('foreign','foreign@test.invalid','locked')",
				"INSERT INTO tenants(id,name,owner_id) VALUES('a','A','owner'),('b','B','foreign')",
				"INSERT INTO user_tenant(user_id,tenant_id) VALUES('owner','a'),('foreign','b')",
				"INSERT INTO document_resources(dataset_id,data_id,tenant_id,mode) VALUES('dataset','blob','a','restricted')",
				"INSERT INTO access_groups(id,tenant_id,name) VALUES('group','a','Group')",
			} {
				if _, err := db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			// Invalid states fail identically in both dialects, even if a future
			// caller bypasses the policy's input validation.
			for _, query := range []string{
				"UPDATE document_resources SET mode='public'",
				"UPDATE document_resources SET acl_revision=0",
				"UPDATE document_resources SET content_revision=0",
				"UPDATE document_resources SET tenant_id='missing'",
				"UPDATE access_groups SET revision=0",
				"INSERT INTO access_group_members(group_id,tenant_id,user_id) VALUES('group','a','foreign')",
				"INSERT INTO access_group_members(group_id,tenant_id,user_id) VALUES('group','b','foreign')",
				"INSERT INTO document_grants(dataset_id,data_id,principal_kind,principal_id,role,granted_by) VALUES('dataset','blob','user','owner','owner','owner')",
				"INSERT INTO document_grants(dataset_id,data_id,principal_kind,principal_id,role,granted_by) VALUES('dataset','missing','user','owner','viewer','owner')",
				"INSERT INTO document_grants(dataset_id,data_id,principal_kind,principal_id,role,granted_by) VALUES('dataset','blob','public','owner','viewer','owner')",
			} {
				if _, err := db.Exec(query); err == nil {
					t.Fatalf("invalid state accepted: %s", query)
				}
			}
		})
	}
}
