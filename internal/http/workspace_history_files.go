package http

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
	"github.com/stek0v/levara/pkg/workspace"
)

// Callers hold the branch/project lock and authority fence throughout publication.
func prepareWorkspaceCommit(ctx context.Context, cfg APIConfig, req workspaceCommitRequest, commitID string) (workspaceCommitRecord, error) {
	if err := ctx.Err(); err != nil {
		return workspaceCommitRecord{}, err
	}
	branch := defaultBranch(req.Branch)
	if req.ProjectID == "" {
		return workspaceCommitRecord{}, workspace.ErrMissingProjectID
	}
	final, err := workspaceCommitDir(cfg, req.ProjectID, branch, commitID)
	if err != nil {
		return workspaceCommitRecord{}, err
	}
	parent, err := openWorkspaceDirectory(cfg, filepath.Dir(final), true)
	if err != nil {
		return workspaceCommitRecord{}, err
	}
	defer parent.Close()
	stage := ".commit-" + uuid.NewString()
	if err := parent.Mkdir(stage, 0700); err != nil {
		return workspaceCommitRecord{}, err
	}
	defer parent.RemoveAll(stage)
	staged, err := workspace.OpenDir(parent, stage, false)
	if err != nil {
		return workspaceCommitRecord{}, err
	}
	defer staged.Close()
	files, err := workspace.OpenDir(staged, "files", true)
	if err != nil {
		return workspaceCommitRecord{}, err
	}
	defer files.Close()
	record := workspaceCommitRecord{ProjectID: req.ProjectID, Branch: branch, CommitID: commitID, Message: req.Message, Author: req.Author,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Path: final, Files: []workspaceCommitFile{}}
	source, err := openWorkspaceDirectory(cfg, workspaceProjectRoot(cfg, req.ProjectID, branch), false)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return workspaceCommitRecord{}, err
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
			record.Files = append(record.Files, workspaceCommitFile{Path: name, Digest: digestBytes(data), Size: int64(len(data))})
			return nil
		})
		if err != nil {
			return workspaceCommitRecord{}, err
		}
	}
	sort.Slice(record.Files, func(i, j int) bool { return record.Files[i].Path < record.Files[j].Path })
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return workspaceCommitRecord{}, err
	}
	if err := workspace.WriteFile(ctx, staged, "commit.json", append(raw, '\n'), 0644); err != nil {
		return workspaceCommitRecord{}, err
	}
	if err := files.Close(); err != nil {
		return workspaceCommitRecord{}, err
	}
	if err := staged.Close(); err != nil {
		return workspaceCommitRecord{}, err
	}
	if _, err := parent.Lstat(filepath.Base(final)); err == nil {
		return workspaceCommitRecord{}, errors.New("workspace commit already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return workspaceCommitRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return workspaceCommitRecord{}, err
	}
	if err := parent.Rename(stage, filepath.Base(final)); err != nil {
		return workspaceCommitRecord{}, err
	}
	return record, nil
}

func loadWorkspaceCommitRecordConfined(cfg APIConfig, dir string) (workspaceCommitRecord, error) {
	root, err := openWorkspaceDirectory(cfg, dir, false)
	if err != nil {
		return workspaceCommitRecord{}, err
	}
	defer root.Close()
	data, err := workspace.ReadFile(root, "commit.json")
	if err != nil {
		return workspaceCommitRecord{}, err
	}
	var record workspaceCommitRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return workspaceCommitRecord{}, err
	}
	record.Path = dir
	if record.Files == nil {
		record.Files = []workspaceCommitFile{}
	}
	return record, nil
}

func listWorkspaceCommitRecordsConfined(cfg APIConfig, projectID, branch string) (workspaceLogResponse, error) {
	branch = defaultBranch(branch)
	out := workspaceLogResponse{ProjectID: projectID, Branch: branch, Commits: []workspaceCommitRecord{}}
	if projectID == "" {
		return out, workspace.ErrMissingProjectID
	}
	dir := workspaceCommitsRoot(cfg, projectID, branch)
	root, err := openWorkspaceDirectory(cfg, dir, false)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	defer root.Close()
	file, err := root.Open(".")
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
		record, err := loadWorkspaceCommitRecordConfined(cfg, filepath.Join(dir, entry.Name()))
		if err != nil {
			return out, err
		}
		expected, targetErr := workspaceCommitDir(cfg, projectID, branch, record.CommitID)
		if targetErr != nil || filepath.Clean(expected) != filepath.Clean(filepath.Join(dir, entry.Name())) || record.ProjectID != projectID || defaultBranch(record.Branch) != branch {
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

func validateWorkspaceSnapshotPaths(files []workspaceCommitFile) error {
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

func validateWorkspaceLiveTree(ctx context.Context, root *os.Root) error {
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

func restoreWorkspaceSnapshot(ctx context.Context, cfg APIConfig, projectID, branch, commitDir string, record workspaceCommitRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	branch = defaultBranch(branch)
	if projectID == "" || record.ProjectID != projectID || defaultBranch(record.Branch) != branch {
		return errors.New("workspace commit target does not match request")
	}
	expected, err := workspaceCommitDir(cfg, projectID, branch, record.CommitID)
	if err != nil {
		return err
	}
	if filepath.Clean(expected) != filepath.Clean(commitDir) {
		return errors.New("workspace commit directory does not match record")
	}
	if err := validateWorkspaceSnapshotPaths(record.Files); err != nil {
		return err
	}
	history, err := openWorkspaceDirectory(cfg, filepath.Join(commitDir, "files"), false)
	if err != nil {
		return err
	}
	defer history.Close()
	livePath := workspaceProjectRoot(cfg, projectID, branch)
	parent, err := openWorkspaceDirectory(cfg, filepath.Dir(livePath), true)
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
		if int64(len(data)) != file.Size || digestBytes(data) != file.Digest {
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
		if err := validateWorkspaceLiveTree(ctx, live); err != nil {
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
	return publishWorkspaceRestoreJournal(ctx, parent, projectID, branch, stage, filepath.Base(livePath), backup)
}

// This is two-renames publication under the caller's cooperative project lock,
// not atomic directory exchange or crash recovery (T20).
func publishWorkspaceRestore(parent *os.Root, stage, live, backup string) error {
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

type workspaceRestoreJournal struct {
	ProjectID string `json:"project_id"`
	Branch    string `json:"branch"`
	Stage     string `json:"stage"`
	Live      string `json:"live"`
	Backup    string `json:"backup"`
	HadLive   bool   `json:"had_live"`
}

func publishWorkspaceRestoreJournal(ctx context.Context, parent *os.Root, projectID, branch, stage, live, backup string) error {
	info, err := parent.Lstat(live)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("workspace live directory is unsafe")
	}
	journal := workspaceRestoreJournal{ProjectID: projectID, Branch: branch, Stage: stage, Live: live, Backup: backup, HadLive: err == nil}
	raw, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	name := ".restore-state-" + safeWorkspaceID(branch) + ".json"
	if err := workspace.WriteFile(ctx, parent, name, raw, 0600); err != nil {
		return err
	}
	publishErr := publishWorkspaceRestore(parent, stage, live, backup)
	if publishErr != nil {
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if recoverErr := recoverWorkspaceRestoreJournal(recoveryCtx, parent, name, journal); recoverErr != nil {
			return fmt.Errorf("%w (restore recovery retained: %v)", publishErr, recoverErr)
		}
		return publishErr
	}
	return parent.Remove(name)
}

// The native project lock makes this journal recovery exclusive with all
// cooperating reads/writes. It never chooses an unverified path from a journal.
func recoverWorkspaceRestores(ctx context.Context, cfg APIConfig, projectID string) error {
	parent, err := openWorkspaceDirectory(cfg, filepath.Dir(workspaceProjectRoot(cfg, projectID, "main")), false)
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
		var journal workspaceRestoreJournal
		if err := json.Unmarshal(data, &journal); err != nil {
			return err
		}
		if journal.ProjectID != projectID || journal.Branch == "" || entry.Name() != ".restore-state-"+safeWorkspaceID(journal.Branch)+".json" ||
			journal.Live != filepath.Base(workspaceProjectRoot(cfg, projectID, journal.Branch)) ||
			!strings.HasPrefix(journal.Stage, ".restore-") || !strings.HasPrefix(journal.Backup, ".backup-") {
			return errors.New("workspace restore journal target mismatch")
		}
		if _, err := uuid.Parse(strings.TrimPrefix(journal.Stage, ".restore-")); err != nil {
			return errors.New("invalid restore stage identity")
		}
		if _, err := uuid.Parse(strings.TrimPrefix(journal.Backup, ".backup-")); err != nil {
			return errors.New("invalid restore backup identity")
		}
		if err := recoverWorkspaceRestoreJournal(ctx, parent, entry.Name(), journal); err != nil {
			return err
		}
	}
	return nil
}

func recoverWorkspaceRestoreJournal(ctx context.Context, parent *os.Root, name string, journal workspaceRestoreJournal) error {
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
		err = validateWorkspaceLiveTree(ctx, root)
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
