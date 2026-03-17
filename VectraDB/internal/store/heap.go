package store

type Match struct {
	Index uint32
	Score float32
}

// ── MinHeap (top = smallest score) ──────────────────────────────────────────

type MinHeap []Match

func (h *MinHeap) Len() int { return len(*h) }

func (h *MinHeap) Push(m Match) {
	*h = append(*h, m)
	h.up(len(*h) - 1)
}

func (h *MinHeap) Pop() Match {
	old := *h
	n := len(old)
	top := old[0]
	old[0] = old[n-1]
	*h = old[:n-1]
	if len(*h) > 0 {
		h.down(0, len(*h))
	}
	return top
}

func (h *MinHeap) Peek() Match { return (*h)[0] }

func (h *MinHeap) Replace(m Match) {
	(*h)[0] = m
	h.down(0, len(*h))
}

func (h *MinHeap) up(j int) {
	for {
		i := (j - 1) / 2
		if i == j || (*h)[j].Score >= (*h)[i].Score {
			break
		}
		(*h)[i], (*h)[j] = (*h)[j], (*h)[i]
		j = i
	}
}

func (h *MinHeap) down(i0, n int) {
	i := i0
	for {
		j1 := 2*i + 1
		if j1 >= n || j1 < 0 {
			break
		}
		j := j1
		if j2 := j1 + 1; j2 < n && (*h)[j2].Score < (*h)[j1].Score {
			j = j2
		}
		if (*h)[i].Score <= (*h)[j].Score {
			break
		}
		(*h)[i], (*h)[j] = (*h)[j], (*h)[i]
		i = j
	}
}

// ── MaxHeap (top = largest score, for pruning results) ──────────────────────

type MaxHeap []Match

func (h *MaxHeap) Len() int { return len(*h) }

func (h *MaxHeap) Push(m Match) {
	*h = append(*h, m)
	h.up(len(*h) - 1)
}

func (h *MaxHeap) Pop() Match {
	old := *h
	n := len(old)
	top := old[0]
	old[0] = old[n-1]
	*h = old[:n-1]
	if len(*h) > 0 {
		h.down(0, len(*h))
	}
	return top
}

func (h *MaxHeap) Peek() Match { return (*h)[0] }

// Replace replaces the top element and re-heapifies.
func (h *MaxHeap) Replace(m Match) {
	(*h)[0] = m
	h.down(0, len(*h))
}

func (h *MaxHeap) up(j int) {
	for {
		i := (j - 1) / 2
		if i == j || (*h)[j].Score <= (*h)[i].Score {
			break
		}
		(*h)[i], (*h)[j] = (*h)[j], (*h)[i]
		j = i
	}
}

func (h *MaxHeap) down(i0, n int) {
	i := i0
	for {
		j1 := 2*i + 1
		if j1 >= n || j1 < 0 {
			break
		}
		j := j1
		if j2 := j1 + 1; j2 < n && (*h)[j2].Score > (*h)[j1].Score {
			j = j2
		}
		if (*h)[i].Score >= (*h)[j].Score {
			break
		}
		(*h)[i], (*h)[j] = (*h)[j], (*h)[i]
		i = j
	}
}

// ── searchResult for HNSW (node + precomputed distance) ─────────────────────

type searchResult struct {
	node *HNSWNode
	d    float32
}

type srMinHeap []searchResult

func (h *srMinHeap) Len() int { return len(*h) }

func (h *srMinHeap) Push(s searchResult) {
	*h = append(*h, s)
	h.up(len(*h) - 1)
}

func (h *srMinHeap) Pop() searchResult {
	old := *h
	n := len(old)
	top := old[0]
	old[0] = old[n-1]
	*h = old[:n-1]
	if len(*h) > 0 {
		h.down(0, len(*h))
	}
	return top
}

func (h *srMinHeap) Peek() searchResult { return (*h)[0] }

func (h *srMinHeap) up(j int) {
	for {
		i := (j - 1) / 2
		if i == j || (*h)[j].d >= (*h)[i].d {
			break
		}
		(*h)[i], (*h)[j] = (*h)[j], (*h)[i]
		j = i
	}
}

func (h *srMinHeap) down(i0, n int) {
	i := i0
	for {
		j1 := 2*i + 1
		if j1 >= n || j1 < 0 {
			break
		}
		j := j1
		if j2 := j1 + 1; j2 < n && (*h)[j2].d < (*h)[j1].d {
			j = j2
		}
		if (*h)[i].d <= (*h)[j].d {
			break
		}
		(*h)[i], (*h)[j] = (*h)[j], (*h)[i]
		i = j
	}
}

type srMaxHeap []searchResult

func (h *srMaxHeap) Len() int { return len(*h) }

func (h *srMaxHeap) Push(s searchResult) {
	*h = append(*h, s)
	h.up(len(*h) - 1)
}

func (h *srMaxHeap) Pop() searchResult {
	old := *h
	n := len(old)
	top := old[0]
	old[0] = old[n-1]
	*h = old[:n-1]
	if len(*h) > 0 {
		h.down(0, len(*h))
	}
	return top
}

func (h *srMaxHeap) Peek() searchResult { return (*h)[0] }

func (h *srMaxHeap) Replace(s searchResult) {
	(*h)[0] = s
	h.down(0, len(*h))
}

func (h *srMaxHeap) up(j int) {
	for {
		i := (j - 1) / 2
		if i == j || (*h)[j].d <= (*h)[i].d {
			break
		}
		(*h)[i], (*h)[j] = (*h)[j], (*h)[i]
		j = i
	}
}

func (h *srMaxHeap) down(i0, n int) {
	i := i0
	for {
		j1 := 2*i + 1
		if j1 >= n || j1 < 0 {
			break
		}
		j := j1
		if j2 := j1 + 1; j2 < n && (*h)[j2].d > (*h)[j1].d {
			j = j2
		}
		if (*h)[i].d >= (*h)[j].d {
			break
		}
		(*h)[i], (*h)[j] = (*h)[j], (*h)[i]
		i = j
	}
}
