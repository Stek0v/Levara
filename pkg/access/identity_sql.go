package access

import (
	"context"
	"database/sql"
	"errors"
)

// SQLIdentityBridge links a verified SSO subject to a provisioned SCIM user.
// TrustedIssuers explicitly maps an exact SSO issuer to its exact SCIM directory
// issuer. Neither email nor normalized issuer/subject strings are identity keys.
// Configure the map once at startup; concurrent mutation is not supported.
type SQLIdentityBridge struct {
	DB             *sql.DB
	Q              QueryRewriter
	TrustedIssuers map[string]string
}

func (SQLIdentityBridge) Method() string { return "sql" }

func (b SQLIdentityBridge) ResolveExternal(ctx context.Context, ext ExternalIdentity) (Principal, error) {
	if err := ext.Validate(); err != nil {
		return Principal{}, err
	}
	issuer, ok := b.TrustedIssuers[ext.Issuer]
	if !ok || issuer == "" {
		return Principal{}, ErrSubjectNotMapped
	}
	if b.DB == nil {
		return Principal{}, ErrProvisioningNoDB
	}
	query := `SELECT u.id, u.email, u.is_superuser FROM scim_identities s
		JOIN users u ON u.id = s.user_id
		WHERE s.issuer = $1 AND s.external_id = $2 AND u.is_active = true`
	if b.Q != nil {
		query = b.Q(query)
	}
	var p Principal
	err := b.DB.QueryRowContext(ctx, query, issuer, ext.Subject).Scan(&p.UserID, &p.Email, &p.Superuser)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrSubjectNotMapped
	}
	if err != nil {
		return Principal{}, err
	}
	p.AuthMethod = "sso"
	return p, nil
}
