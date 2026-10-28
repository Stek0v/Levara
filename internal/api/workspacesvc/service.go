// service.go — workspace filesystem service: path math, manifest IO and
// file access scoped to the workspace root.
//
// Extracted verbatim from internal/http/workspace.go, workspace_ops.go and
// workspace_artifacts.go (Phase-2 workspace campaign foundation,
// code-modernization brief §3). Every function is pure over the workspace
// root string — no APIConfig, no HTTP, no ACL — so the workspace route
// slices import this package directly instead of threading closures.
// internal/http keeps thin cfg-based delegates with unchanged signatures.
package workspacesvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/stek0v/levara/pkg/workspace"
)

// Root resolves the effective workspace root from the configured path.
func Root(workspacePath string) string {
	if workspacePath != "" {
		return workspacePath
	}
	return "data/workspace"
}

// DefaultBranch normalizes an optional branch name.
func DefaultBranch(branch string) string {
	return workspace.DefaultBranch(branch)
}

// SafeID normalizes an identifier for filesystem use.
func SafeID(s string) string {
	return workspace.SafeID(s)
}

// ManifestPath renders the canonical manifest location.
func ManifestPath(root, projectID, branch string) string {
	return workspace.ManifestPath(root, projectID, branch)
}

// ProjectRoot renders the project/branch directory inside the root.
func ProjectRoot(root, projectID, branch string) string {
	return workspace.ProjectRoot(root, projectID, branch)
}

// FilePath validates a caller-supplied relative path and resolves it to an
// absolute path inside the project root. Returns (absolute, slashRel, err).
func FilePath(root, projectID, branch, path string) (string, string, error) {
	if projectID == "" {
		return "", "", workspace.ErrMissingProjectID
	}
	if path == "" {
		return "", "", workspace.ErrMissingPath
	}
	if strings.IndexByte(path, 0) >= 0 {
		return "", "", errors.New("workspace path contains NUL")
	}
	if filepath.IsAbs(path) {
		return "", "", errors.New("workspace path must be relative")
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", "", errors.New("workspace path escapes project root")
	}
	rel := filepath.ToSlash(clean)
	return filepath.Join(ProjectRoot(root, projectID, branch), clean), rel, nil
}

// RunDir resolves the run directory, requiring a simple identifier.
func RunDir(root, projectID, branch, runID string) (string, error) {
	if runID == "" {
		return "", errors.New("run_id required")
	}
	if filepath.IsAbs(runID) || strings.Contains(runID, "/") || strings.Contains(runID, string(os.PathSeparator)) || strings.Contains(runID, "..") {
		return "", errors.New("run_id must be a simple identifier")
	}
	return filepath.Join(ProjectRoot(root, projectID, branch), "runs", SafeID(runID)), nil
}

// CommitsRoot renders the commit storage directory for a project/branch.
func CommitsRoot(root, projectID, branch string) string {
	return filepath.Join(root, ".kb", "commits", SafeID(projectID), SafeID(branch))
}

// CommitDir resolves a commit directory, requiring a simple identifier.
func CommitDir(root, projectID, branch, commitID string) (string, error) {
	if commitID == "" {
		return "", errors.New("commit_id required")
	}
	if filepath.IsAbs(commitID) || strings.Contains(commitID, "/") || strings.Contains(commitID, string(os.PathSeparator)) || strings.Contains(commitID, "..") {
		return "", errors.New("commit_id must be a simple identifier")
	}
	return filepath.Join(CommitsRoot(root, projectID, branch), SafeID(commitID)), nil
}

// BranchLockKey renders the normalized project/branch lock key.
func BranchLockKey(projectID, branch string) string {
	return SafeID(projectID) + "/" + SafeID(DefaultBranch(branch))
}

// OpenDirectory opens the os.Root confining the absolute path.
func OpenDirectory(root, absolute string, create bool) (*os.Root, error) {
	relative, err := filepath.Rel(root, absolute)
	if err != nil {
		return nil, err
	}
	return workspace.OpenRoot(root, filepath.ToSlash(relative), create)
}

// OpenFileParent opens the directory containing the file and returns the
// base name to operate on.
func OpenFileParent(root, absolute string, create bool) (*os.Root, string, error) {
	dir, err := OpenDirectory(root, filepath.Dir(absolute), create)
	return dir, filepath.Base(absolute), err
}

// ReadFile reads a workspace file through the confining root.
func ReadFile(root, absolute string) ([]byte, error) {
	dir, name, err := OpenFileParent(root, absolute, false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return workspace.ReadFile(dir, name)
}

// WriteFile writes a workspace file through the confining root.
func WriteFile(ctx context.Context, root, absolute string, data []byte) error {
	dir, name, err := OpenFileParent(root, absolute, true)
	if err != nil {
		return err
	}
	defer dir.Close()
	return workspace.WriteFile(ctx, dir, name, data, 0644)
}

// SaveManifest persists a manifest to the given absolute path.
func SaveManifest(ctx context.Context, root, absolute string, manifest *workspace.Manifest) error {
	dir, name, err := OpenFileParent(root, absolute, true)
	if err != nil {
		return err
	}
	defer dir.Close()
	return manifest.SaveRoot(ctx, dir, name)
}

// ReadManifest loads the manifest for a project/branch, trying the
// canonical path then the legacy location. The bool reports whether an
// existing manifest was found; a missing one yields a fresh empty manifest.
func ReadManifest(root, projectID, branch string) (*workspace.Manifest, bool, error) {
	if projectID == "" {
		return nil, false, workspace.ErrMissingProjectID
	}
	branch = DefaultBranch(branch)
	paths := []string{
		ManifestPath(root, projectID, branch),
		workspace.LegacyManifestPath(root, projectID, branch),
	}
	for i, path := range paths {
		dir, name, err := OpenFileParent(root, path, false)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		manifest, err := workspace.LoadManifestRoot(dir, name)
		dir.Close()
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		if manifest.ProjectID != "" &&
			workspace.SafeID(manifest.ProjectID) == workspace.SafeID(projectID) &&
			workspace.SafeID(DefaultBranch(manifest.Branch)) == workspace.SafeID(branch) {
			return manifest, true, nil
		}
		if i == 0 {
			return nil, false, errors.New("workspace manifest target mismatch")
		}
	}
	return workspace.NewManifest(projectID, branch), false, nil
}

// LoadManifest loads the manifest and reports its canonical path.
func LoadManifest(root, projectID, branch string) (*workspace.Manifest, string, error) {
	manifest, _, err := ReadManifest(root, projectID, branch)
	if err != nil {
		return nil, "", err
	}
	return manifest, ManifestPath(root, projectID, DefaultBranch(branch)), nil
}

// ListMarkdownPaths walks the absolute directory inside the confining
// root and returns the sorted slash-relative .md paths, rejecting
// nonregular files.
func ListMarkdownPaths(root, absolute string) ([]string, error) {
	dir, err := OpenDirectory(root, absolute, false)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	var paths []string
	err = fs.WalkDir(dir.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("workspace contains unsafe nonregular file")
		}
		if strings.EqualFold(filepath.Ext(name), ".md") {
			paths = append(paths, filepath.ToSlash(name))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

// DigestBytes renders the sha256 hex digest of data.
func DigestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// DigestText renders the sha256 hex digest of text.
func DigestText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// NewerTimeString returns the newer of two RFC3339 timestamp strings.
func NewerTimeString(current, candidate string) string {
	if candidate == "" {
		return current
	}
	if current == "" {
		return candidate
	}
	ct, cok := parseOpsTime(current)
	nt, nok := parseOpsTime(candidate)
	if !cok {
		return candidate
	}
	if !nok {
		return current
	}
	if nt.After(ct) {
		return candidate
	}
	return current
}

// OlderTimeString returns the older of two RFC3339 timestamp strings.
func OlderTimeString(current, candidate string) string {
	if candidate == "" {
		return current
	}
	if current == "" {
		return candidate
	}
	ct, cok := parseOpsTime(current)
	nt, nok := parseOpsTime(candidate)
	if !cok {
		return candidate
	}
	if !nok {
		return current
	}
	if nt.Before(ct) {
		return candidate
	}
	return current
}

// ParseOpsTime parses RFC3339(Nano) timestamps.
func ParseOpsTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err == nil {
		return t, true
	}
	t, err = time.Parse(time.RFC3339, s)
	return t, err == nil
}

func parseOpsTime(s string) (time.Time, bool) {
	return ParseOpsTime(s)
}
