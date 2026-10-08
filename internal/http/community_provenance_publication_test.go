package http

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/community"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/llm"
)

type communityBlockingModel struct {
	started chan struct{}
	finish  chan struct{}
	calls   atomic.Int32
}

func (p *communityBlockingModel) ChatCompletion(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	if p.calls.Add(1) == 1 {
		close(p.started)
	}
	<-p.finish
	return &llm.CompletionResponse{Content: "late summary"}, nil
}
func (p *communityBlockingModel) Name() string { return "community-blocking" }

func communityNativeFixture(t *testing.T, f *documentHTTPFixture, ctx context.Context) {
	t.Helper()
	f.db.SetMaxOpenConns(1)
	publish := func(ref access.DocumentRef, revision int64, generation string, deps string) {
		version, hash, err := f.p.SourceVersion(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		policy, release, err := f.p.BeginReadFence(ctx, GetDBProvider() == DBSQLite)
		if err != nil {
			t.Fatal(err)
		}
		err = policy.CommitDocumentIndexVersioned(ctx, f.owner, ref, revision, "docs", generation, access.DocumentPublicationLineage{SourceRevision: version, RawContentHash: hash, SourcesJSON: deps})
		release()
		if err != nil {
			t.Fatal(err)
		}
	}
	version, hash, err := f.p.SourceVersion(ctx, access.DocumentRef{DatasetID: "alpha", DataID: "visible"})
	if err != nil {
		t.Fatal(err)
	}
	nativeCtx := context.WithValue(ctx, searchEvidenceKey{}, &searchEvidence{sources: make(map[searchDocumentSource]struct{})})
	trackSearchSource(nativeCtx, searchDocumentSource{DatasetID: "alpha", DocumentID: "visible", SourceRevision: version, RawContentHash: hash})
	raw, marshalErr := json.Marshal(searchSources(nativeCtx))
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	publish(f.r.DocumentRef, f.r.ContentRevision, "blob-generation", string(raw))
	publish(access.DocumentRef{DatasetID: "alpha", DataID: "visible"}, 0, "visible-generation", "[]")
	publish(access.DocumentRef{DatasetID: "beta", DataID: "blob"}, 0, "beta-generation", "[]")
	for _, n := range []struct {
		id, name, dataset, doc, generation string
		revision                           int64
	}{
		{"a", "NATIVE_BLOB", "alpha", "blob", "blob-generation", f.r.ContentRevision},
		{"b", "NATIVE_VISIBLE", "alpha", "visible", "visible-generation", 0},
		{"c", "NATIVE_BETA_ENDPOINT", "beta", "blob", "beta-generation", 0},
		{"d", "NATIVE_EXTRA_D", "alpha", "visible", "visible-generation", 0},
		{"e", "NATIVE_EXTRA_E", "alpha", "visible", "visible-generation", 0},
		{"f", "NATIVE_EXTRA_F", "alpha", "visible", "visible-generation", 0},
	} {
		props, _ := json.Marshal(map[string]any{"document_id": n.doc, "content_revision": n.revision, "collection": "docs", "generation": n.generation})
		f.exec("INSERT INTO graph_nodes(id,name,type,description,dataset_id,properties) VALUES($1,$2,'Entity','immutable description',$3,$4)", n.id, n.name, n.dataset, string(props))
	}
	props, _ := json.Marshal(map[string]any{"document_id": "blob", "content_revision": f.r.ContentRevision, "collection": "docs", "generation": "blob-generation"})
	for _, e := range []struct{ id, src, dst, relation, from, until string }{
		{"ab", "a", "b", "ACTIVE_AB", "2020-01-01T00:00:00Z", "2030-01-01T00:00:00Z"},
		{"bc", "b", "c", "ACTIVE_BC", "2020-01-01T00:00:00Z", "2030-01-01T00:00:00Z"},
		{"ca", "c", "a", "ACTIVE_CA", "2020-01-01T00:00:00Z", "2030-01-01T00:00:00Z"},
		{"de", "d", "e", "ACTIVE_DE", "2020-01-01T00:00:00Z", "2030-01-01T00:00:00Z"},
		{"ef", "e", "f", "ACTIVE_EF", "2020-01-01T00:00:00Z", "2030-01-01T00:00:00Z"},
		{"fd", "f", "d", "ACTIVE_FD", "2020-01-01T00:00:00Z", "2030-01-01T00:00:00Z"},
		{"bridge", "c", "d", "BRIDGE", "2020-01-01T00:00:00Z", "2030-01-01T00:00:00Z"},
		{"future", "a", "b", "FUTURE_SECRET", "2030-01-01T00:00:00Z", "2040-01-01T00:00:00Z"},
		{"expired", "b", "c", "EXPIRED_SECRET", "2000-01-01T00:00:00Z", "2020-01-01T00:00:00Z"},
	} {
		f.exec("INSERT INTO graph_edges(id,source_id,target_id,relationship_name,dataset_id,properties,confidence,valid_from,valid_until) VALUES($1,$2,$3,$4,'alpha',$5,1,$6,$7)", e.id, e.src, e.dst, e.relation, string(props), e.from, e.until)
	}
	f.exec("UPDATE graph_edges SET confidence=0.01 WHERE id='bridge'")
}
func communityPublicationSnapshot(t *testing.T, f *documentHTTPFixture, ctx context.Context) string {
	t.Helper()
	var tables [][][]any
	for _, q := range []string{"SELECT * FROM graph_communities ORDER BY id", "SELECT * FROM community_members ORDER BY community_id,node_id"} {
		rows, err := f.db.QueryContext(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		var table [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			table = append(table, values)
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("snapshot err=%v close=%v", err, closeErr)
		}
		tables = append(tables, table)
	}
	raw, err := json.Marshal(tables)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestCommunityProvenancePublication(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		communityNativeFixture(t, f, ctx)
		model := &recordingLLM{responses: []string{"native summary", "parent summary"}}
		cfg := community.SummarizeConfig{DB: f.db, LLMProvider: model, LLMModel: "fixture", MinMembers: 1, MaxContext: 50, Concurrency: 2}
		d, err := community.RebuildPublished(ctx, community.DefaultConfig(), cfg, GetDBProvider() == DBSQLite)
		if err != nil || d == nil {
			t.Fatalf("build=%v err=%v", d, err)
		}
		if len(d.Levels) < 2 {
			t.Fatalf("fixture did not exercise parent summaries: levels=%d", len(d.Levels))
		}
		model.mu.Lock()
		prompts := strings.Join(model.prompts, "\n")
		model.mu.Unlock()
		for _, s := range []string{"NATIVE_BLOB", "NATIVE_VISIBLE", "NATIVE_BETA_ENDPOINT", "ACTIVE_AB"} {
			if !strings.Contains(prompts, s) {
				t.Fatalf("snapshot prompt lacks %s: %s", s, prompts)
			}
		}
		if strings.Contains(prompts, "FUTURE_SECRET") || strings.Contains(prompts, "EXPIRED_SECRET") {
			t.Fatalf("inactive episode entered prompt: %s", prompts)
		}
		rows, err := f.db.QueryContext(ctx, "SELECT generation,sources_json,lineage_verified FROM graph_communities ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		var proof string
		count := 0
		for rows.Next() {
			var generation, raw string
			var flag int
			if err := rows.Scan(&generation, &raw, &flag); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			if generation == "" || flag != 1 || proof != "" && proof != raw {
				_ = rows.Close()
				t.Fatalf("partial/mismatched publication gen=%q flag=%d", generation, flag)
			}
			proof = raw
			count++
		}
		rowErr := rows.Err()
		_ = rows.Close()
		if rowErr != nil || count == 0 {
			t.Fatalf("publications=%d err=%v", count, rowErr)
		}
		sources, err := community.ParseSources(proof)
		if err != nil {
			t.Fatal(err)
		}
		if len(sources) != 4 {
			t.Fatalf("full native+raw dependency proof=%+v", sources)
		}
		if err := community.CheckSources(ctx, f.p, sources); err != nil {
			t.Fatal(err)
		}
		for _, s := range sources {
			if len(s.InputSHA256) != 64 {
				t.Fatalf("missing input digest: %+v", s)
			}
		}
		t.Run("missingLLM_preserves_verified_proof", func(t *testing.T) {
			noModel := community.SummarizeConfig{DB: f.db}
			if _, err := community.RebuildPublished(ctx, community.DefaultConfig(), noModel, GetDBProvider() == DBSQLite); err != nil {
				t.Fatal(err)
			}
			var verified int
			if err := f.db.QueryRowContext(ctx, "SELECT MIN(lineage_verified) FROM graph_communities").Scan(&verified); err != nil || verified != 1 {
				t.Fatalf("flag=%d err=%v", verified, err)
			}
		})
		before := communityPublicationSnapshot(t, f, ctx)
		t.Run("latter_membership_failure_rollback", func(t *testing.T) {
			if GetDBProvider() == DBSQLite {
				f.exec("CREATE TRIGGER proof_member_reject BEFORE INSERT ON community_members WHEN NEW.node_id='c' BEGIN SELECT RAISE(ABORT,'proof fixture rejection'); END")
				defer f.exec("DROP TRIGGER proof_member_reject")
			} else {
				f.exec("CREATE FUNCTION proof_member_reject_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.node_id='c' THEN RAISE EXCEPTION 'proof fixture rejection'; END IF; RETURN NEW; END $$")
				f.exec("CREATE TRIGGER proof_member_reject BEFORE INSERT ON community_members FOR EACH ROW EXECUTE FUNCTION proof_member_reject_fn()")
				defer f.exec("DROP FUNCTION proof_member_reject_fn()")
				defer f.exec("DROP TRIGGER proof_member_reject ON community_members")
			}
			if _, err := community.RebuildPublished(ctx, community.DefaultConfig(), community.SummarizeConfig{DB: f.db}, GetDBProvider() == DBSQLite); err == nil {
				t.Fatal("injected failure committed")
			}
			if got := communityPublicationSnapshot(t, f, ctx); got != before {
				t.Fatal("failed publication changed full previous rows")
			}
		})
		t.Run("all_detection_sources_despite_topN", func(t *testing.T) {
			cfg.MaxContext = 1
			if _, err := community.RebuildPublished(ctx, community.DefaultConfig(), cfg, GetDBProvider() == DBSQLite); err != nil {
				t.Fatal(err)
			}
			var raw string
			if err := f.db.QueryRowContext(ctx, "SELECT sources_json FROM graph_communities LIMIT 1").Scan(&raw); err != nil {
				t.Fatal(err)
			}
			got, err := community.ParseSources(raw)
			if err != nil || len(got) != 4 {
				t.Fatalf("topN lost full dependencies=%v err=%v", got, err)
			}
		})
		t.Run("retired_source_fails_before_model", func(t *testing.T) {
			before := communityPublicationSnapshot(t, f, ctx)
			model.mu.Lock()
			calls := len(model.prompts)
			model.mu.Unlock()
			f.exec("UPDATE document_resources SET tombstoned=$1 WHERE dataset_id='alpha' AND data_id='blob'", true)
			defer f.exec("UPDATE document_resources SET tombstoned=$1 WHERE dataset_id='alpha' AND data_id='blob'", false)
			if _, err := community.RebuildPublished(ctx, community.DefaultConfig(), cfg, GetDBProvider() == DBSQLite); err == nil {
				t.Fatal("retired known source entered compatibility build")
			}
			model.mu.Lock()
			after := len(model.prompts)
			model.mu.Unlock()
			if after != calls || communityPublicationSnapshot(t, f, ctx) != before {
				t.Fatal("retired source reached provider/publication")
			}
		})
		t.Run("missing_known_generation_fails_before_model", func(t *testing.T) {
			before := communityPublicationSnapshot(t, f, ctx)
			f.exec("UPDATE graph_nodes SET properties=$1 WHERE id='a'", fmt.Sprintf("{\"document_id\":\"blob\",\"content_revision\":%d,\"collection\":\"docs\"}", f.r.ContentRevision))
			defer f.exec("UPDATE graph_nodes SET properties=$1 WHERE id='a'", fmt.Sprintf("{\"document_id\":\"blob\",\"content_revision\":%d,\"collection\":\"docs\",\"generation\":\"blob-generation\"}", f.r.ContentRevision))
			model.mu.Lock()
			calls := len(model.prompts)
			model.mu.Unlock()
			if _, err := community.RebuildPublished(ctx, community.DefaultConfig(), cfg, GetDBProvider() == DBSQLite); err == nil {
				t.Fatal("missing known generation auto-certified")
			}
			model.mu.Lock()
			after := len(model.prompts)
			model.mu.Unlock()
			if after != calls || communityPublicationSnapshot(t, f, ctx) != before {
				t.Fatal("missing proof reached provider/publication")
			}
		})

		t.Run("nonempty_workspace_lineage_rejected", func(t *testing.T) {
			var prior string
			if err := f.db.QueryRowContext(ctx, "SELECT sources_json FROM document_index_publications WHERE dataset_id='alpha' AND data_id='blob' AND collection_name='docs'").Scan(&prior); err != nil {
				t.Fatal(err)
			}
			defer f.exec("UPDATE document_index_publications SET sources_json=$1 WHERE dataset_id='alpha' AND data_id='blob' AND collection_name='docs'", prior)
			version, hash, err := f.p.SourceVersion(ctx, access.DocumentRef{DatasetID: "alpha", DataID: "visible"})
			if err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"project", "path", "metadata_only"} {
				entry := searchDocumentSource{DatasetID: "alpha", DocumentID: "visible", SourceRevision: version, RawContentHash: hash}
				if kind == "project" {
					entry.ProjectID = "alpha"
				}
				if kind == "path" {
					entry.Path = "workspace.txt"
				}
				if kind == "metadata_only" {
					entry.DatasetMetadataOnly = true
				}
				raw, err := json.Marshal([]searchDocumentSource{entry})
				if err != nil {
					t.Fatal(err)
				}
				f.exec("UPDATE document_index_publications SET sources_json=$1 WHERE dataset_id='alpha' AND data_id='blob' AND collection_name='docs'", string(raw))
				model.mu.Lock()
				calls := len(model.prompts)
				model.mu.Unlock()
				if _, err := community.RebuildPublished(ctx, community.DefaultConfig(), cfg, GetDBProvider() == DBSQLite); err == nil {
					t.Fatalf("unsupported %s lineage accepted", kind)
				}
				model.mu.Lock()
				after := len(model.prompts)
				model.mu.Unlock()
				if after != calls {
					t.Fatalf("unsupported %s reached provider", kind)
				}
			}
		})

		t.Run("known_revised_source_fails_before_model", func(t *testing.T) {
			before := communityPublicationSnapshot(t, f, ctx)
			model.mu.Lock()
			calls := len(model.prompts)
			model.mu.Unlock()
			f.exec("UPDATE data SET raw_content_hash=$1 WHERE id='blob'", fmt.Sprintf("%x", sha256.Sum256([]byte("changed bytes"))))
			if _, err := community.RebuildPublished(ctx, community.DefaultConfig(), cfg, GetDBProvider() == DBSQLite); err == nil {
				t.Fatal("revised known source became compatibility output")
			}
			model.mu.Lock()
			after := len(model.prompts)
			model.mu.Unlock()
			if after != calls || communityPublicationSnapshot(t, f, ctx) != before {
				t.Fatal("invalid source reached provider/publication")
			}
		})
	})
}

func TestCommunityLegacyProofCompatibility(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		f.db.SetMaxOpenConns(1)
		f.exec("INSERT INTO graph_nodes(id,name) VALUES('legacy-a','legacy'),('legacy-b','legacy'),('legacy-c','legacy')")
		f.exec("INSERT INTO graph_edges(id,source_id,target_id,relationship_name,valid_from) VALUES('legacy-edge','legacy-a','legacy-b','related_to',NULL)")
		if _, err := community.RebuildPublished(ctx, community.DefaultConfig(), community.SummarizeConfig{DB: f.db}, GetDBProvider() == DBSQLite); err != nil {
			t.Fatal(err)
		}
		var flag int
		var raw string
		if err := f.db.QueryRowContext(ctx, "SELECT lineage_verified,sources_json FROM graph_communities LIMIT 1").Scan(&flag, &raw); err != nil || flag != 0 {
			t.Fatalf("legacy auto-adopted flag=%d err=%v", flag, err)
		}
		if _, err := community.ParseSources(raw); err == nil {
			t.Fatal("empty legacy proof accepted")
		}
		for _, raw := range []string{"null", "[]", `[{"dataset_id":"alpha","document_id":"blob","source_revision":1,"raw_content_hash":"bad","input_sha256":"bad"}]`, `[{"project_id":"alpha"}]`} {
			if _, err := community.ParseSources(raw); err == nil {
				t.Fatalf("unsupported proof accepted: %s", raw)
			}
		}
	})
}

func TestCommunityCanceledModelRetainsNativeFence(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		parent, cancelParent := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelParent()
		communityNativeFixture(t, f, parent)
		f.exec("INSERT INTO graph_communities(id,summary) VALUES('prior','immutable prior')")
		before := communityPublicationSnapshot(t, f, parent)
		var writer *sql.DB
		var writerPID int
		if GetDBProvider() == DBPostgres {
			config, err := pgx.ParseConfig(os.Getenv("LEVARA_TEST_POSTGRES_DSN"))
			if err != nil {
				t.Fatal(err)
			}
			var schema string
			if err := f.db.QueryRowContext(parent, "SELECT current_schema()").Scan(&schema); err != nil {
				t.Fatal(err)
			}
			config.RuntimeParams["search_path"] = schema
			writer = stdlib.OpenDB(*config)
			writer.SetMaxOpenConns(1)
			defer writer.Close()
			if err := writer.QueryRowContext(parent, "SELECT pg_backend_pid()").Scan(&writerPID); err != nil {
				t.Fatal(err)
			}
		} else {
			var seq int
			var name, path string
			if err := f.db.QueryRowContext(parent, "PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
				t.Fatal(err)
			}
			var err error
			writer, err = sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			writer.SetMaxOpenConns(1)
			defer writer.Close()
			if _, err := writer.Exec("PRAGMA busy_timeout=3000"); err != nil {
				t.Fatal(err)
			}
		}
		model := &communityBlockingModel{started: make(chan struct{}), finish: make(chan struct{})}
		var finishOnce sync.Once
		finish := func() { finishOnce.Do(func() { close(model.finish) }) }
		defer finish()
		ctx, cancel := context.WithCancel(parent)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := community.RebuildPublished(ctx, community.DefaultConfig(), community.SummarizeConfig{DB: f.db, LLMProvider: model, LLMModel: "fixture", MinMembers: 1, Concurrency: 1}, GetDBProvider() == DBSQLite)
			done <- err
		}()
		select {
		case <-model.started:
		case <-parent.Done():
			t.Fatal("model did not start")
		}
		cancel()
		select {
		case err := <-done:
			t.Fatalf("returned before actual provider finish: %v", err)
		default:
		}
		writeDone := make(chan error, 1)
		go func() {
			_, err := writer.ExecContext(parent, "UPDATE data SET name=name WHERE id='blob'")
			writeDone <- err
		}()
		// A separately connected writer must remain blocked while the canceled model
		// still owns source bytes. Native PostgreSQL wait state proves the lock.
		if GetDBProvider() == DBPostgres {
			waitCtx, stop := context.WithTimeout(parent, 2*time.Second)
			defer stop()
			observed := false
			for !observed && waitCtx.Err() == nil {
				// Observer uses a separate connection; neither builder nor writer pool is reused.
				config, err := pgx.ParseConfig(os.Getenv("LEVARA_TEST_POSTGRES_DSN"))
				if err != nil {
					t.Fatal(err)
				}
				observer := stdlib.OpenDB(*config)
				var waiting int
				err = observer.QueryRowContext(waitCtx, "SELECT COUNT(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND pid=$1", writerPID).Scan(&waiting)
				_ = observer.Close()
				if err != nil {
					t.Fatal(err)
				}
				observed = waiting > 0
			}
			if !observed {
				t.Fatal("independent writer never entered native lock wait")
			}
		} else {
			probe, stop := context.WithTimeout(parent, 150*time.Millisecond)
			_, err := f.db.ExecContext(probe, "SELECT 1")
			stop()
			if err == nil {
				t.Fatal("builder released single-connection fence early")
			}
		}
		finish()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("builder err=%v", err)
			}
		case <-parent.Done():
			t.Fatal("builder did not drain")
		}
		select {
		case err := <-writeDone:
			if err != nil {
				t.Fatal(err)
			}
		case <-parent.Done():
			t.Fatal("writer did not drain")
		}
		if model.calls.Load() != 1 {
			t.Fatalf("started new provider after cancellation: %d", model.calls.Load())
		}
		if got := communityPublicationSnapshot(t, f, parent); got != before {
			t.Fatal("canceled late response published")
		}
	})
}

func TestCommunityEmbeddingFailureKeepsSQLPublication(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		communityNativeFixture(t, f, ctx)
		collections, err := store.NewCollectionManager(2, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer collections.Close()
		if err := collections.Create("_community_summaries"); err != nil {
			t.Fatal(err)
		}
		if err := collections.Insert("_community_summaries", "prior-vector", []float32{1, 0}, map[string]any{"text": "prior immutable vector"}); err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) }))
		defer server.Close()
		model := &recordingLLM{responses: []string{"summary", "parent"}}
		d, err := community.RebuildPublished(ctx, community.DefaultConfig(), community.SummarizeConfig{DB: f.db, LLMProvider: model, LLMModel: "fixture", MinMembers: 1, EmbedClient: embed.NewClient(server.URL, "fixture", 16, 1), Collections: collections}, GetDBProvider() == DBSQLite)
		if err == nil || d == nil {
			t.Fatalf("embedding failure=%v result=%v", err, d)
		}
		var flag int
		if err := f.db.QueryRowContext(ctx, "SELECT MIN(lineage_verified) FROM graph_communities").Scan(&flag); err != nil || flag != 1 {
			t.Fatalf("SQL publication lost flag=%d err=%v", flag, err)
		}
		hits, searchErr := collections.Search("_community_summaries", []float32{1, 0}, 10)
		if searchErr != nil || len(hits) != 1 || hits[0].ID != "prior-vector" {
			t.Fatalf("failed embedding changed prior vectors: %v err=%v", hits, searchErr)
		}
	})
}

func TestCommunityLateEmbeddingDoesNotInsert(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		parent, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		communityNativeFixture(t, f, parent)
		collections, err := store.NewCollectionManager(2, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer collections.Close()
		started := make(chan struct{})
		finish := make(chan struct{})
		drained := make(chan struct{})
		var startedOnce, finishOnce sync.Once
		release := func() { finishOnce.Do(func() { close(finish) }) }
		defer release()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			startedOnce.Do(func() { close(started) })
			var request struct {
				Input []string `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			<-finish
			vectors := make([][]float32, len(request.Input))
			for i := range vectors {
				vectors[i] = []float32{1, 0}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vectors})
			close(drained)
		}))
		defer server.Close()
		ctx, cancel := context.WithCancel(parent)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := community.RebuildPublished(ctx, community.DefaultConfig(), community.SummarizeConfig{DB: f.db, LLMProvider: &recordingLLM{responses: []string{"leaf one", "leaf two", "parent"}}, LLMModel: "fixture", MinMembers: 1, EmbedClient: embed.NewClient(server.URL, "fixture", 16, 1), Collections: collections}, GetDBProvider() == DBSQLite)
			done <- err
		}()
		select {
		case <-started:
		case <-parent.Done():
			release()
			t.Fatal("embedding did not start")
		}
		cancel()
		release()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("late embedding ignored cancellation")
			}
		case <-parent.Done():
			t.Fatal("embedding did not drain")
		}
		select {
		case <-drained:
		case <-parent.Done():
			t.Fatal("HTTP handler did not drain")
		}
		if collections.Has("_community_summaries") {
			t.Fatal("late embedding inserted canceled vectors")
		}
		var flag int
		if err := f.db.QueryRowContext(parent, "SELECT MIN(lineage_verified) FROM graph_communities").Scan(&flag); err != nil || flag != 1 {
			t.Fatalf("committed SQL publication lost flag=%d err=%v", flag, err)
		}
	})
}

func TestCommunityEmbeddingGenerationMatchesSQL(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		communityNativeFixture(t, f, ctx)
		collections, err := store.NewCollectionManager(2, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer collections.Close()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Input []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				w.WriteHeader(400)
				return
			}
			vectors := make([][]float32, len(request.Input))
			for i := range vectors {
				vectors[i] = []float32{1, 0}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vectors})
		}))
		defer server.Close()
		var guardCalls, guardReleases atomic.Int32
		guarded := embed.NewClient(server.URL, "fixture", 16, 1).WithGuard(func(ctx context.Context) (func(), error) {
			_, _, release, err := f.p.BeginTransferFenceTx(ctx, GetDBProvider() == DBSQLite)
			if err != nil {
				return nil, err
			}
			guardCalls.Add(1)
			return func() { release(); guardReleases.Add(1) }, nil
		})
		_, err = community.RebuildPublished(ctx, community.DefaultConfig(), community.SummarizeConfig{DB: f.db, LLMProvider: &recordingLLM{responses: []string{"leaf one", "leaf two", "parent"}}, LLMModel: "fixture", MinMembers: 1, EmbedClient: guarded, Collections: collections}, GetDBProvider() == DBSQLite)
		if err != nil {
			t.Fatal(err)
		}
		if guardCalls.Load() == 0 || guardCalls.Load() != guardReleases.Load() {
			t.Fatalf("sameDB guard calls=%d releases=%d", guardCalls.Load(), guardReleases.Load())
		}
		hits, err := collections.Search("_community_summaries", []float32{1, 0}, 10)
		if err != nil || len(hits) == 0 {
			t.Fatalf("published vectors=%v err=%v", hits, err)
		}
		for _, hit := range hits {
			var meta struct {
				ID         string `json:"community_id"`
				Generation string `json:"generation"`
				Text       string `json:"text"`
			}
			if err := json.Unmarshal(hit.Data, &meta); err != nil {
				t.Fatal(err)
			}
			var generation, text string
			if err := f.db.QueryRowContext(ctx, Q("SELECT generation,summary FROM graph_communities WHERE id=$1"), meta.ID).Scan(&generation, &text); err != nil {
				t.Fatal(err)
			}
			if meta.ID != hit.ID || meta.Generation == "" || meta.Generation != generation || meta.Text != text {
				t.Fatalf("vector lacks authoritative generation/text: %+v SQL=%s/%s", meta, generation, text)
			}
		}
	})
}

func TestCommunitySuccessfulLateEmbeddingDoesNotInsert(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		parent, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		communityNativeFixture(t, f, parent)
		collections, err := store.NewCollectionManager(2, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer collections.Close()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Input []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				w.WriteHeader(400)
				return
			}
			vectors := make([][]float32, len(request.Input))
			for i := range vectors {
				vectors[i] = []float32{1, 0}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vectors})
		}))
		defer server.Close()
		client := func(cancel context.CancelFunc) *embed.Client {
			return embed.NewClient(server.URL, "fixture", 16, 1).WithGuard(func(ctx context.Context) (func(), error) {
				_, _, release, err := f.p.BeginTransferFenceTx(ctx, GetDBProvider() == DBSQLite)
				if err != nil {
					return nil, err
				}
				// embedBatch defers guard release until after its successful response decode.
				return func() { release(); cancel() }, nil
			})
		}
		probe, cancelProbe := context.WithCancel(parent)
		defer cancelProbe()
		vectors, err := client(cancelProbe).EmbedTexts(probe, []string{"control successful vector"})
		if err != nil || len(vectors) != 1 || !errors.Is(probe.Err(), context.Canceled) {
			t.Fatalf("control did not return successful vectors with canceled observer: vectors=%v err=%v ctx=%v", vectors, err, probe.Err())
		}
		ctx, cancel := context.WithCancel(parent)
		defer cancel()
		d, err := community.RebuildPublished(ctx, community.DefaultConfig(), community.SummarizeConfig{DB: f.db, LLMProvider: &recordingLLM{responses: []string{"leaf one", "leaf two", "parent"}}, LLMModel: "fixture", MinMembers: 1, EmbedClient: client(cancel), Collections: collections}, GetDBProvider() == DBSQLite)
		if d == nil || !errors.Is(err, context.Canceled) || !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("late successful vectors escaped cancellation: d=%v err=%v ctx=%v", d, err, ctx.Err())
		}
		if collections.Has("_community_summaries") {
			t.Fatal("successful vectors returned after cancel were inserted")
		}
		var verified int
		if err := f.db.QueryRowContext(parent, "SELECT MIN(lineage_verified) FROM graph_communities").Scan(&verified); err != nil || verified != 1 {
			t.Fatalf("SQL publication lost verified=%d err=%v", verified, err)
		}
	})
}
