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

func (s pgSCIMQuery) ByEmail(ctx context.Context, issuer, email string) (uids, externalIDs []string, total int, err error) {
	rows, err := s.DB.QueryContext(ctx, s.rewrite(`
		SELECT u.id, COALESCE(si.external_id, u.email)
		FROM users u
		LEFT JOIN scim_identities si ON si.user_id = u.id AND si.issuer = $1
		WHERE u.email = $2`), issuer, email)
	if err != nil {
		return nil, nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, ext string
		if err := rows.Scan(&id, &ext); err != nil {
			return nil, nil, 0, err
		}
		uids = append(uids, id)
		externalIDs = append(externalIDs, ext)
		total++
	}
	return uids, externalIDs, total, rows.Err()
}

func (s pgSCIMQuery) List(ctx context.Context, issuer string, start, count int) (uids, externalIDs []string, total int, err error) {
	if err := s.DB.QueryRowContext(ctx, s.rewrite(`
		SELECT COUNT(*) FROM users u
		JOIN scim_identities si ON si.user_id = u.id AND si.issuer = $1`), issuer).Scan(&total); err != nil {
		return nil, nil, 0, err
	}
	if total == 0 {
		return nil, nil, 0, nil
	}
	rows, err := s.DB.QueryContext(ctx, s.rewrite(`
		SELECT u.id, si.external_id
		FROM users u
		JOIN scim_identities si ON si.user_id = u.id AND si.issuer = $1
		ORDER BY si.created_at, u.id
		LIMIT $2 OFFSET $3`), issuer, count, start-1)
	if err != nil {
		return nil, nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, ext string
		if err := rows.Scan(&id, &ext); err != nil {
			return nil, nil, 0, err
		}
		uids = append(uids, id)
		externalIDs = append(externalIDs, ext)
	}
	return uids, externalIDs, total, rows.Err()
}

func (s pgSCIMQuery) ByID(ctx context.Context, id string) (email string, active bool, externalID string, err error) {
	err = s.DB.QueryRowContext(ctx, s.rewrite(`
		SELECT u.email, u.is_active, COALESCE(si.external_id, '')
		FROM users u
		LEFT JOIN scim_identities si ON si.user_id = $1
		WHERE u.id = $2`), id, id).Scan(&email, &active, &externalID)
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
