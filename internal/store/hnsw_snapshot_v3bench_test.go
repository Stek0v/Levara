//go:build v3bench

package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type v3LazyState string

const (
	v3LazyIdle    v3LazyState = "idle"
	v3LazyLoading v3LazyState = "loading"
	v3LazyReady   v3LazyState = "ready"
	v3LazyError   v3LazyState = "error"
	v3LazyClosed  v3LazyState = "closed"
)

var errV3LazyClosed = errors.New("lazy store closed")

type v3LazyLevara struct {
	mu        sync.Mutex
	state     v3LazyState
	done      chan struct{}
	db        *Levara
	err       error
	loads     int
	load      func() (*Levara, error)
	closeOnce sync.Once
	closeErr  error
}

func newV3LazyLevara(load func() (*Levara, error)) *v3LazyLevara {
	return &v3LazyLevara{state: v3LazyIdle, load: load}
}

func (l *v3LazyLevara) Search(query []float32, k int) ([]VectroRecord, v3LazyState, error) {
	l.mu.Lock()
	switch l.state {
	case v3LazyIdle:
		l.state, l.done, l.loads = v3LazyLoading, make(chan struct{}), l.loads+1
		go func() {
			db, err := l.load()
			l.mu.Lock()
			if l.state == v3LazyClosed {
				l.mu.Unlock()
				if db != nil {
					err = errors.Join(err, db.Close())
				}
				l.mu.Lock()
				l.closeErr = err
				close(l.done)
				l.mu.Unlock()
				return
			}
			l.db, l.err = db, err
			if err != nil {
				l.state = v3LazyError
			} else {
				l.state = v3LazyReady
			}
			close(l.done)
			l.mu.Unlock()
		}()
		l.mu.Unlock()
		return nil, v3LazyLoading, nil
	case v3LazyLoading:
		l.mu.Unlock()
		return nil, v3LazyLoading, nil
	case v3LazyError:
		err := l.err
		l.mu.Unlock()
		return nil, v3LazyError, err
	case v3LazyClosed:
		l.mu.Unlock()
		return nil, v3LazyClosed, errV3LazyClosed
	default:
		db := l.db
		l.mu.Unlock()
		return db.Search(query, k), v3LazyReady, nil
	}
}

func (l *v3LazyLevara) Wait(ctx context.Context) error {
	l.mu.Lock()
	done, state, err := l.done, l.state, l.err
	l.mu.Unlock()
	if state == v3LazyIdle {
		return errors.New("lazy load not started")
	}
	if state == v3LazyClosed {
		if done == nil {
			return errV3LazyClosed
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return errV3LazyClosed
		}
	}
	if state == v3LazyError || state == v3LazyReady {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.err
	}
}

func (l *v3LazyLevara) close() error {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		state, done, db := l.state, l.done, l.db
		l.state, l.db = v3LazyClosed, nil
		l.mu.Unlock()
		if state == v3LazyLoading {
			<-done
			return
		}
		if db != nil {
			l.closeErr = db.Close()
		}
	})
	return l.closeErr
}

func TestV3LazyLevaraSingleLoadAndExplicitState(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	root := t.TempDir()
	var mu sync.Mutex
	loads := 0
	lazy := newV3LazyLevara(func() (*Levara, error) {
		mu.Lock()
		loads++
		mu.Unlock()
		close(started)
		<-release
		return NewLevara(2, filepath.Join(root, "meta.bin"))
	})
	const callers = 32
	states := make(chan v3LazyState, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, state, err := lazy.Search([]float32{1, 0}, 10)
			if err != nil || results != nil {
				t.Errorf("loading read returned data/error: results=%v err=%v", results, err)
			}
			states <- state
		}()
	}
	<-started
	wg.Wait()
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := lazy.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	close(states)
	for state := range states {
		if state != v3LazyLoading {
			t.Fatalf("first read state=%s want loading", state)
		}
	}
	mu.Lock()
	if loads != 1 || lazy.loads != 1 {
		t.Fatalf("loads=%d internal=%d want 1", loads, lazy.loads)
	}
	mu.Unlock()
	_, state, err := lazy.Search([]float32{1, 0}, 10)
	if err != nil || state != v3LazyReady {
		t.Fatalf("ready read state=%s err=%v", state, err)
	}
	_ = lazy.close()

	wantErr := errors.New("load failed")
	failed := newV3LazyLevara(func() (*Levara, error) { return nil, wantErr })
	if results, state, err := failed.Search([]float32{1, 0}, 1); results != nil || state != v3LazyLoading || err != nil {
		t.Fatalf("failed loader first state=%s results=%v err=%v", state, results, err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := failed.Wait(ctx); !errors.Is(err, wantErr) {
		t.Fatalf("wait error=%v want %v", err, wantErr)
	}
	if results, state, err := failed.Search([]float32{1, 0}, 1); results != nil || state != v3LazyError || !errors.Is(err, wantErr) {
		t.Fatalf("failed loader terminal state=%s results=%v err=%v", state, results, err)
	}
}

func TestV3LazyLevaraCloseDuringLoad(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	root := t.TempDir()
	var loaded *Levara
	lazy := newV3LazyLevara(func() (*Levara, error) {
		close(started)
		<-release
		db, err := NewLevara(2, filepath.Join(root, "meta.bin"))
		loaded = db
		return db, err
	})
	if _, state, err := lazy.Search([]float32{1, 0}, 1); state != v3LazyLoading || err != nil {
		t.Fatalf("start state=%s err=%v", state, err)
	}
	<-started
	closed := make(chan error, 1)
	go func() { closed <- lazy.close() }()
	close(release)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if loaded == nil || !loaded.wal.closed.Load() {
		t.Fatal("database returned after close was not closed")
	}
	if err := lazy.close(); err != nil {
		t.Fatal(err)
	}
	if results, state, err := lazy.Search([]float32{1, 0}, 1); results != nil || state != v3LazyClosed || !errors.Is(err, errV3LazyClosed) {
		t.Fatalf("closed search state=%s results=%v err=%v", state, results, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lazy.Wait(ctx); !errors.Is(err, errV3LazyClosed) {
		t.Fatalf("closed wait error=%v", err)
	}
}
