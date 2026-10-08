package http

import (
	"context"
	"strings"
	"testing"
	"time"

	accesspkg "github.com/stek0v/levara/pkg/access"
)

func TestWorkspaceIndexAuthorityFence(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB = f.db
		actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
		release, err := beginWorkspaceEffectFence(ctx, f.cfg, actor, "alpha", workspaceAccessWrite)
		if err != nil {
			t.Fatal(err)
		}
		if f.db.Stats().InUse != 1 {
			release()
			t.Fatal("workspace effect must retain its authorization transaction")
		}
		release()
		if f.db.Stats().InUse != 0 {
			t.Fatal("authorization transaction did not drain")
		}
		for _, tc := range []struct {
			name  string
			actor accesspkg.MetadataActor
		}{
			{"expired", accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(-time.Second).Unix()}}},
			{"viewer_write", accesspkg.MetadataActor{Actor: accesspkg.Actor{UserID: "viewer", TenantID: "a"}, Credential: actor.Credential}},
			{"foreign_tenant", accesspkg.MetadataActor{Actor: accesspkg.Actor{UserID: "owner", TenantID: "b"}, Credential: actor.Credential}},
			{"inactive", accesspkg.MetadataActor{Actor: accesspkg.Actor{UserID: "inactive", TenantID: "a"}, Credential: actor.Credential}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				release, err := beginWorkspaceEffectFence(ctx, f.cfg, tc.actor, "alpha", workspaceAccessWrite)
				if release != nil {
					release()
				}
				if err == nil {
					t.Fatal("invalid workspace authority accepted")
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("failed authorization retained transaction")
				}
			})
		}
		t.Run("rest_unavailable_authority_status", func(t *testing.T) {
			unavailable := cfg
			unavailable.DB = nil
			unavailable.RequireAuth = true
			f.app.Post("/api/v1/workspace-fence-test", workspaceIndexHandler(unavailable))
			status, body, _ := f.request("owner", "POST", "/workspace-fence-test", `{"project_id":"alpha","path":"ledger.md","text":"Workspace authority storage is unavailable."}`)
			if status != 503 {
				t.Fatalf("authority unavailable status=%d body=%s", status, body)
			}
		})
		t.Run("authorized_index", func(t *testing.T) {
			resp, err := indexWorkspaceMarkdownAuthorized(ctx, cfg, workspaceIndexRequest{
				ProjectID: "alpha", Branch: "main", Generation: "g1", Path: "ledger.md",
				Text: strings.Repeat("Copper count seventeen. Zinc count twenty-three. Total forty units. ", 20),
			}, actor)
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.Result.VectorIDs) == 0 {
				t.Fatal("authorized index did not publish vectors")
			}
		})
		for _, collection := range []string{"_memories", "_memories_alpha"} {
			t.Run("reserved_"+collection, func(t *testing.T) {
				_, err := indexWorkspaceMarkdownAuthorized(ctx, cfg, workspaceIndexRequest{
					ProjectID: "alpha", Branch: "main", Generation: "g2", Path: "reserved.md", Collection: collection, Text: "Reserved collections must not reach native callbacks.",
				}, actor)
				if err == nil || !strings.Contains(err.Error(), "reserved memory collection") {
					t.Fatalf("reserved collection error = %v", err)
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("reserved collection retained transaction")
				}
			})
		}

	})
}
