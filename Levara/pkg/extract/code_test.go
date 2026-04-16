package extract

import (
	"strings"
	"testing"
)

// T-3: golden tests for the regex-based code analyzer.
// AnalyzeCode supports Go and Python; both via single-pass line scans
// + regex. Tests lock in:
//   - language detection from extension
//   - entity extraction (function, method, class, interface, import)
//   - relation extraction (CALLS, IMPORTS, EXTENDS)
//   - graceful handling of unknown languages

// ──────────────────────────────────────────────────────────────────
// detectLanguage
// ──────────────────────────────────────────────────────────────────

func TestDetectLanguage(t *testing.T) {
	cases := map[string]string{
		"main.go":     "go",
		"app.py":      "python",
		"server.js":   "javascript",
		"types.ts":    "javascript",
		"frontend.TS": "javascript", // case-insensitive
		"app.rb":      "unknown",
		"README.md":   "unknown",
	}
	for filename, want := range cases {
		if got := detectLanguage(filename); got != want {
			t.Errorf("detectLanguage(%q) = %q, want %q", filename, got, want)
		}
	}
}

// ──────────────────────────────────────────────────────────────────
// AnalyzeCode — Go
// ──────────────────────────────────────────────────────────────────

const goSample = `package main

import (
	"fmt"
	"net/http"
)

type Handler struct {
	prefix string
}

type Greeter interface {
	Greet() string
}

func main() {
	fmt.Println("hello")
	h := &Handler{prefix: "hi"}
	h.Greet()
}

func (h *Handler) Greet() string {
	return h.prefix
}

func helper(x int) int {
	return x + 1
}
`

func TestAnalyzeGo_DetectsLanguage(t *testing.T) {
	a := AnalyzeCode(goSample, "main.go")
	if a.Language != "go" {
		t.Errorf("Language = %q, want go", a.Language)
	}
}

func TestAnalyzeGo_ExtractsFunctions(t *testing.T) {
	a := AnalyzeCode(goSample, "main.go")
	names := entityNames(a, "function")
	for _, want := range []string{"main", "helper"} {
		if !contains(names, want) {
			t.Errorf("function %q missing; got %v", want, names)
		}
	}
}

func TestAnalyzeGo_ExtractsMethods(t *testing.T) {
	a := AnalyzeCode(goSample, "main.go")
	var greet *CodeEntity
	for i := range a.Entities {
		if a.Entities[i].Type == "method" && a.Entities[i].Name == "Greet" {
			greet = &a.Entities[i]
			break
		}
	}
	if greet == nil {
		t.Fatal("method Greet not detected")
	}
	if greet.Parent != "Handler" {
		t.Errorf("Greet.Parent = %q, want Handler", greet.Parent)
	}
}

func TestAnalyzeGo_ExtractsStructAndInterface(t *testing.T) {
	a := AnalyzeCode(goSample, "main.go")
	classes := entityNames(a, "class")
	ifaces := entityNames(a, "interface")
	if !contains(classes, "Handler") {
		t.Errorf("struct Handler missing; classes=%v", classes)
	}
	if !contains(ifaces, "Greeter") {
		t.Errorf("interface Greeter missing; ifaces=%v", ifaces)
	}
}

func TestAnalyzeGo_ExtractsImports(t *testing.T) {
	a := AnalyzeCode(goSample, "main.go")
	imports := entityNames(a, "import")
	if !contains(imports, "fmt") {
		t.Errorf("import fmt missing; got %v", imports)
	}
	// net/http should map to last segment "http"
	if !contains(imports, "http") {
		t.Errorf("import http (from net/http) missing; got %v", imports)
	}

	// IMPORTS relations should carry the full path.
	hasImport := false
	for _, r := range a.Relations {
		if r.Relationship == "IMPORTS" && r.Target == "net/http" {
			hasImport = true
		}
	}
	if !hasImport {
		t.Error("IMPORTS relation for net/http missing")
	}
}

func TestAnalyzeGo_ExtractsCallRelations(t *testing.T) {
	a := AnalyzeCode(goSample, "main.go")
	// fmt.Println call should produce a CALLS relation.
	hasCall := false
	for _, r := range a.Relations {
		if r.Relationship == "CALLS" && r.Target == "fmt.Println" {
			hasCall = true
		}
	}
	if !hasCall {
		t.Error("CALLS relation for fmt.Println missing")
	}
}

// ──────────────────────────────────────────────────────────────────
// AnalyzeCode — Python
// ──────────────────────────────────────────────────────────────────

const pySample = `import os
from typing import List, Dict

class Animal:
    def __init__(self, name):
        self.name = name

class Dog(Animal):
    def bark(self):
        print(self.name)

def main():
    d = Dog("Rex")
    d.bark()
`

func TestAnalyzePython_DetectsLanguage(t *testing.T) {
	a := AnalyzeCode(pySample, "zoo.py")
	if a.Language != "python" {
		t.Errorf("Language = %q, want python", a.Language)
	}
}

func TestAnalyzePython_ClassesAndInheritance(t *testing.T) {
	a := AnalyzeCode(pySample, "zoo.py")
	classes := entityNames(a, "class")
	for _, want := range []string{"Animal", "Dog"} {
		if !contains(classes, want) {
			t.Errorf("class %q missing; got %v", want, classes)
		}
	}
	// Dog EXTENDS Animal
	hasExtends := false
	for _, r := range a.Relations {
		if r.Relationship == "EXTENDS" && r.Source == "Dog" && r.Target == "Animal" {
			hasExtends = true
		}
	}
	if !hasExtends {
		t.Error("EXTENDS relation Dog->Animal missing")
	}
}

func TestAnalyzePython_MethodsAttachedToClass(t *testing.T) {
	a := AnalyzeCode(pySample, "zoo.py")
	var bark *CodeEntity
	for i := range a.Entities {
		if a.Entities[i].Name == "bark" && a.Entities[i].Type == "method" {
			bark = &a.Entities[i]
		}
	}
	if bark == nil {
		t.Fatal("method bark not found")
	}
	if bark.Parent != "Dog" {
		t.Errorf("bark.Parent = %q, want Dog", bark.Parent)
	}
}

func TestAnalyzePython_ImportsCaptured(t *testing.T) {
	a := AnalyzeCode(pySample, "zoo.py")
	imports := entityNames(a, "import")
	// Plain `import os` and `from typing import List, Dict` → 3 imports total
	for _, want := range []string{"os", "List", "Dict"} {
		if !contains(imports, want) {
			t.Errorf("import %q missing; got %v", want, imports)
		}
	}

	// from X import Y → IMPORTS relation target should be "X.Y"
	hasFromImport := false
	for _, r := range a.Relations {
		if r.Relationship == "IMPORTS" && strings.HasPrefix(r.Target, "typing.") {
			hasFromImport = true
		}
	}
	if !hasFromImport {
		t.Error("IMPORTS relation with typing.* prefix missing")
	}
}

// ──────────────────────────────────────────────────────────────────
// AnalyzeCode — unknown language
// ──────────────────────────────────────────────────────────────────

func TestAnalyzeCode_UnknownLanguageReturnsEmpty(t *testing.T) {
	a := AnalyzeCode("some ruby code\nputs 'hi'", "main.rb")
	if a.Language != "unknown" {
		t.Errorf("Language = %q, want unknown", a.Language)
	}
	if len(a.Entities) != 0 || len(a.Relations) != 0 {
		t.Errorf("expected empty result for unknown language; got %d entities, %d relations",
			len(a.Entities), len(a.Relations))
	}
}

// ──────────────────────────────────────────────────────────────────
// helpers
// ──────────────────────────────────────────────────────────────────

func entityNames(a CodeAnalysis, typeFilter string) []string {
	var out []string
	for _, e := range a.Entities {
		if typeFilter == "" || e.Type == typeFilter {
			out = append(out, e.Name)
		}
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
