package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConfinedFilesystemNamespaceAndFiles(t *testing.T) {
	base := t.TempDir()
	branch, err := OpenRoot(base, "projects/alpha/main", true)
	if err != nil {
		t.Fatal(err)
	}
	branch.Close()
	sibling := filepath.Join(base, "projects", "beta", "main")
	if err := os.MkdirAll(sibling, 0755); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"projects/link", "projects/alpha/linked", ".kb/history/link", ".kb/manifests/link"} {
		target := filepath.Join(base, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(sibling, target); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if root, err := OpenRoot(base, relative, false); err == nil {
			root.Close()
			t.Fatalf("admitted namespace symlink %s", relative)
		}
	}
	baseLink := filepath.Join(t.TempDir(), "base")
	if err := os.Symlink(base, baseLink); err != nil {
		t.Fatal(err)
	}
	if root, err := OpenRoot(baseLink, ".", false); err == nil {
		root.Close()
		t.Fatal("admitted redirected base")
	}
	root, err := OpenRoot(base, "projects/alpha/main", false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	outside := filepath.Join(sibling, "secret.md")
	if err := os.WriteFile(outside, []byte("protected sibling"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink(outside, "leaf.md"); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink(sibling, "parent"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"leaf.md", "parent/secret.md"} {
		if _, err := ReadFile(root, name); err == nil {
			t.Fatalf("read redirected %s", name)
		}
		if err := WriteFile(context.Background(), root, name, []byte("overwrite"), 0644); err == nil {
			t.Fatalf("wrote redirected %s", name)
		}
		if err := AppendFile(context.Background(), root, name, []byte("append"), 0644); err == nil {
			t.Fatalf("appended redirected %s", name)
		}
	}
	actual, err := os.ReadFile(outside)
	if err != nil || string(actual) != "protected sibling" {
		t.Fatalf("changed sibling bytes: %q %v", actual, err)
	}
	if err := root.Mkdir("directory.md", 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(root, "directory.md"); err == nil {
		t.Fatal("read nonregular file")
	}
}

func TestConfinedCanonicalPathsAndExactBytes(t *testing.T) {
	root, err := OpenRoot(t.TempDir(), ".", false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"", "../escape", "a/../b", "a//b", "a/", "/absolute", "a\\b", "C:drive"} {
		if child, err := OpenDir(root, name, true); err == nil {
			child.Close()
			t.Fatalf("admitted directory %q", name)
		}
		if err := WriteFile(context.Background(), root, name, []byte("bad"), 0644); err == nil {
			t.Fatalf("admitted file %q", name)
		}
	}
	for i, data := range [][]byte{nil, []byte("Unicode Привет\r\n"), {0, 255, 1, 13, 10}} {
		name := []string{"empty", "nested/text.md", "binary"}[i]
		if err := WriteFile(context.Background(), root, name, data, 0644); err != nil {
			t.Fatal(err)
		}
		got, err := ReadFile(root, name)
		if err != nil || !bytes.Equal(data, got) {
			t.Fatalf("byte mismatch %q %v", got, err)
		}
	}
	if _, err := ReadFile(root, "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing not distinguished: %v", err)
	}
}

func TestConfinedSpecialFileDoesNotBlock(t *testing.T) {
	mkfifo, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("native FIFO fixture unavailable")
	}
	base := t.TempDir()
	if out, err := exec.Command(mkfifo, filepath.Join(base, "pipe")).CombinedOutput(); err != nil {
		t.Fatalf("mkfifo: %v %s", err, out)
	}
	root, err := OpenRoot(base, ".", false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	done := make(chan error, 1)
	go func() { _, err := ReadFile(root, "pipe"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("admitted FIFO")
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO read blocked")
	}
	if err := WriteFile(context.Background(), root, "pipe", []byte("unsafe"), 0644); err == nil {
		t.Fatal("write admitted FIFO")
	}
	if err := AppendFile(context.Background(), root, "pipe", []byte("unsafe"), 0644); err == nil {
		t.Fatal("append admitted FIFO")
	}
}

func TestAtomicWorkspaceWritesCompleteAndCleanup(t *testing.T) {
	base := t.TempDir()
	root, err := OpenRoot(base, ".", false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	old := bytes.Repeat([]byte("o"), 256*1024)
	next := bytes.Repeat([]byte("n"), len(old))
	if err := WriteFile(context.Background(), root, "file", old, 0644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WriteFile(ctx, root, "file", next, 0644); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write: %v", err)
	}
	got, err := ReadFile(root, "file")
	if err != nil || !bytes.Equal(got, old) {
		t.Fatal("canceled write changed old bytes")
	}
	errorsCh := make(chan error, 16)
	stop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			got, err := ReadFile(root, "file")
			if err != nil {
				errorsCh <- err
				return
			}
			if !bytes.Equal(got, old) && !bytes.Equal(got, next) {
				errorsCh <- errors.New("partial file observed")
				return
			}
			select {
			case <-stop:
				return
			default:
			}
		}
	}()
	var writers sync.WaitGroup
	for i := 0; i < 8; i++ {
		writers.Add(1)
		go func(i int) {
			defer writers.Done()
			release, err := LockProject(context.Background(), base, "atomic")
			if err != nil {
				errorsCh <- err
				return
			}
			defer release()
			data := old
			if i%2 == 0 {
				data = next
			}
			if err := WriteFile(context.Background(), root, "file", data, 0644); err != nil {
				errorsCh <- err
			}
		}(i)
	}
	writers.Wait()
	close(stop)
	<-readerDone
	close(errorsCh)
	for err := range errorsCh {
		t.Error(err)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".workspace-tmp-") {
			t.Fatalf("temporary leaked: %s", e.Name())
		}
	}
}

func TestConfinedManifestRootRoundTrip(t *testing.T) {
	root, err := OpenRoot(t.TempDir(), ".", false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	m := NewManifest("alpha", "main")
	if err := m.ActivateGeneration("generation"); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveRoot(context.Background(), root, "manifests/main.json"); err != nil {
		t.Fatal(err)
	}
	got, err := LoadManifestRoot(root, "manifests/main.json")
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectID != "alpha" || got.ActiveGeneration != "generation" {
		t.Fatalf("wrong manifest %+v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.ActiveGeneration = "unpublished"
	if err := m.SaveRoot(ctx, root, "manifests/main.json"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled manifest succeeded")
	}
	got, err = LoadManifestRoot(root, "manifests/main.json")
	if err != nil || got.ActiveGeneration != "generation" {
		t.Fatal("canceled manifest changed publication")
	}
	if err := WriteFile(context.Background(), root, "bad.json", []byte("{corrupt"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifestRoot(root, "bad.json"); err == nil {
		t.Fatal("corrupt manifest admitted")
	}
}

func TestConfinedAppendFileExactAndCanceled(t *testing.T) {
	root, err := OpenRoot(t.TempDir(), ".", false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, record := range []string{"first\n", "Привет\r\n"} {
		if err := AppendFile(context.Background(), root, "audit/events.jsonl", []byte(record), 0644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := AppendFile(ctx, root, "audit/events.jsonl", []byte("forbidden\n"), 0644); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled append: %v", err)
	}
	data, err := ReadFile(root, "audit/events.jsonl")
	if err != nil || string(data) != "first\nПривет\r\n" {
		t.Fatalf("append changed bytes: %q %v", data, err)
	}
}

func TestConfinedHardLinkAliasesRejected(t *testing.T) {
	for _, location := range []string{"sibling", "outside"} {
		t.Run(location, func(t *testing.T) {
			base := t.TempDir()
			root, err := OpenRoot(base, "projects/alpha/main", true)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			targetBase := t.TempDir()
			if location == "sibling" {
				targetBase = filepath.Join(base, "projects", "beta", "main")
				if err := os.MkdirAll(targetBase, 0755); err != nil {
					t.Fatal(err)
				}
			}
			target := filepath.Join(targetBase, "secret")
			original := []byte("{\"version\":1,\"project_id\":\"beta\",\"branch\":\"main\"}\n")
			if err := os.WriteFile(target, original, 0644); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(base, "projects", "alpha", "main", "alias")
			if err := os.Link(target, alias); err != nil {
				t.Fatalf("native hardlink fixture: %v", err)
			}
			if _, err := ReadFile(root, "alias"); err == nil {
				t.Fatal("read admitted hardlink")
			}
			if err := AppendFile(context.Background(), root, "alias", []byte("append"), 0644); err == nil {
				t.Fatal("append admitted hardlink")
			}
			if err := WriteFile(context.Background(), root, "alias", []byte("replacement"), 0644); err == nil {
				t.Fatal("write admitted hardlink")
			}
			if _, err := LoadManifestRoot(root, "alias"); err == nil {
				t.Fatal("manifest load admitted hardlink")
			}
			if err := NewManifest("alpha", "main").SaveRoot(context.Background(), root, "alias"); err == nil {
				t.Fatal("manifest save admitted hardlink")
			}
			for _, name := range []string{target, alias} {
				actual, err := os.ReadFile(name)
				if err != nil || !bytes.Equal(actual, original) {
					t.Fatalf("changed alias target %q: %q %v", name, actual, err)
				}
			}
			entries, err := os.ReadDir(filepath.Dir(alias))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".workspace-tmp-") {
					t.Fatal("failed write left temporary")
				}
			}
		})
	}
}

func TestConfinedHardLinkProjectLockRejected(t *testing.T) {
	if !processLockSupported() {
		t.Skip("native lock unsupported")
	}
	base := t.TempDir()
	locks, err := OpenRoot(base, ".kb/locks", true)
	if err != nil {
		t.Fatal(err)
	}
	locks.Close()
	target := filepath.Join(t.TempDir(), "outside-lock")
	if err := os.WriteFile(target, []byte("outside lock bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, ".kb", "locks", "alpha.lock")
	if err := os.Link(target, alias); err != nil {
		t.Fatal(err)
	}
	if release, err := LockProject(context.Background(), base, "alpha"); err == nil {
		release()
		t.Fatal("lock admitted hardlink")
	}
	actual, err := os.ReadFile(target)
	if err != nil || string(actual) != "outside lock bytes" {
		t.Fatal("lock changed aliased target")
	}
}

func TestWorkspaceProcessLockConcurrentFirstCreation(t *testing.T) {
	if !processLockSupported() {
		t.Skip("native lock unsupported")
	}
	base := t.TempDir()
	start := make(chan struct{})
	failures := make(chan error, 24)
	var workers sync.WaitGroup
	var inside, admitted atomic.Int32
	for i := 0; i < 24; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			release, err := LockProject(ctx, base, "fresh")
			if err != nil {
				failures <- err
				return
			}
			if inside.Add(1) != 1 {
				failures <- errors.New("multiple lock owners")
			}
			admitted.Add(1)
			if inside.Add(-1) != 0 {
				failures <- errors.New("overlapping lock release")
			}
			release()
		}()
	}
	close(start)
	workers.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if admitted.Load() != 24 {
		t.Fatalf("first lock creation admitted %d/24", admitted.Load())
	}
	path := filepath.Join(base, ".kb", "locks", "fresh.lock")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("missing regular persistent lock: %v", err)
	}
}
