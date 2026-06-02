package consolidate

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// Summarizer turns a cluster's source values into one consolidated statement.
type Summarizer interface {
	Summarize(ctx context.Context, sources []string) (string, error)
}

var (
	numberRe = regexp.MustCompile(`\d+`)
	// Capitalized multi-char tokens: crude entity proxy (Pi, Levara, DeepSeek...).
	// It also matches all-caps code/SQL keywords (REPL, NULL, CREATE), which a
	// faithful summary may legitimately reword — hence the fraction tolerance
	// below rather than an all-or-nothing entity check (findings P2.5).
	entityRe = regexp.MustCompile(`\b[A-Z][A-Za-z0-9]+\b`)
)

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

	srcEnts := tokenSet(entityRe, sources...)
	outEnts := tokenSet(entityRe, out)
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
