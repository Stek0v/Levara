package workspace

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func DefaultBranch(branch string) string {
	if branch == "" {
		return "main"
	}
	return branch
}

func SafeID(s string) string {
	if s == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "default"
	}
	return out
}

func ProjectRoot(root, projectID, branch string) string {
	return filepath.Join(root, "projects", SafeID(projectID), SafeID(branch))
}

func ManifestPath(root, projectID, branch string) string {
	return filepath.Join(root, ".kb", "manifests", SafeID(projectID)+"__"+SafeID(branch)+".json")
}

func ListLocalProjects(root string) []string {
	entries, err := os.ReadDir(filepath.Join(root, "projects"))
	if err != nil {
		return []string{}
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() {
			ids = append(ids, entry.Name())
		}
	}
	sort.Strings(ids)
	return ids
}

func ListLocalBranches(root, projectID string) []string {
	entries, err := os.ReadDir(filepath.Dir(ProjectRoot(root, projectID, "")))
	if err != nil {
		return []string{}
	}
	var branches []string
	for _, entry := range entries {
		if entry.IsDir() {
			branches = append(branches, entry.Name())
		}
	}
	sort.Strings(branches)
	return branches
}
