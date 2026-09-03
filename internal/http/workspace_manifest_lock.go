package http

import "sync"

// workspaceManifestLocks serializes load-modify-save cycles on workspace
// manifests per project/branch (finding H6, 2026-09-03 review): manifest
// updates are read-modify-write; without a keyed lock, concurrent
// index/reconcile/watch/GC operations lose each other's chunks or
// generation activation state. Callers must hold the lock for the whole
// load→mutate→Save sequence.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newKeyedMutex() *keyedMutex { return &keyedMutex{locks: map[string]*sync.Mutex{}} }

func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*sync.Mutex{}
	}
	m, ok := k.locks[key]
	if !ok {
		m = &sync.Mutex{}
		k.locks[key] = m
	}
	k.mu.Unlock()
	m.Lock()
	return m.Unlock
}

var workspaceManifestLocks = newKeyedMutex()
