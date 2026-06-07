package ontology

import (
	"strings"
	"testing"
)

// T-8: golden tests for RDF/XML ontology parsing + fuzzy matching.
//
// Uses small inline fixtures instead of testdata/ files — keeps the test
// hermetic and makes it easy to see exactly what shape we're asserting.

// ─────────────────────────────────────────────────────────────────
// uriToName
// ─────────────────────────────────────────────────────────────────

func TestURIToName_HashSeparator(t *testing.T) {
	got := uriToName("http://xmlns.com/foaf/0.1/#Person")
	if got != "Person" {
		t.Errorf("got %q, want Person", got)
	}
}

func TestURIToName_SlashSeparator(t *testing.T) {
	got := uriToName("http://xmlns.com/foaf/0.1/Person")
	if got != "Person" {
		t.Errorf("got %q, want Person", got)
	}
}

func TestURIToName_HashWinsOverSlash(t *testing.T) {
	// Mixed URI: # should be preferred over / because # is more specific.
	got := uriToName("http://example.org/path/to/res#LocalName")
	if got != "LocalName" {
		t.Errorf("got %q, want LocalName", got)
	}
}

func TestURIToName_NoSeparator(t *testing.T) {
	got := uriToName("Plain")
	if got != "Plain" {
		t.Errorf("got %q, want Plain", got)
	}
}

// ─────────────────────────────────────────────────────────────────
// Parse
// ─────────────────────────────────────────────────────────────────

// Minimal FOAF-like ontology: 3 classes (Person, Organization, Group),
// one subClassOf relation, a NamedIndividual.
const foafLikeRDF = `<?xml version="1.0"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"
         xmlns:rdfs="http://www.w3.org/2000/01/rdf-schema#"
         xmlns:owl="http://www.w3.org/2002/07/owl#">
  <owl:Class rdf:about="http://xmlns.com/foaf/0.1/Person">
    <rdfs:label>Person</rdfs:label>
    <rdfs:comment>A human being, whether living, dead, real, or imaginary.</rdfs:comment>
  </owl:Class>
  <owl:Class rdf:about="http://xmlns.com/foaf/0.1/Organization">
    <rdfs:label>Organization</rdfs:label>
    <rdfs:comment>An organization.</rdfs:comment>
  </owl:Class>
  <owl:Class rdf:about="http://xmlns.com/foaf/0.1/Group">
    <rdfs:label>Group</rdfs:label>
    <rdfs:subClassOf rdf:resource="http://xmlns.com/foaf/0.1/Organization"/>
  </owl:Class>
  <owl:NamedIndividual rdf:about="http://example.org/Alice">
  </owl:NamedIndividual>
</rdf:RDF>`

func TestParse_ClassesCount(t *testing.T) {
	o, err := Parse(strings.NewReader(foafLikeRDF), "foaf-lite")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(o.Classes); got != 3 {
		t.Errorf("classes = %d, want 3", got)
	}
	if got := len(o.Individuals); got != 1 {
		t.Errorf("individuals = %d, want 1", got)
	}
	if o.Name != "foaf-lite" {
		t.Errorf("Name = %q, want foaf-lite", o.Name)
	}
}

func TestParse_ClassFieldsPopulated(t *testing.T) {
	o, _ := Parse(strings.NewReader(foafLikeRDF), "foaf-lite")

	var person *OntologyClass
	for i := range o.Classes {
		if o.Classes[i].Name == "Person" {
			person = &o.Classes[i]
			break
		}
	}
	if person == nil {
		t.Fatal("Person class not found")
	}
	if person.URI != "http://xmlns.com/foaf/0.1/Person" {
		t.Errorf("URI = %q", person.URI)
	}
	if !strings.Contains(person.Description, "human being") {
		t.Errorf("Description = %q, want to contain 'human being'", person.Description)
	}
}

func TestParse_SubClassOfCaptured(t *testing.T) {
	o, _ := Parse(strings.NewReader(foafLikeRDF), "foaf-lite")
	var group *OntologyClass
	for i := range o.Classes {
		if o.Classes[i].Name == "Group" {
			group = &o.Classes[i]
		}
	}
	if group == nil {
		t.Fatal("Group class not found")
	}
	if !strings.HasSuffix(group.ParentURI, "Organization") {
		t.Errorf("ParentURI = %q, want to end with Organization", group.ParentURI)
	}
}

func TestParse_IndividualURICorrect(t *testing.T) {
	o, _ := Parse(strings.NewReader(foafLikeRDF), "foaf-lite")
	if o.Individuals[0].URI != "http://example.org/Alice" {
		t.Errorf("URI = %q", o.Individuals[0].URI)
	}
	if o.Individuals[0].Name != "Alice" {
		t.Errorf("Name = %q, want Alice", o.Individuals[0].Name)
	}
}

func TestParse_EmptyDocument(t *testing.T) {
	const empty = `<?xml version="1.0"?><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/>`
	o, err := Parse(strings.NewReader(empty), "empty")
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Classes) != 0 || len(o.Individuals) != 0 {
		t.Errorf("expected empty ontology, got %d classes, %d individuals",
			len(o.Classes), len(o.Individuals))
	}
}

func TestParse_MalformedXML(t *testing.T) {
	const broken = `<?xml version="1.0"?><unclosed`
	_, err := Parse(strings.NewReader(broken), "broken")
	if err == nil {
		t.Fatal("expected parse error on malformed XML")
	}
}

// ─────────────────────────────────────────────────────────────────
// FuzzyMatch + ValidateEntity
// ─────────────────────────────────────────────────────────────────

func TestFuzzyMatch_ExactHit(t *testing.T) {
	o, _ := Parse(strings.NewReader(foafLikeRDF), "foaf-lite")
	m := o.FuzzyMatch("Person", 0.8)
	if m == nil {
		t.Fatal("expected match for 'Person'")
	}
	if m.Name != "Person" {
		t.Errorf("got %q", m.Name)
	}
}

func TestFuzzyMatch_CaseInsensitive(t *testing.T) {
	o, _ := Parse(strings.NewReader(foafLikeRDF), "foaf-lite")
	m := o.FuzzyMatch("PERSON", 0.8)
	if m == nil || m.Name != "Person" {
		t.Errorf("case-insensitive match failed: %+v", m)
	}
}

func TestFuzzyMatch_TypoCloseEnough(t *testing.T) {
	// "Persn" → "Person": distance 1, max 6, score = 1 - 1/6 ≈ 0.833, above 0.8 cutoff.
	o, _ := Parse(strings.NewReader(foafLikeRDF), "foaf-lite")
	m := o.FuzzyMatch("Persn", 0.8)
	if m == nil {
		t.Fatal("expected fuzzy match for 'Persn'")
	}
	if m.Name != "Person" {
		t.Errorf("got %q, want Person", m.Name)
	}
}

func TestFuzzyMatch_BelowCutoff(t *testing.T) {
	o, _ := Parse(strings.NewReader(foafLikeRDF), "foaf-lite")
	if m := o.FuzzyMatch("Xyzzy", 0.8); m != nil {
		t.Errorf("got unexpected match for 'Xyzzy': %+v", m)
	}
}

func TestFuzzyMatch_DefaultCutoffApplied(t *testing.T) {
	// cutoff <= 0 should be coerced to 0.8.
	o, _ := Parse(strings.NewReader(foafLikeRDF), "foaf-lite")
	if m := o.FuzzyMatch("Person", 0); m == nil {
		t.Error("default cutoff: exact match lost")
	}
	if m := o.FuzzyMatch("Xyzzy", -1); m != nil {
		t.Error("default cutoff: bogus match allowed")
	}
}

func TestValidateEntity_Hit(t *testing.T) {
	o, _ := Parse(strings.NewReader(foafLikeRDF), "foaf-lite")
	ok, name := o.ValidateEntity("Organization")
	if !ok || name != "Organization" {
		t.Errorf("ValidateEntity(Organization) = %v,%q", ok, name)
	}
}

func TestValidateEntity_Miss(t *testing.T) {
	o, _ := Parse(strings.NewReader(foafLikeRDF), "foaf-lite")
	if ok, _ := o.ValidateEntity("Xyzzy"); ok {
		t.Error("ValidateEntity(Xyzzy) should return false")
	}
}

func TestListClasses_Alphabetic(t *testing.T) {
	o, _ := Parse(strings.NewReader(foafLikeRDF), "foaf-lite")
	names := o.ListClasses()
	if len(names) != 3 {
		t.Fatalf("got %d names, want 3: %v", len(names), names)
	}
	// Order is parse order, not alphabetic — lock the exact sequence.
	want := []string{"Person", "Organization", "Group"}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("names[%d] = %q, want %q", i, names[i], w)
		}
	}
}

// ─────────────────────────────────────────────────────────────────
// schema-org-like fixture (broader real-world shape)
// ─────────────────────────────────────────────────────────────────

const schemaOrgLike = `<?xml version="1.0"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"
         xmlns:rdfs="http://www.w3.org/2000/01/rdf-schema#"
         xmlns:owl="http://www.w3.org/2002/07/owl#">
  <owl:Class rdf:about="https://schema.org/Thing"/>
  <owl:Class rdf:about="https://schema.org/Person">
    <rdfs:subClassOf rdf:resource="https://schema.org/Thing"/>
  </owl:Class>
  <owl:Class rdf:about="https://schema.org/Organization">
    <rdfs:subClassOf rdf:resource="https://schema.org/Thing"/>
  </owl:Class>
  <owl:Class rdf:about="https://schema.org/MusicGroup">
    <rdfs:label>MusicGroup</rdfs:label>
    <rdfs:subClassOf rdf:resource="https://schema.org/Organization"/>
  </owl:Class>
</rdf:RDF>`

func TestParse_SchemaOrgLike_HierarchyPreserved(t *testing.T) {
	o, err := Parse(strings.NewReader(schemaOrgLike), "schema-org-lite")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(o.Classes); got != 4 {
		t.Fatalf("classes = %d, want 4", got)
	}

	parentOf := map[string]string{}
	for _, c := range o.Classes {
		parentOf[c.Name] = c.ParentURI
	}
	if !strings.HasSuffix(parentOf["MusicGroup"], "Organization") {
		t.Errorf("MusicGroup parent = %q, want to end with Organization", parentOf["MusicGroup"])
	}
	if !strings.HasSuffix(parentOf["Person"], "Thing") {
		t.Errorf("Person parent = %q, want to end with Thing", parentOf["Person"])
	}
	if parentOf["Thing"] != "" {
		t.Errorf("Thing parent = %q, want empty (root class)", parentOf["Thing"])
	}
}

func TestParse_SchemaOrgLike_FuzzyFindsNested(t *testing.T) {
	o, _ := Parse(strings.NewReader(schemaOrgLike), "schema-org-lite")
	m := o.FuzzyMatch("MusicGroups", 0.8)
	if m == nil || m.Name != "MusicGroup" {
		t.Errorf("fuzzy MusicGroups → %+v, want MusicGroup", m)
	}
}
