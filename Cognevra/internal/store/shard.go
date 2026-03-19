package store

import (
	"hash/fnv"
	"sort"
	"sync"
)

// BatchItem is a single record for batch operations.
// Defined in store so ShardHandler doesn't import cluster.
type BatchItem struct {
	ID     string
	Vector []float32
	Data   interface{}
}

type ShardHandler interface {
	Insert(id string, vector []float32, data interface{}) error
	// BatchInsert inserts multiple records in a single consensus round-trip.
	// Returns a (possibly empty) slice of per-record errors.
	BatchInsert(records []BatchItem) []error
	Search(query []float32, topK int) []VectroRecord
	Delete(id string) error
	BatchDelete(ids []string) []error
}

type Cluster struct {
	shards    []ShardHandler
	numShards int
}

func NewCluster(shards []ShardHandler) *Cluster {
	return &Cluster{
		shards:    shards,
		numShards: len(shards),
	}
}

func (c *Cluster) NumShards() int { return c.numShards }

func (c *Cluster) getShard(id string) ShardHandler {
	h := fnv.New32a()
	h.Write([]byte(id))
	idx := int(h.Sum32()) % c.numShards
	if idx < 0 {
		idx = -idx
	}
	return c.shards[idx]
}

func (c *Cluster) Insert(id string, vector []float32, data any) error {
	targetShard := c.getShard(id)
	return targetShard.Insert(id, vector, data)
}

// BatchInsert groups records by their target shard, then issues one
// BatchInsert per shard concurrently — reducing Raft round-trips from
// len(records) down to min(len(records), numShards).
func (c *Cluster) BatchInsert(items []BatchItem) []error {
	// Group items by shard index
	groups := make([][]BatchItem, c.numShards)
	for i := range groups {
		groups[i] = []BatchItem{}
	}
	for _, item := range items {
		h := fnv.New32a()
		h.Write([]byte(item.ID))
		idx := int(h.Sum32()) % c.numShards
		if idx < 0 {
			idx = -idx
		}
		groups[idx] = append(groups[idx], item)
	}

	// Fire one BatchInsert per shard concurrently
	type result struct {
		errs []error
	}
	resultCh := make(chan result, c.numShards)
	var wg sync.WaitGroup

	for i, group := range groups {
		if len(group) == 0 {
			continue
		}
		wg.Add(1)
		go func(shard ShardHandler, batch []BatchItem) {
			defer wg.Done()
			resultCh <- result{errs: shard.BatchInsert(batch)}
		}(c.shards[i], group)
	}

	wg.Wait()
	close(resultCh)

	var allErrs []error
	for r := range resultCh {
		allErrs = append(allErrs, r.errs...)
	}
	return allErrs
}

func (c *Cluster) Delete(id string) error {
	return c.getShard(id).Delete(id)
}

func (c *Cluster) BatchDelete(ids []string) []error {
	var allErrs []error
	for _, id := range ids {
		if err := c.Delete(id); err != nil {
			allErrs = append(allErrs, err)
		}
	}
	return allErrs
}

func (c *Cluster) Search(query []float32, topK int) []VectroRecord {
	var wg sync.WaitGroup

	resultCh := make(chan []VectroRecord, c.numShards)

	for _, shard := range c.shards {
		wg.Add(1)
		go func(s ShardHandler) {
			defer wg.Done()
			resultCh <- s.Search(query, topK)
		}(shard)
	}

	wg.Wait()
	close(resultCh)

	allMatches := make([]VectroRecord, 0, topK*c.numShards)
	for shardResults := range resultCh {
		allMatches = append(allMatches, shardResults...)
	}

	sort.Slice(allMatches, func(i, j int) bool {
		return allMatches[i].Score > allMatches[j].Score
	})

	if len(allMatches) > topK {
		return allMatches[:topK]
	}
	return allMatches
}
