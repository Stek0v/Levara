package extract

import (
	"strings"
	"testing"
)

// T-3 smoke for pkg/extract.
//
// Extract() has three paths we can test without external dependencies:
//   - text formats (txt, md, json, csv, log, ...): passthrough
//   - format detection: extension → format, magic bytes (PDF/ZIP), MIME
//   - format classification: isTextFormat / isImageFormat / isAudioFormat
//
// Skipped (require external deps):
//   - PDF/DOCX/PPTX/XLSX extraction → tabula library + binary test fixtures
//   - Image OCR → Ollama vision model running locally
//   - Audio transcription → Whisper API endpoint
// These are integration paths covered separately, not unit tests.

// ──────────────────────────────────────────────────────────────────
// detectFormat
// ──────────────────────────────────────────────────────────────────

func TestDetectFormat_ByExtension(t *testing.T) {
	cases := map[string]string{
		"file.pdf":      "pdf",
		"doc.docx":      "docx",
		"slides.pptx":   "pptx",
		"book.xlsx":     "xlsx",
		"page.html":     "html",
		"page.HTM":      "html", // case-insensitive
		"book.epub":     "epub",
		"notes.txt":     "txt",
		"README.md":     "md",
		"data.json":     "json",
		"feed.xml":      "xml",
		"config.yaml":   "yaml",
		"config.yml":    "yaml",
		"data.csv":      "csv",
		"sys.log":       "log",
		"img.png":       "png",
		"photo.jpg":     "jpg",
		"photo.JPEG":    "jpg",
		"anim.gif":      "gif",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			got := detectFormat(nil, name, "")
			if got != want {
				t.Errorf("detectFormat(%q) = %q, want %q", name, got, want)
			}
		})
	}
}

func TestDetectFormat_MagicBytes_PDF(t *testing.T) {
	// %PDF magic at start of file
	data := []byte("%PDF-1.4\n")
	if got := detectFormat(data, "", ""); got != "pdf" {
		t.Errorf("PDF magic not detected: %q", got)
	}
}

func TestDetectFormat_MagicBytes_ZIP(t *testing.T) {
	// "PK" header signals docx/pptx/xlsx/epub (all ZIP-based)
	data := []byte{'P', 'K', 0x03, 0x04}
	if got := detectFormat(data, "", ""); got != "docx" {
		t.Errorf("ZIP magic should fall back to docx, got %q", got)
	}
}

func TestDetectFormat_MIMEFallback(t *testing.T) {
	cases := map[string]string{
		"application/pdf": "pdf",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   "docx",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "xlsx",
		"text/html":             "html",
		"application/epub+zip":  "epub",
	}
	for mt, want := range cases {
		got := detectFormat(nil, "", mt)
		if got != want {
			t.Errorf("MIME %q → %q, want %q", mt, got, want)
		}
	}
}

func TestDetectFormat_DefaultsToTxt(t *testing.T) {
	if got := detectFormat([]byte("hello"), "anonymous", ""); got != "txt" {
		t.Errorf("got %q, want txt fallback", got)
	}
}

// ──────────────────────────────────────────────────────────────────
// isTextFormat / isImageFormat
// ──────────────────────────────────────────────────────────────────

func TestIsTextFormat(t *testing.T) {
	for _, f := range []string{"txt", "md", "json", "xml", "yaml", "csv", "log"} {
		if !isTextFormat(f) {
			t.Errorf("isTextFormat(%q) = false, want true", f)
		}
	}
	for _, f := range []string{"pdf", "docx", "pptx", "xlsx", "html", "png", "unknown"} {
		if isTextFormat(f) {
			t.Errorf("isTextFormat(%q) = true, want false", f)
		}
	}
}

func TestIsImageFormat(t *testing.T) {
	for _, f := range []string{"png", "jpg", "jpeg", "gif", "webp", "bmp", "tiff", "ico", "heic", "avif", "svg"} {
		if !isImageFormat(f) {
			t.Errorf("isImageFormat(%q) = false, want true", f)
		}
	}
	for _, f := range []string{"pdf", "docx", "txt", "mp3"} {
		if isImageFormat(f) {
			t.Errorf("isImageFormat(%q) = true, want false", f)
		}
	}
}

func TestIsAudioFormat(t *testing.T) {
	for _, name := range []string{"clip.mp3", "voice.WAV", "song.flac", "stream.webm"} {
		if !isAudioFormat(name) {
			t.Errorf("isAudioFormat(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"file.txt", "doc.pdf", "image.png"} {
		if isAudioFormat(name) {
			t.Errorf("isAudioFormat(%q) = true, want false", name)
		}
	}
}

// ──────────────────────────────────────────────────────────────────
// Extract (text format passthrough — no external deps)
// ──────────────────────────────────────────────────────────────────

func TestExtract_PlainTextPassthrough(t *testing.T) {
	body := []byte("Hello world.\nSecond line.\n")
	r, err := Extract(body, "notes.txt", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != string(body) {
		t.Errorf("Text = %q, want passthrough", r.Text)
	}
	if r.Markdown != string(body) {
		t.Errorf("Markdown = %q, want passthrough", r.Markdown)
	}
	if r.Format != "txt" {
		t.Errorf("Format = %q, want txt", r.Format)
	}
	if r.Pages != 1 {
		t.Errorf("Pages = %d, want 1", r.Pages)
	}
}

func TestExtract_MarkdownPassthrough(t *testing.T) {
	r, err := Extract([]byte("# Title\n\nBody."), "README.md", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Format != "md" {
		t.Errorf("Format = %q, want md", r.Format)
	}
}

func TestExtract_AudioWithoutEndpointErrors(t *testing.T) {
	// Audio dispatch needs WHISPER_ENDPOINT env. Without it, must error
	// gracefully (not crash with nil deref).
	t.Setenv("WHISPER_ENDPOINT", "")
	_, err := Extract([]byte("fake"), "clip.mp3", "")
	if err == nil {
		t.Fatal("expected error when WHISPER_ENDPOINT unset")
	}
	if !strings.Contains(err.Error(), "WHISPER_ENDPOINT") {
		t.Errorf("err should mention WHISPER_ENDPOINT: %v", err)
	}
}
