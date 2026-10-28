// workspacesvc_delegates.go — cfg-based delegates over the workspace
// filesystem service (internal/api/workspacesvc, Phase-2 workspace
// campaign foundation). Signatures match the original package-local
// helpers so every consumer in internal/http is unchanged; future
// route-group slices import workspacesvc directly.
package http

import (
	"context"
	"os"

	"github.com/stek0v/levara/internal/api/workspacesvc"
	"github.com/stek0v/levara/pkg/workspace"
)

func workspaceRoot(cfg APIConfig) string {
	return workspacesvc.Root(cfg.WorkspacePath)
}

func workspaceManifestPath(cfg APIConfig, projectID, branch string) string {
	return workspacesvc.ManifestPath(workspaceRoot(cfg), projectID, branch)
}

func workspaceProjectRoot(cfg APIConfig, projectID, branch string) string {
	return workspacesvc.ProjectRoot(workspaceRoot(cfg), projectID, branch)
}

func workspaceFilePath(cfg APIConfig, projectID, branch, path string) (string, string, error) {
	return workspacesvc.FilePath(workspaceRoot(cfg), projectID, branch, path)
}

func workspaceRunDir(cfg APIConfig, projectID, branch, runID string) (string, error) {
	return workspacesvc.RunDir(workspaceRoot(cfg), projectID, branch, runID)
}

func workspaceCommitsRoot(cfg APIConfig, projectID, branch string) string {
	return workspacesvc.CommitsRoot(workspaceRoot(cfg), projectID, branch)
}

func workspaceCommitDir(cfg APIConfig, projectID, branch, commitID string) (string, error) {
	return workspacesvc.CommitDir(workspaceRoot(cfg), projectID, branch, commitID)
}

func workspaceBranchLockKey(projectID, branch string) string {
	return workspacesvc.BranchLockKey(projectID, branch)
}

func openWorkspaceDirectory(cfg APIConfig, absolute string, create bool) (*os.Root, error) {
	return workspacesvc.OpenDirectory(workspaceRoot(cfg), absolute, create)
}

func openWorkspaceFileParent(cfg APIConfig, absolute string, create bool) (*os.Root, string, error) {
	return workspacesvc.OpenFileParent(workspaceRoot(cfg), absolute, create)
}

func readWorkspaceFile(cfg APIConfig, absolute string) ([]byte, error) {
	return workspacesvc.ReadFile(workspaceRoot(cfg), absolute)
}

func writeWorkspaceFile(ctx context.Context, cfg APIConfig, absolute string, data []byte) error {
	return workspacesvc.WriteFile(ctx, workspaceRoot(cfg), absolute, data)
}

func saveWorkspaceManifest(ctx context.Context, cfg APIConfig, absolute string, manifest *workspace.Manifest) error {
	return workspacesvc.SaveManifest(ctx, workspaceRoot(cfg), absolute, manifest)
}

func readWorkspaceManifest(cfg APIConfig, projectID, branch string) (*workspace.Manifest, bool, error) {
	return workspacesvc.ReadManifest(workspaceRoot(cfg), projectID, branch)
}

func loadWorkspaceManifest(cfg APIConfig, projectID, branch string) (*workspace.Manifest, string, error) {
	return workspacesvc.LoadManifest(workspaceRoot(cfg), projectID, branch)
}

func listWorkspaceMarkdownPaths(cfg APIConfig, absolute string) ([]string, error) {
	return workspacesvc.ListMarkdownPaths(workspaceRoot(cfg), absolute)
}

func digestBytes(data []byte) string {
	return workspacesvc.DigestBytes(data)
}

func digestText(text string) string {
	return workspacesvc.DigestText(text)
}

func newerTimeString(current, candidate string) string {
	return workspacesvc.NewerTimeString(current, candidate)
}
