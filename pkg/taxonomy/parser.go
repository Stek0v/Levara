package taxonomy

import (
	"bufio"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxSeedBytes = 1 << 20
const MaxNodes = 1000

var ErrInvalid = errors.New("invalid taxonomy request")
var ErrConflict = errors.New("taxonomy conflict")

type Catalog struct {
	Domains []Domain `json:"domains"`
}
type Node struct {
	ID                         string   `json:"id,omitempty"`
	Name                       string   `json:"name"`
	Description                string   `json:"description"`
	Aliases                    []string `json:"aliases"`
	descriptionSet, aliasesSet bool
}
type Domain struct {
	Node
	Collections []Collection `json:"collections"`
}
type Collection struct {
	Node
	Documents []Document `json:"documents"`
}
type Document struct {
	Node
	SourceDocumentID string `json:"source_document_id"`
}

func validText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}
func Key(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func Parse(seed string) (Catalog, error) {
	catalog := Catalog{Domains: []Domain{}}
	if len(seed) > MaxSeedBytes || !utf8.ValidString(seed) {
		return catalog, ErrInvalid
	}
	seed = strings.TrimPrefix(seed, "\ufeff")
	scanner := bufio.NewScanner(strings.NewReader(seed))
	scanner.Buffer(make([]byte, 4096), MaxSeedBytes+1)
	header, section := false, false
	di, ci, doci := -1, -1, -1
	count := 0
	var current *Node
	fail := func(line int) (Catalog, error) { return Catalog{}, fmt.Errorf("%w: line %d", ErrInvalid, line) }
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		if !validText(text) {
			return fail(line)
		}
		if !header {
			if text != "# Domains" {
				return fail(line)
			}
			header = true
			continue
		}
		switch {
		case strings.HasPrefix(text, "## "):
			name := strings.TrimSpace(strings.TrimPrefix(text, "## "))
			if name == "" {
				return fail(line)
			}
			for _, d := range catalog.Domains {
				if Key(d.Name) == Key(name) {
					return fail(line)
				}
			}
			catalog.Domains = append(catalog.Domains, Domain{Node: Node{Name: name, Aliases: []string{}}, Collections: []Collection{}})
			di = len(catalog.Domains) - 1
			ci = -1
			doci = -1
			section = false
			current = &catalog.Domains[di].Node
			count++
		case text == "### Collections":
			if di < 0 || section || ci >= 0 {
				return fail(line)
			}
			section = true
			current = nil
		case strings.HasPrefix(text, "#### "):
			if di < 0 || !section {
				return fail(line)
			}
			name := strings.TrimSpace(strings.TrimPrefix(text, "#### "))
			if name == "" {
				return fail(line)
			}
			for _, c := range catalog.Domains[di].Collections {
				if Key(c.Name) == Key(name) {
					return fail(line)
				}
			}
			catalog.Domains[di].Collections = append(catalog.Domains[di].Collections, Collection{Node: Node{Name: name, Aliases: []string{}}, Documents: []Document{}})
			ci = len(catalog.Domains[di].Collections) - 1
			doci = -1
			current = &catalog.Domains[di].Collections[ci].Node
			count++
		case strings.HasPrefix(text, "##### Документ:"):
			if ci < 0 {
				return fail(line)
			}
			name := strings.TrimSpace(strings.TrimPrefix(text, "##### Документ:"))
			if name == "" {
				return fail(line)
			}
			for _, d := range catalog.Domains[di].Collections[ci].Documents {
				if Key(d.Name) == Key(name) {
					return fail(line)
				}
			}
			catalog.Domains[di].Collections[ci].Documents = append(catalog.Domains[di].Collections[ci].Documents, Document{Node: Node{Name: name, Aliases: []string{}}})
			doci = len(catalog.Domains[di].Collections[ci].Documents) - 1
			current = &catalog.Domains[di].Collections[ci].Documents[doci].Node
			count++
		case strings.HasPrefix(text, "Описание:"):
			if current == nil || current.descriptionSet {
				return fail(line)
			}
			current.Description = strings.TrimSpace(strings.TrimPrefix(text, "Описание:"))
			current.descriptionSet = true
		case strings.HasPrefix(text, "Алиасы:"):
			if current == nil || current.aliasesSet {
				return fail(line)
			}
			raw := strings.TrimSpace(strings.TrimPrefix(text, "Алиасы:"))
			current.aliasesSet = true
			if raw != "" {
				seen := map[string]bool{}
				for _, a := range strings.Split(raw, ",") {
					a = strings.TrimSpace(a)
					if a == "" {
						return fail(line)
					}
					if !seen[Key(a)] {
						current.Aliases = append(current.Aliases, a)
						seen[Key(a)] = true
					}
				}
			}
		case strings.HasPrefix(text, "Источник:"):
			if doci < 0 {
				return fail(line)
			}
			doc := &catalog.Domains[di].Collections[ci].Documents[doci]
			if doc.SourceDocumentID != "" {
				return fail(line)
			}
			doc.SourceDocumentID = strings.TrimSpace(strings.TrimPrefix(text, "Источник:"))
			if doc.SourceDocumentID == "" {
				return fail(line)
			}
		default:
			return fail(line)
		}
		if count > MaxNodes {
			return fail(line)
		}
	}
	if scanner.Err() != nil || !header {
		return catalog, ErrInvalid
	}
	for _, d := range catalog.Domains {
		for _, c := range d.Collections {
			for _, doc := range c.Documents {
				if doc.SourceDocumentID == "" {
					return catalog, ErrInvalid
				}
			}
		}
	}
	return catalog, nil
}
