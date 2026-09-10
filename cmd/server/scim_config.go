package main

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/stek0v/levara/pkg/access"
)

// configureSCIMStore binds the managed feature subset to one existing tenant.
// Base schema and EnsureDirectoryGroupSchema are also initialized independently at
// startup so ordinary local group APIs can always enforce external ownership.
func configureSCIMStore(ctx context.Context, store scimStore, issuer string) (scimStore, *access.SCIMStore, error) {
	tenant := os.Getenv("LEVARA_SCIM_TENANT_ID")
	if tenant == "" {
		return store, nil, nil
	}
	if strings.TrimSpace(tenant) != tenant {
		return nil, nil, errors.New("scim tenant must be an exact existing ID")
	}
	var sqlStore access.SCIMStore
	switch s := store.(type) {
	case access.SCIMStore:
		sqlStore = s
	case *access.SCIMStore:
		if s == nil {
			return nil, nil, access.ErrSCIMDisabled
		}
		sqlStore = *s
	default:
		return nil, nil, errors.New("managed SCIM requires SQL store")
	}
	if err := access.EnsureDirectoryGroupSchema(ctx, sqlStore.DB, sqlStore.Q); err != nil {
		return nil, nil, err
	}
	if err := sqlStore.BindDirectory(ctx, issuer, tenant); err != nil {
		return nil, nil, err
	}
	sqlStore.TenantID = tenant
	return sqlStore, &sqlStore, nil
}
