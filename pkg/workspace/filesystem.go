package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"
)

func confinedName(name string, directory bool) error {
	if directory && name == "." {
		return nil
	}
	if name == "" || name == "." || strings.ContainsAny(name, "\\\x00:") || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
		return fmt.Errorf("noncanonical workspace path")
	}
	return nil
}

// OpenRoot anchors the configured base and descends only non-symlink directories.
// The configured base's ancestors are trusted configuration; namespace descendants
// are checked individually, even for links pointing elsewhere inside the base.
func OpenRoot(base, relative string, create bool) (*os.Root, error) {
	if err := confinedName(relative, true); err != nil {
		return nil, err
	}
	before, err := os.Lstat(base)
	if errors.Is(err, os.ErrNotExist) && create {
		if err = os.MkdirAll(base, 0755); err != nil {
			return nil, err
		}
		before, err = os.Lstat(base)
	}
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("unsafe workspace base")
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	after, afterErr := os.Lstat(base)
	if err != nil || afterErr != nil || !os.SameFile(before, opened) || !os.SameFile(before, after) || after.Mode()&os.ModeSymlink != 0 {
		root.Close()
		return nil, fmt.Errorf("workspace base changed during open")
	}
	result, err := OpenDir(root, relative, create)
	root.Close()
	return result, err
}

// OpenDir retains a directory capability after verifying each namespace component.
func OpenDir(parent *os.Root, relative string, create bool) (*os.Root, error) {
	if parent == nil {
		return nil, fmt.Errorf("workspace root required")
	}
	if err := confinedName(relative, true); err != nil {
		return nil, err
	}
	current, err := parent.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	if relative == "." {
		return current, nil
	}
	for _, part := range strings.Split(relative, "/") {
		before, err := current.Lstat(part)
		if errors.Is(err, os.ErrNotExist) && create {
			if err = current.Mkdir(part, 0755); err != nil && !errors.Is(err, os.ErrExist) {
				current.Close()
				return nil, err
			}
			before, err = current.Lstat(part)
		}
		if err != nil {
			current.Close()
			return nil, err
		}
		if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			current.Close()
			return nil, fmt.Errorf("unsafe workspace directory")
		}
		next, err := current.OpenRoot(part)
		if err != nil {
			current.Close()
			return nil, err
		}
		opened, openErr := next.Stat(".")
		after, afterErr := current.Lstat(part)
		if openErr != nil || afterErr != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, opened) || !os.SameFile(before, after) {
			next.Close()
			current.Close()
			return nil, fmt.Errorf("workspace directory changed during open")
		}
		current.Close()
		current = next
	}
	return current, nil
}

func confinedParent(root *os.Root, name string, create bool) (*os.Root, string, error) {
	if err := confinedName(name, false); err != nil {
		return nil, "", err
	}
	parent, err := OpenDir(root, path.Dir(name), create)
	return parent, path.Base(name), err
}

func regularLeaf(root *os.Root, name string, absent bool) (os.FileInfo, error) {
	info, err := root.Lstat(name)
	if absent && errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("workspace file must be regular")
	}
	return info, nil
}

// openWorkspaceFile uses platform nonblocking/nofollow flags, then verifies the
// descriptor against both namespace observations before any byte reads or writes.
func openWorkspaceFile(root *os.Root, name string, flags int, perm os.FileMode) (*os.File, error) {
	before, err := regularLeaf(root, name, flags&os.O_CREATE != 0)
	if err != nil {
		return nil, err
	}
	openFlags := flags
	createContender := flags&os.O_CREATE != 0 && flags&os.O_EXCL == 0
	if createContender {
		if before != nil {
			openFlags = flags &^ os.O_CREATE
		} else {
			openFlags = flags | os.O_EXCL
		}
	}
	f, err := workspaceNativeOpen(root, name, openFlags, perm)
	if createContender && before == nil && errors.Is(err, os.ErrExist) {
		// Exactly one exclusive creator owns the first inode. Contenders must
		// verify that winner before opening it, never retry arbitrary errors.
		before, err = regularLeaf(root, name, false)
		if err != nil {
			return nil, fmt.Errorf("workspace creation winner verification: %w", err)
		}
		f, err = workspaceNativeOpen(root, name, flags&^(os.O_CREATE|os.O_EXCL), perm)
	}
	if err != nil {
		return nil, fmt.Errorf("workspace verified native open: %w", err)
	}
	info, statErr := f.Stat()
	after, afterErr := regularLeaf(root, name, false)
	if statErr != nil || afterErr != nil || !info.Mode().IsRegular() || !os.SameFile(info, after) || (before != nil && !os.SameFile(before, info)) {
		f.Close()
		return nil, fmt.Errorf("workspace file changed during open")
	}
	links, linkErr := workspaceLinkCount(f)
	if linkErr != nil || links != 1 {
		f.Close()
		return nil, fmt.Errorf("workspace file has unsafe links")
	}
	return f, nil
}

// Existing write targets must obey the same descriptor/link policy as appends.
func writableWorkspaceLeaf(root *os.Root, name string) error {
	info, err := regularLeaf(root, name, true)
	if err != nil {
		return err
	}
	if info == nil {
		return nil
	}
	f, err := openWorkspaceFile(root, name, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	return f.Close()
}

func ReadFile(root *os.Root, name string) ([]byte, error) {
	parent, leaf, err := confinedParent(root, name, false)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	if _, err := regularLeaf(parent, leaf, false); err != nil {
		return nil, err
	}
	// Linearize at the native nofollow open. An atomic rename may unlink the
	// descriptor (Nlink=0) or replace the namespace without changing its bytes.
	f, err := workspaceNativeOpen(parent, leaf, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	info, statErr := f.Stat()
	links, linkErr := workspaceLinkCount(f)
	if statErr != nil || !info.Mode().IsRegular() || linkErr != nil || links > 1 {
		f.Close()
		return nil, fmt.Errorf("workspace file has unsafe type or links")
	}
	data, readErr := io.ReadAll(f)
	closeErr := f.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return data, nil
}

// WriteFile publishes complete bytes by same-directory rename. It does not
// coordinate uncooperative editors; cooperating callers hold LockProject.
func WriteFile(ctx context.Context, root *os.Root, name string, data []byte, perm os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parent, leaf, err := confinedParent(root, name, true)
	if err != nil {
		return err
	}
	defer parent.Close()
	if err := writableWorkspaceLeaf(parent, leaf); err != nil {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := ".workspace-tmp-" + hex.EncodeToString(nonce[:])
	f, err := openWorkspaceFile(parent, temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	defer parent.Remove(temp)
	defer f.Close()
	for offset := 0; offset < len(data); {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := offset + 64*1024
		if end > len(data) {
			end = len(data)
		}
		n, err := f.Write(data[offset:end])
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		offset += n
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writableWorkspaceLeaf(parent, leaf); err != nil {
		return err
	}
	return parent.Rename(temp, leaf)
}

// AppendFile appends one complete audit record using a verified regular
// descriptor. It does not acquire LockProject: middleware may already hold it.
func AppendFile(ctx context.Context, root *os.Root, name string, data []byte, perm os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parent, leaf, err := confinedParent(root, name, true)
	if err != nil {
		return err
	}
	defer parent.Close()
	f, err := openWorkspaceFile(parent, leaf, os.O_WRONLY|os.O_CREATE|os.O_APPEND, perm)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	n, err := f.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return f.Close()
}

// LockProject coordinates cooperating processes across all branches of a project.
// ponytail: per-project serialization is the ceiling; add branch locks only after
// measured contention justifies the additional cross-process ordering.
func LockProject(ctx context.Context, base, projectID string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(projectID) == "" {
		return nil, ErrMissingProjectID
	}
	root, err := OpenRoot(base, ".kb/locks", true)
	if err != nil {
		return nil, err
	}
	f, err := openWorkspaceFile(root, SafeID(projectID)+".lock", os.O_RDWR|os.O_CREATE, 0600)
	root.Close()
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		acquired, err := workspaceTryLock(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		if acquired {
			if err := ctx.Err(); err != nil {
				workspaceUnlock(f)
				f.Close()
				return nil, err
			}
			var once sync.Once
			return func() { once.Do(func() { workspaceUnlock(f); f.Close() }) }, nil
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
