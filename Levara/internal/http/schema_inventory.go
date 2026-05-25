package http

import (
	"strings"

	"github.com/stek0v/levara/internal/contract"
)

// SchemaObject is a type alias for contract.SchemaObject so inventory entries
// are interchangeable with the canonical contract type.
type SchemaObject = contract.SchemaObject

// SchemaKind is a type alias for string kept for readability at call sites.
type SchemaKind = string

const (
	SchemaTable SchemaKind = "table"
	SchemaIndex SchemaKind = "index"
	SchemaAlter SchemaKind = "alter"
)

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
		out = append(out, SchemaObject{Kind: kind, Name: name, Provider: string(provider)})
	}
	return out
}

func classifySchemaStatement(stmt string) (SchemaKind, string, bool) {
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
