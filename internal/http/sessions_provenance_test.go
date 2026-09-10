package http

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	accesspkg "github.com/stek0v/levara/pkg/access"
)

func sessionHistoryContext(actor accesspkg.Actor, sources ...searchDocumentSource) context.Context {
	ctx := context.WithValue(context.Background(), searchActorKey{}, actor)
	ctx = context.WithValue(ctx, searchEvidenceKey{}, &searchEvidence{sources: make(map[searchDocumentSource]struct{})})
	for _, source := range sources {
		trackSearchSource(ctx, source)
	}
	return ctx
}

func TestSessionHistoryKeepsAdministrativeBasisInDerivedAnswer(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.cfg.RequireAuth = true
		f.exec("UPDATE users SET is_superuser=true WHERE id='owner'")
		actor := accesspkg.Actor{UserID: "owner"}
		ctx := sessionHistoryContext(actor)
		if _, err := RecordSessionInteraction(ctx, f.cfg, "admin-lineage", "q", "administrative secret", "rag"); err != nil {
			t.Fatal(err)
		}
		if _, err := GetScopedSessionContext(ctx, f.cfg, "admin-lineage", 5); err != nil {
			t.Fatal(err)
		}
		if !searchEvidenceRequiresAdmin(ctx) {
			t.Fatal("fixture did not inherit administrator basis")
		}
		// A new ordinary document must not launder inherited administrative text.
		f.exec("INSERT INTO datasets(id,name,owner_id) VALUES('ordinary-history','ordinary-history','owner')")
		trackSearchSource(ctx, searchDocumentSource{DatasetID: "ordinary-history"})
		if _, err := RecordSessionInteraction(ctx, f.cfg, "admin-lineage", "followup", "derived administrative secret", "rag"); err != nil {
			t.Fatal(err)
		}
		f.exec("UPDATE users SET is_superuser=false WHERE id='owner'")
		fresh := sessionHistoryContext(actor)
		turns, err := loadSessionTurns(fresh, f.cfg, "admin-lineage", 10)
		if err != nil || len(turns) != 0 {
			t.Fatalf("demoted administrator recovered inherited text: %+v %v", turns, err)
		}
	})
}

func TestSessionHistoryRecordedSourcesRevokedBeforeReplay(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.cfg.RequireAuth = true
		RegisterSessionAPI(f.app.Group("/api/v1"), f.cfg)
		principal := accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "peer"}
		var err error
		f.r, err = f.p.GrantDocument(context.Background(), f.owner, f.r.DocumentRef, f.r.ACLRevision, principal, accesspkg.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		actor := accesspkg.Actor{UserID: "peer", TenantID: "a", APIKeyPermissions: "read"}
		source := searchDocumentSource{DatasetID: "alpha", DocumentID: "blob", ContentRevision: f.r.ContentRevision, VectorID: "vector-proof"}
		ctx := sessionHistoryContext(actor, source)
		id, err := RecordSessionInteraction(ctx, f.cfg, "trusted", "secret question", "document secret answer", "rag")
		if err != nil || id == "" {
			t.Fatalf("record: id=%q err=%v", id, err)
		}
		var raw string
		if err := f.db.QueryRow(Q("SELECT sources FROM interaction_provenance WHERE interaction_id=$1"), id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var stored []searchDocumentSource
		if err := json.Unmarshal([]byte(raw), &stored); err != nil || len(stored) != 1 || stored[0] != source {
			t.Fatalf("source proof roundtrip: %s err=%v", raw, err)
		}
		fresh := sessionHistoryContext(actor)
		prompt := prependSessionContext(fresh, f.cfg, "trusted", "follow up")
		if !strings.Contains(prompt, "document secret answer") || len(searchSources(fresh)) != 1 {
			t.Fatalf("allowed replay lost evidence: %q", prompt)
		}
		if _, err := RecordSessionInteraction(fresh, f.cfg, "trusted", "follow up", "derived secret answer", "rag"); err != nil {
			t.Fatal(err)
		}
		status, body, _ := f.request("peer", "GET", "/interactions/trusted", "", "X-Test-Key", "read")
		if status != 200 || !strings.Contains(string(body), "document secret answer") || !strings.Contains(string(body), "derived secret answer") {
			t.Fatalf("allowed export: %d %s", status, body)
		}
		f.r, err = f.p.RevokeDocument(context.Background(), f.owner, f.r.DocumentRef, f.r.ACLRevision, principal)
		if err != nil {
			t.Fatal(err)
		}
		fresh = sessionHistoryContext(actor)
		if got := prependSessionContext(fresh, f.cfg, "trusted", "follow up"); got != "follow up" || len(searchSources(fresh)) != 0 {
			t.Fatalf("revoked answer entered prompt: %q", got)
		}
		for _, path := range []string{"/interactions/trusted", "/interactions"} {
			status, body, _ = f.request("peer", "GET", path, "")
			if status != 200 || strings.Contains(string(body), "secret") || string(body) != "[]" {
				t.Errorf("revoked export: %d %s", status, body)
			}
		}
		if _, err := RecordSessionInteraction(ctx, f.cfg, "trusted", "new", "stale answer", "rag"); !errors.Is(err, errSessionForbidden) {
			t.Fatalf("late revoked publication: %v", err)
		}
		var count int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM interactions WHERE session_id='trusted'").Scan(&count); err != nil || count != 2 {
			t.Fatalf("denied publication modified history: %d %v", count, err)
		}
	})
}

func TestSessionHistoryClientTextCannotForgeScopeOrProof(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.cfg.RequireAuth = true
		RegisterSessionAPI(f.app.Group("/api/v1"), f.cfg)
		body := `{"session_id":"client","query":"my question","response":"fake assistant","user_id":"owner","owner_id":"owner","tenant_id":"b","kind":"server","sources":[{"dataset_id":"alpha","document_id":"blob","content_revision":1}]}`
		status, result, _ := f.request("peer", "POST", "/interactions", body, "X-Test-Key", "read")
		if status != 403 {
			t.Fatalf("read key wrote client history: %d %s", status, result)
		}
		status, result, _ = f.request("peer", "POST", "/interactions", body, "X-Test-Key", "write")
		if status != 201 {
			t.Fatalf("client record: %d %s", status, result)
		}
		var owner, tenant, kind, raw string
		if err := f.db.QueryRow("SELECT owner_id,tenant_id,kind,sources FROM interaction_provenance WHERE kind <> 'session'").Scan(&owner, &tenant, &kind, &raw); err != nil {
			t.Fatal(err)
		}
		if owner != "peer" || tenant != "a" || kind != "client" || raw != "[]" {
			t.Fatalf("client forged provenance: %q %q %q %q", owner, tenant, kind, raw)
		}
		ctx := sessionHistoryContext(accesspkg.Actor{UserID: "peer", TenantID: "a"})
		got, err := GetScopedSessionContext(ctx, f.cfg, "client", 5)
		if err != nil || !strings.Contains(got, "User-supplied conversation text:") || strings.Contains(got, "Assistant:") || len(searchSources(ctx)) != 0 {
			t.Fatalf("client acquired trusted assistant status: %q %v", got, err)
		}
		for _, user := range []string{"owner", "foreign", "root"} {
			status, result, _ = f.request(user, "GET", "/interactions/client", "")
			if status != 403 || strings.Contains(string(result), "fake assistant") {
				t.Errorf("foreign %s export: %d %s", user, status, result)
			}
			status, result, _ = f.request(user, "POST", "/interactions", body)
			if status != 409 {
				t.Errorf("foreign %s append: %d %s", user, status, result)
			}
		}
		f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('peer','b')")
		status, result, _ = f.request("peer", "GET", "/interactions/client", "", "X-Tenant-Id", "b")
		if status != 403 {
			t.Fatalf("same user foreign tenant read: %d %s", status, result)
		}
		status, result, _ = f.request("peer", "POST", "/interactions", body, "X-Tenant-Id", "b")
		if status != 409 {
			t.Fatalf("same user foreign tenant append: %d %s", status, result)
		}
		for _, user := range []string{"inactive", "missing", ""} {
			status, result, _ = f.request(user, "POST", "/interactions", `{"query":"denied"}`)
			if status != 403 {
				t.Errorf("invalid actor %q wrote: %d %s", user, status, result)
			}
		}
		status, _, _ = f.request("peer", "POST", "/interactions", `{"query":`)
		if status != 400 {
			t.Errorf("malformed JSON: %d", status)
		}
	})
}

func TestSessionHistorySourceRevisionAndPolicyErrors(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.cfg.RequireAuth = true
		RegisterSessionAPI(f.app.Group("/api/v1"), f.cfg)
		ctx := sessionHistoryContext(f.owner, searchDocumentSource{DatasetID: "alpha", DocumentID: "blob", ContentRevision: 1})
		id, err := RecordSessionInteraction(ctx, f.cfg, "versioned", "question", "old revision answer", "rag")
		if err != nil {
			t.Fatal(err)
		}
		f.r, err = f.p.AdvanceDocumentContent(context.Background(), f.owner, f.r.DocumentRef, f.r.ACLRevision, f.r.ContentRevision)
		if err != nil {
			t.Fatal(err)
		}
		got, err := GetScopedSessionContext(sessionHistoryContext(f.owner), f.cfg, "versioned", 5)
		if err != nil || got != "" {
			t.Fatalf("stale revision replay: %q %v", got, err)
		}
		f.exec("UPDATE interaction_provenance SET sources='invalid json' WHERE interaction_id=$1", id)
		status, body, _ := f.request("owner", "GET", "/interactions/versioned", "")
		if status != 500 || strings.Contains(string(body), "old revision answer") {
			t.Fatalf("invalid proof response: %d %s", status, body)
		}
		f.exec("UPDATE interaction_provenance SET sources='[{\"dataset_id\":\"alpha\",\"document_id\":\"blob\",\"content_revision\":2}]' WHERE interaction_id=$1", id)
		f.exec("ALTER TABLE document_resources RENAME TO unavailable_document_resources")
		status, body, _ = f.request("owner", "GET", "/interactions/versioned", "")
		if status != 500 || strings.Contains(string(body), "old revision answer") {
			t.Fatalf("SQL policy failure fell back: %d %s", status, body)
		}
	})
}

func TestSessionHistoryEverySourceRevalidatedAfterGroupRemoval(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ctx := context.Background()
		group, err := f.p.CreateGroup(ctx, f.owner, "a", "Readers")
		if err != nil {
			t.Fatal(err)
		}
		group, err = f.p.ReplaceGroupMembers(ctx, f.owner, group.ID, group.Revision, []string{"peer"})
		if err != nil {
			t.Fatal(err)
		}
		f.r, err = f.p.GrantDocument(ctx, f.owner, f.r.DocumentRef, f.r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentGroup, ID: group.ID}, accesspkg.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		other, err := f.p.RegisterDocument(ctx, f.owner, accesspkg.DocumentRef{DatasetID: "beta", DataID: "blob"}, "a", accesspkg.DocumentRestricted)
		if err != nil {
			t.Fatal(err)
		}
		other, err = f.p.GrantDocument(ctx, f.owner, other.DocumentRef, other.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "peer"}, accesspkg.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		actor := accesspkg.Actor{UserID: "peer", TenantID: "a"}
		sources := []searchDocumentSource{{DatasetID: "alpha", DocumentID: "blob", ContentRevision: f.r.ContentRevision}, {DatasetID: "beta", DocumentID: "blob", ContentRevision: other.ContentRevision}}
		if _, err := RecordSessionInteraction(sessionHistoryContext(actor, sources...), f.cfg, "group", "question", "combined answer", "rag"); err != nil {
			t.Fatal(err)
		}
		if got, err := GetScopedSessionContext(sessionHistoryContext(actor), f.cfg, "group", 5); err != nil || !strings.Contains(got, "combined answer") {
			t.Fatalf("group allowed replay: %q %v", got, err)
		}
		if _, err := f.p.ReplaceGroupMembers(ctx, f.owner, group.ID, group.Revision, nil); err != nil {
			t.Fatal(err)
		}
		if allowed, err := searchDocumentAllowed(ctx, f.cfg, actor, sources[1]); err != nil || !allowed {
			t.Fatalf("control source lost access: %v %v", allowed, err)
		}
		if got, err := GetScopedSessionContext(sessionHistoryContext(actor), f.cfg, "group", 5); err != nil || got != "" {
			t.Fatalf("partly revoked answer replayed: %q %v", got, err)
		}
	})
}

func TestSessionHistoryAtomicRecordAndAnchor(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		if err := MigrateSchema(f.db); err != nil {
			t.Fatal(err)
		}
		if GetDBProvider() == DBPostgres {
			f.exec(`CREATE FUNCTION reject_session_turn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind <> 'session' THEN RAISE EXCEPTION 'test rejected turn'; END IF; RETURN NEW; END $$`)
			f.exec(`CREATE TRIGGER reject_session_turn BEFORE INSERT ON interaction_provenance FOR EACH ROW EXECUTE FUNCTION reject_session_turn()`)
		} else {
			f.exec(`CREATE TRIGGER reject_session_turn BEFORE INSERT ON interaction_provenance WHEN NEW.kind <> 'session' BEGIN SELECT RAISE(ABORT,'test rejected turn'); END`)
		}
		ctx := sessionHistoryContext(f.owner)
		if _, err := recordSessionTurn(ctx, f.cfg, "rollback", "question", "answer", "client", "client"); err == nil {
			t.Fatal("injected provenance failure was ignored")
		}
		for _, table := range []string{"interactions", "interaction_provenance"} {
			var count int
			if err := f.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial %s survived rollback: %d %v", table, count, err)
			}
		}
		if GetDBProvider() == DBPostgres {
			f.exec("DROP TRIGGER reject_session_turn ON interaction_provenance")
		} else {
			f.exec("DROP TRIGGER reject_session_turn")
		}
		id, err := recordSessionTurn(ctx, f.cfg, "anchor", "question", "answer", "client", "client")
		if err != nil {
			t.Fatal(err)
		}
		f.exec("DELETE FROM interactions WHERE id=$1", id)
		var remaining int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM interaction_provenance WHERE kind <> 'session'").Scan(&remaining); err != nil || remaining != 0 {
			t.Fatalf("deleted interaction left provenance: %d %v", remaining, err)
		}
		if _, err := recordSessionTurn(sessionHistoryContext(accesspkg.Actor{UserID: "peer", TenantID: "a"}), f.cfg, "anchor", "inject", "fake", "client", "client"); !errors.Is(err, errSessionConflict) {
			t.Fatalf("deleted turns released ownership: %v", err)
		}
		if _, err := recordSessionTurn(ctx, f.cfg, "anchor", "another", "owned", "client", "client"); err != nil {
			t.Fatalf("owner cannot append: %v", err)
		}
	})
}

func TestSessionHistoryConcurrentSessionOwnership(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		start := make(chan struct{})
		results := make(chan error, 2)
		var wg sync.WaitGroup
		for _, user := range []string{"owner", "peer"} {
			wg.Add(1)
			go func(user string) {
				defer wg.Done()
				<-start
				_, err := recordSessionTurn(sessionHistoryContext(accesspkg.Actor{UserID: user, TenantID: "a"}), f.cfg, "collision", "question", user, "client", "client")
				results <- err
			}(user)
		}
		close(start)
		wg.Wait()
		close(results)
		success, conflict := 0, 0
		for err := range results {
			if err == nil {
				success++
			} else if errors.Is(err, errSessionConflict) {
				conflict++
			} else {
				t.Errorf("unexpected concurrent result: %v", err)
			}
		}
		if success != 1 || conflict != 1 {
			t.Fatalf("ownership not exclusive: success=%d conflict=%d", success, conflict)
		}
		var count int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM interactions WHERE session_id='collision'").Scan(&count); err != nil || count != 1 {
			t.Fatalf("collision persisted extra turns: %d %v", count, err)
		}
	})
}

func TestSessionHistoryNoAuthLegacyAndUnknownSources(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.exec("INSERT INTO interactions(id,session_id,user_id,query,response) VALUES('old','legacy','owner','old question','old answer')")
		if got := GetSessionContext(f.db, context.Background(), "legacy", 5); !strings.Contains(got, "old answer") {
			t.Fatalf("noauth legacy compatibility: %q", got)
		}
		if got := GetSessionContext(f.db, sessionHistoryContext(f.owner), "legacy", 5); got != "" {
			t.Fatalf("legacy helper used authenticated caller: %q", got)
		}
		for _, actor := range []accesspkg.Actor{f.owner, {UserID: "root"}} {
			ctx := sessionHistoryContext(actor)
			if _, err := RecordSessionInteraction(ctx, f.cfg, actor.UserID, "question", "unproven answer", "rag"); err != nil {
				t.Fatal(err)
			}
			got, err := GetScopedSessionContext(ctx, f.cfg, actor.UserID, 5)
			if err != nil || (actor.UserID == "owner" && got != "") || (actor.UserID == "root" && !strings.Contains(got, "unproven answer")) {
				t.Fatalf("unknown provenance policy for %s: %q %v", actor.UserID, got, err)
			}
		}
	})
}

func TestSessionHistoryMissingStorageAndRevokedIdentity(t *testing.T) {
	actor := accesspkg.Actor{UserID: "owner", TenantID: "a"}
	ctx := sessionHistoryContext(actor)
	if _, err := RecordSessionInteraction(ctx, APIConfig{RequireAuth: true}, "session", "query", "answer", "rag"); err == nil {
		t.Fatal("missing storage silently dropped a requested scoped record")
	}
	if _, err := GetScopedSessionContext(ctx, APIConfig{RequireAuth: true}, "session", 5); err == nil {
		t.Fatal("missing storage silently treated as empty scoped history")
	}
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		RegisterSessionAPI(f.app.Group("/api/v1"), f.cfg)
		if _, err := recordSessionTurn(ctx, f.cfg, "identity", "private query", "private text", "client", "client"); err != nil {
			t.Fatal(err)
		}
		f.exec("DELETE FROM user_tenant WHERE user_id='owner' AND tenant_id='a'")
		if _, err := GetScopedSessionContext(ctx, f.cfg, "identity", 5); !errors.Is(err, errSessionForbidden) {
			t.Fatalf("revoked tenant membership: %v", err)
		}
		f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('owner','a')")
		f.exec("UPDATE users SET is_active=$1 WHERE id='owner'", false)
		status, body, _ := f.request("owner", "GET", "/interactions/identity", "")
		if status != 403 || strings.Contains(string(body), "private text") {
			t.Fatalf("inactive history export: %d %s", status, body)
		}
		f.exec("UPDATE users SET is_active=$1 WHERE id='owner'", true)
		f.exec("ALTER TABLE interactions RENAME TO unavailable_interactions")
		status, body, _ = f.request("peer", "GET", "/interactions/identity", "")
		if status != 403 {
			t.Fatalf("content storage consulted before foreign scope rejection: %d %s", status, body)
		}
		f.exec("ALTER TABLE users RENAME TO unavailable_users")
		status, body, _ = f.request("owner", "GET", "/interactions/identity", "")
		if status != 500 {
			t.Fatalf("identity SQL failure was not reported: %d %s", status, body)
		}
	})
}

func TestSessionHistoryLegacyScopeAndCollision(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		RegisterSessionAPI(f.app.Group("/api/v1"), f.cfg)
		f.exec("INSERT INTO interactions(id,session_id,user_id,query,response) VALUES('old','private-session','owner','private prompt','private answer')")
		for _, user := range []string{"peer", "owner"} {
			status, body, _ := f.request(user, "GET", "/interactions/private-session", "")
			if status >= 500 || strings.Contains(string(body), "private answer") || strings.Contains(string(body), "private prompt") {
				t.Errorf("unproven history replayed for %s: status=%d body=%s", user, status, body)
			}
		}
		status, body, _ := f.request("peer", "POST", "/interactions", `{"session_id":"private-session","query":"injected","response":"fake assistant"}`)
		if status != 409 {
			t.Errorf("foreign session append: status=%d body=%s", status, body)
		}
		var count int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM interactions WHERE session_id='private-session'").Scan(&count); err != nil || count != 1 {
			t.Errorf("foreign append changed history: count=%d err=%v", count, err)
		}
	})
}

func TestSessionHistorySQLFailureIsNotSaved(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		RegisterSessionAPI(f.app.Group("/api/v1"), f.cfg)
		f.exec("ALTER TABLE interactions RENAME TO unavailable_interactions")
		status, body, _ := f.request("owner", "POST", "/interactions", `{"query":"hello","response":"self supplied"}`)
		if status != 500 || strings.Contains(string(body), `"saved":true`) {
			t.Fatalf("SQL failure claimed saved: status=%d body=%s", status, body)
		}
	})
}
