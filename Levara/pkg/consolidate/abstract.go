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
	entityRe = regexp.MustCompile(`\b[A-Z][A-Za-z0-9]+\b`)
)

// AbstractValue calls the Summarizer and enforces the coverage guard:
//   - every number present in the sources must appear in the output;
//   - every number in the output must appear in some source (no invented numbers);
//   - every capitalized entity token in the sources must appear in the output.
//
// On any violation (or LLM error) it returns an error and the caller leaves the
// cluster untouched.
func AbstractValue(ctx context.Context, s Summarizer, sources []string) (string, error) {
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
	for e := range srcEnts {
		if !outEnts[e] {
			return "", fmt.Errorf("consolidate: summary dropped source entity %q", e)
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
