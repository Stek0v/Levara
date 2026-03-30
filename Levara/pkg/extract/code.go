// code.go — Source code analysis: extract functions, classes, imports, and call relationships.
// Supports Go and Python via regex-based parsing (no external AST tools).
package extract

import (
	"regexp"
	"strings"
)

// CodeEntity represents a code element (function, class, import, etc.)
type CodeEntity struct {
	Name   string `json:"name"`
	Type   string `json:"type"`    // "function", "class", "method", "import", "module"
	File   string `json:"file"`
	Line   int    `json:"line"`
	Parent string `json:"parent,omitempty"` // class name for methods
}

// CodeRelation represents a relationship between code entities.
type CodeRelation struct {
	Source       string `json:"source"`
	Target       string `json:"target"`
	Relationship string `json:"relationship"` // "CALLS", "IMPORTS", "EXTENDS", "IMPLEMENTS"
}

// CodeAnalysis holds extracted code entities and relations.
type CodeAnalysis struct {
	Entities  []CodeEntity  `json:"entities"`
	Relations []CodeRelation `json:"relations"`
	Language  string         `json:"language"`
}

// AnalyzeCode extracts entities and relations from source code.
func AnalyzeCode(source, filename string) CodeAnalysis {
	lang := detectLanguage(filename)
	switch lang {
	case "go":
		return analyzeGo(source, filename)
	case "python":
		return analyzePython(source, filename)
	default:
		return CodeAnalysis{Language: lang}
	}
}

func detectLanguage(filename string) string {
	ext := strings.ToLower(filename)
	if strings.HasSuffix(ext, ".go") {
		return "go"
	}
	if strings.HasSuffix(ext, ".py") {
		return "python"
	}
	if strings.HasSuffix(ext, ".js") || strings.HasSuffix(ext, ".ts") {
		return "javascript"
	}
	return "unknown"
}

// ── Go Analysis ──

var (
	goFuncRe   = regexp.MustCompile(`(?m)^func\s+(?:\((\w+)\s+\*?(\w+)\)\s+)?(\w+)\s*\(`)
	goImportRe = regexp.MustCompile(`(?m)^\s*"([^"]+)"`)
	goCallRe   = regexp.MustCompile(`\b(\w+)\.([\w]+)\s*\(`)
	goStructRe = regexp.MustCompile(`(?m)^type\s+(\w+)\s+struct\s*\{`)
	goIfaceRe  = regexp.MustCompile(`(?m)^type\s+(\w+)\s+interface\s*\{`)
)

func analyzeGo(source, filename string) CodeAnalysis {
	a := CodeAnalysis{Language: "go"}
	lines := strings.Split(source, "\n")

	// Extract functions and methods
	for i, line := range lines {
		if m := goFuncRe.FindStringSubmatch(line); m != nil {
			receiver := m[2]
			funcName := m[3]
			e := CodeEntity{Name: funcName, Type: "function", File: filename, Line: i + 1}
			if receiver != "" {
				e.Type = "method"
				e.Parent = receiver
			}
			a.Entities = append(a.Entities, e)
		}
	}

	// Extract structs and interfaces
	for i, line := range lines {
		if m := goStructRe.FindStringSubmatch(line); m != nil {
			a.Entities = append(a.Entities, CodeEntity{Name: m[1], Type: "class", File: filename, Line: i + 1})
		}
		if m := goIfaceRe.FindStringSubmatch(line); m != nil {
			a.Entities = append(a.Entities, CodeEntity{Name: m[1], Type: "interface", File: filename, Line: i + 1})
		}
	}

	// Extract imports
	inImport := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "import (" {
			inImport = true
			continue
		}
		if inImport && trimmed == ")" {
			inImport = false
			continue
		}
		if inImport {
			if m := goImportRe.FindStringSubmatch(line); m != nil {
				pkg := m[1]
				parts := strings.Split(pkg, "/")
				name := parts[len(parts)-1]
				a.Entities = append(a.Entities, CodeEntity{Name: name, Type: "import", File: filename})
				a.Relations = append(a.Relations, CodeRelation{Source: filename, Target: pkg, Relationship: "IMPORTS"})
			}
		}
	}

	// Extract function calls (method calls: obj.Method())
	for _, m := range goCallRe.FindAllStringSubmatch(source, -1) {
		a.Relations = append(a.Relations, CodeRelation{Source: filename, Target: m[1] + "." + m[2], Relationship: "CALLS"})
	}

	return a
}

// ── Python Analysis ──

var (
	pyFuncRe   = regexp.MustCompile(`(?m)^(\s*)def\s+(\w+)\s*\(`)
	pyClassRe  = regexp.MustCompile(`(?m)^class\s+(\w+)(?:\(([^)]*)\))?:`)
	pyImportRe = regexp.MustCompile(`(?m)^(?:from\s+([\w.]+)\s+)?import\s+([\w., ]+)`)
	pyCallRe   = regexp.MustCompile(`\b(\w+)\.(\w+)\s*\(`)
)

func analyzePython(source, filename string) CodeAnalysis {
	a := CodeAnalysis{Language: "python"}
	lines := strings.Split(source, "\n")
	currentClass := ""

	for i, line := range lines {
		// Classes
		if m := pyClassRe.FindStringSubmatch(line); m != nil {
			currentClass = m[1]
			a.Entities = append(a.Entities, CodeEntity{Name: m[1], Type: "class", File: filename, Line: i + 1})
			if m[2] != "" {
				for _, parent := range strings.Split(m[2], ",") {
					parent = strings.TrimSpace(parent)
					if parent != "" && parent != "object" {
						a.Relations = append(a.Relations, CodeRelation{Source: m[1], Target: parent, Relationship: "EXTENDS"})
					}
				}
			}
		}

		// Functions/methods
		if m := pyFuncRe.FindStringSubmatch(line); m != nil {
			indent := len(m[1])
			funcName := m[2]
			e := CodeEntity{Name: funcName, File: filename, Line: i + 1}
			if indent > 0 && currentClass != "" {
				e.Type = "method"
				e.Parent = currentClass
			} else {
				e.Type = "function"
				currentClass = ""
			}
			a.Entities = append(a.Entities, e)
		}

		// Imports
		if m := pyImportRe.FindStringSubmatch(line); m != nil {
			module := m[1]
			imports := m[2]
			for _, imp := range strings.Split(imports, ",") {
				imp = strings.TrimSpace(imp)
				if imp == "" {
					continue
				}
				source := filename
				target := imp
				if module != "" {
					target = module + "." + imp
				}
				a.Entities = append(a.Entities, CodeEntity{Name: imp, Type: "import", File: filename})
				a.Relations = append(a.Relations, CodeRelation{Source: source, Target: target, Relationship: "IMPORTS"})
			}
		}
	}

	// Method calls
	for _, m := range pyCallRe.FindAllStringSubmatch(source, -1) {
		a.Relations = append(a.Relations, CodeRelation{Source: filename, Target: m[1] + "." + m[2], Relationship: "CALLS"})
	}

	return a
}
