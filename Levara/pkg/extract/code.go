// code.go — Source code analysis: extract functions, classes, imports, and call relationships.
// Go uses go/ast (stdlib); Python uses regex (no external deps).
package extract

import (
	"go/ast"
	"go/parser"
	"go/token"
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

// ── Go Analysis (AST-based) ──

func analyzeGo(source, filename string) CodeAnalysis {
	a := CodeAnalysis{Language: "go"}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, source, parser.AllErrors)
	if err != nil && f == nil {
		return a
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			line := fset.Position(d.Pos()).Line
			e := CodeEntity{Name: d.Name.Name, Type: "function", File: filename, Line: line}
			if d.Recv != nil && len(d.Recv.List) > 0 {
				if rt := receiverTypeName(d.Recv.List[0].Type); rt != "" {
					e.Type = "method"
					e.Parent = rt
				}
			}
			a.Entities = append(a.Entities, e)
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				line := fset.Position(ts.Pos()).Line
				switch ts.Type.(type) {
				case *ast.StructType:
					a.Entities = append(a.Entities, CodeEntity{Name: ts.Name.Name, Type: "class", File: filename, Line: line})
				case *ast.InterfaceType:
					a.Entities = append(a.Entities, CodeEntity{Name: ts.Name.Name, Type: "interface", File: filename, Line: line})
				}
			}
		}
	}

	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		parts := strings.Split(path, "/")
		name := parts[len(parts)-1]
		a.Entities = append(a.Entities, CodeEntity{Name: name, Type: "import", File: filename})
		a.Relations = append(a.Relations, CodeRelation{Source: filename, Target: path, Relationship: "IMPORTS"})
	}

	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		a.Relations = append(a.Relations, CodeRelation{
			Source:       filename,
			Target:       x.Name + "." + sel.Sel.Name,
			Relationship: "CALLS",
		})
		return true
	})

	return a
}

// receiverTypeName extracts the bare type name from a method receiver,
// unwrapping pointers and generic type parameters.
func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return receiverTypeName(t.X)
	case *ast.IndexListExpr:
		return receiverTypeName(t.X)
	}
	return ""
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
