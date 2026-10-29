// history.go — workspace commit staging, restore publication and crash
// recovery.
//
// Extracted verbatim from internal/http/workspace_history_files.go
// (Phase-2 workspace campaign continuation; code-modernization brief §3).
// Root-parameterized like workspacesvc: no APIConfig, no HTTP. The
// stage→live→backup two-rename publication protocol and its journal
// recovery are preserved bit-for-bit; internal/http keeps cfg-based
// delegates and the type aliases.
package workspacehistory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/stek0v/levara/internal/api/workspacesvc"
	"github.com/stek0v/levara/pkg/workspace"
)

// CommitFile is one snapshotted file entry.
type CommitFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// CommitRecord is the commit.json payload of a workspace commit.
type CommitRecord struct {
	ProjectID string      `json:"project_id"`
	Branch    string      `json:"branch"`
	CommitID  string      `json:"commit_id"`
	Message   string      `json:"message,omitempty"`
	Author    string      `json:"author,omitempty"`
	CreatedAt string      `json:"created_at"`
	Path      string      `json:"path,omitempty"`
	Files     []CommitFile `json:"files"`
}

// RestoreJournal is the crash-recovery record of an in-flight restore.
type RestoreJournal struct {
	ProjectID string `json:"project_id"`
	Branch    string `json:"branch"`
	Stage     string `json:"stage"`
	Live      string `json:"live"`
	Backup    string `json:"backup"`
	HadLive   bool   `json:"had_live"`
}

// PrepareCommit stages the project's live tree into a staging directory
// under the commits root and atomically renames it into its final
// commit-id directory. Must run under the caller's project lock.
func PrepareCommit(ctx context.Context, root string, req CommitRequestInput, commitID string) (CommitRecord, error) {
	if err := ctx.Err(); err != nil {
		return CommitRecord{}, err
	}
	branch := workspacesvc.DefaultBranch(req.Branch)
	if req.ProjectID == "" {
		return CommitRecord{}, workspace.ErrMissingProjectID
	}
	final, err := workspacesvc.CommitDir(root, req.ProjectID, branch, commitID)
	if err != nil {
		return CommitRecord{}, err
	}
	parent, err := workspacesvc.OpenDirectory(root, filepath.Dir(final), true)
	if err != nil {
		return CommitRecord{}, err
	}
	defer parent.Close()
	stage := ".commit-" + uuid.NewString()
	if err := parent.Mkdir(stage, 0700); err != nil {
		return CommitRecord{}, err
	}
	defer parent.RemoveAll(stage)
	staged, err := workspace.OpenDir(parent, stage, false)
	if err != nil {
		return CommitRecord{}, err
	}
	defer staged.Close()
	files, err := workspace.OpenDir(staged, "files", true)
	if err != nil {
		return CommitRecord{}, err
	}
	defer files.Close()
	record := CommitRecord{ProjectID: req.ProjectID, Branch: branch, CommitID: commitID, Message: req.Message, Author: req.Author,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Path: final, Files: []CommitFile{}}
	source, err := workspacesvc.OpenDirectory(root, workspacesvc.ProjectRoot(root, req.ProjectID, branch), false)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return CommitRecord{}, err
	}
	if err == nil {
		defer source.Close()
		err = fs.WalkDir(source.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("workspace snapshot symlink rejected")
			}
			if info.IsDir() {
				return nil
			}
			if !info.Mode().IsRegular() {
				return errors.New("workspace snapshot file must be regular")
			}
			data, err := workspace.ReadFile(source, name)
			if err != nil {
				return err
			}
			if err := workspace.WriteFile(ctx, files, name, data, 0644); err != nil {
				return err
			}
			record.Files = append(record.Files, CommitFile{Path: name, Digest: workspacesvc.DigestBytes(data), Size: int64(len(data))})
			return nil
		})
		if err != nil {
			return CommitRecord{}, err
		}
	}
	sort.Slice(record.Files, func(i, j int) bool { return record.Files[i].Path < record.Files[j].Path })
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return CommitRecord{}, err
	}
	if err := workspace.WriteFile(ctx, staged, "commit.json", append(raw, '\n'), 0644); err != nil {
		return CommitRecord{}, err
	}
	if err := files.Close(); err != nil {
		return CommitRecord{}, err
	}
	if err := staged.Close(); err != nil {
		return CommitRecord{}, err
	}
	if _, err := parent.Lstat(filepath.Base(final)); err == nil {
		return CommitRecord{}, errors.New("workspace commit already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return CommitRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return CommitRecord{}, err
	}
	if err := parent.Rename(stage, filepath.Base(final)); err != nil {
		return CommitRecord{}, err
	}
	return record, nil
}

// CommitRequestInput is the caller-supplied subset of the commit request
// the staging step needs (kept narrow: no HTTP types).
type CommitRequestInput struct {
	ProjectID string
	Branch    string
	Message   string
	Author    string
}

// LoadCommitRecordConfined reads commit.json from a confined directory.
func LoadCommitRecordConfined(root, dir string) (CommitRecord, error) {
	dirRoot, err := workspacesvc.OpenDirectory(root, dir, false)
	if err != nil {
		return CommitRecord{}, err
	}
	defer dirRoot.Close()
	data, err := workspace.ReadFile(dirRoot, "commit.json")
	if err != nil {
		return CommitRecord{}, err
	}
	var record CommitRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return CommitRecord{}, err
	}
	record.Path = dir
	if record.Files == nil {
		record.Files = []CommitFile{}
	}
	return record, nil
}

// LogResponse is the history listing.
type LogResponse struct {
	ProjectID string        `json:"project_id"`
	Branch    string        `json:"branch"`
	Commits   []CommitRecord `json:"commits"`
}

// ListCommitRecordsConfined lists and validates a branch's commit
// records, newest first.
func ListCommitRecordsConfined(root, projectID, branch string) (LogResponse, error) {
	branch = workspacesvc.DefaultBranch(branch)
	out := LogResponse{ProjectID: projectID, Branch: branch, Commits: []CommitRecord{}}
	if projectID == "" {
		return out, workspace.ErrMissingProjectID
	}
	dir := workspacesvc.CommitsRoot(root, projectID, branch)
	dirRoot, err := workspacesvc.OpenDirectory(root, dir, false)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	defer dirRoot.Close()
	file, err := dirRoot.Open(".")
	if err != nil {
		return out, err
	}
	entries, readErr := file.ReadDir(-1)
	closeErr := file.Close()
	if readErr != nil {
		return out, readErr
	}
	if closeErr != nil {
		return out, closeErr
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return out, errors.New("workspace history symlink rejected")
		}
		if !entry.IsDir() {
			continue
		}
		record, err := LoadCommitRecordConfined(root, filepath.Join(dir, entry.Name()))
		if err != nil {
			return out, err
		}
		expected, targetErr := workspacesvc.CommitDir(root, projectID, branch, record.CommitID)
		if targetErr != nil || filepath.Clean(expected) != filepath.Clean(filepath.Join(dir, entry.Name())) || record.ProjectID != projectID || workspacesvc.DefaultBranch(record.Branch) != branch {
			return out, errors.New("workspace commit target does not match history")
		}
		if _, err := time.Parse(time.RFC3339Nano, record.CreatedAt); err != nil {
			return out, errors.New("workspace commit time is invalid")
		}
		out.Commits = append(out.Commits, record)
	}
	sort.Slice(out.Commits, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, out.Commits[i].CreatedAt)
		b, _ := time.Parse(time.RFC3339Nano, out.Commits[j].CreatedAt)
		if a.Equal(b) {
			return out.Commits[i].CommitID > out.Commits[j].CommitID
		}
		return a.After(b)
	})
	return out, nil
}

// ValidateSnapshotPaths rejects non-canonical, conflicting or aliased
// snapshot paths.
func ValidateSnapshotPaths(files []CommitFile) error {
	seen := map[string]bool{}
	dirs := map[string]bool{}
	for _, file := range files {
		name := file.Path
		if name == "" || name == "." || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00") || name == ".." || strings.HasPrefix(name, "../") || filepath.VolumeName(name) != "" {
			return errors.New("workspace snapshot path is not canonical")
		}
		if seen[name] || dirs[name] {
			return errors.New("workspace snapshot path conflict")
		}
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return errors.New("workspace snapshot file/directory conflict")
			}
			dirs[parent] = true
		}
		if file.Size < 0 {
			return errors.New("workspace snapshot size is invalid")
		}
		seen[name] = true
	}
	return nil
}

// validateLiveTree rejects symlinks and non-regular files in a live tree.
func validateLiveTree(ctx context.Context, root *os.Root) error {
	return fs.WalkDir(root.FS(), ".", func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("workspace restore live symlink rejected")
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("workspace restore live file must be regular")
		}
		return nil
	})
}

// RestoreSnapshot verifies a commit record against its confined files and
// stages the restore for journal-publication. Must run under the caller's
// project lock.
func RestoreSnapshot(ctx context.Context, root, projectID, branch, commitDir string, record CommitRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	branch = workspacesvc.DefaultBranch(branch)
	if projectID == "" || record.ProjectID != projectID || workspacesvc.DefaultBranch(record.Branch) != branch {
		return errors.New("workspace commit target does not match request")
	}
	expected, err := workspacesvc.CommitDir(root, projectID, branch, record.CommitID)
	if err != nil {
		return err
	}
	if filepath.Clean(expected) != filepath.Clean(commitDir) {
		return errors.New("workspace commit directory does not match record")
	}
	if err := ValidateSnapshotPaths(record.Files); err != nil {
		return err
	}
	history, err := workspacesvc.OpenDirectory(root, filepath.Join(commitDir, "files"), false)
	if err != nil {
		return err
	}
	defer history.Close()
	livePath := workspacesvc.ProjectRoot(root, projectID, branch)
	parent, err := workspacesvc.OpenDirectory(root, filepath.Dir(livePath), true)
	if err != nil {
		return err
	}
	defer parent.Close()
	stage := ".restore-" + uuid.NewString()
	backup := ".backup-" + uuid.NewString()
	if err := parent.Mkdir(stage, 0700); err != nil {
		return err
	}
	defer parent.RemoveAll(stage)
	staged, err := workspace.OpenDir(parent, stage, false)
	if err != nil {
		return err
	}
	defer staged.Close()
	for _, file := range record.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := workspace.ReadFile(history, file.Path)
		if err != nil {
			return err
		}
		if int64(len(data)) != file.Size || workspacesvc.DigestBytes(data) != file.Digest {
			return errors.New("workspace snapshot bytes do not match recorded digest/size")
		}
		if _, err := staged.Lstat(file.Path); err == nil {
			return errors.New("workspace snapshot paths alias on this filesystem")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := workspace.WriteFile(ctx, staged, file.Path, data, 0644); err != nil {
			return err
		}
	}
	live, err := workspace.OpenDir(parent, filepath.Base(livePath), false)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		if err := validateLiveTree(ctx, live); err != nil {
			live.Close()
			return err
		}
		info, statErr := live.Stat(".")
		entry, entryErr := parent.Lstat(filepath.Base(livePath))
		live.Close()
		if statErr != nil || entryErr != nil || entry.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, entry) {
			return errors.New("workspace live directory changed")
		}
	}
	if err := staged.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return PublishRestoreJournal(ctx, parent, projectID, branch, stage, filepath.Base(livePath), backup)
}

// PublishRestore is the two-renames publication under the caller's
// cooperative project lock — not atomic directory exchange (T20).
func PublishRestore(parent *os.Root, stage, live, backup string) error {
	if _, err := parent.Lstat(backup); err == nil {
		return errors.New("workspace restore backup already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	info, err := parent.Lstat(live)
	if errors.Is(err, os.ErrNotExist) {
		return parent.Rename(stage, live)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("workspace live directory is unsafe")
	}
	if err := parent.Rename(live, backup); err != nil {
		return err
	}
	if err := parent.Rename(stage, live); err != nil {
		if rollbackErr := parent.Rename(backup, live); rollbackErr != nil {
			return fmt.Errorf("workspace restore publication failed: %v; rollback failed, backup retained: %w", err, rollbackErr)
		}
		return err
	}
	if err := parent.RemoveAll(backup); err != nil {
		return fmt.Errorf("workspace restore published; backup cleanup failed: %w", err)
	}
	return nil
}

// PublishRestoreJournal writes the crash-recovery journal, publishes the
// restore and clears the journal on success; on failure it attempts
// recovery immediately.
func PublishRestoreJournal(ctx context.Context, parent *os.Root, projectID, branch, stage, live, backup string) error {
	info, err := parent.Lstat(live)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("workspace live directory is unsafe")
	}
	journal := RestoreJournal{ProjectID: projectID, Branch: branch, Stage: stage, Live: live, Backup: backup, HadLive: err == nil}
	raw, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	name := ".restore-state-" + workspacesvc.SafeID(branch) + ".json"
	if err := workspace.WriteFile(ctx, parent, name, raw, 0600); err != nil {
		return err
	}
	publishErr := PublishRestore(parent, stage, live, backup)
	if publishErr != nil {
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if recoverErr := RecoverRestoreJournal(recoveryCtx, parent, name, journal); recoverErr != nil {
			return fmt.Errorf("%w (restore recovery retained: %v)", publishErr, recoverErr)
		}
		return publishErr
	}
	return parent.Remove(name)
}

// RecoverRestores replays every restore journal found for the project.
// The native project lock makes this exclusive with cooperating
// reads/writes; it never chooses an unverified path from a journal.
func RecoverRestores(ctx context.Context, root, projectID string) error {
	parent, err := workspacesvc.OpenDirectory(root, filepath.Dir(workspacesvc.ProjectRoot(root, projectID, "main")), false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer parent.Close()
	dir, err := parent.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".restore-state-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := workspace.ReadFile(parent, entry.Name())
		if err != nil {
			return err
		}
		var journal RestoreJournal
		if err := json.Unmarshal(data, &journal); err != nil {
			return err
		}
		if journal.ProjectID != projectID || journal.Branch == "" || entry.Name() != ".restore-state-"+workspacesvc.SafeID(journal.Branch)+".json" ||
			journal.Live != filepath.Base(workspacesvc.ProjectRoot(root, projectID, journal.Branch)) ||
			!strings.HasPrefix(journal.Stage, ".restore-") || !strings.HasPrefix(journal.Backup, ".backup-") {
			return errors.New("workspace restore journal target mismatch")
		}
		if _, err := uuid.Parse(strings.TrimPrefix(journal.Stage, ".restore-")); err != nil {
			return errors.New("invalid restore stage identity")
		}
		if _, err := uuid.Parse(strings.TrimPrefix(journal.Backup, ".backup-")); err != nil {
			return errors.New("invalid restore backup identity")
		}
		if err := RecoverRestoreJournal(ctx, parent, entry.Name(), journal); err != nil {
			return err
		}
	}
	return nil
}

// RecoverRestoreJournal resolves one journal: roll back or roll forward,
// then clean up.
func RecoverRestoreJournal(ctx context.Context, parent *os.Root, name string, journal RestoreJournal) error {
	exists := func(path string) (bool, error) {
		info, err := parent.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("unsafe workspace restore recovery directory")
		}
		root, err := workspace.OpenDir(parent, path, false)
		if err != nil {
			return false, err
		}
		err = validateLiveTree(ctx, root)
		closeErr := root.Close()
		if err != nil {
			return false, err
		}
		if closeErr != nil {
			return false, closeErr
		}
		return true, nil
	}
	live, err := exists(journal.Live)
	if err != nil {
		return err
	}
	backup, err := exists(journal.Backup)
	if err != nil {
		return err
	}
	stage, err := exists(journal.Stage)
	if err != nil {
		return err
	}
	if live && backup && stage {
		return errors.New("ambiguous workspace restore recovery state")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !live && backup {
		if err := parent.Rename(journal.Backup, journal.Live); err != nil {
			return err
		}
	} else if !live && journal.HadLive {
		return errors.New("workspace restore recovery missing previous tree")
	} else if live && backup {
		if err := parent.RemoveAll(journal.Backup); err != nil {
			return err
		}
	}
	if stage {
		if err := parent.RemoveAll(journal.Stage); err != nil {
			return err
		}
	}
	return parent.Remove(name)
}
