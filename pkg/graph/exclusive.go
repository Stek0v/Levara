package graph

import "strings"

// IsExclusiveRelationship defines source relationships with one current target.
func IsExclusiveRelationship(relation string) bool {
	switch strings.ToLower(relation) {
	case "assigned_to", "role_is", "status_is", "located_in", "lives_in", "works_at", "owns", "reports_to", "current_state", "is_a":
		return true
	default:
		return false
	}
}
