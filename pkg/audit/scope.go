package audit

import (
	"database/sql"
	"fmt"
	"regexp"
)

// ScopeQuery applies server-selected actor/tenant boundaries before LIMIT or
// aggregation. Identifiers are fixed; values always use bound parameters.
func ScopeQuery(query string, args []any, f EventFilter) (string, []any) {
	if f.RequireVerifiedScope {
		query += " AND scope_verified=1"
	}
	if f.AgentID != "" {
		query += " AND agent_id=?"
		args = append(args, f.AgentID)
	}
	if f.RestrictTenant {
		query += " AND tenant_id=?"
		args = append(args, f.TenantID)
	}
	return query, args
}

// EnsureColumn upgrades an existing projection without hiding migration errors.
// A concurrent initializer may win ALTER; a successful read verifies the result.
func EnsureColumn(db *sql.DB, table, column, ddl string) error {
	identifier := regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
	if !identifier.MatchString(table) || !identifier.MatchString(column) {
		return fmt.Errorf("invalid schema identifier")
	}
	check := func() error {
		rows, err := db.Query("SELECT " + column + " FROM " + table + " LIMIT 0")
		if err == nil {
			err = rows.Close()
		}
		return err
	}
	if check() == nil {
		return nil
	}
	if _, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + ddl); err != nil {
		if check() != nil {
			return err
		}
	}
	return check()
}
