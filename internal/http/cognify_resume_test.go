package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"database/sql"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func resumeTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "resume.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE datasets (id TEXT PRIMARY KEY, name TEXT)`,
		`CREATE TABLE data (id TEXT PRIMARY KEY)`,
		`CREATE TABLE dataset_data (dataset_id TEXT, data_id TEXT)`,
		`CREATE TABLE document_pipeline_statuses (
			dataset_id TEXT NOT NULL, data_id TEXT NOT NULL, collection_name TEXT NOT NULL,
			source_revision INTEGER NOT NULL, raw_content_hash TEXT NOT NULL, attempt_id TEXT NOT NULL DEFAULT '',
			pipeline_state TEXT NOT NULL, status_json TEXT NOT NULL DEFAULT '{}', updated_at TEXT NOT NULL DEFAULT (CURRENT_TIMESTAMP),
			PRIMARY KEY (dataset_id, data_id, collection_name))`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	seed := func(dataset, collection, state string, n int) {
		for i := 0; i < n; i++ {
			dataID := fmt.Sprintf("%s-%s-d%d", dataset, state[0:1], i)
			if _, err := db.Exec(`INSERT INTO data (id) VALUES (?)`, dataID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO dataset_data (dataset_id, data_id) VALUES (?, ?)`, dataset, dataID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO document_pipeline_statuses
				(dataset_id, data_id, collection_name, source_revision, raw_content_hash, pipeline_state)
				VALUES (?, ?, ?, 1, ?, ?)`, dataset, dataID, collection, fmt.Sprintf("%064x", i), state); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := db.Exec(`INSERT INTO datasets (id, name) VALUES ('ds1','one'), ('ds2','two')`); err != nil {
		t.Fatal(err)
	}
	seed("ds1", "chat-imports", "RUNNING", 3)
	seed("ds1", "chat-imports", "FAILED", 2)
	seed("ds2", "other", "COMPLETED", 4) // terminal: ignored
	// Orphan group whose dataset vanished: ignored.
	if _, err := db.Exec(`INSERT INTO document_pipeline_statuses
		(dataset_id, data_id, collection_name, source_revision, raw_content_hash, pipeline_state)
		VALUES ('ghost','g1','c',1,?,'RUNNING')`, fmt.Sprintf("%064d", 9)); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestUnfinishedCognifyGroups(t *testing.T) {
	groups, err := UnfinishedCognifyGroups(context.Background(), resumeTestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %+v, want only ds1/chat-imports (5 pending)", groups)
	}
	if groups[0].DatasetID != "ds1" || groups[0].Collection != "chat-imports" || groups[0].Pending != 5 {
		t.Fatalf("group wrong: %+v", groups[0])
	}
}

// fakeStarter decrements pending rows on each Start, simulating runs that
// make progress; a noProgress variant simulates a stuck backlog.
type fakeStarter struct {
	db         *sql.DB
	calls      atomic.Int32
	noProgress bool
}

func (f *fakeStarter) Start(ctx context.Context, datasetID, collection string) (CognifyRunSummary, error) {
	f.calls.Add(1)
	if f.noProgress {
		return CognifyRunSummary{}, nil
	}
	_, err := f.db.ExecContext(ctx, `UPDATE document_pipeline_statuses
		SET pipeline_state='COMPLETED'
		WHERE dataset_id=? AND collection_name=?
		  AND data_id = (SELECT data_id FROM document_pipeline_statuses
		                 WHERE dataset_id=? AND collection_name=?
		                   AND pipeline_state IN ('RUNNING','FAILED')
		                 LIMIT 1)`, datasetID, collection, datasetID, collection)
	return CognifyRunSummary{Chunks: 1}, err
}

func TestResumeUnfinishedCognifyDrains(t *testing.T) {
	db := resumeTestDB(t)
	starter := &fakeStarter{db: db}
	ResumeUnfinishedCognify(context.Background(), db, starter)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		n, _ := PendingCognifyCount(context.Background(), db, "ds1", "chat-imports")
		if n == 0 {
			if starter.calls.Load() == 0 {
				t.Fatal("drained without any start calls")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("backlog not drained in time")
}

func TestResumeUnfinishedCognifyStopsWhenStuck(t *testing.T) {
	db := resumeTestDB(t)
	starter := &fakeStarter{db: db, noProgress: true}
	ResumeUnfinishedCognify(context.Background(), db, starter)
	// Exactly one attempt, then the loop must stop instead of hot-spinning.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && starter.calls.Load() == 0 {
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(1 * time.Second) // give a hot loop ample time to over-spin
	if got := starter.calls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 start attempt then stop, got %d", got)
	}
}

func TestLoopbackCognifyStarter(t *testing.T) {
	var runCount atomic.Int32
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/cognify":
			runCount.Add(1)
			if err := jsonAssertSkipGraph(r); err != nil {
				t.Errorf("payload: %v", err)
			}
			w.Write([]byte(`{"pipeline_run_id":"run-1"}`))
		case "/api/v1/cognify/run-1/status":
			// First poll RUNNING, then terminal (poll counter via query p).
			polls.Add(1)
			status := "RUNNING"
			if polls.Load() >= 2 {
				status = "COMPLETED"
			}
			fmt.Fprintf(w, `{"status":%q,"chunks_created":5,"elapsed_ms":9000}`, status)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	starter := LoopbackCognifyStarter{BaseURL: srv.URL, Client: srv.Client()}
	summary, err := starter.Start(context.Background(), "ds1", "chat-imports")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Elapsed <= 0 {
		t.Fatalf("summary should carry run stats: %+v", summary)
	}
}

func jsonAssertSkipGraph(r *http.Request) error {
	var body struct {
		Datasets   []string `json:"datasets"`
		Collection string   `json:"collection"`
		SkipGraph  bool     `json:"skip_graph"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return err
	}
	if len(body.Datasets) != 1 || body.Datasets[0] != "ds1" || body.Collection != "chat-imports" {
		return fmt.Errorf("wrong dataset/collection: %+v", body)
	}
	if !body.SkipGraph {
		return fmt.Errorf("resume must run in rag mode (skip_graph=true)")
	}
	return nil
}
