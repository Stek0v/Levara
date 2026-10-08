package http

import (
	"context"
	"os"
	"testing"
	"time"

	accesspkg "github.com/stek0v/levara/pkg/access"
)

func TestWorkspaceRegisteredProjectNamespace(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB, cfg.RequireAuth = f.db, true
		f.db.SetMaxOpenConns(1)
		actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
		for _, project := range []struct{ id, owner string }{{"team-a", actor.UserID}, {"team_a", "viewer"}, {"uuid-with-hyphens", actor.UserID}} {
			query, args := QArgs("INSERT INTO datasets(id,name,owner_id) VALUES($1,$2,$3)", project.id, project.id, project.owner)
			if _, err := f.db.Exec(query, args...); err != nil {
				t.Fatal(err)
			}
		}
		t.Run("ambiguous_registered_root", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			decision, err := authorizeWorkspace(ctx, f.db, actor.Actor, "team-a", workspaceAccessWrite)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Allowed {
				t.Error("raw object grant accepted ambiguous filesystem namespace")
			}
			_, err = writeWorkspaceMarkdownAuthorized(ctx, cfg, workspaceWriteRequest{workspaceIndexRequest: workspaceIndexRequest{ProjectID: "team-a", Path: "ledger.md", Text: "must remain absent"}}, actor)
			if err == nil {
				t.Error("ambiguous project mutation accepted")
			}
			path, _, err := workspaceFilePath(cfg, "team_a", "main", "ledger.md")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Error("ambiguous write created a shared-root file")
			}
			if f.db.Stats().InUse != 0 {
				t.Fatal("namespace guard leaked SQL")
			}
		})
		t.Run("superuser_cannot_bypass_collision", func(t *testing.T) {
			query, args := QArgs("UPDATE users SET is_superuser=true WHERE id=$1", actor.UserID)
			if _, err := f.db.Exec(query, args...); err != nil {
				t.Fatal(err)
			}
			defer func() {
				query, args := QArgs("UPDATE users SET is_superuser=false WHERE id=$1", actor.UserID)
				if _, err := f.db.Exec(query, args...); err != nil {
					t.Error(err)
				}
			}()
			decision, err := authorizeWorkspace(context.Background(), f.db, actor.Actor, "team-a", workspaceAccessWrite)
			if err != nil || decision.Allowed {
				t.Fatalf("superuser ambiguity allowed=%v error=%v", decision.Allowed, err)
			}
			if _, err := writeWorkspaceMarkdownAuthorized(context.Background(), cfg, workspaceWriteRequest{workspaceIndexRequest: workspaceIndexRequest{ProjectID: "team-a", Path: "admin.md", Text: "must remain absent"}}, actor); err == nil {
				t.Fatal("superuser wrote ambiguous root")
			}
			path, _, err := workspaceFilePath(cfg, "team_a", "main", "admin.md")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("superuser ambiguity created shared-root file")
			}
			if f.db.Stats().InUse != 0 {
				t.Fatal("superuser ambiguity retained SQL")
			}
		})
		t.Run("lock_key_tracks_directory", func(t *testing.T) {
			if workspaceBranchLockKey("team-a", "feature/topic") != workspaceBranchLockKey("team_a", "feature_topic") {
				t.Fatal("one filesystem root gets distinct branch locks")
			}
			if workspaceBranchLockKey("team-a", "main") == workspaceBranchLockKey("team-a", "other") {
				t.Fatal("distinct branch roots share one lock key")
			}
		})
		t.Run("hyphenated_id_compatible", func(t *testing.T) {
			_, err := writeWorkspaceMarkdownAuthorized(context.Background(), cfg, workspaceWriteRequest{workspaceIndexRequest: workspaceIndexRequest{ProjectID: "uuid-with-hyphens", Path: "ledger.md", Text: "compatible project"}}, actor)
			if err != nil {
				t.Fatal(err)
			}
			path, _, err := workspaceFilePath(cfg, "uuid-with-hyphens", "main", "ledger.md")
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(path)
			if err != nil || string(body) != "compatible project" || f.db.Stats().InUse != 0 {
				t.Fatalf("normal hyphenated ID error=%v", err)
			}
		})
	})
}
