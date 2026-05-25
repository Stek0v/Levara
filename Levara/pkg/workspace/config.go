package workspace

const (
	DefaultStoragePath   = "data/uploads"
	DefaultWorkspacePath = "data/workspace"
)

// ResolveRuntimePaths applies Levara's default data roots for API/workspace
// handlers. Keeping this in pkg/workspace makes the workspace path policy
// reusable outside the HTTP transport.
func ResolveRuntimePaths(storagePath, workspacePath string) (string, string) {
	if storagePath == "" {
		storagePath = DefaultStoragePath
	}
	if workspacePath == "" {
		workspacePath = DefaultWorkspacePath
	}
	return storagePath, workspacePath
}
