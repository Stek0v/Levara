package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/memoryindex"
)

type memoryCommitEvidenceDeps struct {
	Deps
	actor  access.MetadataActor
	verify func(context.Context, string, string) error
}

func (d *memoryCommitEvidenceDeps) MetadataActor(context.Context) access.MetadataActor {
	return d.actor
}
func (d *memoryCommitEvidenceDeps) VerifyArtifact(ctx context.Context, uri, digest string) error {
	if d.verify == nil {
		return fmt.Errorf("no artifact verifier configured")
	}
	return d.verify(ctx, uri, digest)
}
func memoryCommitEvidenceFixture(t *testing.T, postgres bool) (*memoryCommitEvidenceDeps, context.Context) {
	t.Helper()
	var base Deps
	if postgres {
		db := openPostgresMemoryTestDB(t)
		createTaskTestSchema(t, db)
		base = &postgresMemoryDeps{fakeDeps: &fakeDeps{db: db}}
	} else {
		base = setupTaskTestDB(t)
	}
	ddl := []string{
		`CREATE TABLE memory_commits(id TEXT PRIMARY KEY,owner_id TEXT,collection_name TEXT,idempotency_key TEXT,status TEXT,request_digest TEXT,plan_digest TEXT,plan_json TEXT,result_json TEXT,created_at TEXT,expires_at TEXT,applied_at TEXT,UNIQUE(owner_id,collection_name,idempotency_key))`,
		`CREATE TABLE users(id TEXT PRIMARY KEY,is_active BOOLEAN,is_superuser BOOLEAN)`,
		`CREATE TABLE credential_epochs(user_id TEXT PRIMARY KEY,epoch BIGINT,revoked_before BIGINT)`,
		`CREATE TABLE api_keys(id TEXT PRIMARY KEY,user_id TEXT,permissions TEXT,revoked BOOLEAN)`,
		`CREATE TABLE auth_sessions(id TEXT PRIMARY KEY,user_id TEXT,revoked BOOLEAN,expires_at BIGINT)`,
		`CREATE TABLE user_tenant(user_id TEXT,tenant_id TEXT)`,
		`INSERT INTO users VALUES ('owner-a',true,false),('owner-b',true,false)`,
	}
	for _, table := range []string{"tenants", "datasets", "data", "dataset_data", "dataset_shares", "document_resources", "document_grants", "access_groups", "access_group_members"} {
		ddl = append(ddl, "CREATE TABLE "+table+" (id TEXT)")
	}
	for _, q := range ddl {
		if _, err := base.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	d := &memoryCommitEvidenceDeps{Deps: base, actor: access.MetadataActor{Actor: access.Actor{UserID: "owner-a"}, Credential: access.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}}
	return d, context.WithValue(context.Background(), UserIDKey, "owner-a")
}
func memoryCommitCandidateArgs(key string) map[string]any {
	return map[string]any{"collection": "levara", "idempotency_key": key, "candidates": []any{map[string]any{"candidate_id": "candidate", "key": key, "value": "A proposed durable observation.", "room": "memory", "hall": "discovery"}}}
}
func memoryCommitTestCandidate(args map[string]any) map[string]any {
	return args["candidates"].([]any)[0].(map[string]any)
}
func memoryCommitPreviewRejected(t *testing.T, r ToolResult) bool {
	t.Helper()
	if r.IsError {
		return true
	}
	var p struct{ Items []struct{ Action string } }
	if err := json.Unmarshal([]byte(r.Content[0].Text), &p); err != nil {
		t.Fatal(err)
	}
	return len(p.Items) == 1 && p.Items[0].Action == "reject"
}
func memoryCommitOwnedTaskReceipt(t *testing.T, d Deps, ctx context.Context) (string, string) {
	t.Helper()
	opened := taskPayload(t, ToolTaskOpen(ctx, d, map[string]any{"collection": "levara", "room": "memory", "objective": "verify a memory evidence relationship", "idempotency_key": fmt.Sprintf("proof-%d", time.Now().UnixNano()), "definition_of_done": []any{map[string]any{"criterion_id": "check", "description": "check passed"}}}))
	receipt := taskPayload(t, ToolTaskReceipt(ctx, d, map[string]any{"task_id": opened["task_id"], "base_version": opened["version"], "idempotency_key": "proof", "receipt_type": "command", "status": "pass", "exit_code": float64(0), "criterion_ids": []any{"check"}, "workspace_revision": "rev-1"}))
	return opened["task_id"].(string), receipt["receipt_id"].(string)
}
func TestMemoryCommitEvidenceDoesNotTrustCallerLabel(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			d, ctx := memoryCommitEvidenceFixture(t, pg)
			args := memoryCommitCandidateArgs("unproven")
			memoryCommitTestCandidate(args)["verification_status"] = "verified"
			id, digest := memoryCommitIDAndDigest(t, ToolMemoryCommitPreview(ctx, d, args))
			r := ToolMemoryCommitApply(ctx, d, map[string]any{"commit_id": id, "plan_digest": digest})
			if r.IsError {
				t.Fatal(r.Content[0].Text)
			}
			var status string
			if err := d.DB().QueryRow("SELECT verification_status FROM memories WHERE key='unproven'").Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "unverified" {
				t.Fatalf("caller upgraded unproven memory to %q", status)
			}
		})
	}
}
func TestMemoryCommitEvidenceRejectsMissingAndForeignProof(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			for _, kind := range []string{"missing task", "missing receipt", "foreign task", "foreign receipt", "wrong collection", "failed command", "missing exit", "stale revision"} {
				t.Run(kind, func(t *testing.T) {
					d, ctx := memoryCommitEvidenceFixture(t, pg)
					task, receipt := memoryCommitOwnedTaskReceipt(t, d, ctx)
					args := memoryCommitCandidateArgs("candidate")
					c := memoryCommitTestCandidate(args)
					c["source_task_id"] = task
					c["source_receipt_ids"] = []any{receipt}
					var q string
					var values []any
					switch kind {
					case "missing task":
						c["source_task_id"] = "missing"
					case "missing receipt":
						c["source_receipt_ids"] = []any{"missing"}
					case "foreign task":
						q = "UPDATE tasks SET owner_id='owner-b' WHERE id=$1"
						values = []any{task}
					case "foreign receipt":
						q = "UPDATE task_receipts SET owner_id='owner-b' WHERE id=$1"
						values = []any{receipt}
					case "wrong collection":
						q = "UPDATE tasks SET collection_name='other' WHERE id=$1"
						values = []any{task}
					case "failed command":
						q = "UPDATE task_receipts SET exit_code=7 WHERE id=$1"
						values = []any{receipt}
					case "missing exit":
						q = "UPDATE task_receipts SET exit_code=NULL WHERE id=$1"
						values = []any{receipt}
					case "stale revision":
						q = "UPDATE tasks SET current_workspace_revision='rev-2' WHERE id=$1"
						values = []any{task}
					}
					if q != "" {
						if _, err := d.DB().Exec(d.Q(q), values...); err != nil {
							t.Fatal(err)
						}
					}
					if !memoryCommitPreviewRejected(t, ToolMemoryCommitPreview(ctx, d, args)) {
						t.Fatal("invalid supplied evidence accepted")
					}
				})
			}
		})
	}
}
func TestMemoryCommitEvidenceExplicitEmptySelection(t *testing.T) {
	d, ctx := memoryCommitEvidenceFixture(t, false)
	id, digest := memoryCommitIDAndDigest(t, ToolMemoryCommitPreview(ctx, d, memoryCommitCandidateArgs("empty")))
	r := ToolMemoryCommitApply(ctx, d, map[string]any{"commit_id": id, "plan_digest": digest, "accepted_candidate_ids": []any{}})
	if r.IsError {
		t.Fatal(r.Content[0].Text)
	}
	var count int
	_ = d.DB().QueryRow("SELECT COUNT(*) FROM memories").Scan(&count)
	if count != 0 {
		t.Fatal("explicit empty selection wrote memories")
	}
}
func TestMemoryCommitEvidenceRequiresLiveWriteCredential(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			for _, kind := range []string{"missing proof", "revoked", "read key"} {
				t.Run(kind, func(t *testing.T) {
					d, ctx := memoryCommitEvidenceFixture(t, pg)
					switch kind {
					case "missing proof":
						d.actor.Credential = access.MetadataCredential{}
					case "revoked":
						_, _ = d.DB().Exec("UPDATE users SET is_active=false WHERE id='owner-a'")
					case "read key":
						d.actor.APIKeyPermissions = `{"read":true}`
						d.actor.Credential = access.MetadataCredential{Kind: "api_key", KeyID: "k"}
						_, _ = d.DB().Exec("INSERT INTO api_keys VALUES ('k','owner-a','{\"read\":true}',false)")
					}
					if r := ToolMemoryCommitPreview(ctx, d, memoryCommitCandidateArgs(kind)); !r.IsError {
						t.Fatal("unauthorized preview accepted")
					}
				})
			}
		})
	}
}
func TestMemoryCommitEvidenceConcurrentPreviewHasOneIdentity(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			d, ctx := memoryCommitEvidenceFixture(t, pg)
			memoryCommitParallelPool(t, d, pg)
			const n = 10
			results := make(chan ToolResult, n)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					results <- ToolMemoryCommitPreview(ctx, d, memoryCommitCandidateArgs("same-request"))
				}()
			}
			close(start)
			wg.Wait()
			close(results)
			var storedID string
			if err := d.DB().QueryRow("SELECT id FROM memory_commits").Scan(&storedID); err != nil {
				t.Fatal(err)
			}
			for r := range results {
				if r.IsError {
					t.Fatal(r.Content[0].Text)
				}
				id, _ := memoryCommitIDAndDigest(t, r)
				if id != storedID {
					t.Fatalf("preview returned non-persisted identity %q != %q", id, storedID)
				}
			}
		})
	}
}

func (d *memoryCommitEvidenceDeps) MemoryIndexOutbox() *memoryindex.Store {
	if p, ok := d.Deps.(interface{ MemoryIndexOutbox() *memoryindex.Store }); ok {
		return p.MemoryIndexOutbox()
	}
	return nil
}
func TestMemoryCommitEvidenceValidReceiptAndRevocationRollback(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			for _, change := range []string{"none", "deactivate", "receipt failed", "revision changed", "task collection changed", "task owner changed", "receipt owner changed", "receipt task changed"} {
				t.Run(change, func(t *testing.T) {
					d, ctx := memoryCommitEvidenceFixture(t, pg)
					task, receipt := memoryCommitOwnedTaskReceipt(t, d, ctx)
					args := memoryCommitCandidateArgs("with-proof")
					c := memoryCommitTestCandidate(args)
					c["source_task_id"] = task
					c["source_receipt_ids"] = []any{receipt}
					c["verification_status"] = "verified"
					first := memoryCommitTestCandidate(memoryCommitCandidateArgs("earlier-add"))
					first["candidate_id"] = "first"
					args["candidates"] = []any{first, c}
					preview := ToolMemoryCommitPreview(ctx, d, args)
					id, digest := memoryCommitIDAndDigest(t, preview)
					if !strings.Contains(preview.Content[0].Text, "receipt-validated") {
						t.Fatal("server evidence state missing from preview")
					}
					q := ""
					var v []any
					switch change {
					case "deactivate":
						q = "UPDATE users SET is_active=false WHERE id='owner-a'"
					case "receipt failed":
						q = "UPDATE task_receipts SET status='fail' WHERE id=$1"
						v = []any{receipt}
					case "revision changed":
						q = "UPDATE tasks SET current_workspace_revision='rev-2' WHERE id=$1"
						v = []any{task}
					case "task collection changed":
						q = "UPDATE tasks SET collection_name='other' WHERE id=$1"
						v = []any{task}
					case "task owner changed":
						q = "UPDATE tasks SET owner_id='owner-b' WHERE id=$1"
						v = []any{task}
					case "receipt owner changed":
						q = "UPDATE task_receipts SET owner_id='owner-b' WHERE id=$1"
						v = []any{receipt}
					case "receipt task changed":
						q = "UPDATE task_receipts SET task_id='other' WHERE id=$1"
						v = []any{receipt}
					}
					if q != "" {
						if _, err := d.DB().Exec(d.Q(q), v...); err != nil {
							t.Fatal(err)
						}
					}
					applied := ToolMemoryCommitApply(ctx, d, map[string]any{"commit_id": id, "plan_digest": digest})
					var count int
					_ = d.DB().QueryRow("SELECT COUNT(*) FROM memories").Scan(&count)
					if change == "none" {
						if applied.IsError || count != 2 {
							t.Fatalf("valid apply=%+v count=%d", applied, count)
						}
						var state string
						if err := d.DB().QueryRow("SELECT verification_status FROM memories WHERE key='with-proof'").Scan(&state); err != nil || state != "receipt-validated" {
							t.Fatalf("state=%s err=%v", state, err)
						}
					} else {
						if !applied.IsError || count != 0 {
							t.Fatalf("stale evidence partially applied: %+v count=%d", applied, count)
						}
						var status string
						_ = d.DB().QueryRow("SELECT status FROM memory_commits").Scan(&status)
						if status != "prepared" {
							t.Fatalf("failure consumed plan: %s", status)
						}
					}
				})
			}
		})
	}
}
func TestMemoryCommitEvidenceArtifactBytesCheckedAgainAndDeadlineBounded(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			d, ctx := memoryCommitEvidenceFixture(t, pg)
			task, rid := memoryCommitOwnedTaskReceipt(t, d, ctx)
			artifact := filepath.Join(t.TempDir(), "proof.txt")
			body := []byte("observed evidence")
			if err := os.WriteFile(artifact, body, 0600); err != nil {
				t.Fatal(err)
			}
			digest := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
			if _, err := d.DB().Exec(d.Q("UPDATE task_receipts SET receipt_type='artifact',evidence_uri=$1,artifact_digest=$2 WHERE id=$3"), "file://"+artifact, digest, rid); err != nil {
				t.Fatal(err)
			}
			calls := 0
			d.verify = func(ctx context.Context, uri, expected string) error {
				calls++
				p, ok := ArtifactReadPolicy(ctx)
				if !ok {
					return errors.New("missing locked artifact policy")
				}
				if _, err := p.IsSuperuser(ctx, "owner-a"); err != nil {
					return err
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				got, err := os.ReadFile(strings.TrimPrefix(uri, "file://"))
				if err != nil {
					return err
				}
				if fmt.Sprintf("sha256:%x", sha256.Sum256(got)) != expected {
					return errors.New("bytes changed")
				}
				return nil
			}
			args := memoryCommitCandidateArgs("artifact")
			c := memoryCommitTestCandidate(args)
			c["source_task_id"] = task
			c["source_receipt_ids"] = []any{rid}
			id, pdigest := memoryCommitIDAndDigest(t, ToolMemoryCommitPreview(ctx, d, args))
			if err := os.WriteFile(artifact, []byte("changed"), 0600); err != nil {
				t.Fatal(err)
			}
			if r := ToolMemoryCommitApply(ctx, d, map[string]any{"commit_id": id, "plan_digest": pdigest}); !r.IsError || calls != 2 {
				t.Fatalf("artifact not rechecked, calls=%d result=%+v", calls, r)
			}
			d.verify = func(ctx context.Context, _, _ string) error {
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("verifier missing deadline")
				}
				<-ctx.Done()
				return ctx.Err()
			}
			parent, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
			defer cancel()
			start := time.Now()
			r := ToolMemoryCommitApply(parent, d, map[string]any{"commit_id": id, "plan_digest": pdigest})
			if !r.IsError || time.Since(start) > time.Second {
				t.Fatalf("parent deadline was extended: elapsed=%s result=%+v", time.Since(start), r)
			}
			var count int
			_ = d.DB().QueryRow("SELECT COUNT(*) FROM memories").Scan(&count)
			if count != 0 {
				t.Fatal("failed artifact persisted memory")
			}
		})
	}
}
func TestMemoryCommitEvidenceSharedSupersedeRequiresCurrentAdmin(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			for _, mode := range []string{"user", "claimed admin", "admin", "revoked admin", "trusted local"} {
				t.Run(mode, func(t *testing.T) {
					d, ctx := memoryCommitEvidenceFixture(t, pg)
					if _, err := d.DB().Exec("INSERT INTO memories(id,key,value,type,owner_id,collection_name) VALUES ('shared','fact','old','project','','levara')"); err != nil {
						t.Fatal(err)
					}
					if mode == "admin" || mode == "revoked admin" {
						_, _ = d.DB().Exec("UPDATE users SET is_superuser=true WHERE id='owner-a'")
					}
					if mode == "claimed admin" {
						d.actor.Superuser = true
					}
					if mode == "trusted local" {
						d.actor.TrustedLocal = true
						d.actor.Credential = access.MetadataCredential{}
					}
					args := memoryCommitCandidateArgs("replacement")
					memoryCommitTestCandidate(args)["supersedes_memory_id"] = "shared"
					preview := ToolMemoryCommitPreview(ctx, d, args)
					if mode == "user" || mode == "claimed admin" {
						if !memoryCommitPreviewRejected(t, preview) {
							t.Fatal("ordinary principal can supersede shared fact")
						}
						return
					}
					id, digest := memoryCommitIDAndDigest(t, preview)
					if mode == "revoked admin" {
						_, _ = d.DB().Exec("UPDATE users SET is_superuser=false WHERE id='owner-a'")
					}
					r := ToolMemoryCommitApply(ctx, d, map[string]any{"commit_id": id, "plan_digest": digest})
					if mode == "revoked admin" {
						if !r.IsError {
							t.Fatal("revoked admin applied shared mutation")
						}
						return
					}
					if r.IsError {
						t.Fatal(r.Content[0].Text)
					}
					var owner string
					if err := d.DB().QueryRow("SELECT owner_id FROM memories WHERE key='replacement'").Scan(&owner); err != nil || owner != "" {
						t.Fatalf("shared scope lost: owner=%s err=%v", owner, err)
					}
				})
			}
		})
	}
}
func TestMemoryCommitEvidenceAPIKeyRevokeAndOwnerScope(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			d, ctx := memoryCommitEvidenceFixture(t, pg)
			d.actor.APIKeyPermissions = "read,write"
			d.actor.Credential = access.MetadataCredential{Kind: "api_key", KeyID: "write-key"}
			if _, err := d.DB().Exec("INSERT INTO api_keys VALUES ('write-key','owner-a','read,write',false)"); err != nil {
				t.Fatal(err)
			}
			id, digest := memoryCommitIDAndDigest(t, ToolMemoryCommitPreview(ctx, d, memoryCommitCandidateArgs("key-scope")))
			if _, err := d.DB().Exec("UPDATE api_keys SET permissions='read' WHERE id='write-key'"); err != nil {
				t.Fatal(err)
			}
			if r := ToolMemoryCommitApply(ctx, d, map[string]any{"commit_id": id, "plan_digest": digest}); !r.IsError {
				t.Fatal("revoked key write scope applied")
			}
			d.actor.Credential = access.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}
			d.actor.APIKeyPermissions = ""
			d.actor.UserID = "owner-b"
			if r := ToolMemoryCommitApply(ctx, d, map[string]any{"commit_id": id, "plan_digest": digest}); !r.IsError {
				t.Fatal("MetadataActor owner was replaced by stale context user")
			}
		})
	}
}
func TestMemoryCommitEvidencePreviewNeverReplacesExpiredOrAppliedPlan(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			d, ctx := memoryCommitEvidenceFixture(t, pg)
			args := memoryCommitCandidateArgs("stable-plan")
			id, digest := memoryCommitIDAndDigest(t, ToolMemoryCommitPreview(ctx, d, args))
			if r := ToolMemoryCommitApply(ctx, d, map[string]any{"commit_id": id, "plan_digest": digest, "accepted_candidate_ids": []any{}}); r.IsError {
				t.Fatal(r.Content[0].Text)
			}
			if _, err := d.DB().Exec("UPDATE memory_commits SET expires_at='2000-01-01T00:00:00Z'"); err != nil {
				t.Fatal(err)
			}
			again := ToolMemoryCommitPreview(ctx, d, args)
			againID, againDigest := memoryCommitIDAndDigest(t, again)
			if id != againID || digest != againDigest || !strings.Contains(again.Content[0].Text, `"status": "applied"`) {
				t.Fatalf("applied plan replaced: %+v", again)
			}
			changed := memoryCommitCandidateArgs("stable-plan")
			memoryCommitTestCandidate(changed)["value"] = "different request"
			if r := ToolMemoryCommitPreview(ctx, d, changed); !r.IsError {
				t.Fatal("different request replaced applied plan")
			}
			var count int
			_ = d.DB().QueryRow("SELECT COUNT(*) FROM memories").Scan(&count)
			if count != 0 {
				t.Fatal("explicit empty selection wrote memories")
			}
		})
	}
}

// Keep the artifact tests on a one-connection pool, but exercise the PostgreSQL
// lock protocol with independent connections sharing only this test's schema.
func memoryCommitParallelPool(t *testing.T, d *memoryCommitEvidenceDeps, postgres bool) {
	t.Helper()
	if !postgres {
		return
	}
	var schema string
	if err := d.DB().QueryRow("SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig(os.Getenv("LEVARA_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(5)
	t.Cleanup(func() { _ = db.Close() })
	d.Deps.(*postgresMemoryDeps).db = db
}

func TestMemoryCommitEvidenceConcurrentConflictAndApply(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			d, ctx := memoryCommitEvidenceFixture(t, pg)
			memoryCommitParallelPool(t, d, pg)
			type outcome struct {
				value  string
				result ToolResult
			}
			results := make(chan outcome, 2)
			start := make(chan struct{})
			for _, value := range []string{"first", "second"} {
				go func() {
					<-start
					args := memoryCommitCandidateArgs("competing")
					memoryCommitTestCandidate(args)["value"] = value
					results <- outcome{value, ToolMemoryCommitPreview(ctx, d, args)}
				}()
			}
			close(start)
			var winner outcome
			failed := 0
			for range 2 {
				r := <-results
				if r.result.IsError {
					failed++
				} else {
					winner = r
				}
			}
			if failed != 1 {
				t.Fatalf("different requests did not have exactly one winner: failures=%d", failed)
			}
			id, digest := memoryCommitIDAndDigest(t, winner.result)
			applies := make(chan ToolResult, 6)
			for range 6 {
				go func() {
					applies <- ToolMemoryCommitApply(ctx, d, map[string]any{"commit_id": id, "plan_digest": digest})
				}()
			}
			var response string
			for range 6 {
				r := <-applies
				if r.IsError {
					t.Fatal(r.Content[0].Text)
				}
				if response == "" {
					response = r.Content[0].Text
				} else if response != r.Content[0].Text {
					t.Fatal("concurrent apply returned a different result")
				}
			}
			var count int
			if err := d.DB().QueryRow("SELECT COUNT(*) FROM memories").Scan(&count); err != nil || count != 1 {
				t.Fatalf("apply count=%d err=%v", count, err)
			}
			var value string
			if err := d.DB().QueryRow("SELECT value FROM memories").Scan(&value); err != nil || value != winner.value {
				t.Fatalf("winning request overwritten: value=%q winner=%q err=%v", value, winner.value, err)
			}
		})
	}
}

func TestMemoryCommitEvidenceExpiredPreparedAndLegacyPlan(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			d, ctx := memoryCommitEvidenceFixture(t, pg)
			args := memoryCommitCandidateArgs("expired")
			id, digest := memoryCommitIDAndDigest(t, ToolMemoryCommitPreview(ctx, d, args))
			if _, err := d.DB().Exec("UPDATE memory_commits SET expires_at='2000-01-01T00:00:00Z'"); err != nil {
				t.Fatal(err)
			}
			if r := ToolMemoryCommitPreview(ctx, d, args); !r.IsError {
				t.Fatal("expired prepared plan refreshed")
			}
			if r := ToolMemoryCommitApply(ctx, d, map[string]any{"commit_id": id, "plan_digest": digest}); !r.IsError {
				t.Fatal("expired prepared plan applied")
			}
			var gotID, gotDigest, status string
			if err := d.DB().QueryRow("SELECT id,plan_digest,status FROM memory_commits").Scan(&gotID, &gotDigest, &status); err != nil || gotID != id || gotDigest != digest || status != "prepared" {
				t.Fatalf("expired plan overwritten: %s %s %s err=%v", gotID, gotDigest, status, err)
			}
			// Before this change, item JSON had no verification_status. Its digest
			// remains usable, while its caller-supplied candidate label is downgraded.
			legacy := memoryCommitPlan{Collection: "levara", OwnerID: "owner-a", Candidates: []memoryCommitCandidate{{CandidateID: "old", Key: "legacy", Value: "legacy value", Room: "memory", Hall: "fact", VerificationStatus: "verified"}}, Items: []memoryCommitItem{{CandidateID: "old", Action: "add", ReasonCode: "no_equivalent"}}}
			encoded, _ := json.Marshal(legacy)
			if strings.Count(string(encoded), "verification_status") != 1 {
				t.Fatal("legacy item unexpectedly acquired status")
			}
			legacyDigest := memoryCommitDigest(legacy)
			if _, err := d.DB().Exec(d.Q("UPDATE memory_commits SET plan_json=$1,plan_digest=$2,expires_at=$3 WHERE id=$4"), string(encoded), legacyDigest, time.Now().Add(time.Hour).Format(time.RFC3339Nano), id); err != nil {
				t.Fatal(err)
			}
			if r := ToolMemoryCommitApply(ctx, d, map[string]any{"commit_id": id, "plan_digest": legacyDigest}); r.IsError {
				t.Fatal(r.Content[0].Text)
			}
			if err := d.DB().QueryRow("SELECT verification_status FROM memories WHERE key='legacy'").Scan(&status); err != nil || status != "unverified" {
				t.Fatalf("legacy unproven label=%s err=%v", status, err)
			}
		})
	}
}

func TestMemoryCommitEvidenceSharedIndexScopeAndRollback(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			d, ctx := memoryCommitEvidenceFixture(t, pg)
			var base *fakeDeps
			if pg {
				base = d.Deps.(*postgresMemoryDeps).fakeDeps
			} else {
				base = d.Deps.(*fakeDeps)
			}
			outbox, err := memoryindex.NewStore(d.DB())
			if err != nil {
				t.Fatal(err)
			}
			base.memoryIndexOutbox, base.embedAvailable, base.hasColls = outbox, true, true
			for _, q := range []string{"UPDATE users SET is_superuser=true WHERE id='owner-a'", "INSERT INTO memories(id,key,value,type,owner_id,collection_name) VALUES ('shared','fact','old','project','','levara')"} {
				if _, err := d.DB().Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			args := memoryCommitCandidateArgs("shared-replacement")
			memoryCommitTestCandidate(args)["supersedes_memory_id"] = "shared"
			id, digest := memoryCommitIDAndDigest(t, ToolMemoryCommitPreview(ctx, d, args))
			trigger := "CREATE TRIGGER fail_commit_index BEFORE INSERT ON memory_index_jobs WHEN NEW.operation='upsert_vector' BEGIN SELECT RAISE(ABORT, 'injected index failure'); END"
			if pg {
				trigger = "CREATE FUNCTION fail_commit_index_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.operation='upsert_vector' THEN RAISE EXCEPTION 'injected index failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_commit_index BEFORE INSERT ON memory_index_jobs FOR EACH ROW EXECUTE FUNCTION fail_commit_index_fn()"
			}
			if _, err := d.DB().Exec(trigger); err != nil {
				t.Fatal(err)
			}
			applyArgs := map[string]any{"commit_id": id, "plan_digest": digest}
			if r := ToolMemoryCommitApply(ctx, d, applyArgs); !r.IsError {
				t.Fatal("injected outbox failure succeeded")
			}
			var memories, jobs int
			if err := d.DB().QueryRow("SELECT COUNT(*) FROM memories WHERE superseded_by=''").Scan(&memories); err != nil {
				t.Fatal(err)
			}
			if err := d.DB().QueryRow("SELECT COUNT(*) FROM memory_index_jobs").Scan(&jobs); err != nil {
				t.Fatal(err)
			}
			if memories != 1 || jobs != 0 {
				t.Fatalf("partial rollback: memories=%d jobs=%d", memories, jobs)
			}
			drop := "DROP TRIGGER fail_commit_index"
			if pg {
				drop += " ON memory_index_jobs"
			}
			if _, err := d.DB().Exec(drop); err != nil {
				t.Fatal(err)
			}
			if r := ToolMemoryCommitApply(ctx, d, applyArgs); r.IsError {
				t.Fatal(r.Content[0].Text)
			}
			if err := d.DB().QueryRow("SELECT COUNT(*) FROM memory_index_jobs WHERE owner_id=''").Scan(&jobs); err != nil || jobs != 2 {
				t.Fatalf("shared jobs owner changed: jobs=%d err=%v", jobs, err)
			}
		})
	}
}
