// Package bm25 provides an in-memory BM25 inverted index for keyword/lexical search.
//
// BM25 (Best Matching 25) scores documents by term frequency and inverse document
// frequency. Combined with vector search (hybrid), it catches exact keyword matches
// that embedding models sometimes miss.
//
// Parameters:
//   - k1 = 1.2 (term frequency saturation)
//   - b  = 0.75 (length normalization)
package bm25

import (
	"container/heap"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"
)

const (
	defaultK1 = 1.2
	defaultB  = 0.75
)

// Document is an indexed document.
type Document struct {
	ID       string
	Text     string
	Metadata string // JSON metadata (passed through)
	tokens   []string
}

// Change describes a mutation applied to an index.
type Change struct {
	Op       string
	ID       string
	Text     string
	Metadata string
}

// Result is a BM25 search result.
type Result struct {
	ID       string
	Score    float64
	Metadata string
}

// resultHeap keeps the lowest retained score at the root.
type resultHeap []Result

func (h resultHeap) Len() int           { return len(h) }
func (h resultHeap) Less(i, j int) bool { return h[i].Score < h[j].Score }
func (h resultHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *resultHeap) Push(value any)    { *h = append(*h, value.(Result)) }
func (h *resultHeap) Pop() any {
	last := len(*h) - 1
	value := (*h)[last]
	*h = (*h)[:last]
	return value
}

// Index is a thread-safe BM25 inverted index.
type Index struct {
	changeMu    sync.Mutex // orders mutations and durable snapshot publication
	mu          sync.RWMutex
	docs        map[string]*Document      // id → doc
	inverted    map[string]map[string]int // term → {docID → term_freq}
	docLen      map[string]int            // id → token count
	totalTokens int
	avgDL       float64 // average document length
	k1          float64
	b           float64
	onChange    func(Change)
	maxDocs     int  // 0 = unlimited; new documents beyond this are dropped (vectors-only tier)
	capped      bool // warned once that the cap is hit (guarded by changeMu)
}

// NewIndex creates an empty BM25 index with default parameters (k1=1.2, b=0.75).
func NewIndex() *Index {
	return NewIndexWithParams(defaultK1, defaultB)
}

// NewIndexWithParams creates a BM25 index with custom k1 and b parameters.
// k1 controls term frequency saturation (higher = more weight to term freq, typical 1.2-2.0).
// b controls length normalization (0 = no normalization, 1 = full normalization, typical 0.75).
func NewIndexWithParams(k1, b float64) *Index {
	if k1 <= 0 {
		k1 = defaultK1
	}
	if b < 0 || b > 1 {
		b = defaultB
	}
	return &Index{
		docs:     make(map[string]*Document),
		inverted: make(map[string]map[string]int),
		docLen:   make(map[string]int),
		k1:       k1,
		b:        b,
		maxDocs:  bm25IndexMaxDocs(),
	}
}

// bm25IndexMaxDocs mirrors the snapshot threshold
// (LEVARA_BM25_SNAPSHOT_MAX_DOCS, default 100k): collections past the
// threshold are vectors-only tier. Keeping the chat-imports index resident
// cost 12.2 GB of heap on prod (3.6 GB snapshot → 12.2 GB of tokens,
// posting lists and strings), so index growth is capped at the same line.
func bm25IndexMaxDocs() int { return bm25SnapshotMaxDocs }

// SetMaxDocs overrides the in-memory document cap (0 = unlimited).
func (idx *Index) SetMaxDocs(max int) {
	idx.changeMu.Lock()
	idx.maxDocs = max
	idx.changeMu.Unlock()
}

// Add indexes a document. If ID exists, it replaces it. New documents are
// dropped once the index holds maxDocs documents (replacements of existing
// IDs always pass — derivative re-renders update in place).
func (idx *Index) Add(id, text, metadata string) {
	idx.changeMu.Lock()
	defer idx.changeMu.Unlock()
	if idx.maxDocs > 0 {
		idx.mu.RLock()
		_, exists := idx.docs[id]
		over := len(idx.docs) >= idx.maxDocs
		idx.mu.RUnlock()
		if over && !exists {
			if !idx.capped {
				idx.capped = true
				log.Printf("[bm25] index capped at %d docs — new documents dropped, replacements still accepted", idx.maxDocs)
			}
			return
		}
	}
	tokens := tokenize(text)

	idx.mu.Lock()
	// Remove old entry if exists
	if old, exists := idx.docs[id]; exists {
		idx.removeTokens(id, old.tokens)
	}

	doc := &Document{ID: id, Text: text, Metadata: metadata, tokens: tokens}
	idx.docs[id] = doc
	idx.totalTokens += len(tokens) - idx.docLen[id]
	idx.docLen[id] = len(tokens)

	// Update inverted index
	tf := make(map[string]int)
	for _, t := range tokens {
		tf[t]++
	}
	for term, count := range tf {
		if idx.inverted[term] == nil {
			idx.inverted[term] = make(map[string]int)
		}
		idx.inverted[term][id] = count
	}

	// Update average doc length
	idx.recalcAvgDL()
	onChange := idx.onChange
	idx.mu.Unlock()

	if onChange != nil {
		onChange(Change{Op: "put", ID: id, Text: text, Metadata: metadata})
	}
}

// AddBatch indexes multiple documents.
func (idx *Index) AddBatch(docs []Document) {
	for _, d := range docs {
		idx.Add(d.ID, d.Text, d.Metadata)
	}
}

// Remove deletes a document from the index.
func (idx *Index) Remove(id string) {
	idx.changeMu.Lock()
	defer idx.changeMu.Unlock()
	idx.mu.Lock()

	doc, exists := idx.docs[id]
	if !exists {
		idx.mu.Unlock()
		return
	}

	idx.removeTokens(id, doc.tokens)
	delete(idx.docs, id)
	idx.totalTokens -= idx.docLen[id]
	delete(idx.docLen, id)
	idx.recalcAvgDL()
	onChange := idx.onChange
	idx.mu.Unlock()

	if onChange != nil {
		onChange(Change{Op: "delete", ID: id})
	}
}

// Search returns top-k documents ranked by BM25 score for the query.
func (idx *Index) Search(query string, topK int) []Result {
	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		return nil
	}
	if topK <= 0 {
		topK = 10
	}

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	N := float64(len(idx.docs))
	if N == 0 {
		return nil
	}

	scores := make(map[string]float64)

	for _, term := range queryTokens {
		postings, ok := idx.inverted[term]
		if !ok {
			continue
		}

		// IDF: log((N - n + 0.5) / (n + 0.5) + 1)
		n := float64(len(postings))
		idf := math.Log((N-n+0.5)/(n+0.5) + 1)

		for docID, freq := range postings {
			dl := float64(idx.docLen[docID])
			// BM25 TF: (f * (k1 + 1)) / (f + k1 * (1 - b + b * dl/avgdl))
			f := float64(freq)
			tf := (f * (idx.k1 + 1)) / (f + idx.k1*(1-idx.b+idx.b*dl/idx.avgDL))
			scores[docID] += idf * tf
		}
	}

	if topK >= len(scores) {
		results := make([]Result, 0, len(scores))
		for id, score := range scores {
			results = append(results, Result{ID: id, Score: score, Metadata: idx.docs[id].Metadata})
		}
		sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
		return results
	}

	results := make(resultHeap, 0, topK)
	for id, score := range scores {
		if len(results) < topK {
			results = append(results, Result{ID: id, Score: score})
			if len(results) == topK {
				heap.Init(&results)
			}
			continue
		}
		if score > results[0].Score {
			results[0] = Result{ID: id, Score: score}
			heap.Fix(&results, 0)
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	for i := range results {
		results[i].Metadata = idx.docs[results[i].ID].Metadata
	}

	return results
}

// Size returns the number of indexed documents.
func (idx *Index) Size() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.docs)
}

// Documents returns a stable snapshot of indexed documents.
// Len returns the number of indexed documents (for memory-guard checks).
func (idx *Index) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.docs)
}

func (idx *Index) Documents() []Document {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	docs := make([]Document, 0, len(idx.docs))
	for _, doc := range idx.docs {
		docs = append(docs, Document{
			ID:       doc.ID,
			Text:     doc.Text,
			Metadata: doc.Metadata,
		})
	}
	return docs
}

// Clear removes all documents from the index.
func (idx *Index) Clear() {
	idx.changeMu.Lock()
	defer idx.changeMu.Unlock()
	idx.mu.Lock()
	idx.docs = make(map[string]*Document)
	idx.inverted = make(map[string]map[string]int)
	idx.docLen = make(map[string]int)
	idx.avgDL = 0
	idx.totalTokens = 0
	onChange := idx.onChange
	idx.mu.Unlock()

	if onChange != nil {
		onChange(Change{Op: "clear"})
	}
}

// SetOnChange registers a synchronous mutation callback.
func (idx *Index) SetOnChange(fn func(Change)) {
	idx.changeMu.Lock()
	defer idx.changeMu.Unlock()
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.onChange = fn
}

func (idx *Index) removeTokens(id string, tokens []string) {
	tf := make(map[string]int)
	for _, t := range tokens {
		tf[t]++
	}
	for term := range tf {
		if postings, ok := idx.inverted[term]; ok {
			delete(postings, id)
			if len(postings) == 0 {
				delete(idx.inverted, term)
			}
		}
	}
}

func (idx *Index) recalcAvgDL() {
	if len(idx.docLen) == 0 {
		idx.avgDL = 0
		return
	}
	idx.avgDL = float64(idx.totalTokens) / float64(len(idx.docLen))
}

// tokenize splits text into lowercase tokens, removing punctuation.
func tokenize(text string) []string {
	text = strings.ToLower(text)
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	// Filter very short tokens
	result := make([]string, 0, len(words))
	for _, w := range words {
		if len(w) >= 2 {
			result = append(result, w)
		}
	}
	return result
}
