package main

import (
	"context"
	"database/sql"
	"errors"

	accesspkg "github.com/stek0v/levara/pkg/access"
)

// pgSCIMQuery is the Postgres-backed read side for the SCIM HTTP layer.
// Read-only over the users/scim_identities tables; mutations go through
// scimStore (SCIMStore in pkg/access).
type pgSCIMQuery struct {
	DB *sql.DB
	Q  accesspkg.QueryRewriter
}

func (s pgSCIMQuery) rewrite(q string) string {
	if s.Q == nil {
		return q
	}
	return s.Q(q)
}

func (s pgSCIMQuery) ByEmail(ctx context.Context, issuer, email string) (users []scimUserRecord, total int, err error) {
	rows, err := s.DB.QueryContext(ctx, s.rewrite(`
		SELECT u.id, u.email, u.is_active, si.external_id
		FROM users u
		JOIN scim_identities si ON si.user_id = u.id AND si.issuer = $1
		WHERE u.email = $2`), issuer, email)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var u scimUserRecord
		if err := rows.Scan(&u.ID, &u.Email, &u.Active, &u.ExternalID); err != nil {
			return nil, 0, err
		}
		users = append(users, u)
		total++
	}
	return users, total, rows.Err()
}

func (s pgSCIMQuery) List(ctx context.Context, issuer string, start, count int) (users []scimUserRecord, total int, err error) {
	if err := s.DB.QueryRowContext(ctx, s.rewrite(`
		SELECT COUNT(*) FROM users u
		JOIN scim_identities si ON si.user_id = u.id AND si.issuer = $1`), issuer).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, nil
	}
	rows, err := s.DB.QueryContext(ctx, s.rewrite(`
		SELECT u.id, u.email, u.is_active, si.external_id
		FROM users u
		JOIN scim_identities si ON si.user_id = u.id AND si.issuer = $1
		ORDER BY si.created_at, u.id
		LIMIT $2 OFFSET $3`), issuer, count, start-1)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var u scimUserRecord
		if err := rows.Scan(&u.ID, &u.Email, &u.Active, &u.ExternalID); err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}
	return users, total, rows.Err()
}

func (s pgSCIMQuery) ByID(ctx context.Context, issuer, id string) (email string, active bool, externalID string, err error) {
	err = s.DB.QueryRowContext(ctx, s.rewrite(`
		SELECT u.email, u.is_active, si.external_id
		FROM users u
		JOIN scim_identities si ON si.user_id = u.id AND si.issuer = $1
		WHERE u.id = $2`), issuer, id).Scan(&email, &active, &externalID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, "", accesspkg.ErrUserNotFound
	}
	return email, active, externalID, err
}

// compile-time interface checks
var (
	_ scimQuerier = pgSCIMQuery{}
	_ scimStore   = accesspkg.SCIMStore{}
)
