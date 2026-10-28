// artifacts.go — workspace context-artifact resolution.
//
// Extracted verbatim from internal/http/workspace_artifacts.go (Phase-2
// workspace campaign, leaf 6; code-modernization brief §3). The registry
// load, include expansion, materialization, filtering, dedupe and kind
// normalization form a pure layer over the workspacesvc filesystem
// service; the file-content reader is injected (ReadFile callback) so the
// package holds no cfg. The HTTP/MCP handlers, the reindex write path and
// the metrics stay in internal/http.
package workspaceartifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/stek0v/levara/internal/api/workspacesvc"
	"github.com/stek0v/levara/pkg/workspace"
)

// Kind enumerates the known artifact categories.
type Kind string

const (
	KindOpenAPI   Kind = "openapi"
	KindDDL       Kind = "ddl"
	KindTerraform Kind = "terraform"
	KindADR       Kind = "adr"
	KindRunbook   Kind = "runbook"
	KindMarkdown  Kind = "markdown"
	KindOther     Kind = "other"
)

// RegistryVersion is the default registry schema version.
const RegistryVersion = 1

type Include struct {
	ProjectID        string   `json:"project_id"`
	Branch           string   `json:"branch,omitempty"`
	Glob             string   `json:"glob"`
	Kind             string   `json:"kind,omitempty"`
	Room             string   `json:"room,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	Index            *bool    `json:"index,omitempty"`
	IncludeInContext *bool    `json:"include_in_context,omitempty"`
}

type Request struct {
	ID               string   `json:"id,omitempty"`
	ProjectID        string   `json:"project_id"`
	Branch           string   `json:"branch,omitempty"`
	Path             string   `json:"path"`
	Kind             string   `json:"kind,omitempty"`
	Title            string   `json:"title,omitempty"`
	Room             string   `json:"room,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	Index            *bool    `json:"index,omitempty"`
	IncludeInContext *bool    `json:"include_in_context,omitempty"`
}

type Registry struct {
	Version   int        `json:"version"`
	Includes  []Include  `json:"includes,omitempty"`
	Artifacts []Request  `json:"artifacts,omitempty"`
}

type Artifact struct {
	ID               string   `json:"id"`
	ProjectID        string   `json:"project_id"`
	Branch           string   `json:"branch"`
	Path             string   `json:"path"`
	Kind             string   `json:"kind"`
	Title            string   `json:"title,omitempty"`
	Room             string   `json:"room,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	Index            bool     `json:"index"`
	IncludeInContext bool     `json:"include_in_context"`
	Exists           bool     `json:"exists"`
	Bytes            int64    `json:"bytes,omitempty"`
	Digest           string   `json:"digest,omitempty"`
	Source           string   `json:"source"`
}

type ListRequest struct {
	ProjectID    string   `json:"project_id,omitempty"`
	Branch       string   `json:"branch,omitempty"`
	Kind         string   `json:"kind,omitempty"`
	IDs          []string `json:"ids,omitempty"`
	IndexOnly    bool     `json:"index_only,omitempty"`
	MetadataOnly bool     `json:"-"`
}

type ListResponse struct {
	Version   int        `json:"version"`
	Path      string     `json:"path"`
	Artifacts []Artifact `json:"artifacts"`
	Total     int        `json:"total"`
}

// Deps carries the filesystem plane: the workspace root and the
// confined artifact-content reader (backed by workspacesvc in http).
type Deps struct {
	Root     string
	ReadFile func(projectID, branch, relPath string) ([]byte, error)
}

// List loads the registry, resolves every include/explicit entry and
// returns the deduped artifact list.
func List(d Deps, req ListRequest) (ListResponse, error) {
	registry, regPath, err := LoadRegistry(d.Root)
	if err != nil {
		return ListResponse{}, err
	}
	artifacts, err := Resolve(d, registry, req)
	if err != nil {
		return ListResponse{}, err
	}
	return ListResponse{
		Version:   registry.Version,
		Path:      regPath,
		Artifacts: artifacts,
		Total:     len(artifacts),
	}, nil
}

// LoadRegistry reads .kb/context-artifacts.json, tolerating absence.
func LoadRegistry(root string) (Registry, string, error) {
	regPath := filepath.Join(root, ".kb", "context-artifacts.json")
	dir, err := workspace.OpenRoot(root, ".kb", false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Registry{Version: RegistryVersion}, regPath, nil
		}
		return Registry{}, regPath, err
	}
	defer dir.Close()
	data, err := workspace.ReadFile(dir, "context-artifacts.json")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Registry{Version: RegistryVersion}, regPath, nil
		}
		return Registry{}, regPath, err
	}
	var registry Registry
	if err := json.Unmarshal(data, &registry); err != nil {
		return Registry{}, regPath, err
	}
	if registry.Version == 0 {
		registry.Version = RegistryVersion
	}
	return registry, regPath, nil
}

// Resolve expands registry includes and explicit artifacts.
func Resolve(d Deps, registry Registry, req ListRequest) ([]Artifact, error) {
	var artifacts []Artifact
	for _, include := range registry.Includes {
		if req.ProjectID != "" && include.ProjectID != req.ProjectID {
			continue
		}
		branch := workspacesvc.DefaultBranch(include.Branch)
		if req.Branch != "" && branch != workspacesvc.DefaultBranch(req.Branch) {
			continue
		}
		matches, err := Expand(d, include, req.MetadataOnly)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, matches...)
	}
	for _, raw := range registry.Artifacts {
		if req.ProjectID != "" && raw.ProjectID != req.ProjectID {
			continue
		}
		branch := workspacesvc.DefaultBranch(raw.Branch)
		if req.Branch != "" && branch != workspacesvc.DefaultBranch(req.Branch) {
			continue
		}
		artifact, err := Materialize(d, raw, "explicit", req.MetadataOnly)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	artifacts = Filter(artifacts, req)
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].ProjectID == artifacts[j].ProjectID {
			if artifacts[i].Branch == artifacts[j].Branch {
				return artifacts[i].Path < artifacts[j].Path
			}
			return artifacts[i].Branch < artifacts[j].Branch
		}
		return artifacts[i].ProjectID < artifacts[j].ProjectID
	})
	return Dedupe(artifacts), nil
}

// Expand walks the include's project root and materializes every glob
// match, rejecting symlinks.
func Expand(d Deps, include Include, metadataOnly bool) ([]Artifact, error) {
	if include.ProjectID == "" {
		return nil, errors.New("context artifact include project_id required")
	}
	if include.Glob == "" {
		return nil, errors.New("context artifact include glob required")
	}
	branch := workspacesvc.DefaultBranch(include.Branch)
	relative, err := filepath.Rel(d.Root, workspacesvc.ProjectRoot(d.Root, include.ProjectID, branch))
	if err != nil {
		return nil, err
	}
	dir, err := workspace.OpenRoot(d.Root, relative, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Artifact{}, nil
		}
		return nil, err
	}
	defer dir.Close()
	var artifacts []Artifact
	err = fs.WalkDir(dir.FS(), ".", func(rel string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("workspace artifact symlink rejected")
		}
		rel = filepath.ToSlash(rel)
		if !GlobMatch(include.Glob, rel) {
			return nil
		}
		index := true
		if include.Index != nil {
			index = *include.Index
		}
		includeInContext := true
		if include.IncludeInContext != nil {
			includeInContext = *include.IncludeInContext
		}
		artifact, err := Materialize(d, Request{
			ProjectID:        include.ProjectID,
			Branch:           branch,
			Path:             rel,
			Kind:             include.Kind,
			Room:             include.Room,
			Tags:             include.Tags,
			Index:            &index,
			IncludeInContext: &includeInContext,
		}, "include:"+include.Glob, metadataOnly)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, artifact)
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Artifact{}, nil
		}
		return nil, err
	}
	return artifacts, nil
}

// Materialize builds one artifact, reading content unless metadataOnly.
func Materialize(d Deps, raw Request, source string, metadataOnly bool) (Artifact, error) {
	if raw.ProjectID == "" {
		return Artifact{}, workspace.ErrMissingProjectID
	}
	branch := workspacesvc.DefaultBranch(raw.Branch)
	_, relPath, err := workspacesvc.FilePath(d.Root, raw.ProjectID, branch, raw.Path)
	if err != nil {
		return Artifact{}, err
	}
	var exists bool
	var digest string
	var size int64
	if !metadataOnly {
		data, readErr := d.ReadFile(raw.ProjectID, branch, relPath)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return Artifact{}, readErr
		}
		exists = readErr == nil
		if exists {
			size = int64(len(data))
			digest = workspacesvc.DigestBytes(data)
		}
	}
	kind := NormalizeKind(raw.Kind, relPath)
	index := true
	if raw.Index != nil {
		index = *raw.Index
	}
	includeInContext := true
	if raw.IncludeInContext != nil {
		includeInContext = *raw.IncludeInContext
	}
	id := raw.ID
	if id == "" {
		id = StableID(raw.ProjectID, branch, relPath)
	}
	tags := UniqueStrings(raw.Tags)
	return Artifact{
		ID:               id,
		ProjectID:        raw.ProjectID,
		Branch:           branch,
		Path:             relPath,
		Kind:             kind,
		Title:            raw.Title,
		Room:             raw.Room,
		Tags:             tags,
		Index:            index,
		IncludeInContext: includeInContext,
		Exists:           exists,
		Bytes:            size,
		Digest:           digest,
		Source:           source,
	}, nil
}

// Filter applies kind/id/index-only constraints.
func Filter(artifacts []Artifact, req ListRequest) []Artifact {
	idSet := stringSet(req.IDs)
	var out []Artifact
	for _, artifact := range artifacts {
		if req.Kind != "" && artifact.Kind != NormalizeKind(req.Kind, artifact.Path) {
			continue
		}
		if len(idSet) > 0 {
			if _, ok := idSet[artifact.ID]; !ok {
				continue
			}
		}
		if req.IndexOnly && !artifact.Index {
			continue
		}
		out = append(out, artifact)
	}
	return out
}

// Dedupe removes same project/branch/path repeats.
func Dedupe(artifacts []Artifact) []Artifact {
	seen := map[string]struct{}{}
	var out []Artifact
	for _, artifact := range artifacts {
		key := artifact.ProjectID + "\x00" + artifact.Branch + "\x00" + artifact.Path
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, artifact)
	}
	return out
}

// GlobMatch matches a slash-normalized glob with ** support.
func GlobMatch(pattern, rel string) bool {
	pattern = filepath.ToSlash(filepath.Clean(pattern))
	rel = filepath.ToSlash(filepath.Clean(rel))
	if pattern == "." || pattern == "" {
		return false
	}
	if ok, _ := path.Match(pattern, rel); ok {
		return true
	}
	re := regexp.QuoteMeta(pattern)
	re = strings.ReplaceAll(re, `\*\*/`, `(?:.*/)?`)
	re = strings.ReplaceAll(re, `\*\*`, `.*`)
	re = strings.ReplaceAll(re, `\*`, `[^/]*`)
	re = strings.ReplaceAll(re, `\?`, `[^/]`)
	ok, _ := regexp.MatchString("^"+re+"$", rel)
	return ok
}

// NormalizeKind resolves the declared kind or infers it from the path.
func NormalizeKind(kind, artifactPath string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case string(KindOpenAPI):
		return string(KindOpenAPI)
	case "sql", "schema", string(KindDDL):
		return string(KindDDL)
	case "tf", "hcl", string(KindTerraform):
		return string(KindTerraform)
	case string(KindADR):
		return string(KindADR)
	case string(KindRunbook):
		return string(KindRunbook)
	case "md", string(KindMarkdown):
		return string(KindMarkdown)
	case "", string(KindOther):
	default:
		return strings.ToLower(strings.TrimSpace(kind))
	}
	ext := strings.ToLower(filepath.Ext(artifactPath))
	base := strings.ToLower(filepath.Base(artifactPath))
	switch {
	case strings.Contains(base, "openapi") || strings.Contains(base, "swagger"):
		return string(KindOpenAPI)
	case ext == ".sql":
		return string(KindDDL)
	case ext == ".tf" || ext == ".hcl":
		return string(KindTerraform)
	case strings.Contains(strings.ToLower(artifactPath), "/adr/"):
		return string(KindADR)
	case strings.Contains(strings.ToLower(artifactPath), "/runbook"):
		return string(KindRunbook)
	case ext == ".md" || ext == ".mdx":
		return string(KindMarkdown)
	default:
		return string(KindOther)
	}
}

// StableID derives the deterministic artifact identity.
func StableID(projectID, branch, artifactPath string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00" + branch + "\x00" + artifactPath))
	return "artifact_" + hex.EncodeToString(sum[:])[:20]
}

func stringSet(xs []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x != "" {
			out[x] = struct{}{}
		}
	}
	return out
}

// UniqueStrings trims, drops empties, dedupes and sorts.
func UniqueStrings(xs []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x == "" {
			continue
		}
		if _, ok := seen[x]; ok {
			continue
		}
		seen[x] = struct{}{}
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}
