// Package extract provides text extraction from PDF, DOCX, and plain text files.
// Pure Go implementation — replaces Python's pypdf + unstructured loaders.
//
// PDF: uses pdfcpu (Apache 2.0) for text extraction.
// DOCX: native ZIP+XML parsing (zero external deps).
// TXT: direct passthrough.
package extract

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Result holds the extracted text and metadata.
type Result struct {
	Text      string
	Format    string // "pdf", "docx", "txt", "unknown"
	Pages     int
	ExtractMs int64
}

// Extract text from file data based on filename extension or MIME type.
func Extract(data []byte, filename, mimeType string) (Result, error) {
	start := time.Now()

	format := detectFormat(data, filename, mimeType)

	var text string
	var pages int
	var err error

	switch format {
	case "pdf":
		text, pages, err = extractPDF(data)
	case "docx":
		text, err = extractDOCX(data)
		pages = 1
	case "txt", "md", "json", "xml", "yaml", "csv", "log":
		text = string(data)
		pages = 1
	default:
		// Try as text fallback
		text = string(data)
		pages = 1
		format = "txt"
	}

	if err != nil {
		return Result{}, err
	}

	return Result{
		Text:      text,
		Format:    format,
		Pages:     pages,
		ExtractMs: time.Since(start).Milliseconds(),
	}, nil
}

func detectFormat(data []byte, filename, mimeType string) string {
	// Try extension first
	if filename != "" {
		ext := strings.ToLower(filepath.Ext(filename))
		switch ext {
		case ".pdf":
			return "pdf"
		case ".docx":
			return "docx"
		case ".doc":
			return "docx" // try docx parser, may fail for old .doc
		case ".txt", ".text":
			return "txt"
		case ".md", ".markdown":
			return "md"
		case ".json":
			return "json"
		case ".xml":
			return "xml"
		case ".yaml", ".yml":
			return "yaml"
		case ".csv":
			return "csv"
		case ".log":
			return "log"
		}
	}

	// Try MIME type
	switch {
	case strings.Contains(mimeType, "pdf"):
		return "pdf"
	case strings.Contains(mimeType, "wordprocessingml") || strings.Contains(mimeType, "msword"):
		return "docx"
	case strings.Contains(mimeType, "text/plain"):
		return "txt"
	case strings.Contains(mimeType, "text/markdown"):
		return "md"
	case strings.Contains(mimeType, "json"):
		return "json"
	}

	// Magic bytes check
	if len(data) >= 4 {
		if data[0] == '%' && data[1] == 'P' && data[2] == 'D' && data[3] == 'F' {
			return "pdf"
		}
		if data[0] == 'P' && data[1] == 'K' { // ZIP (DOCX is ZIP)
			return "docx"
		}
	}

	return "unknown"
}

// detectFormat needs data for magic bytes — helper with data
func init() {
	// Package initialized
}

var _ = fmt.Sprintf // keep fmt imported
