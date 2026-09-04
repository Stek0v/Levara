package mcp

// authority.go — declarative authority manifests (backlog B4).
//
// A task may bind an authority manifest (a small YAML file inside the
// workspace) through its authority_json:
//
//	{"manifest": "tasks/my-task/authority.yaml", "manifest_sha256": "<hex>"}
//
// The manifest declares what the task may touch:
//
//	allowed_tools:  [search, recall_memory]     # MCP tools the executor may call
//	allowed_paths:  [tasks/my-task/artifacts]   # workspace-relative roots
//	allowed_networks: [api.example.com]         # reserved; default deny
//
// Rules enforced here:
//   - manifests must live INSIDE the workspace root (symlink escapes are
//     rejected, including intermediate components);
//   - manifest content is digest-pinned at bind time — a manifest swapped
//     during execution is refused;
//   - undeclared tool/path/network access returns an explicit error, never
//     silent degradation;
//   - authority does not cascade: every step of a task uses exactly the
//     task's own manifest. Handing a credential from one step to another
//     grants nothing unless the manifest itself declares it.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const authorityManifestMaxBytes = 64 << 10 // 64 KiB

// ErrAuthorityNotDeclared is returned when a step requests authority the
// manifest does not declare. The message names the missing grant.
var ErrAuthorityNotDeclared = errors.New("authority not declared")

// AuthorityManifest is the parsed form of a task authority manifest.
type AuthorityManifest struct {
	AllowedTools    []string `yaml:"allowed_tools"`
	AllowedPaths    []string `yaml:"allowed_paths"`
	AllowedNetworks []string `yaml:"allowed_networks"`
}

// ParseAuthorityManifest parses and sanity-checks raw YAML bytes.
func ParseAuthorityManifest(data []byte) (*AuthorityManifest, error) {
	if len(data) > authorityManifestMaxBytes {
		return nil, fmt.Errorf("authority manifest exceeds %d bytes", authorityManifestMaxBytes)
	}
	var m AuthorityManifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("authority manifest YAML: %w", err)
	}
	for i, p := range m.AllowedPaths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if path.IsAbs(p) || strings.HasPrefix(p, "..") || strings.Contains(p, "\x00") {
			return nil, fmt.Errorf("allowed_paths[%d]: must be workspace-relative, got %q", i, p)
		}
		m.AllowedPaths[i] = path.Clean(p)
	}
	return &m, nil
}

// LoadAuthorityManifest reads and parses the manifest at rel under root.
// Symlink escapes — the file itself or any path component pointing outside
// root — are rejected.
func LoadAuthorityManifest(root, rel string) (*AuthorityManifest, error) {
	if rel == "" {
		return nil, fmt.Errorf("authority manifest path is empty")
	}
	if path.IsAbs(rel) || strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("authority manifest path must be workspace-relative")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// The root itself may sit behind a symlink (macOS /tmp): resolve it once.
	absRoot, err = filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, err
	}
	full := filepath.Join(absRoot, filepath.FromSlash(path.Clean(rel)))
	// Reject symlinked components BEFORE resolving: walk from root.
	if err := ensureNoSymlinkEscape(absRoot, full); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("authority manifest %q not found", rel)
		}
		return nil, err
	}
	if !strings.HasPrefix(resolved, absRoot+string(os.PathSeparator)) {
		return nil, fmt.Errorf("authority manifest escapes workspace root")
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, err
	}
	return ParseAuthorityManifest(data)
}

// ensureNoSymlinkEscape walks each component from root to target and fails
// when any existing component is a symlink. Missing tail components are
// allowed (the caller decides whether that is an error).
func ensureNoSymlinkEscape(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("path escapes workspace root")
	}
	cur := root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink %q escapes declared roots", cur)
		}
	}
	return nil
}

// Digest returns the hex sha256 of the manifest file contents — pin this in
// authority_json at bind time.
func (m *AuthorityManifest) Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// VerifyDigest fails when the loaded manifest no longer matches the digest
// recorded at bind time — i.e. the manifest was swapped mid-execution.
func (m *AuthorityManifest) VerifyDigest(data []byte, expected string) error {
	if expected == "" {
		return fmt.Errorf("manifest digest not pinned; refusing to honor %q unverified", "manifest")
	}
	got := m.Digest(data)
	if got != strings.ToLower(expected) {
		return fmt.Errorf("manifest digest mismatch: bound %s, on disk %s (manifest swapped during execution?)", expected, got)
	}
	return nil
}

// CheckToolAccess fails when tool is not in allowed_tools.
func (m *AuthorityManifest) CheckToolAccess(tool string) error {
	for _, t := range m.AllowedTools {
		if t == tool {
			return nil
		}
	}
	return fmt.Errorf("%w: tool %q not in allowed_tools", ErrAuthorityNotDeclared, tool)
}

// CheckPathAccess fails when p (workspace-relative) is not contained in any
// allowed_paths root. Symlink escapes are checked against the same roots.
func (m *AuthorityManifest) CheckPathAccess(root, p string) error {
	if len(m.AllowedPaths) == 0 {
		return fmt.Errorf("%w: no allowed_paths declared", ErrAuthorityNotDeclared)
	}
	clean := path.Clean(strings.TrimSpace(p))
	if path.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return fmt.Errorf("%w: path %q outside workspace-relative roots", ErrAuthorityNotDeclared, p)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	full := filepath.Join(absRoot, filepath.FromSlash(clean))
	if err := ensureNoSymlinkEscape(absRoot, full); err != nil {
		return fmt.Errorf("%w: %v", ErrAuthorityNotDeclared, err)
	}
	inside := false
	for _, allowed := range m.AllowedPaths {
		if clean == allowed || strings.HasPrefix(clean, allowed+"/") {
			inside = true
			break
		}
	}
	if !inside {
		return fmt.Errorf("%w: path %q outside allowed_paths", ErrAuthorityNotDeclared, p)
	}
	// Even textually-inside paths must not resolve outside the declared root
	// through a symlink (checked after the textual containment test).
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err == nil {
		for _, allowed := range m.AllowedPaths {
			allowedAbs, aerr := filepath.EvalSymlinks(filepath.Join(absRoot, filepath.FromSlash(allowed)))
			if aerr != nil {
				continue
			}
			if resolved == allowedAbs || strings.HasPrefix(resolved, allowedAbs+string(os.PathSeparator)) {
				return nil
			}
		}
		// Resolved target exists but escapes every declared root.
		if !strings.HasPrefix(resolved, absRoot+string(os.PathSeparator)) {
			return fmt.Errorf("%w: %q resolves outside workspace root", ErrAuthorityNotDeclared, p)
		}
		return fmt.Errorf("%w: %q resolves outside declared roots", ErrAuthorityNotDeclared, p)
	}
	return nil
}

// CheckNetworkAccess is reserved: network authority is deny-by-default. A
// host is granted only when explicitly listed in allowed_networks.
func (m *AuthorityManifest) CheckNetworkAccess(host string) error {
	for _, h := range m.AllowedNetworks {
		if h == host {
			return nil
		}
	}
	return fmt.Errorf("%w: network host %q not in allowed_networks", ErrAuthorityNotDeclared, host)
}

// VerifyTaskManifest loads the manifest bound in authorityJson (fields
// "manifest" + "manifest_sha256") against workspaceRoot and verifies the
// digest. Called at step claim so a manifest swapped mid-execution fails
// loudly instead of silently changing what the task may touch. Tasks without
// a bound manifest return (nil, nil) — no authority grants apply.
func VerifyTaskManifest(workspaceRoot, authorityJson string) (*AuthorityManifest, error) {
	var binding struct {
		Manifest    string `json:"manifest"`
		ManifestSHA string `json:"manifest_sha256"`
	}
	if err := json.Unmarshal([]byte(authorityJson), &binding); err != nil {
		return nil, nil // unparseable authority_json: no manifest binding
	}
	if binding.Manifest == "" {
		return nil, nil
	}
	data, err := readManifestRaw(workspaceRoot, binding.Manifest)
	if err != nil {
		return nil, err
	}
	m, err := ParseAuthorityManifest(data)
	if err != nil {
		return nil, err
	}
	if err := m.VerifyDigest(data, binding.ManifestSHA); err != nil {
		return nil, err
	}
	return m, nil
}

// readManifestRaw reads manifest bytes with symlink-escape protection.
func readManifestRaw(root, rel string) ([]byte, error) {
	if path.IsAbs(rel) || strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("authority manifest path must be workspace-relative")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if absRoot, err = filepath.EvalSymlinks(absRoot); err != nil {
		return nil, err
	}
	full := filepath.Join(absRoot, filepath.FromSlash(path.Clean(rel)))
	if err := ensureNoSymlinkEscape(absRoot, full); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return nil, fmt.Errorf("authority manifest %q: %w", rel, err)
	}
	if !strings.HasPrefix(resolved, absRoot+string(os.PathSeparator)) {
		return nil, fmt.Errorf("authority manifest escapes workspace root")
	}
	return os.ReadFile(resolved)
}
