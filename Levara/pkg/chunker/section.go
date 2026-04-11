package chunker

import (
	"regexp"
	"strings"
	"unicode"
)

// SectionBoundary marks the start of a document section.
type SectionBoundary struct {
	Offset int    // character (rune) offset in the original text
	Name   string // section heading text
}

// Regex patterns for section detection
var (
	// Markdown headers: ## Title, ### Title, etc.
	markdownHeaderRe = regexp.MustCompile(`(?m)^#{1,4}\s+(.+)$`)

	// Russian chapter headers: "Глава N" (already detected in paragraph.go for chapter numbers)
	chapterHeaderRe = regexp.MustCompile(`(?m)^Глава\s+\d+[.:)]*\s*(.*)$`)
)

// DetectSections scans text for section boundaries.
// Supports:
//   - Markdown headers (## Section Name)
//   - Title-case lines followed by blank lines
//   - Russian chapter headers (Глава N)
//
// Returns boundaries sorted by offset.
func DetectSections(text string) []SectionBoundary {
	if text == "" {
		return nil
	}

	runes := []rune(text)
	var sections []SectionBoundary
	seen := make(map[int]bool) // dedup by offset

	// Pass 1: Markdown headers
	for _, match := range markdownHeaderRe.FindAllStringIndex(text, -1) {
		offset := runeOffset(text, match[0])
		if seen[offset] {
			continue
		}
		submatch := markdownHeaderRe.FindStringSubmatch(text[match[0]:match[1]])
		if len(submatch) >= 2 {
			name := strings.TrimSpace(submatch[1])
			if name != "" {
				sections = append(sections, SectionBoundary{Offset: offset, Name: name})
				seen[offset] = true
			}
		}
	}

	// Pass 2: Russian chapter headers
	for _, match := range chapterHeaderRe.FindAllStringIndex(text, -1) {
		offset := runeOffset(text, match[0])
		if seen[offset] {
			continue
		}
		line := text[match[0]:match[1]]
		name := strings.TrimSpace(line)
		if name != "" {
			sections = append(sections, SectionBoundary{Offset: offset, Name: name})
			seen[offset] = true
		}
	}

	// Pass 3: Title-case lines followed by blank line
	// e.g., "Authentication\n\nJWT tokens..."
	lines := strings.Split(text, "\n")
	byteOff := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if isTitleCaseLine(trimmed) && i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "" {
			offset := runeOffset(text, byteOff)
			if !seen[offset] && trimmed != "" {
				sections = append(sections, SectionBoundary{Offset: offset, Name: trimmed})
				seen[offset] = true
			}
		}

		byteOff += len(line) + 1 // +1 for \n
	}

	// Sort by offset
	sortSections(sections)
	_ = runes // suppress unused warning for rune-offset calculation

	return sections
}

// FindSectionForOffset returns the section name for a given rune offset.
// Returns "" if no section has been detected before this offset.
func FindSectionForOffset(sections []SectionBoundary, offset int) string {
	result := ""
	for _, s := range sections {
		if s.Offset <= offset {
			result = s.Name
		} else {
			break
		}
	}
	return result
}

// isTitleCaseLine checks if a line looks like a section heading:
// starts with uppercase, length 3-80, not a sentence (no ending punctuation),
// contains mostly letters/spaces.
func isTitleCaseLine(line string) bool {
	if len(line) < 3 || len(line) > 80 {
		return false
	}

	runes := []rune(line)

	// Must start with uppercase letter
	if !unicode.IsUpper(runes[0]) {
		return false
	}

	// Must not end with sentence punctuation
	last := runes[len(runes)-1]
	if last == '.' || last == '!' || last == '?' || last == ':' || last == ';' {
		return false
	}

	// Must be mostly letters, spaces, digits, hyphens
	nonAlpha := 0
	for _, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsSpace(r) && !unicode.IsDigit(r) && r != '-' && r != '–' && r != '—' {
			nonAlpha++
		}
	}
	// Allow up to 20% non-alpha characters
	if float64(nonAlpha)/float64(len(runes)) > 0.2 {
		return false
	}

	return true
}

// runeOffset converts byte offset to rune offset.
func runeOffset(text string, byteOff int) int {
	return len([]rune(text[:byteOff]))
}

// sortSections sorts by offset (insertion sort, n is small).
func sortSections(s []SectionBoundary) {
	for i := 1; i < len(s); i++ {
		key := s[i]
		j := i - 1
		for j >= 0 && s[j].Offset > key.Offset {
			s[j+1] = s[j]
			j--
		}
		s[j+1] = key
	}
}
