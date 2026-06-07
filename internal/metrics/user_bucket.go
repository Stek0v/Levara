// Package metrics — user-bucket label helper (T17 / D14).
//
// Prometheus labels with unbounded cardinality are the classic TSDB killer.
// We want per-user visibility for the busiest callers but must not ship
// one time-series per user_id. UserBucket keeps a rolling count per user
// and at refresh time promotes the top-N heaviest users to full-label
// status; everyone else buckets into the "other" label. Anonymous traffic
// ("") hashes to "anon".
//
// The implementation is intentionally simple: an increment-only Observe()
// plus a background refresh that rebuilds the top-N set from accumulated
// counts. Counts reset on every refresh so a burst during one window
// doesn't permanently pin a user.
package metrics

import (
	"sort"
	"sync"
	"time"
)

// UserBucket tracks per-user request counts and maps raw user_ids to a
// bounded label set (top-N users + "other" + "anon").
type UserBucket struct {
	topN int

	mu     sync.RWMutex
	counts map[string]int64    // userID → count since last refresh
	topSet map[string]struct{} // current top-N userIDs

	refreshEvery time.Duration
	stopCh       chan struct{}
	stoppedCh    chan struct{}
}

// NewUserBucket builds a bucket that promotes the topN most active users
// on each refresh tick. refreshEvery <= 0 disables the background ticker;
// you can still call Refresh() manually from tests.
func NewUserBucket(topN int, refreshEvery time.Duration) *UserBucket {
	if topN < 0 {
		topN = 0
	}
	b := &UserBucket{
		topN:         topN,
		counts:       make(map[string]int64),
		topSet:       make(map[string]struct{}),
		refreshEvery: refreshEvery,
	}
	if refreshEvery > 0 {
		b.stopCh = make(chan struct{})
		b.stoppedCh = make(chan struct{})
		go b.loop()
	}
	return b
}

// Label maps a raw user_id to the value that should appear in Prometheus
// labels. Top-N users keep their real ID; everyone else gets "other";
// empty ID (anonymous) gets "anon".
func (b *UserBucket) Label(userID string) string {
	if userID == "" {
		return "anon"
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if _, ok := b.topSet[userID]; ok {
		return userID
	}
	return "other"
}

// Observe increments the activity count for userID. Anonymous traffic is
// not tracked — "anon" is already a stable label and we don't need to
// elevate an "anonymous heavy user" into the top set (that's just overall
// anon traffic).
func (b *UserBucket) Observe(userID string) {
	if userID == "" {
		return
	}
	b.mu.Lock()
	b.counts[userID]++
	b.mu.Unlock()
}

// Refresh recomputes topSet from the current counts and resets the
// counter. Safe to call concurrently with Label/Observe.
func (b *UserBucket) Refresh() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.topN == 0 || len(b.counts) == 0 {
		b.topSet = make(map[string]struct{})
		b.counts = make(map[string]int64)
		return
	}

	// Collect (userID, count) pairs and sort by count descending.
	type entry struct {
		id    string
		count int64
	}
	entries := make([]entry, 0, len(b.counts))
	for id, c := range b.counts {
		entries = append(entries, entry{id, c})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].count > entries[j].count
	})

	n := b.topN
	if n > len(entries) {
		n = len(entries)
	}
	topSet := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		topSet[entries[i].id] = struct{}{}
	}
	b.topSet = topSet
	// Reset counts so bursts in prior windows don't pin a user forever.
	b.counts = make(map[string]int64)
}

// TopSetSize returns the number of currently-promoted user_ids. Exposed
// so a gauge elsewhere can publish levara_user_bucket_size.
func (b *UserBucket) TopSetSize() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.topSet)
}

// Stop halts the background refresh goroutine and waits for it to exit.
// No-op if the bucket was constructed without a ticker.
func (b *UserBucket) Stop() {
	if b.stopCh == nil {
		return
	}
	close(b.stopCh)
	<-b.stoppedCh
	b.stopCh = nil
}

func (b *UserBucket) loop() {
	defer close(b.stoppedCh)
	t := time.NewTicker(b.refreshEvery)
	defer t.Stop()
	for {
		select {
		case <-b.stopCh:
			return
		case <-t.C:
			b.Refresh()
		}
	}
}
