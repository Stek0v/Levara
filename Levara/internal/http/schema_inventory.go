package http

import "strings"

// SchemaObjectKind describes the migration object represented by a statement.
type SchemaObjectKind string

const (
	SchemaTable SchemaObjectKind = "table"
	SchemaIndex SchemaObjectKind = "index"
	SchemaAlter SchemaObjectKind = "alter"
)

// SchemaObject is a compact inventory entry derived from migration statements.
type SchemaObject struct {
	Kind     SchemaObjectKind
	Name     string
	Provider DBProvider
}

// SchemaInventory returns table/index/alter inventory for PostgreSQL and
// SQLite migrations. It is intentionally derived from the migration statements
// rather than maintained manually.
func SchemaInventory() []SchemaObject {
	out := make([]SchemaObject, 0, len(schemaStatements)+len(schemaSQLiteStatements))
	out = append(out, schemaInventoryFor(DBPostgres, schemaStatements)...)
	out = append(out, schemaInventoryFor(DBSQLite, schemaSQLiteStatements)...)
	return out
}

func schemaInventoryFor(provider DBProvider, stmts []string) []SchemaObject {
	out := make([]SchemaObject, 0, len(stmts))
	for _, stmt := range stmts {
		kind, name, ok := classifySchemaStatement(stmt)
		if !ok {
			continue
		}
		out = append(out, SchemaObject{Kind: kind, Name: name, Provider: provider})
	}
	return out
}

func classifySchemaStatement(stmt string) (SchemaObjectKind, string, bool) {
	fields := strings.Fields(strings.TrimSpace(stmt))
	if len(fields) < 3 {
		return "", "", false
	}
	for i := range fields {
		fields[i] = strings.Trim(fields[i], "`\"")
	}
	if len(fields) >= 6 &&
		strings.EqualFold(fields[0], "CREATE") &&
		strings.EqualFold(fields[1], "TABLE") &&
		strings.EqualFold(fields[2], "IF") &&
		strings.EqualFold(fields[3], "NOT") &&
		strings.EqualFold(fields[4], "EXISTS") {
		return SchemaTable, cleanSchemaName(fields[5]), true
	}
	if len(fields) >= 6 &&
		strings.EqualFold(fields[0], "CREATE") &&
		strings.EqualFold(fields[1], "INDEX") &&
		strings.EqualFold(fields[2], "IF") &&
		strings.EqualFold(fields[3], "NOT") &&
		strings.EqualFold(fields[4], "EXISTS") {
		return SchemaIndex, cleanSchemaName(fields[5]), true
	}
	if len(fields) >= 3 && strings.EqualFold(fields[0], "ALTER") && strings.EqualFold(fields[1], "TABLE") {
		return SchemaAlter, cleanSchemaName(fields[2]), true
	}
	return "", "", false
}

func cleanSchemaName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Trim(name, "`\"")
	name = strings.TrimSuffix(name, "(")
	return name
}
