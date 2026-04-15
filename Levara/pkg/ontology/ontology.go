// Package ontology provides RDF/OWL ontology parsing, fuzzy matching, and entity grounding.
// Supports: RDF/XML format (most common for OWL ontologies).
package ontology

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/agnivade/levenshtein"
)

// OntologyClass represents a class in the ontology.
type OntologyClass struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	ParentURI   string `json:"parent_uri,omitempty"`
}

// OntologyIndividual represents an individual (instance) in the ontology.
type OntologyIndividual struct {
	URI      string `json:"uri"`
	Name     string `json:"name"`
	ClassURI string `json:"class_uri"`
}

// Ontology holds parsed RDF/OWL data.
type Ontology struct {
	Name        string               `json:"name"`
	Classes     []OntologyClass      `json:"classes"`
	Individuals []OntologyIndividual `json:"individuals"`
	classMap    map[string]*OntologyClass
}

// LoadFromFile parses an RDF/XML (.owl/.rdf) file.
func LoadFromFile(path string) (*Ontology, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open ontology: %w", err)
	}
	defer f.Close()
	return Parse(f, path)
}

// Parse reads RDF/XML from a reader.
func Parse(r io.Reader, name string) (*Ontology, error) {
	decoder := xml.NewDecoder(r)
	ont := &Ontology{
		Name: name,
	}

	// Parser state — nested-element-aware.
	// currentClassIdx >= 0 means we're inside <owl:Class>...</owl:Class>;
	// any <subClassOf>/<label>/<comment> we see belongs to that class.
	// Using an index (not a *OntologyClass pointer) avoids the classic
	// "slice reallocation on append invalidates pointer" footgun — classMap
	// is rebuilt after parsing completes.
	var (
		currentElement  string
		currentClassIdx = -1
		inIndividual    bool
	)

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse XML: %w", err)
		}

		switch t := token.(type) {
		case xml.StartElement:
			localName := t.Name.Local
			currentElement = localName

			// Extract rdf:about / rdf:ID for Class or NamedIndividual declarations.
			var about string
			var resource string
			for _, attr := range t.Attr {
				switch attr.Name.Local {
				case "about", "ID":
					about = attr.Value
				case "resource":
					resource = attr.Value
				}
			}

			switch localName {
			case "Class":
				if about == "" {
					// Anonymous class (blank node) — ignore; we don't model them.
					continue
				}
				cls := OntologyClass{
					URI:  about,
					Name: uriToName(about),
				}
				ont.Classes = append(ont.Classes, cls)
				currentClassIdx = len(ont.Classes) - 1

			case "NamedIndividual":
				if about == "" {
					continue
				}
				ont.Individuals = append(ont.Individuals, OntologyIndividual{
					URI:  about,
					Name: uriToName(about),
				})
				inIndividual = true

			case "subClassOf":
				// Only meaningful when nested inside an <owl:Class>.
				if currentClassIdx >= 0 && resource != "" {
					ont.Classes[currentClassIdx].ParentURI = resource
				}
			}

		case xml.CharData:
			if currentClassIdx < 0 {
				continue
			}
			text := strings.TrimSpace(string(t))
			if text == "" {
				continue
			}
			switch currentElement {
			case "label":
				ont.Classes[currentClassIdx].Name = text
			case "comment":
				ont.Classes[currentClassIdx].Description = text
			}

		case xml.EndElement:
			switch t.Name.Local {
			case "Class":
				currentClassIdx = -1
			case "NamedIndividual":
				inIndividual = false
			}
		}
	}

	// Rebuild classMap after all appends complete so pointers are stable.
	ont.classMap = make(map[string]*OntologyClass, len(ont.Classes))
	for i := range ont.Classes {
		ont.classMap[strings.ToLower(ont.Classes[i].Name)] = &ont.Classes[i]
	}

	_ = inIndividual // reserved for future individual-level annotation parsing
	return ont, nil
}

// uriToName extracts the local name from a URI.
func uriToName(uri string) string {
	if idx := strings.LastIndex(uri, "#"); idx >= 0 {
		return uri[idx+1:]
	}
	if idx := strings.LastIndex(uri, "/"); idx >= 0 {
		return uri[idx+1:]
	}
	return uri
}

// FuzzyMatch finds the closest class matching a name (Levenshtein distance).
// Returns nil if no match above cutoff.
func (o *Ontology) FuzzyMatch(name string, cutoff float64) *OntologyClass {
	if cutoff <= 0 {
		cutoff = 0.8
	}
	nameLower := strings.ToLower(name)

	// Exact match first
	if cls, ok := o.classMap[nameLower]; ok {
		return cls
	}

	var bestMatch *OntologyClass
	bestScore := 0.0

	for _, cls := range o.Classes {
		clsLower := strings.ToLower(cls.Name)
		dist := levenshtein.ComputeDistance(nameLower, clsLower)
		maxLen := len(nameLower)
		if len(clsLower) > maxLen {
			maxLen = len(clsLower)
		}
		if maxLen == 0 {
			continue
		}
		score := 1.0 - float64(dist)/float64(maxLen)
		if score >= cutoff && score > bestScore {
			bestScore = score
			bestMatch = &cls
		}
	}

	return bestMatch
}

// ValidateEntity checks if an entity name matches any ontology class.
func (o *Ontology) ValidateEntity(name string) (bool, string) {
	match := o.FuzzyMatch(name, 0.8)
	if match != nil {
		return true, match.Name
	}
	return false, ""
}

// ListClasses returns all class names.
func (o *Ontology) ListClasses() []string {
	names := make([]string, len(o.Classes))
	for i, c := range o.Classes {
		names[i] = c.Name
	}
	return names
}
