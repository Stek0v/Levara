// workspace_history_files.go — adapter delegates. The commit staging,
// restore publication and journal recovery live in
// internal/api/workspacehistory (Phase-2 workspace campaign
// continuation); package-local type names and cfg signatures are
// preserved so consumers (task_executor, workspace.go, the publication
// test) are unchanged.
package http

import (
	"context"
	"os"

	"github.com/stek0v/levara/internal/api/workspacehistory"
	"github.com/stek0v/levara/pkg/workspace"
)

type workspaceCommitFile = workspacehistory.CommitFile
type workspaceCommitRecord = workspacehistory.CommitRecord
type workspaceRestoreJournal = workspacehistory.RestoreJournal
type workspaceLogResponse = workspacehistory.LogResponse

type workspaceCommitRequest struct {
	ProjectID string `json:"project_id"`
	Branch    string `json:"branch,omitempty"`
	Message   string `json:"message,omitempty"`
	Author    string `json:"author,omitempty"`
}

func prepareWorkspaceCommit(ctx context.Context, cfg APIConfig, req workspaceCommitRequest, commitID string) (workspaceCommitRecord, error) {
	return workspacehistory.PrepareCommit(ctx, workspaceRoot(cfg), workspacehistory.CommitRequestInput{
		ProjectID: req.ProjectID, Branch: req.Branch, Message: req.Message, Author: req.Author,
	}, commitID)
}

func loadWorkspaceCommitRecordConfined(cfg APIConfig, dir string) (workspaceCommitRecord, error) {
	return workspacehistory.LoadCommitRecordConfined(workspaceRoot(cfg), dir)
}

func listWorkspaceCommitRecordsConfined(cfg APIConfig, projectID, branch string) (workspaceLogResponse, error) {
	out, err := workspacehistory.ListCommitRecordsConfined(workspaceRoot(cfg), projectID, branch)
	if err != nil {
		return workspaceLogResponse{}, err
	}
	return workspaceLogResponse(out), nil
}

func validateWorkspaceSnapshotPaths(files []workspaceCommitFile) error {
	return workspacehistory.ValidateSnapshotPaths(files)
}

func restoreWorkspaceSnapshot(ctx context.Context, cfg APIConfig, projectID, branch, commitDir string, record workspaceCommitRecord) error {
	return workspacehistory.RestoreSnapshot(ctx, workspaceRoot(cfg), projectID, branch, commitDir, record)
}

func publishWorkspaceRestore(parent *os.Root, stage, live, backup string) error {
	return workspacehistory.PublishRestore(parent, stage, live, backup)
}

func recoverWorkspaceRestores(ctx context.Context, cfg APIConfig, projectID string) error {
	return workspacehistory.RecoverRestores(ctx, workspaceRoot(cfg), projectID)
}

var _ = workspace.DefaultBranch
