package http

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/audit"
	"github.com/stek0v/levara/pkg/mcp"
)

func TestAuditScopeCannotComeFromClientLabels(t *testing.T) {
	ctx := context.WithValue(context.Background(), mcpUserIDKey, "forged-user")
	ctx = context.WithValue(ctx, mcp.TenantIDKey, "forged-tenant")
	if got := verifiedAuditScope(ctx); got.Verified || got.ActorID != "" {
		t.Fatalf("unverified labels became proof: %+v", got)
	}
	ctx = context.WithValue(ctx, searchEgressKey{}, searchEgress{kind: "jwt", actor: accesspkg.Actor{UserID: "owner", TenantID: "a"}})
	if got := verifiedAuditScope(ctx); !got.Verified || got.ActorID != "owner" || got.TenantID != "a" {
		t.Fatalf("verified scope lost: %+v", got)
	}
	var event audit.Event
	if err := json.Unmarshal([]byte(`{"actor_id":"forged","VerifiedScope":{"ActorID":"forged","Verified":true},"verified_scope":{"ActorID":"forged","Verified":true}}`), &event); err != nil {
		t.Fatal(err)
	}
	if event.VerifiedScope.Verified {
		t.Fatal("JSON forged verified scope")
	}
}

func TestWorkspaceAuditStillMirrorsWhenLocalLogFails(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(root, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	var seen []audit.Event
	cfg := APIConfig{WorkspacePath: root, WorkspaceAuditSink: audit.EventSinkFunc(func(e audit.Event) { seen = append(seen, e) })}
	err := recordWorkspaceAuditEvent(cfg, workspaceAuditEvent{ProjectID: "project", Operation: "write", Source: "rest", Result: "success", Scope: audit.VerifiedScope{ActorID: "owner", Verified: true}})
	if err == nil || len(seen) != 1 || !seen[0].VerifiedScope.Verified || seen[0].VerifiedScope.ActorID != "owner" {
		t.Fatalf("mirror=%+v err=%v", seen, err)
	}
}
