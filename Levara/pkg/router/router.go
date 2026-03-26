// Package router provides heuristic search type routing for Levara.
//
// Analyzes query text using lightweight signals (regex, string patterns)
// and selects the optimal search strategy given available backends.
// Overhead: ~50-200μs per call, no LLM or network calls.
package router

import (
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/stek0v/cognevra/pkg/temporal"
)

// Capabilities describes which backends are available for search.
type Capabilities struct {
	HasEmbedding bool
	HasBM25      bool
	HasNeo4j     bool
	HasLLM       bool
	HasPostgres  bool
	AllowCypher  bool
}

// Alternative is a candidate search type that was considered but not selected.
type Alternative struct {
	SearchType string  `json:"search_type"`
	Score      float32 `json:"score"`
}

// Decision is the routing result.
type Decision struct {
	SearchType   string        `json:"search_type"`
	Reason       string        `json:"reason"`
	Confidence   float32       `json:"confidence"`
	Alternatives []Alternative `json:"alternatives,omitempty"`
}

// candidate accumulates score for a search type during routing.
type candidate struct {
	searchType string
	score      float32
	reason     string
}

// Route analyzes the query and returns the best search type for the given capabilities.
func Route(query string, caps Capabilities) Decision {
	if query == "" {
		return fallback(caps, "empty query")
	}

	q := strings.TrimSpace(query)
	qLower := strings.ToLower(q)
	words := strings.Fields(qLower)

	var candidates []candidate

	// Signal 1: Cypher literal
	if isCypher(qLower) && caps.AllowCypher && caps.HasNeo4j {
		candidates = append(candidates, candidate{"CYPHER", 1.0, "query is a Cypher statement"})
	}

	// Signal 2: Temporal — dates detected in query
	if events := temporal.ExtractTimestamps(q, time.Now()); len(events) > 0 {
		if caps.HasNeo4j || caps.HasPostgres {
			best := events[0].Confidence
			for _, e := range events[1:] {
				if e.Confidence > best {
					best = e.Confidence
				}
			}
			candidates = append(candidates, candidate{"TEMPORAL", 0.85 + best*0.1, "query contains date: " + events[0].DateStr})
		}
	}

	// Signal 3: Code tokens
	if hasCodeTokens(q) && (caps.HasNeo4j || caps.HasPostgres) {
		candidates = append(candidates, candidate{"CODING_RULES", 0.85, "query contains code patterns"})
	}

	// Signal 4: Relational / graph queries
	if isRelational(qLower) && caps.HasLLM && (caps.HasNeo4j || caps.HasPostgres) {
		candidates = append(candidates, candidate{"GRAPH_COMPLETION", 0.80, "query asks about relationships"})
	}

	// Signal 5: Summary request
	if wantsSummary(qLower) && caps.HasEmbedding {
		candidates = append(candidates, candidate{"SUMMARIES", 0.85, "query requests a summary"})
	}

	// Signal 6: Question → RAG completion (needs LLM)
	if isQuestion(qLower, words) && caps.HasLLM && caps.HasEmbedding {
		candidates = append(candidates, candidate{"RAG_COMPLETION", 0.70, "query is a question"})
	}

	// Signal 7: Short keyword query → lexical or hybrid
	if isKeywordOnly(words) {
		if caps.HasBM25 && caps.HasEmbedding {
			candidates = append(candidates, candidate{"HYBRID", 0.75, "short keyword query"})
		} else if caps.HasBM25 {
			candidates = append(candidates, candidate{"CHUNKS_LEXICAL", 0.70, "short keyword query, no embeddings"})
		}
	}

	// Default: HYBRID > CHUNKS > CHUNKS_LEXICAL
	if caps.HasBM25 && caps.HasEmbedding {
		candidates = append(candidates, candidate{"HYBRID", 0.60, "default best-effort"})
	}
	if caps.HasEmbedding {
		candidates = append(candidates, candidate{"CHUNKS", 0.50, "vector search fallback"})
	}
	if caps.HasBM25 {
		candidates = append(candidates, candidate{"CHUNKS_LEXICAL", 0.40, "keyword search fallback"})
	}

	if len(candidates) == 0 {
		return Decision{SearchType: "CHUNKS", Reason: "no backends available, best effort", Confidence: 0.1}
	}

	// Pick best candidate, collect alternatives
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.score > best.score {
			best = c
		}
	}

	seen := map[string]bool{best.searchType: true}
	var alts []Alternative
	for _, c := range candidates {
		if !seen[c.searchType] {
			seen[c.searchType] = true
			alts = append(alts, Alternative{SearchType: c.searchType, Score: c.score})
		}
	}

	return Decision{
		SearchType:   best.searchType,
		Reason:       best.reason,
		Confidence:   best.score,
		Alternatives: alts,
	}
}

func fallback(caps Capabilities, reason string) Decision {
	if caps.HasBM25 && caps.HasEmbedding {
		return Decision{SearchType: "HYBRID", Reason: reason, Confidence: 0.3}
	}
	if caps.HasEmbedding {
		return Decision{SearchType: "CHUNKS", Reason: reason, Confidence: 0.3}
	}
	if caps.HasBM25 {
		return Decision{SearchType: "CHUNKS_LEXICAL", Reason: reason, Confidence: 0.3}
	}
	return Decision{SearchType: "CHUNKS", Reason: reason, Confidence: 0.1}
}

// ── Signal detectors ──

var cypherPrefixes = []string{"match ", "match(", "return ", "optional match ", "with ", "unwind "}

func isCypher(qLower string) bool {
	for _, p := range cypherPrefixes {
		if strings.HasPrefix(qLower, p) {
			return true
		}
	}
	return false
}

// codeTokenRe matches common code patterns: function(), obj.method, module::item, ptr->field
var codeTokenRe = regexp.MustCompile(`(?:` +
	`\w+\(` + // func(
	`|` + `\w+\.\w+\(` + // obj.method(
	`|` + `\w+::\w+` + // module::item
	`|` + `\w+->\w+` + // ptr->field
	`|` + `(?:func|fn|def|class|import|package|module|struct|interface|impl)\s` + // language keywords
	`)`)

// camelCaseRe detects camelCase or PascalCase identifiers (at least one lowercase-uppercase transition)
var camelCaseRe = regexp.MustCompile(`[a-z][A-Z]`)

// snakeCaseRe detects multi-segment snake_case (word_word)
var snakeCaseRe = regexp.MustCompile(`[a-z]+_[a-z]+_[a-z]+`)

func hasCodeTokens(q string) bool {
	if codeTokenRe.MatchString(q) {
		return true
	}
	// Check for camelCase identifiers
	for _, w := range strings.Fields(q) {
		if camelCaseRe.MatchString(w) && len(w) >= 4 {
			return true
		}
	}
	// Multi-segment snake_case
	if snakeCaseRe.MatchString(q) {
		return true
	}
	return false
}

var questionWordsRu = []string{
	"кто ", "что ", "когда ", "где ", "как ", "почему ", "зачем ",
	"какой ", "какая ", "какие ", "каким ", "сколько ", "чем ",
	"откуда ", "куда ",
}

var questionWordsEn = []string{
	"who ", "what ", "when ", "where ", "how ", "why ",
	"which ", "whose ", "whom ",
	"is ", "are ", "was ", "were ", "do ", "does ", "did ",
	"can ", "could ", "will ", "would ", "should ",
	"tell me ", "explain ", "describe ",
}

func isQuestion(qLower string, words []string) bool {
	if strings.Contains(qLower, "?") {
		return true
	}
	for _, qw := range questionWordsRu {
		if strings.HasPrefix(qLower, qw) {
			return true
		}
	}
	for _, qw := range questionWordsEn {
		if strings.HasPrefix(qLower, qw) {
			return true
		}
	}
	return false
}

var relationalKeywords = []string{
	// English
	"related to", "relationship", "connected to", "linked to",
	"depends on", "dependency", "calls ", "imports ",
	"inherits", "extends", "implements",
	// Russian
	"связан с", "связь", "отношени", "зависит от", "зависимость",
	"вызывает", "импортирует", "наследует", "расширяет", "реализует",
}

func isRelational(qLower string) bool {
	for _, kw := range relationalKeywords {
		if strings.Contains(qLower, kw) {
			return true
		}
	}
	return false
}

var summaryKeywords = []string{
	// English
	"summary", "summarize", "summarise", "overview", "digest", "recap",
	"brief", "outline", "highlights",
	// Russian
	"итог", "итого", "резюме", "обзор", "краткое", "краткий",
	"подвед", "суммар", "пересказ",
}

func wantsSummary(qLower string) bool {
	for _, kw := range summaryKeywords {
		if strings.Contains(qLower, kw) {
			return true
		}
	}
	return false
}

func isKeywordOnly(words []string) bool {
	if len(words) == 0 || len(words) > 3 {
		return false
	}
	for _, w := range words {
		if strings.Contains(w, "?") {
			return false
		}
		// If any word is a question word, not keyword-only
		for _, r := range []rune(w) {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' {
				return false
			}
		}
	}
	return true
}
