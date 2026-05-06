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

// Result is a BM25 search result.
type Result struct {
	ID       string
	Score    float64
	Metadata string
}

// Index is a thread-safe BM25 inverted index.
type Index struct {
	mu       sync.RWMutex
	docs     map[string]*Document      // id → doc
	inverted map[string]map[string]int // term → {docID → term_freq}
	docLen   map[string]int            // id → token count
	avgDL    float64                   // average document length
	k1       float64
	b        float64
}

// NewIndex creates an empty BM25 index.
func NewIndex() *Index {
	return &Index{
		docs:     make(map[string]*Document),
		inverted: make(map[string]map[string]int),
		docLen:   make(map[string]int),
		k1:       defaultK1,
		b:        defaultB,
	}
}

// Add indexes a document. If ID exists, it replaces it.
func (idx *Index) Add(id, text, metadata string) {
	tokens := tokenize(text)

	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Remove old entry if exists
	if old, exists := idx.docs[id]; exists {
		idx.removeTokens(id, old.tokens)
	}

	doc := &Document{ID: id, Text: text, Metadata: metadata, tokens: tokens}
	idx.docs[id] = doc
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
}

// AddBatch indexes multiple documents.
func (idx *Index) AddBatch(docs []Document) {
	for _, d := range docs {
		idx.Add(d.ID, d.Text, d.Metadata)
	}
}

// Remove deletes a document from the index.
func (idx *Index) Remove(id string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	doc, exists := idx.docs[id]
	if !exists {
		return
	}

	idx.removeTokens(id, doc.tokens)
	delete(idx.docs, id)
	delete(idx.docLen, id)
	idx.recalcAvgDL()
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

	// Sort by score descending
	results := make([]Result, 0, len(scores))
	for id, score := range scores {
		results = append(results, Result{
			ID:       id,
			Score:    score,
			Metadata: idx.docs[id].Metadata,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if len(results) > topK {
		results = results[:topK]
	}

	return results
}

// Size returns the number of indexed documents.
func (idx *Index) Size() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.docs)
}

// Clear removes all documents from the index.
func (idx *Index) Clear() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.docs = make(map[string]*Document)
	idx.inverted = make(map[string]map[string]int)
	idx.docLen = make(map[string]int)
	idx.avgDL = 0
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
	total := 0
	for _, l := range idx.docLen {
		total += l
	}
	idx.avgDL = float64(total) / float64(len(idx.docLen))
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
