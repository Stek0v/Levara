// conflicts.go — workspace filesystem-vs-active-generation conflict report.
//
// Extracted verbatim from internal/http/workspace_conflicts.go (Phase-2
// workspace campaign, leaf 2; code-modernization brief §3). The core
// comparison logic is pure: it receives the filesystem service handles
// and job/watcher facts through a small Deps struct; internal/http keeps
// the fiber handler, the MCP tool method and the ACL guards, and calls
// Core(...) through a package-local delegate with the original signature.
package workspaceconflicts

import (
	"context"
	"path/filepath"
	"sort"

	"github.com/stek0v/levara/internal/api/workspacesvc"
	"github.com/stek0v/levara/pkg/workspace"
)

// Request is the conflicts endpoint input.
type Request struct {
	ProjectID string `json:"project_id"`
	Branch    string `json:"branch,omitempty"`
}

// PathState describes one conflicting path.
type PathState struct {
	Path          string `json:"path"`
	State         string `json:"state"`
	FileDigest    string `json:"file_digest,omitempty"`
	IndexedDigest string `json:"indexed_digest,omitempty"`
	IndexedAt     string `json:"indexed_at,omitempty"`
	Detail        string `json:"detail,omitempty"`
}

// WatcherBranch is the per-branch watcher subset mirrored into the
// conflicts report; internal/http fills it from WorkspaceBranchWatchStatus.
type WatcherBranch struct {
	Pending   bool   `json:"pending"`
	LastError string `json:"last_error,omitempty"`
}

// Response is the conflicts report.
type Response struct {
	ProjectID           string              `json:"project_id"`
	Branch              string              `json:"branch"`
	ActiveGeneration    string              `json:"active_generation,omitempty"`
	ManifestPath        string              `json:"manifest_path,omitempty"`
	HasConflicts        bool                `json:"has_conflicts"`
	Policy              string              `json:"policy"`
	DirtyPaths          []PathState         `json:"dirty_paths,omitempty"`
	UnindexedPaths      []PathState         `json:"unindexed_paths,omitempty"`
	MissingIndexedPaths []PathState         `json:"missing_indexed_paths,omitempty"`
	Watcher             WatcherBranch       `json:"watcher"`
	WatcherPending      bool                `json:"watcher_pending"`
	WatcherLastError    string              `json:"watcher_last_error,omitempty"`
	JobsByStatus        map[string]int      `json:"jobs_by_status,omitempty"`
	RecommendedActions  []string            `json:"recommended_actions,omitempty"`
}

// Deps carries the job-store and status-enumeration facts the core needs;
// the filesystem plane comes from workspacesvc rooted at Root.
type Deps struct {
	Root              string
	ListIndexJobs     func(ctx context.Context, projectID, branch string) (map[string]int, error)
	JobFailedStatus   string
	JobDeadLetter     string
	WatcherPending    bool
	WatcherLastError  string
}

// Core computes the conflict report: filesystem truth (walked through the
// confining root) against the active generation's indexed digests.
func Core(ctx context.Context, d Deps, req Request) (Response, error) {
	branch := workspacesvc.DefaultBranch(req.Branch)
	if req.ProjectID == "" {
		return Response{}, workspace.ErrMissingProjectID
	}
	manifest, manifestPath, err := workspacesvc.LoadManifest(d.Root, req.ProjectID, branch)
	if err != nil {
		return Response{}, err
	}
	resp := Response{
		ProjectID:        req.ProjectID,
		Branch:           branch,
		ActiveGeneration: manifest.ActiveGeneration,
		ManifestPath:     manifestPath,
		Policy:           "filesystem_truth_wins; writes are last-writer-wins until workspace_commit; reconcile publishes a new active generation; revert should be followed by reconcile or reindex=true",
		WatcherPending:   d.WatcherPending,
		WatcherLastError: d.WatcherLastError,
		Watcher: WatcherBranch{
			Pending:   d.WatcherPending,
			LastError: d.WatcherLastError,
		},
	}
	if d.ListIndexJobs != nil {
		if jobs, err := d.ListIndexJobs(ctx, req.ProjectID, branch); err == nil && len(jobs) > 0 {
			resp.JobsByStatus = jobs
		}
	}
	if manifest.ActiveGeneration == "" {
		resp.HasConflicts = true
		resp.RecommendedActions = []string{"Run workspace_reconcile with activate_generation=true."}
		return resp, nil
	}

	root := workspacesvc.ProjectRoot(d.Root, req.ProjectID, branch)
	paths, err := workspacesvc.ListMarkdownPaths(d.Root, root)
	if err != nil {
		return Response{}, err
	}
	activeChunks := manifest.ListChunks(workspace.ChunkFilter{
		ProjectID:  req.ProjectID,
		Branch:     branch,
		Generation: manifest.ActiveGeneration,
	})
	indexed := indexedPathDigests(activeChunks)
	seen := map[string]struct{}{}
	for _, rel := range paths {
		seen[rel] = struct{}{}
		filePath := filepath.Join(root, filepath.FromSlash(rel))
		data, err := workspacesvc.ReadFile(d.Root, filePath)
		if err != nil {
			return Response{}, err
		}
		digest := workspacesvc.DigestBytes(data)
		digests := indexed[rel]
		switch {
		case len(digests) == 0:
			resp.UnindexedPaths = append(resp.UnindexedPaths, PathState{
				Path:       rel,
				State:      "unindexed",
				FileDigest: digest,
				Detail:     "Markdown file is present in filesystem truth but absent from active generation.",
			})
		case !digests[digest]:
			resp.DirtyPaths = append(resp.DirtyPaths, PathState{
				Path:          rel,
				State:         "dirty",
				FileDigest:    digest,
				IndexedDigest: firstDigest(digests),
				IndexedAt:     indexedPathUpdatedAt(activeChunks, rel),
				Detail:        "Markdown file digest differs from active generation digest.",
			})
		}
	}
	for rel, digests := range indexed {
		if _, ok := seen[rel]; ok {
			continue
		}
		resp.MissingIndexedPaths = append(resp.MissingIndexedPaths, PathState{
			Path:          rel,
			State:         "missing_from_filesystem",
			IndexedDigest: firstDigest(digests),
			IndexedAt:     indexedPathUpdatedAt(activeChunks, rel),
			Detail:        "Active generation references a path that no longer exists in filesystem truth.",
		})
	}
	sortPathStates(resp.DirtyPaths)
	sortPathStates(resp.UnindexedPaths)
	sortPathStates(resp.MissingIndexedPaths)
	resp.HasConflicts = len(resp.DirtyPaths) > 0 ||
		len(resp.UnindexedPaths) > 0 ||
		len(resp.MissingIndexedPaths) > 0 ||
		resp.WatcherPending ||
		resp.WatcherLastError != "" ||
		resp.JobsByStatus[d.JobFailedStatus] > 0 ||
		resp.JobsByStatus[d.JobDeadLetter] > 0
	resp.RecommendedActions = recommendedActions(d, resp)
	return resp, nil
}

func indexedPathDigests(chunks []workspace.ChunkRecord) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, ch := range chunks {
		if ch.Path == "" || ch.FileDigest == "" {
			continue
		}
		if out[ch.Path] == nil {
			out[ch.Path] = map[string]bool{}
		}
		out[ch.Path][ch.FileDigest] = true
	}
	return out
}

func indexedPathUpdatedAt(chunks []workspace.ChunkRecord, rel string) string {
	latest := ""
	for _, ch := range chunks {
		if ch.Path != rel {
			continue
		}
		latest = workspacesvc.NewerTimeString(latest, ch.UpdatedAt)
	}
	return latest
}

func firstDigest(digests map[string]bool) string {
	xs := make([]string, 0, len(digests))
	for digest := range digests {
		xs = append(xs, digest)
	}
	sort.Strings(xs)
	if len(xs) == 0 {
		return ""
	}
	return xs[0]
}

func sortPathStates(paths []PathState) {
	sort.Slice(paths, func(i, j int) bool { return paths[i].Path < paths[j].Path })
}

func recommendedActions(d Deps, resp Response) []string {
	var actions []string
	if len(resp.DirtyPaths) > 0 || len(resp.UnindexedPaths) > 0 || len(resp.MissingIndexedPaths) > 0 {
		actions = append(actions, "Run workspace_reconcile with a fresh generation and activate_generation=true.")
	}
	if resp.WatcherPending {
		actions = append(actions, "Wait for watcher debounce/worker drain or manually reconcile before answering from search.")
	}
	if resp.WatcherLastError != "" {
		actions = append(actions, "Inspect watcher error state, clear the filesystem/index inconsistency, then reconcile before relying on active generation answers.")
	}
	if resp.JobsByStatus[d.jobFailedLabel()] > 0 {
		actions = append(actions, "Inspect workspace_index_jobs failed entries and rerun reconcile after fixing the underlying indexing error.")
	}
	if resp.JobsByStatus[d.jobDeadLetterLabel()] > 0 {
		actions = append(actions, "Inspect workspace_index_jobs dead_letter entries, fix the root cause, then retry or enqueue a fresh generation.")
	}
	if len(actions) == 0 {
		actions = append(actions, "No action required; active generation matches current Markdown files.")
	}
	return actions
}

func (d Deps) jobFailedLabel() string {
	if d.JobFailedStatus != "" {
		return d.JobFailedStatus
	}
	return "failed"
}

func (d Deps) jobDeadLetterLabel() string {
	if d.JobDeadLetter != "" {
		return d.JobDeadLetter
	}
	return "dead_letter"
}
