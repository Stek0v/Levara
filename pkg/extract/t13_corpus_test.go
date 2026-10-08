package extract

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestT13OCRCLIKeepsDiagnosticsOutOfText(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process diagnostic fixture; no Windows runtime claim")
	}
	binary := filepath.Join(t.TempDir(), "ocr-diagnostics")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf 'Copper count 17.\\n'\nprintf 'Estimating resolution as 302\\n' >&2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OCR_BACKEND", "tesseract-cli")
	t.Setenv("TESSERACT_BINARY", binary)
	got, err := Extract([]byte("process fixture"), "scan.png", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "Copper count 17." {
		t.Fatalf("OCR text includes process diagnostics: %q", got.Text)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf 'missing-language-control\\n' >&2\nexit 3\n"), 0700); err != nil {
		t.Fatal(err)
	}
	got, err = Extract([]byte("process fixture"), "scan.png", "")
	if err == nil || !strings.Contains(err.Error(), "missing-language-control") || got.Text != "" {
		t.Fatalf("error diagnostics lost: result=%+v error=%v", got, err)
	}
}

type t13Case struct {
	File, Mode, Text, SHA256 string
	Pages                    int
	Paragraphs, Cells        []string
}

func t13Words(s string) []string    { return regexp.MustCompile(`[\pL\pN]+`).FindAllString(s, -1) }
func t13Normalized(s string) string { return strings.Join(t13Words(s), " ") }

func TestT13AuthoredCorpus(t *testing.T) {
	for _, tc := range []struct {
		name      string
		want, got []string
		edits     int
	}{
		{"same", []string{"Copper", "count", "17"}, []string{"Copper", "count", "17"}, 0},
		{"dropped-word", []string{"Copper", "count", "17"}, []string{"Copper", "17"}, 1},
		{"wrong-digit", []string{"1", "7"}, []string{"1", "8"}, 1},
		{"Unicode", []string{"П", "р", "и", "в", "е", "т"}, []string{"П", "р", "и", "в", "ё", "т"}, 1},
	} {
		t.Run("metric/"+tc.name, func(t *testing.T) {
			if got := t13Distance(tc.want, tc.got); got != tc.edits {
				t.Fatalf("distance=%d want=%d", got, tc.edits)
			}
		})
	}
	data, err := os.ReadFile("testdata/t13/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Cases    []t13Case
		Criteria struct {
			CER float64 `json:"ocr_max_cer"`
			WER float64 `json:"ocr_max_wer"`
		}
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, tc := range manifest.Cases {
		t.Run(tc.File, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("testdata/t13", tc.File))
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(body)); got != tc.SHA256 {
				t.Fatalf("fixture hash differs from frozen gold: %s", got)
			}
			if tc.Mode == "ocr" {
				if os.Getenv("LEVARA_T13_TESSERACT") != "1" {
					t.Skip("actual English OCR quality unmeasured; set LEVARA_T13_TESSERACT=1")
				}
				if _, err := exec.LookPath("tesseract"); err != nil {
					t.Fatal("opt-in OCR requires installed Tesseract:", err)
				}
				t.Setenv("OCR_BACKEND", "tesseract-cli")
				t.Setenv("TESSERACT_BINARY", "tesseract")
				t.Setenv("TESSERACT_LANG", "eng")
				t.Setenv("TESSERACT_PSM", "")
				t.Setenv("TESSERACT_OEM", "")
				t.Setenv("TESSERACT_TIMEOUT_SECONDS", "10")
			}
			if tc.Mode == "audio-unavailable" {
				t.Setenv("WHISPER_ENDPOINT", "")
			}
			// Numeric labels come from the authored source gold before extraction,
			// never from the extractor output or tolerance-based OCR matching.
			numeric := regexp.MustCompile(`[\pN]+(?:\.[\pN]+)?`)
			wantNumbers := numeric.FindAllString(tc.Text, -1)
			got, err := Extract(body, tc.File, "")
			if tc.Mode == "error" {
				if err == nil {
					t.Fatalf("invalid/unreadable fixture accepted: text=%q", got.Text)
				}
				t.Logf("expected parser error: %v", err)
				return
			}
			if tc.Mode == "audio-unavailable" {
				if err == nil || !strings.Contains(err.Error(), "WHISPER_ENDPOINT not configured") {
					t.Fatalf("unavailable audio backend: %v", err)
				}
				t.Log("audio quality UNMEASURED: labeled Fred TTS WAV exists; no Whisper engine")
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.Pages != 0 && got.Pages != tc.Pages {
				t.Errorf("pages=%d want=%d", got.Pages, tc.Pages)
			}
			if tc.Mode == "ocr" {
				actual, want := strings.Join(strings.Fields(got.Text), " "), strings.Join(strings.Fields(tc.Text), " ")
				if numbers := numeric.FindAllString(actual, -1); !reflect.DeepEqual(numbers, wantNumbers) {
					t.Errorf("OCR numeric facts changed: got=%v want=%v", numbers, wantNumbers)
				}
				t.Logf("OCR exact authored numeric facts=%v", wantNumbers)
				chars := func(s string) []string {
					out := []string{}
					for _, r := range s {
						out = append(out, string(r))
					}
					return out
				}
				ce, we := t13Distance(chars(want), chars(actual)), t13Distance(strings.Fields(want), strings.Fields(actual))
				cer, wer := float64(ce)/float64(len([]rune(want))), float64(we)/float64(len(strings.Fields(want)))
				t.Logf("actual English OCR CER=%d/%d=%.6f WER=%d/%d=%.6f maxCER=%.2f maxWER=%.2f text=%q", ce, len([]rune(want)), cer, we, len(strings.Fields(want)), wer, manifest.Criteria.CER, manifest.Criteria.WER, actual)
				if cer > manifest.Criteria.CER || wer > manifest.Criteria.WER {
					t.Fatal("actual OCR failed predeclared accuracy criteria")
				}
				return
			}
			if tc.Mode == "empty" || tc.Mode == "scan-boundary" {
				if got.Text != "" {
					t.Errorf("expected valid empty text/no scanned-PDF OCR fallback: %q", got.Text)
				}
				t.Logf("empty boundary format=%s pages=%d warnings=%v", got.Format, got.Pages, got.Warnings)
				return
			}
			actual, want := t13Normalized(got.Text), t13Normalized(tc.Text)
			if actual != want {
				t.Errorf("authored token sequence differs\nwant: %s\ngot:  %s", want, actual)
			}
			if tc.File == "unicode.txt" && got.Text != string(body) {
				t.Error("plain Unicode bytes changed")
			}
			remaining := actual
			for _, p := range tc.Paragraphs {
				normalized := t13Normalized(p)
				i := strings.Index(remaining, normalized)
				if i < 0 {
					t.Errorf("paragraph missing/out of order %q", p)
					break
				}
				remaining = remaining[i+len(normalized):]
			}
			if len(tc.Cells) > 0 {
				remaining = actual
				first := t13Normalized(tc.Paragraphs[0])
				if i := strings.Index(remaining, first); i >= 0 {
					remaining = remaining[i+len(first):]
				}
				for _, cell := range tc.Cells {
					i := strings.Index(remaining, t13Normalized(cell))
					if i < 0 {
						t.Errorf("table cell missing/out of row-major order %q", cell)
						break
					}
					remaining = remaining[i+len(t13Normalized(cell)):]
				}
			}
			if strings.Contains(got.Text, "<?xml") || strings.ContainsRune(got.Text, 0) {
				t.Error("container/NUL leaked into extracted content")
			}
			t.Logf("authored quality tokens=%d format=%s pages=%d warnings=%v", len(t13Words(got.Text)), got.Format, got.Pages, got.Warnings)
		})
	}
}

func t13Distance(want, got []string) int {
	row := make([]int, len(got)+1)
	for i := range row {
		row[i] = i
	}
	for i, a := range want {
		previous := row[0]
		row[0] = i + 1
		for j, b := range got {
			old := row[j+1]
			cost := 0
			if a != b {
				cost = 1
			}
			row[j+1] = min(row[j+1]+1, row[j]+1, previous+cost)
			previous = old
		}
	}
	return row[len(got)]
}

func TestT13LargeTextAndUnavailableOCR(t *testing.T) {
	text := strings.Repeat("Привет, команда. Copper count 17.\n", 32768)
	got, err := Extract([]byte(text), "large.txt", "")
	if err != nil || got.Text != text {
		t.Fatalf("large text changed: error=%v bytes=%d want=%d", err, len(got.Text), len(text))
	}
	t.Setenv("OCR_BACKEND", "tesseract-cli")
	t.Setenv("TESSERACT_BINARY", filepath.Join(t.TempDir(), "unavailable-tesseract"))
	image, err := os.ReadFile("testdata/t13/ocr.png")
	if err != nil {
		t.Fatal(err)
	}
	got, err = Extract(image, "ocr.png", "")
	if err == nil || got.Text != "" || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unavailable OCR returned content: %+v %v", got, err)
	}
}
