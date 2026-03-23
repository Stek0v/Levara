// sqlcompat.go — SQL dialect compatibility layer for PostgreSQL and SQLite.
// Wraps SQL queries with Q() to translate PostgreSQL syntax to SQLite when needed.
package http

import (
	"fmt"
	"regexp"
	"strings"
)

// DBProvider tracks which SQL dialect to use.
type DBProvider string

const (
	DBPostgres DBProvider = "postgres"
	DBSQLite   DBProvider = "sqlite"
)

var activeDBProvider DBProvider = DBPostgres

// SetDBProvider sets the active SQL dialect.
func SetDBProvider(p DBProvider) { activeDBProvider = p }

// GetDBProvider returns the current SQL dialect.
func GetDBProvider() DBProvider { return activeDBProvider }

// Q converts PostgreSQL parameterized queries to the active dialect.
// PostgreSQL: $1, $2, $3 -> SQLite: ?, ?, ?
// PostgreSQL: NOW() -> SQLite: CURRENT_TIMESTAMP
// PostgreSQL: ILIKE -> SQLite: LIKE (case-insensitive by default in SQLite)
// PostgreSQL: ::jsonb -> SQLite: (removed, SQLite treats JSON as TEXT)
//
// IMPORTANT: For queries that reuse the same $N placeholder multiple times
// (e.g., ON CONFLICT ... DO UPDATE SET col = $3), use QArgs() instead
// since SQLite uses positional ? and needs the argument duplicated.
func Q(query string) string {
	if activeDBProvider == DBPostgres {
		return query
	}
	// Replace $N placeholders with ?
	query = pgPlaceholderRe.ReplaceAllString(query, "?")
	// Replace NOW()
	query = strings.ReplaceAll(query, "NOW()", "CURRENT_TIMESTAMP")
	query = strings.ReplaceAll(query, "now()", "CURRENT_TIMESTAMP")
	// Replace ILIKE with LIKE (SQLite LIKE is case-insensitive for ASCII by default)
	query = strings.ReplaceAll(query, " ILIKE ", " LIKE ")
	query = strings.ReplaceAll(query, " ilike ", " LIKE ")
	// Remove ::jsonb casts (SQLite stores JSON as TEXT)
	query = strings.ReplaceAll(query, "::jsonb", "")
	return query
}

var pgPlaceholderRe = regexp.MustCompile(`\$(\d+)`)

// QArgs converts a PostgreSQL query to the active dialect AND adjusts
// the argument list for parameter reuse. PostgreSQL allows $3 to appear
// multiple times (always refers to arg[2]). SQLite uses positional ?,
// so repeated references need duplicated arguments.
//
// Returns the converted query and the adjusted argument slice.
func QArgs(query string, args ...any) (string, []any) {
	if activeDBProvider == DBPostgres {
		return query, args
	}

	// Replace syntax elements
	query = strings.ReplaceAll(query, "NOW()", "CURRENT_TIMESTAMP")
	query = strings.ReplaceAll(query, "now()", "CURRENT_TIMESTAMP")
	query = strings.ReplaceAll(query, " ILIKE ", " LIKE ")
	query = strings.ReplaceAll(query, " ilike ", " LIKE ")
	query = strings.ReplaceAll(query, "::jsonb", "")

	// Find all $N references in order and build new args list
	matches := pgPlaceholderRe.FindAllStringSubmatch(query, -1)
	if len(matches) == 0 {
		return query, args
	}

	var newArgs []any
	for _, m := range matches {
		idx := 0
		for _, ch := range m[1] {
			idx = idx*10 + int(ch-'0')
		}
		if idx >= 1 && idx <= len(args) {
			newArgs = append(newArgs, args[idx-1])
		}
	}

	// Replace all $N with ?
	query = pgPlaceholderRe.ReplaceAllString(query, "?")

	return query, newArgs
}

// InPlaceholders generates SQL for checking membership in a list.
// PostgreSQL: = ANY(ARRAY[$1, $2, $3])
// SQLite: IN (?, ?, ?)
func InPlaceholders(count int, startIdx int) string {
	if activeDBProvider == DBPostgres {
		parts := make([]string, count)
		for i := range parts {
			parts[i] = fmt.Sprintf("$%d", startIdx+i)
		}
		return "= ANY(ARRAY[" + strings.Join(parts, ",") + "])"
	}
	parts := make([]string, count)
	for i := range parts {
		parts[i] = "?"
	}
	return "IN (" + strings.Join(parts, ",") + ")"
}
