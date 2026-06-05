package access

// APIKeyIdentity is the verified result of an API-key lookup. Token hashing and
// the key→user query stay in the transport/auth layer (Phase 2B leaves token
// parsing where it is); this is only the transport-independent shape that
// adapters and policy code consume, so callers stop passing bare
// (userID, permissions) tuples around.
type APIKeyIdentity struct {
	UserID      string
	Permissions string
}

// Valid reports whether the lookup resolved to a user.
func (id APIKeyIdentity) Valid() bool { return id.UserID != "" }

// Actor projects the API-key identity into the shared Actor shape so policy
// code never has to know it originated from an API key.
func (id APIKeyIdentity) Actor() Actor {
	return Actor{
		UserID:            id.UserID,
		APIKeyPermissions: id.Permissions,
		AuthMethod:        "api_key",
	}
}
