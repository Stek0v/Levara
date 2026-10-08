package http

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	accesspkg "github.com/stek0v/levara/pkg/access"
)

type workspaceFileProcessInput struct {
	Mode                                         string
	Root                                         string
	Provider                                     DBProvider
	SQLite                                       string
	PGSchema                                     string
	Actor                                        accesspkg.MetadataActor
	Ready, Start, Stop, ObservedOld, ObservedNew string
	Text, Digest                                 string
}

func workspaceFileProcessDatabase(t *testing.T, cfg APIConfig, actor accesspkg.MetadataActor) workspaceFileProcessInput {
	t.Helper()
	input := workspaceFileProcessInput{Root: cfg.WorkspacePath, Actor: actor}
	if cfg.DB == nil {
		return input
	}
	input.Provider = GetDBProvider()
	if input.Provider == DBPostgres {
		if err := cfg.DB.QueryRow("SHOW search_path").Scan(&input.PGSchema); err != nil {
			t.Fatal(err)
		}
	} else {
		rows, err := cfg.DB.Query("PRAGMA database_list")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var sequence int
			var name, file string
			if err := rows.Scan(&sequence, &name, &file); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if name == "main" {
				input.SQLite = file
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		if input.SQLite == "" {
			t.Fatal("native SQLite fixture is not a real shared file")
		}
	}
	return input
}

func TestWorkspaceFileProcessChild(t *testing.T) {
	raw := os.Getenv("LEVARA_WORKSPACE_FILE_PROCESS")
	if raw == "" {
		return
	}
	var input workspaceFileProcessInput
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatal(err)
	}
	cfg := APIConfig{WorkspacePath: input.Root, RequireAuth: !input.Actor.TrustedLocal}
	if !input.Actor.TrustedLocal {
		SetDBProvider(input.Provider)
		var db *sql.DB
		var err error
		if input.Provider == DBPostgres {
			config, err := pgx.ParseConfig(os.Getenv("LEVARA_TEST_POSTGRES_DSN"))
			if err != nil {
				t.Fatal(err)
			}
			config.RuntimeParams["search_path"] = input.PGSchema
			db = stdlib.OpenDB(*config)
		} else {
			uri := url.URL{Scheme: "file", Path: input.SQLite}
			query := uri.Query()
			query.Set("_pragma", "busy_timeout(5000)")
			uri.RawQuery = query.Encode()
			db, err = sql.Open("sqlite3", uri.String())
		}
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		defer db.Close()
		if input.Provider == DBSQLite {
			rows, err := db.Query("PRAGMA database_list")
			if err != nil {
				t.Fatal(err)
			}
			actualPath := ""
			for rows.Next() {
				var sequence int
				var name, file string
				if err := rows.Scan(&sequence, &name, &file); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				if name == "main" {
					actualPath = file
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil || actualPath != input.SQLite {
				t.Fatalf("child connected to wrong SQLite database: actual=%q expected=%q err=%v", actualPath, input.SQLite, err)
			}
		}
		var identities int
		if err := db.QueryRow(Q("SELECT COUNT(*) FROM users WHERE id=$1 AND is_active=$2"), input.Actor.UserID, true).Scan(&identities); err != nil || identities != 1 {
			t.Fatalf("child native identity/schema unavailable: count=%d err=%v", identities, err)
		}
		cfg.DB = db
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := os.WriteFile(input.Ready, []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	workspaceFileProcessWait(t, ctx, input.Start)
	if input.Mode == "cas" {
		noIndex := false
		_, err := writeWorkspaceMarkdownAuthorized(ctx, cfg, workspaceWriteRequest{workspaceIndexRequest: workspaceIndexRequest{ProjectID: "alpha", Path: "cas.md", Text: input.Text}, Index: &noIndex, ExpectedFileDigest: &input.Digest}, input.Actor)
		result := map[string]any{"success": err == nil, "error": ""}
		if err != nil {
			result["error"] = err.Error()
		}
		out, _ := json.Marshal(result)
		fmt.Printf("WORKSPACE_FILE_RESULT:%s\n", out)
		return
	}
	if input.Mode != "reader" {
		t.Fatalf("unknown child mode %s", input.Mode)
	}
	for {
		if _, err := os.Stat(input.Stop); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := ctx.Err(); err != nil {
			t.Fatal(err)
		}
		release, err := beginWorkspaceEffectFence(ctx, cfg, input.Actor, "alpha", workspaceAccessRead)
		if err != nil {
			t.Fatal(err)
		}
		// This observer follows the server's cooperative project-lock and
		// native SQL authority protocol for one complete-tree read.
		tree := workspaceIntegrityTree(t, workspaceProjectRoot(cfg, "alpha", "main"))
		release()
		if len(tree) != 16 {
			t.Fatalf("process observed incomplete tree (%d files)", len(tree))
		}
		state := ""
		for name, data := range tree {
			if data != "current "+name && data != "snapshot "+name {
				t.Fatalf("process observed corrupt file %s=%q", name, data)
			}
			current := strings.HasPrefix(data, "current ")
			tag := "snapshot"
			if current {
				tag = "current"
			}
			if state != "" && state != tag {
				t.Fatal("process observed mixed old/new restore tree")
			}
			state = tag
		}
		marker := input.ObservedNew
		if state == "current" {
			marker = input.ObservedOld
		}
		if err := os.WriteFile(marker, []byte(state), 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
}

func workspaceFileProcessWait(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("process barrier %s: %v", path, ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func workspaceFileProcessStart(t *testing.T, ctx context.Context, input workspaceFileProcessInput) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestWorkspaceFileProcessChild$")
	cmd.Env = append(os.Environ(), "LEVARA_WORKSPACE_FILE_PROCESS="+string(encoded))
	output := new(bytes.Buffer)
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, output
}

func TestWorkspaceCompetingCASIndependentProcesses(t *testing.T) {
	workspaceIntegrityModes(t, func(t *testing.T, cfg APIConfig, actor accesspkg.MetadataActor) {
		workspaceIntegrityPut(t, cfg, "cas.md", []byte("initial"))
		input := workspaceFileProcessDatabase(t, cfg, actor)
		barriers := t.TempDir()
		input.Mode = "cas"
		input.Digest = digestBytes([]byte("initial"))
		input.Start = filepath.Join(barriers, "start")
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		var commands []*exec.Cmd
		var outputs []*bytes.Buffer
		for i := 0; i < 2; i++ {
			child := input
			child.Ready = filepath.Join(barriers, fmt.Sprintf("ready-%d", i))
			child.Text = fmt.Sprintf("winner-%d", i)
			cmd, out := workspaceFileProcessStart(t, ctx, child)
			commands = append(commands, cmd)
			outputs = append(outputs, out)
			workspaceFileProcessWait(t, ctx, child.Ready)
		}
		if err := os.WriteFile(input.Start, nil, 0600); err != nil {
			t.Fatal(err)
		}
		successes, conflicts := 0, 0
		for i, cmd := range commands {
			if err := cmd.Wait(); err != nil {
				t.Fatalf("child process %d failed: %v\n%s", i, err, outputs[i])
			}
			out := outputs[i].String()
			marker := "WORKSPACE_FILE_RESULT:"
			at := strings.LastIndex(out, marker)
			if at < 0 {
				t.Fatalf("no child result: %s", out)
			}
			line := strings.SplitN(out[at+len(marker):], "\n", 2)[0]
			var result struct {
				Success bool   `json:"success"`
				Error   string `json:"error"`
			}
			if err := json.Unmarshal([]byte(line), &result); err != nil {
				t.Fatal(err)
			}
			if result.Success {
				successes++
			} else if strings.Contains(result.Error, "conflict") {
				conflicts++
			} else {
				t.Fatalf("child authority/runtime failure: %s", result.Error)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("actual process CAS successes=%d conflicts=%d", successes, conflicts)
		}
		path, _, err := workspaceFilePath(cfg, "alpha", "main", "cas.md")
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "winner-0" && string(data) != "winner-1" {
			t.Fatalf("incomplete winner=%q err=%v", data, err)
		}
	})
}

func TestWorkspaceRestoreIndependentProcessReader(t *testing.T) {
	workspaceIntegrityModes(t, func(t *testing.T, cfg APIConfig, actor accesspkg.MetadataActor) {
		for i := 0; i < 16; i++ {
			name := fmt.Sprintf("dir/file-%02d.md", i)
			workspaceIntegrityPut(t, cfg, name, []byte("snapshot "+name))
		}
		record, err := commitWorkspaceAuthorized(context.Background(), cfg, workspaceCommitRequest{ProjectID: "alpha"}, actor)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 16; i++ {
			name := fmt.Sprintf("dir/file-%02d.md", i)
			workspaceIntegrityPut(t, cfg, name, []byte("current "+name))
		}
		barriers := t.TempDir()
		input := workspaceFileProcessDatabase(t, cfg, actor)
		input.Mode = "reader"
		input.Ready = filepath.Join(barriers, "ready")
		input.Start = filepath.Join(barriers, "start")
		input.Stop = filepath.Join(barriers, "stop")
		input.ObservedOld = filepath.Join(barriers, "old")
		input.ObservedNew = filepath.Join(barriers, "new")
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		cmd, out := workspaceFileProcessStart(t, ctx, input)
		workspaceFileProcessWait(t, ctx, input.Ready)
		if err := os.WriteFile(input.Start, nil, 0600); err != nil {
			t.Fatal(err)
		}
		workspaceFileProcessWait(t, ctx, input.ObservedOld)
		if _, err := revertWorkspaceAuthorized(ctx, cfg, workspaceRevertRequest{ProjectID: "alpha", CommitID: record.CommitID, Force: true}, actor); err != nil {
			t.Fatal(err)
		}
		workspaceFileProcessWait(t, ctx, input.ObservedNew)
		if err := os.WriteFile(input.Stop, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatalf("actual reader process failed: %v\n%s", err, out)
		}
		if cfg.DB != nil && cfg.DB.Stats().InUse != 0 {
			t.Fatal("parent restore leaked native SQL")
		}
	})
}
