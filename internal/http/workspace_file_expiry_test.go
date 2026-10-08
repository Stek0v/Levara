package http

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/workspace"
)

func workspaceExpiryStorageState(t *testing.T, root string) map[string]string {
	t.Helper()
	state := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			state[relative+"/"] = "<directory>"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		state[relative] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestWorkspaceFileQueuedCredentialExpiry(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		for _, operation := range []string{"write", "commit", "revert", "run", "delete", "gc", "enqueue"} {
			t.Run(operation, func(t *testing.T) {
				cfg, closeCfg := newWorkspaceTestConfig(t)
				defer closeCfg()
				cfg.DB, cfg.RequireAuth = f.db, true
				fresh := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
				workspaceIntegrityPut(t, cfg, "note.md", []byte("snapshot bytes"))
				commit, err := commitWorkspaceAuthorized(context.Background(), cfg, workspaceCommitRequest{ProjectID: "alpha"}, fresh)
				if err != nil {
					t.Fatal(err)
				}
				workspaceIntegrityPut(t, cfg, "note.md", []byte("current bytes"))
				manifest, path, err := loadWorkspaceManifest(cfg, "alpha", "main")
				if err != nil {
					t.Fatal(err)
				}
				if err := saveWorkspaceManifest(context.Background(), cfg, path, manifest); err != nil {
					t.Fatal(err)
				}
				noIndex := false
				invoke := func(ctx context.Context, actor accesspkg.MetadataActor) error {
					switch operation {
					case "write":
						_, err := writeWorkspaceMarkdownAuthorized(ctx, cfg, workspaceWriteRequest{workspaceIndexRequest: workspaceIndexRequest{ProjectID: "alpha", Path: "note.md", Text: "new bytes"}, Index: &noIndex}, actor)
						return err
					case "commit":
						_, err := commitWorkspaceAuthorized(ctx, cfg, workspaceCommitRequest{ProjectID: "alpha"}, actor)
						return err
					case "revert":
						_, err := revertWorkspaceAuthorized(ctx, cfg, workspaceRevertRequest{ProjectID: "alpha", CommitID: commit.CommitID, Force: true}, actor)
						return err
					case "run":
						_, err := startWorkspaceRunAuthorized(ctx, cfg, workspaceRunStartRequest{ProjectID: "alpha", RunID: "queued-expiry", Result: "must require live authority"}, actor)
						return err
					case "delete":
						_, err := deleteWorkspaceMarkdownAuthorized(ctx, cfg, workspaceDeleteRequest{ProjectID: "alpha", Generation: "queued", Path: "note.md"}, actor)
						return err
					case "gc":
						_, err := gcWorkspaceGenerationsAuthorized(ctx, cfg, workspaceGCRequest{ProjectID: "alpha"}, actor)
						return err
					case "enqueue":
						_, err := enqueueWorkspaceIndexJobFromPayloadAuthorized(ctx, cfg, workspaceIndexJobPayload{Operation: "reindex", ProjectID: "alpha", Branch: "main", Generation: "queued", Paths: []string{"note.md"}}, actor)
						return err
					}
					panic("unknown operation")
				}
				holdCtx, cancelHold := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancelHold()
				unlock, err := workspace.LockProject(holdCtx, workspaceRoot(cfg), "alpha")
				if err != nil {
					t.Fatal(err)
				}
				defer unlock()
				before := workspaceExpiryStorageState(t, workspaceRoot(cfg))
				short := fresh
				short.Credential.ExpiresAt = time.Now().Unix() + 2
				ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- invoke(ctx, short) }()
				// Holding an independent native lock descriptor leaves the
				// admitted worker's real pool1 SQL fence awaiting filesystem
				// ownership. Observe it before the credential expires.
				barrier := time.Now().Add(time.Second)
				for f.db.Stats().InUse != 1 {
					select {
					case err := <-done:
						t.Fatalf("operation returned before lock wait: %v", err)
					default:
					}
					if time.Now().After(barrier) {
						t.Fatal("native worker never acquired SQL before filesystem lock wait")
					}
					time.Sleep(time.Millisecond)
				}
				if time.Now().Unix() >= short.Credential.ExpiresAt {
					t.Fatal("worker admission barrier arrived after credential expiry")
				}
				select {
				case err := <-done:
					t.Fatalf("held project lock did not block admitted operation: %v", err)
				default:
				}
				expiry := time.Unix(short.Credential.ExpiresAt, 0)
				if wait := time.Until(expiry); wait > 0 {
					timer := time.NewTimer(wait)
					<-timer.C
				}
				var operationErr error
				select {
				case operationErr = <-done:
				case <-time.After(time.Second):
					unlock()
					select {
					case <-done:
					case <-time.After(time.Second):
					}
					t.Fatal("credential expiry did not cancel queued filesystem acquisition")
				}
				if operationErr == nil {
					t.Fatal("expired queued operation reported success")
				}
				unlock()
				after := workspaceExpiryStorageState(t, workspaceRoot(cfg))
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("expired %s changed tree/history/run/manifest/jobs before=%v after=%v", operation, before, after)
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("expired lock wait leaked native SQL")
				}
				if err := invoke(context.Background(), fresh); err != nil {
					t.Fatalf("fresh credential positive control failed: %v", err)
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("positive control leaked native SQL")
				}
			})
		}
	})
}
