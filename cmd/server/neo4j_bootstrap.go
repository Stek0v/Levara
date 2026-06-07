package main

import "strings"

// shouldBootstrapNeo4jSchema decides whether startup should attempt Neo4j
// schema bootstrap. Empty value defaults to enabled for backward compatibility.
//
// Disable values: "0", "false", "no", "off" (case-insensitive).
func shouldBootstrapNeo4jSchema(raw string) bool {
	v := strings.TrimSpace(strings.ToLower(raw))
	if v == "" {
		return true
	}
	switch v {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}
