// Package extract provides text extraction from PDF, DOCX, PPTX, XLSX, HTML, EPUB
// using github.com/tsawler/tabula — a pure Go document parser with layout analysis,
// table detection, and RAG-ready chunking.
//
// Replaces Python's pypdf + unstructured + Docling with a single Go library.
// Supports: PDF (with OCR fallback), DOCX, PPTX, XLSX, HTML, EPUB, ODT, TXT, MD, CSV.
package extract

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stek0v/cognevra/pkg/audio"
	"github.com/tsawler/tabula"
)

// Result holds extracted text and metadata.
type Result struct {
	Text      string
	Markdown  string // markdown-formatted version
	Format    string // "pdf", "docx", "pptx", "xlsx", "html", "epub", "txt"
	Pages     int
	ExtractMs int64
	Warnings  []string
}

// Extract text from file data. Writes to temp file for tabula, then extracts.
func Extract(data []byte, filename, mimeType string) (Result, error) {
	start := time.Now()

	format := detectFormat(data, filename, mimeType)

	// Audio formats — transcribe via Whisper API
	if isAudioFormat(filename) {
		whisperEndpoint := os.Getenv("WHISPER_ENDPOINT")
		if whisperEndpoint == "" {
			return Result{}, fmt.Errorf("audio file detected but WHISPER_ENDPOINT not configured")
		}
		client := audio.NewWhisperClient(whisperEndpoint, os.Getenv("WHISPER_API_KEY"), os.Getenv("WHISPER_MODEL"))
		text, err := client.Transcribe(context.Background(), data, filename)
		if err != nil {
			return Result{}, fmt.Errorf("whisper transcription: %w", err)
		}
		return Result{
			Text:      text,
			Format:    "audio_transcript",
			Pages:     1,
			ExtractMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Plain text formats — direct passthrough (no temp file needed)
	if isTextFormat(format) {
		return Result{
			Text:      string(data),
			Markdown:  string(data),
			Format:    format,
			Pages:     1,
			ExtractMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Binary formats — write temp file for tabula
	ext := "." + format
	if filename != "" {
		ext = filepath.Ext(filename)
	}

	tmpFile, err := os.CreateTemp("", "extract-*"+ext)
	if err != nil {
		return Result{}, fmt.Errorf("create temp: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return Result{}, fmt.Errorf("write temp: %w", err)
	}
	tmpFile.Close()

	// Extract with tabula
	ext_instance := tabula.Open(tmpFile.Name())
	defer ext_instance.Close()

	// Get plain text
	text, warnings, err := ext_instance.ExcludeHeadersAndFooters().Text()
	if err != nil {
		return Result{}, fmt.Errorf("extract %s: %w", format, err)
	}

	// Get markdown version
	markdown, _, _ := tabula.Open(tmpFile.Name()).ExcludeHeadersAndFooters().ToMarkdown()

	// Get page count
	pages, _ := tabula.Open(tmpFile.Name()).PageCount()
	if pages == 0 {
		pages = 1
	}

	// Collect warnings
	var warnStrs []string
	for _, w := range warnings {
		warnStrs = append(warnStrs, w.String())
	}

	return Result{
		Text:      strings.TrimSpace(text),
		Markdown:  strings.TrimSpace(markdown),
		Format:    format,
		Pages:     pages,
		ExtractMs: time.Since(start).Milliseconds(),
		Warnings:  warnStrs,
	}, nil
}

// ExtractChunks returns RAG-ready chunks with metadata.
func ExtractChunks(data []byte, filename string, targetSize, maxSize, minSize, overlap int) ([]Chunk, error) {
	ext := filepath.Ext(filename)
	if ext == "" {
		ext = ".pdf"
	}

	tmpFile, err := os.CreateTemp("", "chunks-*"+ext)
	if err != nil {
		return nil, fmt.Errorf("create temp: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return nil, err
	}
	tmpFile.Close()

	e := tabula.Open(tmpFile.Name()).ExcludeHeadersAndFooters()
	defer e.Close()

	tabulaChunks, _, err := e.Chunks()
	if err != nil {
		return nil, fmt.Errorf("chunk: %w", err)
	}

	var chunks []Chunk
	for _, c := range tabulaChunks.Chunks {
		chunks = append(chunks, Chunk{
			ID:              c.ID,
			Text:            c.Text,
			SectionTitle:    c.Metadata.SectionTitle,
			PageStart:       c.Metadata.PageStart,
			PageEnd:         c.Metadata.PageEnd,
			WordCount:       c.Metadata.WordCount,
			EstimatedTokens: c.Metadata.EstimatedTokens,
			HasTable:        c.Metadata.HasTable,
			HasList:         c.Metadata.HasList,
		})
	}

	return chunks, nil
}

// Chunk is a RAG-ready text chunk with metadata.
type Chunk struct {
	ID              string
	Text            string
	SectionTitle    string
	PageStart       int
	PageEnd         int
	WordCount       int
	EstimatedTokens int
	HasTable        bool
	HasList         bool
}

func detectFormat(data []byte, filename, mimeType string) string {
	if filename != "" {
		ext := strings.ToLower(filepath.Ext(filename))
		switch ext {
		case ".pdf":
			return "pdf"
		case ".docx":
			return "docx"
		case ".pptx":
			return "pptx"
		case ".xlsx":
			return "xlsx"
		case ".html", ".htm":
			return "html"
		case ".epub":
			return "epub"
		case ".odt":
			return "odt"
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

	// Magic bytes
	if len(data) >= 4 {
		if data[0] == '%' && data[1] == 'P' && data[2] == 'D' && data[3] == 'F' {
			return "pdf"
		}
		if data[0] == 'P' && data[1] == 'K' {
			return "docx" // ZIP-based (could be docx/pptx/xlsx/epub)
		}
	}

	// MIME
	switch {
	case strings.Contains(mimeType, "pdf"):
		return "pdf"
	case strings.Contains(mimeType, "wordprocessingml"):
		return "docx"
	case strings.Contains(mimeType, "presentationml"):
		return "pptx"
	case strings.Contains(mimeType, "spreadsheetml"):
		return "xlsx"
	case strings.Contains(mimeType, "text/html"):
		return "html"
	case strings.Contains(mimeType, "epub"):
		return "epub"
	}

	return "txt"
}

func isTextFormat(format string) bool {
	switch format {
	case "txt", "md", "json", "xml", "yaml", "csv", "log":
		return true
	}
	return false
}

// isAudioFormat checks if file extension is a supported audio format.
func isAudioFormat(filename string) bool {
	return audio.IsSupported(filename)
}
