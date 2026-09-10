package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stek0v/levara/pkg/backup"
)

func TestVerifiedCLIExactInventoryAndReceipt(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("POSTGRES_DSN", "")
	t.Setenv("DB_PROVIDER", "sqlite")
	t.Setenv("STORAGE_BACKEND", "local")
	t.Setenv("NEO4J_URL", "")
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(base, "data")
	output := filepath.Join(base, "output")
	for _, p := range []string{"workspace", "uploads", "node-1/collections", "node-1/shard_0"} {
		if err = os.MkdirAll(filepath.Join(data, p), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(data, "node-1/shard_0/meta.bin.wal"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(data, "levara.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE example(id TEXT PRIMARY KEY); INSERT INTO example VALUES ('record')"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	args := []string{"--data-dir", data, "--output-dir", output, "--standalone=true", "--node-id", "node-1", "--shards", "1", "--dim", "2"}
	var out bytes.Buffer
	if err = runVerifiedCommand(context.Background(), "verified", args, &out); err != nil {
		t.Fatal(err)
	}
	var r backup.RestoreReceipt
	if err = json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Tables != 1 || r.CompletedAt == "" {
		t.Fatalf("incomplete receipt: %s", out.String())
	}
	out.Reset()
	if err = runVerifiedCommand(context.Background(), "status", []string{"--output-dir", output}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), r.ArchiveSHA256) {
		t.Fatal("status lost success hash")
	}
	out.Reset()
	if err = runVerifiedCommand(context.Background(), "verify", []string{"--input", r.Archive}, &out); err != nil {
		t.Fatal(err)
	}
}
func TestVerifiedCLIRejectsAmbiguousCredentialsAndUnknownArguments(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:secret1@host/db")
	t.Setenv("POSTGRES_DSN", "postgres://user:secret2@host/db")
	var out bytes.Buffer
	err := runVerifiedCommand(context.Background(), "verified", nil, &out)
	if err == nil || strings.Contains(err.Error(), "secret") || out.Len() != 0 {
		t.Fatalf("unsafe ambiguity result: %v", err)
	}
	err = runVerifiedCommand(context.Background(), "verify", []string{"--db-dsn", "postgres://user:secret3@host/db"}, &out)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unexpected secret-bearing args result: %v", err)
	}
}

func TestPreparedBackupScriptRestoresServiceOnlyWhenInitiallyActive(t *testing.T) {
	for _, tc := range []struct {
		name                string
		active, backupFails bool
	}{{"active success", true, false}, {"active failure", true, true}, {"inactive success", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "calls")
			script, err := filepath.Abs("../../deploy/raspberry/backup.sh")
			if err != nil {
				t.Fatal(err)
			}
			commands := map[string]string{
				"id":        "echo 0",
				"install":   "exit 0",
				"runuser":   "printf 'backup\\n' >> \"$BACKUP_TEST_CALLS\"; exit \"$BACKUP_TEST_RESULT\"",
				"systemctl": "printf '%s\\n' \"$1\" >> \"$BACKUP_TEST_CALLS\"; if [ \"$1\" = is-active ]; then exit \"$BACKUP_TEST_ACTIVE\"; fi",
			}
			for name, body := range commands {
				if err = os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			active, result := "1", "0"
			if tc.active {
				active = "0"
			}
			if tc.backupFails {
				result = "7"
			}
			cmd := exec.Command("bash", script, filepath.Join(dir, "backup"))
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "LEVARA_BACKUP_NODE_ID=node-1", "LEVARA_BACKUP_SHARDS=1", "LEVARA_BACKUP_DIM=2", "DB_PROVIDER=sqlite", "LEVARA_BACKUP_STANDALONE=true", "LEVARA_BACKUP_QUIESCE=1", "BACKUP_TEST_CALLS="+logPath, "BACKUP_TEST_ACTIVE="+active, "BACKUP_TEST_RESULT="+result)
			output, err := cmd.CombinedOutput()
			if (err != nil) != tc.backupFails {
				t.Fatalf("script err=%v output=%s", err, output)
			}
			calls, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			want := "is-active\nbackup\n"
			if tc.active {
				want = "is-active\nstop\nbackup\nstart\n"
			}
			if string(calls) != want {
				t.Fatalf("calls=%q want=%q", calls, want)
			}
		})
	}
}

func TestVerifiedCLIHelpDoesNotPrintConfiguredCredentials(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:secret@host/db")
	t.Setenv("NEO4J_URL", "bolt://u:secret@host/db")
	var out bytes.Buffer
	if err := runVerifiedCommand(context.Background(), "verified", []string{"--help"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "--node-id") || strings.Contains(out.String(), "secret") {
		t.Fatalf("unsafe help: %s", out.String())
	}
}
