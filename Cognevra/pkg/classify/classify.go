package classify

import (
	"path/filepath"
	"strings"
)

// DocType represents classified document type.
type DocType string

const (
	TypeTextDocument DocType = "text_document"  // PDF, DOCX, TXT, MD, HTML
	TypeTabularData  DocType = "tabular_data"   // CSV, XLSX, JSON (array)
	TypeCodeFile     DocType = "code_file"      // .py, .js, .go, .ts, .java, .rs, .c, .cpp
	TypePresentation DocType = "presentation"   // PPTX
	TypeSpreadsheet  DocType = "spreadsheet"    // XLSX (with formulas)
	TypeMarkdown     DocType = "markdown"       // .md (separate from text because structured)
	TypeEmail        DocType = "email"          // .eml
	TypeLogFile      DocType = "log_file"       // .log
	TypeUnknown      DocType = "unknown"
)

// ClassifyResult is the classification output.
type ClassifyResult struct {
	Type               DocType
	RecommendedChunker string // "paragraph", "sentence", "row", "code"
	MinChunkChars      int
	MaxChunkChars      int
}

// Classify determines document type from filename extension and content.
func Classify(filename string, content []byte) ClassifyResult {
	ext := strings.ToLower(filepath.Ext(filename))

	switch ext {
	case ".csv", ".tsv":
		return ClassifyResult{Type: TypeTabularData, RecommendedChunker: "row", MinChunkChars: 20, MaxChunkChars: 5000}
	case ".py", ".js", ".ts", ".go", ".java", ".rs", ".c", ".cpp", ".h", ".rb", ".php", ".swift", ".kt":
		return ClassifyResult{Type: TypeCodeFile, RecommendedChunker: "paragraph", MinChunkChars: 50, MaxChunkChars: 3000}
	case ".pptx":
		return ClassifyResult{Type: TypePresentation, RecommendedChunker: "paragraph", MinChunkChars: 30, MaxChunkChars: 1500}
	case ".xlsx", ".xls":
		return ClassifyResult{Type: TypeSpreadsheet, RecommendedChunker: "row", MinChunkChars: 20, MaxChunkChars: 5000}
	case ".md", ".markdown":
		return ClassifyResult{Type: TypeMarkdown, RecommendedChunker: "paragraph", MinChunkChars: 50, MaxChunkChars: 2000}
	case ".log":
		return ClassifyResult{Type: TypeLogFile, RecommendedChunker: "sentence", MinChunkChars: 50, MaxChunkChars: 1000}
	case ".eml":
		return ClassifyResult{Type: TypeEmail, RecommendedChunker: "paragraph", MinChunkChars: 50, MaxChunkChars: 2000}
	default:
		// Content-based heuristics
		if looksLikeCSV(content) {
			return ClassifyResult{Type: TypeTabularData, RecommendedChunker: "row", MinChunkChars: 20, MaxChunkChars: 5000}
		}
		if looksLikeCode(content) {
			return ClassifyResult{Type: TypeCodeFile, RecommendedChunker: "paragraph", MinChunkChars: 50, MaxChunkChars: 3000}
		}
		return ClassifyResult{Type: TypeTextDocument, RecommendedChunker: "paragraph", MinChunkChars: 50, MaxChunkChars: 2000}
	}
}

// looksLikeCSV checks if content has consistent comma/tab delimiters.
func looksLikeCSV(content []byte) bool {
	if len(content) == 0 {
		return false
	}
	limit := len(content)
	if limit > 2000 {
		limit = 2000
	}
	lines := strings.SplitN(string(content[:limit]), "\n", 6)
	if len(lines) < 3 {
		return false
	}
	commaCount := strings.Count(lines[0], ",")
	if commaCount < 2 {
		return false
	}
	end := len(lines)
	if end > 5 {
		end = 5
	}
	for _, line := range lines[1:end] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.Count(line, ",") != commaCount {
			return false
		}
	}
	return true
}

// looksLikeCode checks for code patterns in content.
func looksLikeCode(content []byte) bool {
	if len(content) == 0 {
		return false
	}
	limit := len(content)
	if limit > 1000 {
		limit = 1000
	}
	s := string(content[:limit])
	codePatterns := []string{"func ", "def ", "class ", "import ", "package ", "#include", "const ", "var ", "let ", "function "}
	matches := 0
	for _, p := range codePatterns {
		if strings.Contains(s, p) {
			matches++
		}
	}
	return matches >= 2
}
