package http

import (
	"strings"
	"testing"
)

func TestTaskRequestDigestSchemaUpgrade(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		f.exec("INSERT INTO tasks(id,idempotency_key,owner_id,collection_name,room,objective) VALUES('legacy-task','legacy-task','owner','levara','task-runtime','legacy proof')")
		f.exec("INSERT INTO task_receipts(id,task_id,idempotency_key,receipt_type,status,observation) VALUES('legacy-receipt','legacy-task','legacy-receipt','observation','pass','original evidence')")
		f.exec("INSERT INTO task_checkpoints(id,task_id,idempotency_key,summary) VALUES('legacy-checkpoint','legacy-task','legacy-checkpoint','original checkpoint')")
		for _, table := range []string{"task_receipts", "task_checkpoints"} {
			f.exec("ALTER TABLE " + table + " DROP COLUMN request_digest")
		}
		for i := 0; i < 2; i++ {
			if err := MigrateSchema(f.db); err != nil {
				t.Fatal(err)
			}
			for _, item := range []struct{ table, field, value string }{
				{"task_receipts", "observation", "original evidence"}, {"task_checkpoints", "summary", "original checkpoint"},
			} {
				var value, digest string
				if err := f.db.QueryRow("SELECT "+item.field+",request_digest FROM "+item.table+" WHERE task_id='legacy-task'").Scan(&value, &digest); err != nil {
					t.Fatal(err)
				}
				if value != item.value || digest != "" {
					t.Fatalf("legacy row altered or identity guessed: value=%q digest=%q", value, digest)
				}
				if _, err := f.db.Exec("UPDATE " + item.table + " SET request_digest=NULL WHERE task_id='legacy-task'"); err == nil {
					t.Fatal("nullable request identity admitted")
				}
			}
		}
	})
}

func TestTaskRequestDigestMigrationFailsClosed(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		// Simulate an unapplied digest ALTER. The generic migration historically
		// tolerates ALTER errors; the postcheck must not report successful startup.
		previousPG, previousSQLite := schemaStatements, schemaSQLiteStatements
		remove := func(statements []string) []string {
			result := make([]string, 0, len(statements))
			for _, stmt := range statements {
				if strings.Contains(stmt, "ALTER TABLE task_") && strings.Contains(stmt, "request_digest") {
					continue
				}
				result = append(result, stmt)
			}
			return result
		}
		schemaStatements = remove(schemaStatements)
		schemaSQLiteStatements = remove(schemaSQLiteStatements)
		defer func() { schemaStatements = previousPG; schemaSQLiteStatements = previousSQLite }()
		f.exec("ALTER TABLE task_receipts DROP COLUMN request_digest")
		if err := MigrateSchema(f.db); err == nil || !strings.Contains(err.Error(), "task request digest") {
			t.Fatalf("missing mandatory column did not fail startup: %v", err)
		}
	})
}
