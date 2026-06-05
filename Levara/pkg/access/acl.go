package access

// ACL permission types grantable on a dataset. These are the canonical
// permission vocabulary shared by the grant and check paths so the two cannot
// drift; HTTP handlers validate and enumerate through these helpers instead of
// carrying their own literal maps.
const (
	PermissionRead   = "read"
	PermissionWrite  = "write"
	PermissionDelete = "delete"
	PermissionShare  = "share"
)

// PermissionTypes returns the canonical ordered list of grantable ACL
// permissions. Callers building an "all permissions" result map (e.g. an ACL
// check response) iterate this so the permission set stays single-sourced.
func PermissionTypes() []string {
	return []string{PermissionRead, PermissionWrite, PermissionDelete, PermissionShare}
}

// ValidPermissionType reports whether pt is a grantable ACL permission
// (read, write, delete, share). Empty or unknown values are rejected.
func ValidPermissionType(pt string) bool {
	switch pt {
	case PermissionRead, PermissionWrite, PermissionDelete, PermissionShare:
		return true
	default:
		return false
	}
}
