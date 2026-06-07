package consolidate

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Summarizer turns a cluster's source values into one consolidated statement.
type Summarizer interface {
	Summarize(ctx context.Context, sources []string) (string, error)
}

var (
	numberRe = regexp.MustCompile(`\d+`)
	// Capitalized multi-char tokens: crude entity proxy (Pi, Levara, DeepSeek...).
	// It deliberately over-matches; isEntityToken then filters the noise it picks
	// up — sentence-start common words and code/SQL keywords (findings P2.5).
	entityRe = regexp.MustCompile(`\b[A-Z][A-Za-z0-9]+\b`)
)

// nonEntityStopwords are capitalized tokens entityRe matches that carry no entity
// meaning: common English words (frequent at sentence starts) and code/SQL
// keywords. A faithful summary routinely rewords these away, so counting their
// omission against entity coverage produced false rejects — the live `localllm`
// cluster was rejected purely for dropping "REPL"/"Real"/"NULL" (findings P2.5).
// Keys are lowercased; matching is case-insensitive.
var nonEntityStopwords = map[string]bool{
	// common English function/sentence-start words
	"the": true, "this": true, "that": true, "these": true, "those": true,
	"there": true, "then": true, "their": true, "them": true, "they": true,
	"when": true, "where": true, "while": true, "what": true, "which": true,
	"who": true, "whom": true, "why": true, "how": true, "and": true, "but": true,
	"nor": true, "not": true, "for": true, "from": true, "into": true, "onto": true,
	"over": true, "under": true, "after": true, "before": true, "with": true,
	"been": true, "being": true, "have": true, "has": true, "had": true,
	"does": true, "did": true, "can": true, "could": true, "may": true,
	"might": true, "must": true, "shall": true, "should": true, "will": true,
	"would": true, "its": true, "all": true, "any": true, "each": true,
	"both": true, "more": true, "most": true, "other": true, "some": true,
	"such": true, "only": true, "own": true, "same": true, "than": true,
	"too": true, "very": true, "just": true, "now": true, "new": true,
	"also": true, "real": true, "use": true, "used": true, "using": true,
	"add": true, "added": true, "set": true, "get": true, "got": true,
	"run": true, "runs": true, "note": true, "see": true, "here": true,
	"yes": true, "are": true, "was": true, "were": true,
	// code / SQL keywords
	"repl": true, "null": true, "nil": true, "true": true, "false": true,
	"void": true, "select": true, "insert": true, "update": true, "delete": true,
	"create": true, "drop": true, "alter": true, "table": true, "join": true,
	"group": true, "order": true, "limit": true, "return": true, "func": true,
	"const": true, "let": true, "var": true, "todo": true, "fixme": true,
}

// isEntityToken decides whether a capitalized token entityRe matched is a
// meaning-bearing entity (Levara, DeepSeek, HNSW) versus stopword noise
// (Real, REPL, The). Digit-bearing or genuinely mixed-case identifiers are
// always entities — dictionary words never look like that — so the stopword
// gate only applies to plain-capitalized and all-caps tokens.
func isEntityToken(tok string) bool {
	hasDigit, allUpper, hasInnerUpper := false, true, false
	for i, r := range tok {
		switch {
		case unicode.IsDigit(r):
			hasDigit = true
		case unicode.IsLetter(r) && !unicode.IsUpper(r):
			allUpper = false
		}
		if i > 0 && unicode.IsUpper(r) {
			hasInnerUpper = true
		}
	}
	if hasDigit {
		return true
	}
	if hasInnerUpper && !allUpper { // camelCase identifier: DeepSeek, OpenAI
		return true
	}
	return !nonEntityStopwords[strings.ToLower(tok)]
}

// MaxEntityDropFraction is the share of source entity tokens a summary may omit
// before the coverage guard rejects it. Numbers remain all-or-nothing; only the
// noisier capitalized-token signal is fraction-gated.
const MaxEntityDropFraction = 0.10

// AbstractValue calls the Summarizer and enforces the coverage guard:
//   - every number present in the sources must appear in the output;
//   - every number in the output must appear in some source (no invented numbers);
//   - at most MaxEntityDropFraction of source entity tokens may be omitted.
//
// On any violation (or LLM error) it returns an error and the caller leaves the
// cluster untouched.
func AbstractValue(ctx context.Context, s Summarizer, sources []string) (string, error) {
	if len(sources) == 0 {
		return "", fmt.Errorf("consolidate: no sources")
	}
	out, err := s.Summarize(ctx, sources)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", fmt.Errorf("consolidate: empty summary")
	}

	srcNums := tokenSet(numberRe, sources...)
	outNums := tokenSet(numberRe, out)

	for n := range srcNums {
		if !outNums[n] {
			return "", fmt.Errorf("consolidate: summary dropped source number %q", n)
		}
	}
	for n := range outNums {
		if !srcNums[n] {
			return "", fmt.Errorf("consolidate: summary invented number %q", n)
		}
	}

	srcEnts := entitySet(sources...)
	outEnts := entitySet(out)
	var dropped []string
	for e := range srcEnts {
		if !outEnts[e] {
			dropped = append(dropped, e)
		}
	}
	if n := len(srcEnts); n > 0 {
		if frac := float64(len(dropped)) / float64(n); frac > MaxEntityDropFraction {
			return "", fmt.Errorf("consolidate: summary dropped %d/%d source entities (%.0f%% > %.0f%%): %v",
				len(dropped), n, frac*100, MaxEntityDropFraction*100, dropped)
		}
	}

	return out, nil
}

func tokenSet(re *regexp.Regexp, texts ...string) map[string]bool {
	set := map[string]bool{}
	for _, t := range texts {
		for _, m := range re.FindAllString(t, -1) {
			set[m] = true
		}
	}
	return set
}

// entitySet returns the meaning-bearing capitalized tokens, dropping the
// stopword noise entityRe over-matches (see isEntityToken).
func entitySet(texts ...string) map[string]bool {
	set := map[string]bool{}
	for tok := range tokenSet(entityRe, texts...) {
		if isEntityToken(tok) {
			set[tok] = true
		}
	}
	return set
}
