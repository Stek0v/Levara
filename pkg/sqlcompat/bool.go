// Package sqlcompat holds dialect-neutral SQL fragments shared by pkg/mcp
// and internal/http without import cycles.
package sqlcompat

// Provider names the active SQL dialect ("postgres" or "sqlite").
type Provider string

const (
	Postgres Provider = "postgres"
	SQLite   Provider = "sqlite"
)

var active Provider = Postgres

// SetProvider sets the active dialect for SQLBool* helpers.
func SetProvider(p Provider) { active = p }

// CurrentProvider returns the active dialect.
func CurrentProvider() Provider { return active }

// BoolTrue returns a predicate matching a truthy boolean column.
func BoolTrue(column string) string {
	if active == Postgres {
		return column + " IS TRUE"
	}
	return column + " = 1"
}

// BoolFalse returns a predicate matching a falsy boolean column.
func BoolFalse(column string) string {
	if active == Postgres {
		return column + " IS NOT TRUE"
	}
	return column + " = 0"
}
