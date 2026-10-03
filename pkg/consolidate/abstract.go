package consolidate

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Summarizer turns a cluster's source values into one consolidated statement.
type Summarizer interface {
	Summarize(ctx context.Context, sources []string) (string, error)
}

// FeedbackSummarizer is an optional Summarizer capability: re-request the
// abstraction after a coverage violation, naming the exact facts the previous
// draft lost so the model can repair its own text. Summarizers without it skip
// the retry ladder and fall straight through to the number ledger.
type FeedbackSummarizer interface {
	Summarizer
	SummarizeWithFeedback(ctx context.Context, sources []string, violations string) (string, error)
}

// MaxRepairAttempts bounds the feedback retry loop before the deterministic
// ledger fallback takes over. Consolidation is background work, so latency is
// free; two rounds of targeted feedback catch most dropped-fact drafts.
const MaxRepairAttempts = 2

var (
	numberRe = regexp.MustCompile(`\d+`)
	// Capitalized multi-char tokens: crude entity proxy (Pi, Levara, DeepSeek...).
	// It deliberately over-matches; isEntityToken then filters the noise it picks
	// up — sentence-start common words and code/SQL keywords (findings P2.5).
	entityRe = regexp.MustCompile(`\b[A-Z][A-Za-z0-9]+\b`)
	// numberUnitRe matches digit-led numeric facts as whole units: dotted or
	// colon-joined composites first (IP:port 194.84.83.138:17100, versions
	// 2.1.3, times 13:29, arXiv ids 2609.24972), then bare digit runs. The old
	// bare-\d+ tokenizer split one composite fact into several independent
	// "numbers" — an address counted as four — so a faithful summary tripped
	// the guard once per octet (live labirint-1 cluster: 12 "dropped numbers",
	// 4 of them fragments of a single VPN endpoint).
	numberUnitRe = regexp.MustCompile(`\d+(?:[.:]\d+)+|\d+`)
)

// LedgerMarker prefixes the deterministic number tail AbstractValue appends
// when the draft still misses source numbers after the repair ladder. Kept
// here so benchmarks and tests can detect ledger-repaired records.
const LedgerMarker = "Числа источников:"

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
	// common tech-narrative words a faithful summary routinely rewords
	// (labirint-1/ub-main-1 cascade-probe rejects were tripped by Port/INVALID/ID)
	"id": true, "port": true, "invalid": true,
	// code / SQL keywords
	"repl": true, "null": true, "nil": true, "true": true, "false": true,
	"void": true, "select": true, "insert": true, "update": true, "delete": true,
	"create": true, "drop": true, "alter": true, "table": true, "join": true,
	"group": true, "order": true, "limit": true, "return": true, "func": true,
	"const": true, "let": true, "var": true, "todo": true, "fixme": true,
}

// secretShaped reports whether a token looks like a credential or hash
// fragment rather than a name: digits + mixed case + 8+ chars (G3B2npPciJah).
func secretShaped(tok string) bool {
	if len(tok) < 8 {
		return false
	}
	hasDigit, hasLower, hasUpper := false, false, false
	for _, r := range tok {
		switch {
		case unicode.IsDigit(r):
			hasDigit = true
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		}
	}
	return hasDigit && hasLower && hasUpper
}

// isEntityToken decides whether a capitalized token entityRe matched is a
// meaning-bearing entity (Levara, DeepSeek, HNSW) versus stopword noise
// (Real, REPL, The). Digit-bearing or genuinely mixed-case identifiers are
// always entities — dictionary words never look like that — so the stopword
// gate only applies to plain-capitalized and all-caps tokens. Exception:
// secret-shaped tokens are not entities — a consolidation must not be
// REQUIRED to propagate credential fragments into the merged record.
func isEntityToken(tok string) bool {
	if secretShaped(tok) {
		return false
	}
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
// before the coverage guard rejects it. Number units remain all-or-nothing on
// the final record (the ledger closes the gap deterministically); only the
// noisier capitalized-token signal is fraction-gated.
const MaxEntityDropFraction = 0.10

// numberUnits extracts the numeric units a record must preserve. Digit runs
// embedded in identifiers are excluded — they are part of a name, not a
// quantity: hostnames (redis-170-test), filenames (id_ed25519), commit hashes
// (f427778). The rule is prefix-adjacency only and applies to bare runs of 3+
// digits, so "256-dim" keeps its number (the fact is the count) and short
// version labels (v1, v2) stay protected. Composite units are never skipped —
// the point is that an address survives as one piece.
func numberUnits(texts ...string) map[string]bool {
	set := map[string]bool{}
	for _, t := range texts {
		for _, loc := range numberUnitRe.FindAllStringIndex(t, -1) {
			unit := t[loc[0]:loc[1]]
			if !strings.ContainsAny(unit, ".:") && len(unit) >= 3 && isIdentifierEmbedded(t, loc[0]) {
				continue
			}
			set[unit] = true
		}
	}
	return set
}

// isIdentifierEmbedded reports whether the digit run starting at index i sits
// inside a name: a letter directly before it (id_ed25519, f427778) or a [-_.]
// glue chained to a letter (redis-170-test).
func isIdentifierEmbedded(t string, i int) bool {
	if i < 2 {
		return false
	}
	if unicode.IsLetter(rune(t[i-1])) {
		return true
	}
	return strings.ContainsRune("-_.", rune(t[i-1])) && unicode.IsLetter(rune(t[i-2]))
}

// compositeDigitGroups collects the digit runs of composite units ("17100"
// from "194.84.83.138:17100"): a bare unit at one side of the comparison is
// legitimate when it appears as a group inside the other side's composite.
func compositeDigitGroups(units map[string]bool) map[string]bool {
	set := map[string]bool{}
	for u := range units {
		if strings.ContainsAny(u, ".:") {
			for _, g := range numberRe.FindAllString(u, -1) {
				set[g] = true
			}
		}
	}
	return set
}

// compositeGroupsCovered accepts the reassembled form of a composite unit:
// "194.84.83.138:17100" survives as "194.84.83.138 port 17100" when every
// digit group reappears.
func compositeGroupsCovered(unit string, frags map[string]bool) bool {
	if !strings.ContainsAny(unit, ".:") {
		return false
	}
	for _, g := range numberRe.FindAllString(unit, -1) {
		if !frags[g] {
			return false
		}
	}
	return true
}

// unitCovered reports whether a numeric unit survives on the other side of the
// comparison: exact unit match, presence as a digit group of a composite there
// (a bare source port survives inside the full IP:port the model wrote), or —
// for composite units — the reassembled form. Bare units are never blessed by
// substring luck: "84" inside a source "1284" does not cover a bare "84".
func unitCovered(unit string, units, compGroups, frags map[string]bool) bool {
	if units[unit] || compGroups[unit] {
		return true
	}
	return compositeGroupsCovered(unit, frags)
}

// AbstractValue calls the Summarizer and enforces the coverage guard:
//   - every numeric unit present in the sources must appear in the output;
//   - every numeric unit in the output must appear in some source (no invented
//     numbers);
//   - at most MaxEntityDropFraction of source entity tokens may be omitted.
//
// A violating draft is repaired before it is rejected: first up to
// MaxRepairAttempts feedback retries (when the Summarizer implements
// FeedbackSummarizer), then a deterministic number ledger that appends the
// missing units verbatim. Invented numbers and entity over-drops are not
// ledger-repairable — they still fail. On failure the caller leaves the
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

	violation := CoverageViolations(sources, out)
	if violation == "" {
		return out, nil
	}

	if fs, ok := s.(FeedbackSummarizer); ok {
		for i := 0; i < MaxRepairAttempts && violation != ""; i++ {
			repaired, err := fs.SummarizeWithFeedback(ctx, sources, violation)
			if err != nil {
				break // fall through to the deterministic ladder
			}
			repaired = strings.TrimSpace(repaired)
			if repaired == "" {
				break
			}
			out = repaired
			violation = CoverageViolations(sources, out)
		}
		if violation == "" {
			return out, nil
		}
	}

	if missing := droppedNumberUnits(sources, out); len(missing) > 0 {
		out = out + "\n\n" + LedgerMarker + " " + strings.Join(missing, ", ")
		violation = CoverageViolations(sources, out)
		if violation == "" {
			return out, nil
		}
	}
	return "", errors.New(violation)
}

// CoverageViolations describes how out violates the coverage guard against
// sources: dropped numeric units first, then invented ones, then entity
// over-drop. Empty string means the record is clean. Exported for benchmark
// probes and tooling that classify drafts without running the full repair
// ladder.
func CoverageViolations(sources []string, out string) string {
	srcUnits := numberUnits(sources...)
	outUnits := numberUnits(out)
	srcFrags := tokenSet(numberRe, sources...)
	outFrags := tokenSet(numberRe, out)
	srcCompGroups := compositeDigitGroups(srcUnits)
	outCompGroups := compositeDigitGroups(outUnits)
	tokCompound, compoundFrags, exemptEnts, runs := entityCompounds(sources...)
	filterCompoundNumberUnits(srcUnits, runs)
	filterCompoundNumberUnits(outUnits, runs)

	var dropped []string
	for u := range srcUnits {
		if !unitCovered(u, outUnits, outCompGroups, outFrags) {
			dropped = append(dropped, u)
		}
	}
	if len(dropped) > 0 {
		sort.Strings(dropped)
		return fmt.Sprintf("consolidate: summary dropped source numbers %v", dropped)
	}

	var invented []string
	for u := range outUnits {
		if !unitCovered(u, srcUnits, srcCompGroups, srcFrags) {
			invented = append(invented, u)
		}
	}
	if len(invented) > 0 {
		sort.Strings(invented)
		return fmt.Sprintf("consolidate: summary invented numbers %v", invented)
	}

	srcEnts := entitySet(sources...)
	outEnts := entitySet(out)
	var droppedEnts []string
	for e := range srcEnts {
		if exemptEnts[e] || outEnts[e] {
			continue
		}
		if cid, ok := tokCompound[e]; ok {
			covered := false
			for _, sib := range compoundFrags[cid] {
				if outEnts[sib] {
					covered = true
					break
				}
			}
			if covered {
				continue
			}
		}
		droppedEnts = append(droppedEnts, e)
	}
	if n := len(srcEnts); n > 0 {
		if frac := float64(len(droppedEnts)) / float64(n); frac > MaxEntityDropFraction {
			return fmt.Sprintf("consolidate: summary dropped %d/%d source entities (%.0f%% > %.0f%%): %v",
				len(droppedEnts), n, frac*100, MaxEntityDropFraction*100, droppedEnts)
		}
	}
	return ""
}

// droppedNumberUnits returns the sorted source numeric units missing from out —
// exactly what the ledger tail appends.
func droppedNumberUnits(sources []string, out string) []string {
	outUnits := numberUnits(out)
	outFrags := tokenSet(numberRe, out)
	outCompGroups := compositeDigitGroups(outUnits)
	_, _, _, runs := entityCompounds(sources...)
	srcUnits := numberUnits(sources...)
	filterCompoundNumberUnits(srcUnits, runs)
	var missing []string
	for u := range srcUnits {
		if !unitCovered(u, outUnits, outCompGroups, outFrags) {
			missing = append(missing, u)
		}
	}
	sort.Strings(missing)
	return missing
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

// entityCompoundRe matches glue-joined runs that can carry several entity
// fragments of one fact: usernames (IM-ADM-VMW@vsphere.local) and credential
// blobs (npPciJah!G3B2npPciJah!G3B2) shatter into independent "entities"
// under plain entityRe, so a summary keeping one fragment was vetoed for the
// others — the live labirint-1 cluster failed with 5 of 9 dropped "entities"
// being fragments of two credentials.
var entityCompoundRe = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9@!_./-]*`)

// entityCompounds groups meaning-bearing entity tokens by their glue-joined
// compound run. A run holding 2+ distinct fragments becomes one fact — covered
// when ANY fragment survives. A run containing a secret-shaped fragment is a
// credential blob: NONE of its fragments is required (a consolidation must not
// propagate password pieces), so they are returned as exempt.
func entityCompounds(texts ...string) (tokCompound map[string]string, compoundFrags map[string][]string, exempt map[string]bool, runs []string) {
	tokCompound = map[string]string{}
	compoundFrags = map[string][]string{}
	exempt = map[string]bool{}
	for _, t := range texts {
		for _, run := range entityCompoundRe.FindAllString(t, -1) {
			var frags []string
			secret := false
			seen := map[string]bool{}
			for _, m := range entityRe.FindAllString(run, -1) {
				if seen[m] {
					continue
				}
				if isEntityToken(m) {
					seen[m] = true
					frags = append(frags, m)
				} else if secretShaped(m) {
					seen[m] = true
					frags = append(frags, m)
					secret = true
				}
			}
			switch {
			case secret:
				for _, f := range frags {
					exempt[f] = true
				}
				runs = append(runs, run)
			case len(frags) >= 2:
				for _, f := range frags {
					if _, exists := tokCompound[f]; !exists {
						tokCompound[f] = run
					}
				}
				compoundFrags[run] = frags
				runs = append(runs, run)
			}
		}
	}
	return tokCompound, compoundFrags, exempt, runs
}

// filterCompoundNumberUnits drops number units that live inside compound runs
// (usernames, credential blobs): a digit inside "IM-ADM-VMW@vsphere.local" or
// "G3B2npPciJah" is part of a name, not a quantity, and must not be required.
func filterCompoundNumberUnits(units map[string]bool, runs []string) {
	for u := range units {
		for _, run := range runs {
			if strings.Contains(run, u) {
				delete(units, u)
				break
			}
		}
	}
}
