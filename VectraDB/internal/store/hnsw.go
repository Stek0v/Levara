package store

import (
	"encoding/json"
	"math"
	"math/rand"
	"sync"
)

const (
	HNSW_M         = 16       // Max Neighbours per node
	MNSW_M0        = 32       // Max Neighbours at layer 0 (usually 2*M)
	HNSW_LevelMult = 1 / 0.69 // Normalization factor for level generation
)

type HNSWNode struct {
	ID          string
	Layer       int
	Connections [][]uint32 // [Level][arenaOffset] — uint32 for zero-alloc lookups
	ArenaOffset uint32
	sync.RWMutex
}

type HNSWIndex struct {
	EntryNodeID string
	Nodes       map[string]*HNSWNode
	nodesByIdx  []*HNSWNode // direct lookup by ArenaOffset — no map hashing
	MaxLayer    int
	Arena       *VectorArena
	sync.RWMutex
}

// visitedPool recycles visited sets to avoid per-search map allocations
var visitedPool = sync.Pool{
	New: func() any {
		return make(map[uint32]struct{}, 128)
	},
}

func acquireVisited() map[uint32]struct{} {
	m := visitedPool.Get().(map[uint32]struct{})
	for k := range m {
		delete(m, k)
	}
	return m
}

func releaseVisited(m map[uint32]struct{}) {
	visitedPool.Put(m)
}

// Return a new HNSW Index Tree
func NewHNSWIndex(arena *VectorArena) *HNSWIndex {
	return &HNSWIndex{
		Nodes:      make(map[string]*HNSWNode),
		nodesByIdx: make([]*HNSWNode, 0, 4096),
		MaxLayer:   -1,
		Arena:      arena,
	}
}

// normalizeVec returns an L2-normalized copy of v.
func normalizeVec(v []float32) []float32 {
	var mag2 float32
	for _, x := range v {
		mag2 += x * x
	}
	if mag2 == 0 || (mag2 > 0.999 && mag2 < 1.001) {
		return v // already unit-length (or zero)
	}
	inv := float32(1.0 / math.Sqrt(float64(mag2)))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

// randomLevel generates a level for a new node (Geometric Distribution)
func (h *HNSWIndex) randomLevel() int {
	lvl := 0
	for rand.Float64() < 0.5 {
		lvl++
	}
	return lvl
}

// dist computes dot-product distance (1 - dot(v1, v2)) with 4-way loop unrolling.
// Vectors MUST be L2-normalized (Arena normalizes on insert).
// For unit vectors: dot product = cosine similarity, so this is cosine distance.
// Lower values = more similar. Identical → 0, orthogonal → 1, opposite → 2.
func dist(v1, v2 []float32) float32 {
	n := len(v1)
	var d0, d1, d2, d3 float32

	i := 0
	for ; i+3 < n; i += 4 {
		d0 += v1[i] * v2[i]
		d1 += v1[i+1] * v2[i+1]
		d2 += v1[i+2] * v2[i+2]
		d3 += v1[i+3] * v2[i+3]
	}
	for ; i < n; i++ {
		d0 += v1[i] * v2[i]
	}
	return 1 - (d0 + d1 + d2 + d3)
}

// nodeByOffset returns node by ArenaOffset via direct slice lookup.
func (h *HNSWIndex) nodeByOffset(offset uint32) *HNSWNode {
	if int(offset) < len(h.nodesByIdx) {
		return h.nodesByIdx[offset]
	}
	return nil
}

// registerNode indexes a node for O(1) lookup by ArenaOffset.
func (h *HNSWIndex) registerNode(node *HNSWNode) {
	idx := int(node.ArenaOffset)
	if idx >= len(h.nodesByIdx) {
		newSlice := make([]*HNSWNode, idx+1024)
		copy(newSlice, h.nodesByIdx)
		h.nodesByIdx = newSlice
	}
	h.nodesByIdx[idx] = node
}

// vecFn abstracts arena access: GetNoLock during Add (write-locked),
// GetUnsafe during Search (read-locked).
type vecFn func(offset uint32) []float32

func (h *HNSWIndex) vecNoLock(offset uint32) []float32 {
	return h.Arena.GetNoLock(offset)
}

func (h *HNSWIndex) vecUnsafe(offset uint32) []float32 {
	v, _ := h.Arena.GetUnsafe(offset)
	return v
}

// searchLayer finds the closest node to query in a specific layer (greedy)
func (h *HNSWIndex) searchLayer(query []float32, entryPoint *HNSWNode, layer int, getVec vecFn) *HNSWNode {
	curr := entryPoint
	minDist := dist(query, getVec(curr.ArenaOffset))

	for {
		changed := false
		curr.RLock()
		friends := curr.Connections[layer]
		curr.RUnlock()

		for _, fOffset := range friends {
			friendNode := h.nodeByOffset(fOffset)
			if friendNode == nil {
				continue
			}
			d := dist(query, getVec(friendNode.ArenaOffset))
			if d < minDist {
				minDist = d
				curr = friendNode
				changed = true
			}
		}

		if !changed {
			break
		}
	}
	return curr
}

// Add inserts a new node to the HNSW graph with M-neighbor connections.
// Uses GetNoLock since we hold the exclusive write lock — no concurrent arena modifications.
func (h *HNSWIndex) Add(vector []float32, id string, idx uint32) {
	h.Lock()
	defer h.Unlock()

	if _, exists := h.Nodes[id]; exists {
		return
	}

	level := h.randomLevel()
	newNode := &HNSWNode{
		ID:          id,
		Layer:       level,
		Connections: make([][]uint32, level+1),
		ArenaOffset: idx,
	}
	// Pre-allocate connection slices with expected capacity
	for l := 0; l <= level; l++ {
		cap := HNSW_M
		if l == 0 {
			cap = MNSW_M0
		}
		newNode.Connections[l] = make([]uint32, 0, cap)
	}

	h.Nodes[id] = newNode
	h.registerNode(newNode)

	if h.EntryNodeID == "" {
		h.EntryNodeID = id
		h.MaxLayer = level
		return
	}

	getVec := h.vecNoLock
	curr := h.Nodes[h.EntryNodeID]

	// Zoom Phase: greedy search from top layer down to node's level
	for l := h.MaxLayer; l > level; l-- {
		curr = h.searchLayer(vector, curr, l, getVec)
	}

	startLayer := level
	if h.MaxLayer < level {
		startLayer = h.MaxLayer
	}

	// Build Phase: find M neighbors at each layer and create bidirectional links
	for l := startLayer; l >= 0; l-- {
		maxConn := HNSW_M
		if l == 0 {
			maxConn = MNSW_M0
		}

		neighbors := h.searchLayerTopK(vector, curr, l, maxConn, getVec)

		for _, sr := range neighbors {
			newNode.Connections[l] = append(newNode.Connections[l], sr.node.ArenaOffset)

			sr.node.Lock()
			sr.node.Connections[l] = append(sr.node.Connections[l], idx)
			if len(sr.node.Connections[l]) > maxConn {
				h.pruneConnections(sr.node, l, maxConn, getVec)
			}
			sr.node.Unlock()
		}

		if len(neighbors) > 0 {
			curr = neighbors[0].node
		}
	}

	if level > h.MaxLayer {
		h.MaxLayer = level
		h.EntryNodeID = id
	}
}

// pruneConnections keeps only the closest maxConn neighbors using insertion sort.
func (h *HNSWIndex) pruneConnections(node *HNSWNode, layer, maxConn int, getVec vecFn) {
	conns := node.Connections[layer]
	if len(conns) <= maxConn {
		return
	}

	nodeVec := getVec(node.ArenaOffset)

	type scored struct {
		offset uint32
		d      float32
	}
	items := make([]scored, 0, len(conns))
	for _, offset := range conns {
		cn := h.nodeByOffset(offset)
		if cn == nil {
			continue
		}
		items = append(items, scored{offset, dist(nodeVec, getVec(cn.ArenaOffset))})
	}

	if len(items) <= maxConn {
		newConns := make([]uint32, len(items))
		for i, s := range items {
			newConns[i] = s.offset
		}
		node.Connections[layer] = newConns
		return
	}

	// Insertion sort — optimal for small N (M=16..33)
	for i := 1; i < len(items); i++ {
		key := items[i]
		j := i - 1
		for j >= 0 && items[j].d > key.d {
			items[j+1] = items[j]
			j--
		}
		items[j+1] = key
	}

	newConns := make([]uint32, maxConn)
	for i := 0; i < maxConn; i++ {
		newConns[i] = items[i].offset
	}
	node.Connections[layer] = newConns
}

// searchLayerTopK performs beam search at the given layer using heap-based
// candidate/result management with pooled visited maps.
func (h *HNSWIndex) searchLayerTopK(query []float32, entry *HNSWNode, layer, ef int, getVec vecFn) []searchResult {
	visited := acquireVisited()
	defer releaseVisited(visited)

	visited[entry.ArenaOffset] = struct{}{}

	entryDist := dist(query, getVec(entry.ArenaOffset))

	candidates := srMinHeap{{entry, entryDist}}
	results := srMaxHeap{{entry, entryDist}}

	for candidates.Len() > 0 {
		best := candidates.Pop()

		if results.Len() >= ef && best.d > results.Peek().d {
			break
		}

		best.node.RLock()
		friends := best.node.Connections[layer]
		best.node.RUnlock()

		for _, fOffset := range friends {
			if _, seen := visited[fOffset]; seen {
				continue
			}
			visited[fOffset] = struct{}{}
			fNode := h.nodeByOffset(fOffset)
			if fNode == nil {
				continue
			}
			fDist := dist(query, getVec(fNode.ArenaOffset))

			if results.Len() < ef {
				results.Push(searchResult{fNode, fDist})
				candidates.Push(searchResult{fNode, fDist})
			} else if fDist < results.Peek().d {
				results.Replace(searchResult{fNode, fDist})
				candidates.Push(searchResult{fNode, fDist})
			}
		}
	}

	out := make([]searchResult, results.Len())
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = results.Pop()
	}
	return out
}

// Search finds and returns the k closest nodes to the query vector.
func (h *HNSWIndex) Search(query []float32, k int) []VectroRecord {
	// Normalize query so dot-product distance works correctly.
	normQ := normalizeVec(query)

	h.RLock()
	entryID := h.EntryNodeID
	maxL := h.MaxLayer
	h.RUnlock()

	if entryID == "" {
		return nil
	}

	efSearch := k * 8
	if efSearch < 64 {
		efSearch = 64
	}

	query = normQ
	getVec := h.vecUnsafe

	h.RLock()
	curr := h.Nodes[entryID]
	h.RUnlock()

	for l := maxL; l > 0; l-- {
		curr = h.searchLayer(query, curr, l, getVec)
	}

	topResults := h.searchLayerTopK(query, curr, 0, efSearch, getVec)
	if len(topResults) > k {
		topResults = topResults[:k]
	}

	records := make([]VectroRecord, 0, len(topResults))
	for _, sr := range topResults {
		records = append(records, VectroRecord{
			ID:    sr.node.ID,
			Score: 1 - sr.d,
			Data:  json.RawMessage("{}"),
		})
	}
	return records
}
