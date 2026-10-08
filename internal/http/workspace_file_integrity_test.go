package http

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	accesspkg "github.com/stek0v/levara/pkg/access"
)

func workspaceIntegrityModes(t *testing.T, run func(*testing.T, APIConfig, accesspkg.MetadataActor)) {
	t.Helper()
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		cfg := f.cfg
		cfg.RequireAuth = true
		cfg.WorkspacePath = t.TempDir()
		actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
		run(t, cfg, actor)
	})
	t.Run("trusted-local", func(t *testing.T) {
		run(t, APIConfig{WorkspacePath: t.TempDir()}, accesspkg.MetadataActor{TrustedLocal: true})
	})
}

func workspaceIntegrityPut(t *testing.T, cfg APIConfig, name string, data []byte) string {
	t.Helper()
	path, _, err := workspaceFilePath(cfg, "alpha", "main", name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func workspaceIntegrityTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		tree[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestWorkspaceReadWriteCurrentFileIntegrity(t *testing.T) {
	workspaceIntegrityModes(t, func(t *testing.T, cfg APIConfig, actor accesspkg.MetadataActor) {
		for _, text := range []string{"", "line\r\nКириллица 🌍\r\n", "externally edited\n"} {
			path := workspaceIntegrityPut(t, cfg, "current.md", []byte(text))
			response, err := readWorkspaceMarkdown(cfg, workspaceReadRequest{ProjectID: "alpha", Path: "current.md"})
			if err != nil || response.Text != text {
				t.Fatalf("exact read text=%q want=%q err=%v", response.Text, text, err)
			}
			raw, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if fields["file_digest"] != digestBytes([]byte(text)) {
				t.Fatalf("actual digest=%v want=%s", fields["file_digest"], digestBytes([]byte(text)))
			}
			stale := digestBytes([]byte("previous index"))
			noIndex := false
			_, err = writeWorkspaceMarkdownAuthorized(context.Background(), cfg, workspaceWriteRequest{workspaceIndexRequest: workspaceIndexRequest{ProjectID: "alpha", Path: "current.md", Text: "must not overwrite"}, Index: &noIndex, ExpectedFileDigest: &stale}, actor)
			if err == nil || !strings.Contains(err.Error(), "conflict") {
				t.Fatalf("stale CAS error=%v", err)
			}
			current, err := os.ReadFile(path)
			if err != nil || string(current) != text {
				t.Fatalf("stale CAS changed bytes=%q err=%v", current, err)
			}
		}
		workspaceIntegrityPut(t, cfg, "invalid.md", []byte{0xff, 0xfe, 0x00})
		if _, err := readWorkspaceMarkdown(cfg, workspaceReadRequest{ProjectID: "alpha", Path: "invalid.md"}); err == nil {
			t.Fatal("invalid UTF-8 read silently succeeded")
		}
		noIndex := false
		absent := ""
		request := workspaceWriteRequest{workspaceIndexRequest: workspaceIndexRequest{ProjectID: "alpha", Path: "absent.md", Text: ""}, Index: &noIndex, ExpectedFileDigest: &absent}
		if _, err := writeWorkspaceMarkdownAuthorized(context.Background(), cfg, request, actor); err != nil {
			t.Fatalf("absent CAS create: %v", err)
		}
		request.Text = "must not treat empty as absent"
		if _, err := writeWorkspaceMarkdownAuthorized(context.Background(), cfg, request, actor); err == nil {
			t.Fatal("empty file confused with absent file")
		}
		emptyDigest := digestBytes(nil)
		request.ExpectedFileDigest = &emptyDigest
		request.Text = "new exact text"
		if _, err := writeWorkspaceMarkdownAuthorized(context.Background(), cfg, request, actor); err != nil {
			t.Fatalf("empty file digest rejected: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		before := workspaceIntegrityTree(t, workspaceProjectRoot(cfg, "alpha", "main"))
		request.ExpectedFileDigest = nil
		request.Text = "cancelled"
		if _, err := writeWorkspaceMarkdownAuthorized(ctx, cfg, request, actor); err == nil {
			t.Fatal("canceled write succeeded")
		}
		if after := workspaceIntegrityTree(t, workspaceProjectRoot(cfg, "alpha", "main")); !reflect.DeepEqual(before, after) {
			t.Fatal("canceled write changed branch")
		}
		request.Text = string([]byte{0xff})
		if _, err := writeWorkspaceMarkdownAuthorized(context.Background(), cfg, request, actor); err == nil {
			t.Fatal("invalid UTF-8 write silently succeeded")
		}
		if after := workspaceIntegrityTree(t, workspaceProjectRoot(cfg, "alpha", "main")); !reflect.DeepEqual(before, after) {
			t.Fatal("invalid UTF-8 write changed branch")
		}
	})
}

func TestWorkspaceRestoreCorruptSnapshotPreservesCurrentTree(t *testing.T) {
	for _, damage := range []string{"missing-second", "corrupt-second", "wrong-size", "wrong-digest", "duplicate", "path-conflict", "symlink-second", "cancelled"} {
		t.Run(damage, func(t *testing.T) {
			workspaceIntegrityModes(t, func(t *testing.T, cfg APIConfig, actor accesspkg.MetadataActor) {
				workspaceIntegrityPut(t, cfg, "a.md", []byte("snapshot first"))
				workspaceIntegrityPut(t, cfg, "b.md", []byte("snapshot second"))
				record, err := commitWorkspaceAuthorized(context.Background(), cfg, workspaceCommitRequest{ProjectID: "alpha"}, actor)
				if err != nil || len(record.Files) != 2 {
					t.Fatalf("native snapshot err=%v files=%v", err, record.Files)
				}
				workspaceIntegrityPut(t, cfg, "a.md", []byte("current first"))
				workspaceIntegrityPut(t, cfg, "b.md", []byte("current second"))
				workspaceIntegrityPut(t, cfg, "extra.txt", []byte("current only"))
				second := filepath.Join(record.Path, "files", "b.md")
				switch damage {
				case "missing-second":
					if err := os.Remove(second); err != nil {
						t.Fatal(err)
					}
				case "corrupt-second":
					if err := os.WriteFile(second, []byte("wrong"), 0600); err != nil {
						t.Fatal(err)
					}
				case "wrong-size":
					record.Files[1].Size++
				case "wrong-digest":
					record.Files[1].Digest = digestBytes([]byte("wrong"))
				case "duplicate":
					record.Files = append(record.Files, record.Files[0])
				case "path-conflict":
					record.Files = append(record.Files, workspaceCommitFile{Path: "a.md/child.md", Digest: digestBytes(nil), Size: 0})
				case "symlink-second":
					outside := filepath.Join(t.TempDir(), "outside.md")
					if err := os.WriteFile(outside, []byte("snapshot second"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(second); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(outside, second); err != nil {
						t.Fatal(err)
					}
				}
				if damage == "wrong-size" || damage == "wrong-digest" || damage == "duplicate" || damage == "path-conflict" {
					raw, err := json.Marshal(record)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(record.Path, "commit.json"), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				before := workspaceIntegrityTree(t, workspaceProjectRoot(cfg, "alpha", "main"))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if damage == "cancelled" {
					cancel()
				}
				_, err = revertWorkspaceAuthorized(ctx, cfg, workspaceRevertRequest{ProjectID: "alpha", CommitID: record.CommitID, Force: true}, actor)
				if err == nil {
					t.Fatalf("%s restore succeeded", damage)
				}
				after := workspaceIntegrityTree(t, workspaceProjectRoot(cfg, "alpha", "main"))
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("%s destroyed current tree before=%v after=%v", damage, before, after)
				}
				parent := filepath.Dir(workspaceProjectRoot(cfg, "alpha", "main"))
				entries, err := os.ReadDir(parent)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.Contains(entry.Name(), "stage") || strings.Contains(entry.Name(), "backup") {
						t.Fatalf("failed restore leaked unpublished sibling %s", entry.Name())
					}
				}
			})
		})
	}
}

func TestWorkspaceHistoryRestoreExactEmptyBinaryBytes(t *testing.T) {
	workspaceIntegrityModes(t, func(t *testing.T, cfg APIConfig, actor accesspkg.MetadataActor) {
		expected := map[string][]byte{"empty.md": {}, "nested/data.bin": {0, 255, 128, 13, 10}, "unicode.md": []byte("é 🌍\r\n")}
		for path, data := range expected {
			workspaceIntegrityPut(t, cfg, path, data)
		}
		record, err := commitWorkspaceAuthorized(context.Background(), cfg, workspaceCommitRequest{ProjectID: "alpha"}, actor)
		if err != nil || len(record.Files) != len(expected) {
			t.Fatalf("snapshot bytes err=%v files=%v", err, record.Files)
		}
		for _, file := range record.Files {
			if file.Size != int64(len(expected[file.Path])) || file.Digest != digestBytes(expected[file.Path]) {
				t.Fatalf("snapshot actual copied metadata=%+v", file)
			}
		}
		for path := range expected {
			workspaceIntegrityPut(t, cfg, path, []byte("changed"))
		}
		workspaceIntegrityPut(t, cfg, "extra.txt", []byte("remove on successful restore"))
		if _, err := revertWorkspaceAuthorized(context.Background(), cfg, workspaceRevertRequest{ProjectID: "alpha", CommitID: record.CommitID, Force: true}, actor); err != nil {
			t.Fatal(err)
		}
		tree := workspaceIntegrityTree(t, workspaceProjectRoot(cfg, "alpha", "main"))
		if len(tree) != len(expected) {
			t.Fatalf("restored tree includes extras: %v", tree)
		}
		for path, data := range expected {
			if !bytes.Equal([]byte(tree[path]), data) {
				t.Fatalf("restored %s bytes=%v want=%v", path, []byte(tree[path]), data)
			}
		}
	})
}
