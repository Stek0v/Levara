package mcp

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validManifestYAML = `allowed_tools:
  - search
  - recall_memory
allowed_paths:
  - tasks/t1/artifacts
allowed_networks:
  - api.example.com
`

// ── parsing & digest ──

func TestParseAuthorityManifest(t *testing.T) {
	m, err := ParseAuthorityManifest([]byte(validManifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.AllowedTools) != 2 || m.AllowedTools[0] != "search" {
		t.Fatalf("tools parsed wrong: %v", m.AllowedTools)
	}
	if len(m.AllowedPaths) != 1 || m.AllowedPaths[0] != "tasks/t1/artifacts" {
		t.Fatalf("paths parsed wrong: %v", m.AllowedPaths)
	}
}

func TestParseAuthorityManifestRejectsAbsoluteAndEscapePaths(t *testing.T) {
	for _, bad := range []string{
		"allowed_paths:\n  - /etc\n",
		"allowed_paths:\n  - ../secrets\n",
	} {
		if _, err := ParseAuthorityManifest([]byte(bad)); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

func TestManifestDigestPinning(t *testing.T) {
	m, err := ParseAuthorityManifest([]byte(validManifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.VerifyDigest([]byte(validManifestYAML), m.Digest([]byte(validManifestYAML))); err != nil {
		t.Fatal(err)
	}
	// Swapped content must fail digest verification.
	if err := m.VerifyDigest([]byte("allowed_tools: [admin]\n"), m.Digest([]byte(validManifestYAML))); err == nil {
		t.Fatal("expected digest mismatch error")
	}
	// Empty digest = unpinned = refuse.
	if err := m.VerifyDigest([]byte(validManifestYAML), ""); err == nil {
		t.Fatal("expected unpinned-digest error")
	}
}

// ── loading: symlink escape corner case ──

func setupManifestTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tasks", "t1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tasks", "t1", "authority.yaml"), []byte(validManifestYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLoadAuthorityManifestOK(t *testing.T) {
	root := setupManifestTree(t)
	m, err := LoadAuthorityManifest(root, "tasks/t1/authority.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.AllowedTools) == 0 {
		t.Fatal("empty manifest")
	}
}

func TestLoadAuthorityManifestRejectsSymlinkEscape(t *testing.T) {
	root := setupManifestTree(t)
	// Secrets outside the workspace.
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.yaml")
	if err := os.WriteFile(secret, []byte("allowed_tools: [admin]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Symlink the manifest itself (replace the regular file).
	if err := os.Remove(filepath.Join(root, "tasks", "t1", "authority.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "tasks", "t1", "authority.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAuthorityManifest(root, "tasks/t1/authority.yaml"); err == nil {
		t.Fatal("expected symlinked manifest to be rejected")
	}
	// Symlink an INTERMEDIATE directory component.
	root2 := setupManifestTree(t)
	if err := os.MkdirAll(filepath.Join(root2, "tasks", "t1", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root2, "tasks", "t1", "sub", "m.yaml"), []byte(validManifestYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root2, "tasks", "t1", "linkdir")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAuthorityManifest(root2, "tasks/t1/linkdir/m.yaml"); err == nil {
		t.Fatal("expected symlinked directory component to be rejected")
	}
}

func TestLoadAuthorityManifestRejectsEscapeAndMissing(t *testing.T) {
	root := setupManifestTree(t)
	if _, err := LoadAuthorityManifest(root, "../outside.yaml"); err == nil {
		t.Fatal("expected escape rejection")
	}
	if _, err := LoadAuthorityManifest(root, "tasks/t1/nope.yaml"); err == nil {
		t.Fatal("expected missing-manifest error")
	}
	if _, err := LoadAuthorityManifest(root, ""); err == nil {
		t.Fatal("expected empty-path rejection")
	}
}

// ── access checks: explicit failure, never silent ──

func TestCheckToolAccessExplicit(t *testing.T) {
	m, _ := ParseAuthorityManifest([]byte(validManifestYAML))
	if err := m.CheckToolAccess("search"); err != nil {
		t.Fatal(err)
	}
	err := m.CheckToolAccess("workspace_write")
	if err == nil || !errors.Is(err, ErrAuthorityNotDeclared) {
		t.Fatalf("want ErrAuthorityNotDeclared, got %v", err)
	}
	if !strings.Contains(err.Error(), "workspace_write") {
		t.Fatal("error must name the denied tool")
	}
}

func TestCheckNetworkAccessDenyByDefault(t *testing.T) {
	m, _ := ParseAuthorityManifest([]byte(validManifestYAML))
	if err := m.CheckNetworkAccess("api.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckNetworkAccess("evil.example.net"); err == nil {
		t.Fatal("network access must be deny-by-default")
	}
}

func TestVerifyTaskManifestBinding(t *testing.T) {
	root := setupManifestTree(t)
	raw, err := os.ReadFile(filepath.Join(root, "tasks", "t1", "authority.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m, _ := ParseAuthorityManifest(raw)
	digest := m.Digest(raw)

	// Bound + intact → OK.
	binding := `{"manifest": "tasks/t1/authority.yaml", "manifest_sha256": "` + digest + `"}`
	if _, err := VerifyTaskManifest(root, binding); err != nil {
		t.Fatal(err)
	}

	// Manifest swapped on disk after binding → loud failure.
	if err := os.WriteFile(filepath.Join(root, "tasks", "t1", "authority.yaml"), []byte("allowed_tools: [admin]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyTaskManifest(root, binding); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("want digest mismatch, got %v", err)
	}
}

func TestVerifyTaskManifestUnboundTask(t *testing.T) {
	// No manifest binding → no-op, no error.
	if _, err := VerifyTaskManifest(t.TempDir(), `{"auto_run": true}`); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyTaskManifest(t.TempDir(), `{`); err != nil {
		t.Fatal(err)
	}
}

// ── path containment ──

func TestCheckPathAccessContainment(t *testing.T) {
	root := setupManifestTree(t)
	m, _ := ParseAuthorityManifest([]byte(validManifestYAML))
	if err := m.CheckPathAccess(root, "tasks/t1/artifacts/out.md"); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckPathAccess(root, "tasks/t2/other.md"); err == nil {
		t.Fatal("path outside allowed_paths must fail")
	}
}

func TestCheckPathAccessSymlinkInsideAllowedRootEscapes(t *testing.T) {
	root := setupManifestTree(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "f.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	artDir := filepath.Join(root, "tasks", "t1", "artifacts")
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "f.md"), filepath.Join(artDir, "link.md")); err != nil {
		t.Fatal(err)
	}
	m, _ := ParseAuthorityManifest([]byte(validManifestYAML))
	err := m.CheckPathAccess(root, "tasks/t1/artifacts/link.md")
	if err == nil || !errors.Is(err, ErrAuthorityNotDeclared) {
		t.Fatalf("symlink resolving outside declared root must be denied, got %v", err)
	}
}
