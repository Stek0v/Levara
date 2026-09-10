package mcp

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/stek0v/levara/pkg/access"
)

// Preview also writes prepared state. Both entry points acquire the same short
// fence before checking live credentials or looking at any prepared plan.
func memoryCommitBegin(ctx context.Context, deps Deps) (*sql.Tx, access.MetadataActor, access.SQLPolicy, error) {
	actor := deps.MetadataActor(ctx)
	policy := access.SQLPolicy{DB: deps.DB(), Q: deps.Q}
	if deps.DB() == nil {
		return nil, actor, policy, errors.New("database not configured")
	}
	if !access.APIKeyAllows(actor.APIKeyPermissions, access.ActionWrite) || !actor.TrustedLocal && actor.UserID == "" {
		return nil, actor, policy, access.ErrDocumentForbidden
	}
	sqlite := memoryCommitSQLite(deps)
	var tx *sql.Tx
	var err error
	if actor.TrustedLocal {
		tx, err = deps.DB().BeginTx(ctx, nil)
	} else {
		tx, policy, err = policy.BeginMetadataWrite(ctx, actor, sqlite)
	}
	if err != nil {
		return nil, actor, policy, err
	}
	fail := func(e error) (*sql.Tx, access.MetadataActor, access.SQLPolicy, error) {
		_ = tx.Rollback()
		return nil, actor, policy, e
	}
	// Serialize plan comparisons/publication with all memory writers. This also
	// turns a SQLite deferred transaction into a writer before taking a snapshot.
	if sqlite {
		_, err = tx.ExecContext(ctx, "UPDATE memory_commits SET id=id WHERE 1=0")
	} else {
		_, err = tx.ExecContext(ctx, "LOCK TABLE memory_commits, memories IN SHARE ROW EXCLUSIVE MODE")
	}
	if err != nil {
		return fail(err)
	}
	policy = policy.WithReadTransaction(tx)
	if err = memoryCommitRecheck(ctx, policy, actor); err != nil {
		return fail(err)
	}
	return tx, actor, policy, nil
}
func memoryCommitSQLite(deps Deps) bool {
	return strings.Contains(strings.ToLower(fmt.Sprintf("%T", deps.DB().Driver())), "sqlite")
}
func memoryCommitRecheck(ctx context.Context, policy access.SQLPolicy, actor access.MetadataActor) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !access.APIKeyAllows(actor.APIKeyPermissions, access.ActionWrite) || actor.Credential.Kind == "api_key" && strings.TrimSpace(actor.APIKeyPermissions) == "" {
		return access.ErrDocumentForbidden
	}
	if actor.TrustedLocal {
		return nil
	}
	if actor.UserID == "" {
		return access.ErrRevokedCredential
	}
	c := actor.Credential
	if err := policy.RecheckCredential(ctx, actor.UserID, c.Kind, c.KeyID, actor.APIKeyPermissions, c.SessionID, c.Epoch, c.IssuedAt, c.ExpiresAt); err != nil {
		return err
	}
	if actor.TenantID != "" {
		ok, err := policy.IsTenantMember(ctx, actor.UserID, actor.TenantID)
		if err != nil {
			return err
		}
		if !ok {
			return access.ErrDocumentForbidden
		}
	}
	return nil
}
func memoryCommitCanMutateShared(ctx context.Context, policy access.SQLPolicy, actor access.MetadataActor) bool {
	if actor.TrustedLocal {
		return true
	}
	ok, err := policy.IsSuperuser(ctx, actor.UserID)
	return err == nil && ok
}
func memoryCommitValidateEvidence(ctx context.Context, tx *sql.Tx, deps Deps, owner, collection string, c memoryCommitCandidate) (string, error) {
	if c.SourceTaskID == "" && len(c.SourceReceiptIDs) == 0 {
		return "unverified", nil
	}
	invalid := errors.New("source evidence is missing, inaccessible, failed, or stale")
	if strings.TrimSpace(c.SourceTaskID) == "" || len(c.SourceTaskID) > 256 || len(c.SourceReceiptIDs) < 1 || len(c.SourceReceiptIDs) > 64 {
		return "unverified", invalid
	}
	suffix := ""
	if !memoryCommitSQLite(deps) {
		suffix = " FOR SHARE"
	}
	var revision string
	if err := tx.QueryRowContext(ctx, deps.Q("SELECT current_workspace_revision FROM tasks WHERE id=$1 AND owner_id=$2 AND collection_name=$3"+suffix), c.SourceTaskID, owner, collection).Scan(&revision); err != nil || revision == "" {
		return "unverified", invalid
	}
	seen := map[string]bool{}
	for _, id := range c.SourceReceiptIDs {
		if len(id) == 0 || len(id) > 256 || seen[id] {
			return "unverified", invalid
		}
		seen[id] = true
		var kind, status, rev, uri, digest string
		var exit sql.NullInt64
		if err := tx.QueryRowContext(ctx, deps.Q("SELECT receipt_type,status,workspace_revision,evidence_uri,artifact_digest,exit_code FROM task_receipts WHERE id=$1 AND task_id=$2 AND owner_id=$3"+suffix), id, c.SourceTaskID, owner).Scan(&kind, &status, &rev, &uri, &digest, &exit); err != nil {
			return "unverified", invalid
		}
		if status != "pass" || rev != revision || kind == "command" && (!exit.Valid || exit.Int64 != 0) {
			return "unverified", invalid
		}
		switch kind {
		case "command", "artifact", "source", "observation", "reviewer":
		default:
			return "unverified", invalid
		}
		if kind == "artifact" || digest != "" {
			normalized := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(digest)), "sha256:")
			if len(normalized) != 64 || uri == "" || len(uri) > 4096 {
				return "unverified", invalid
			}
			if _, err := hex.DecodeString(normalized); err != nil {
				return "unverified", invalid
			}
			verifier, ok := deps.(ArtifactVerifier)
			if !ok {
				return "unverified", invalid
			}
			lockedPolicy := (access.SQLPolicy{DB: deps.DB(), Q: deps.Q}).WithReadTransaction(tx)
			artifactCtx := context.WithValue(ctx, artifactReadPolicyKey{}, lockedPolicy)
			if err := verifier.VerifyArtifact(artifactCtx, uri, digest); err != nil {
				return "unverified", invalid
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return "unverified", err
	}
	return "receipt-validated", nil
}

// ArtifactReadPolicy returns the policy bound to the current memory-commit
// transaction. Verifiers must use it for authorization reads instead of opening
// another DB connection. Its lifetime ends with this verifier call; it must not
// be retained or committed by the verifier. The private key cannot be supplied
// through tool arguments, and wrapping preserves the existing request context.
func ArtifactReadPolicy(ctx context.Context) (access.SQLPolicy, bool) {
	p, ok := ctx.Value(artifactReadPolicyKey{}).(access.SQLPolicy)
	return p, ok
}

type artifactReadPolicyKey struct{}

func memoryCommitSourceReceipts(raw any) ([]string, bool) {
	if raw == nil {
		return nil, true
	}
	var result []string
	switch values := raw.(type) {
	case []string:
		if len(values) > 64 {
			return nil, false
		}
		result = append(result, values...)
	case []any:
		if len(values) > 64 {
			return nil, false
		}
		for _, v := range values {
			id, ok := v.(string)
			if !ok {
				return nil, false
			}
			result = append(result, id)
		}
	default:
		return nil, false
	}
	for _, id := range result {
		if len(id) == 0 || len(id) > 256 {
			return nil, false
		}
	}
	return result, true
}
