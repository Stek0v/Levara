//go:build darwin || linux

package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/mcp"
	"golang.org/x/sys/unix"
)

// os.Root permits symlinks within its root. An authority grant can be narrower
// than that root, so use descriptor-relative O_NOFOLLOW descent for every
// component, including the manifest and grant directory. The configured base
// directory is trusted; opened directory descriptors are the capabilities.
func taskOpenDir(base *os.File, rel string, create bool) (*os.File, error) {
	fd, err := unix.Dup(int(base.Fd()))
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), "task-directory")
	if rel == "." {
		return current, nil
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			current.Close()
			return nil, errors.New("noncanonical task directory")
		}
		fd, err = unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(err, unix.ENOENT) && create {
			if err = unix.Mkdirat(int(current.Fd()), part, 0755); err != nil && !errors.Is(err, unix.EEXIST) {
				current.Close()
				return nil, err
			}
			fd, err = unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		current.Close()
		if err != nil {
			return nil, errors.New("task directory missing or unsafe")
		}
		current = os.NewFile(uintptr(fd), "task-directory")
	}
	return current, nil
}

func taskReadAt(parent *os.File, name string, limit int64) ([]byte, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "task-file")
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		return nil, errors.New("task reads require a regular file without hard links")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("task file exceeds size limit")
	}
	return data, nil
}

func readConfinedArtifact(ctx context.Context, root, relative string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	base, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	defer base.Close()
	parent, err := taskOpenDir(base, path.Dir(relative), false)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	data, err := taskReadAt(parent, path.Base(relative), 64<<20)
	if err != nil {
		return nil, err
	}
	return data, ctx.Err()
}

func taskWorkspaceFile(ctx context.Context, root string, e *mcp.TaskExecution, rel string, write []byte, expected *string) ([]byte, error) {
	base, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	defer base.Close()
	info, err := base.Stat()
	if err != nil || !info.IsDir() {
		return nil, errors.New("task workspace base is not a directory")
	}
	var binding struct {
		Manifest string `json:"manifest"`
		Digest   string `json:"manifest_sha256"`
	}
	if err = json.Unmarshal([]byte(e.AuthorityJSON), &binding); err != nil {
		return nil, err
	}
	if binding.Manifest == "" || binding.Digest == "" || path.IsAbs(binding.Manifest) || path.Clean(binding.Manifest) != binding.Manifest || strings.HasPrefix(binding.Manifest, "../") {
		return nil, errors.New("task requires a canonical digest-pinned authority manifest")
	}
	manifestDir, err := taskOpenDir(base, path.Dir(binding.Manifest), false)
	if err != nil {
		return nil, err
	}
	defer manifestDir.Close()
	data, err := taskReadAt(manifestDir, path.Base(binding.Manifest), 64<<10)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), binding.Digest) {
		return nil, errors.New("task authority manifest digest changed")
	}
	manifest, err := mcp.ParseAuthorityManifest(data)
	if err != nil {
		return nil, err
	}
	if err = manifest.CheckToolAccess(e.Action.Name); err != nil {
		return nil, err
	}
	// A grant is an existing directory capability; the artifact must descend
	// from it. This intentionally excludes grants naming an individual file.
	grant := ""
	for _, allowed := range manifest.AllowedPaths {
		if allowed == "." || strings.HasPrefix(rel, allowed+"/") {
			if len(allowed) > len(grant) {
				grant = allowed
			}
		}
	}
	if grant == "" {
		return nil, errors.New("task path is outside declared directory grants")
	}
	permitted, err := taskOpenDir(base, grant, false)
	if err != nil {
		return nil, err
	}
	defer permitted.Close()
	within := rel
	if grant != "." {
		within = strings.TrimPrefix(rel, grant+"/")
	}
	// Recheck cancellation before directory creation, then before publication.
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	parent, err := taskOpenDir(permitted, path.Dir(within), write != nil)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	name := path.Base(within)
	if write == nil {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		return taskReadAt(parent, name, 1<<20)
	}
	var st unix.Stat_t
	err = unix.Fstatat(int(parent.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return nil, err
	}
	if err == nil && (st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1) {
		return nil, errors.New("task write target must be a regular file without links")
	}
	alreadyWritten := false
	if expected != nil {
		old, err := taskReadAt(parent, name, 1<<20)
		current := ""
		if err == nil {
			current = digestBytes(old)
		} else if !errors.Is(err, unix.ENOENT) {
			return nil, err
		}
		if current != *expected {
			// A previous attempt may have published this action before its
			// receipt. Reconcile identical bytes only; retain every gate below.
			if err != nil || !bytes.Equal(old, write) {
				return nil, errors.New("workspace write conflict: expected_file_digest mismatch")
			}
			alreadyWritten = true
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	temp := ".task-" + uuid.NewString()
	fd, err := unix.Openat(int(parent.Fd()), temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	defer unix.Unlinkat(int(parent.Fd()), temp, 0)
	f := os.NewFile(uintptr(fd), "task-artifact")
	_, err = f.Write(write)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	currentManifest, err := taskReadAt(manifestDir, path.Base(binding.Manifest), 64<<10)
	if err != nil {
		return nil, err
	}
	currentSum := sha256.Sum256(currentManifest)
	if currentSum != sum {
		return nil, errors.New("task authority manifest changed before publication")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if alreadyWritten {
		return nil, nil
	}
	if err = unix.Renameat(int(parent.Fd()), temp, int(parent.Fd()), name); err != nil {
		return nil, fmt.Errorf("task artifact publication: %w", err)
	}
	return nil, nil
}
