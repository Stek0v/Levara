package http

import (
	"context"
	"testing"
)

func TestTaskProducedLinkTracksCurrentSemanticContent(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.exec(`INSERT INTO tasks(id,idempotency_key,collection_name,room,objective) VALUES('task','task-key','levara','memory','test')`)
		f.exec(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall) VALUES('memory','key','value','project','owner','levara','memory','fact')`)

		link := func() {
			f.exec(`INSERT INTO task_memory_links(task_id,memory_id,relation) VALUES('task','memory','produced') ON CONFLICT DO NOTHING`)
		}
		count := func() int {
			var n int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM task_memory_links WHERE task_id='task' AND memory_id='memory' AND relation='produced'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}

		link()
		f.exec(`INSERT INTO task_memory_links(task_id,memory_id,relation) VALUES('task','memory','referenced') ON CONFLICT DO NOTHING`)
		f.exec(`UPDATE memories SET value=value,type=type,room=room,hall=hall WHERE id='memory'`)
		f.exec(`UPDATE memories SET is_pinned=TRUE WHERE id='memory'`)
		if n := count(); n != 1 {
			t.Fatalf("exact and metadata-only updates removed produced link: %d", n)
		}

		for _, field := range []string{"value", "type", "room", "hall"} {
			link()
			f.exec(`UPDATE memories SET ` + field + `=` + field + ` || '-changed' WHERE id='memory'`)
			if n := count(); n != 0 {
				t.Fatalf("semantic update %s retained produced link: %d", field, n)
			}
			var referenced int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM task_memory_links WHERE task_id='task' AND memory_id='memory' AND relation='referenced'`).Scan(&referenced); err != nil {
				t.Fatal(err)
			}
			if referenced != 1 {
				t.Fatalf("semantic update %s removed non-produced link", field)
			}
		}

		link()
		tx, err := f.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`UPDATE memories SET value=value || '-rolled-back' WHERE id='memory'`); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if n := count(); n != 1 {
			t.Fatalf("rollback did not restore produced link: %d", n)
		}
	})
}
