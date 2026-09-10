//go:build darwin || linux

package backup

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

var ErrSourceActive = errors.New("backup: data root has an active writer or backup")

// Lease owns a stable OS lock. Never unlink the lock file. The server must retain
// its writer lease until process exit, after every background writer has exited.
type Lease struct {
	file *os.File
	once sync.Once
	err  error
}

func (l *Lease) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() { l.err = l.file.Close() })
	return l.err
}
func AcquireWriterLease(dataDir string) (*Lease, error)  { return acquireDataLease(dataDir) }
func AcquireOfflineLease(dataDir string) (*Lease, error) { return acquireDataLease(dataDir) }
func acquireDataLease(dataDir string) (*Lease, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	// A no-follow open prevents replacing the stable lock with an external target.
	f, err := root.OpenFile(".levara-runtime.lock", os.O_RDWR|os.O_CREATE|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("backup: open writer lock: %w", err)
	}
	fd := int(f.Fd())
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, errors.New("backup: invalid writer lock")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrSourceActive
		}
		return nil, err
	}
	return &Lease{file: f}, nil
}
