//go:build !ocr

package extract

import (
	"strings"
	"testing"
)

func TestExtract_TesseractCLIBackendReportsMissingBinary(t *testing.T) {
	t.Setenv("OCR_BACKEND", "tesseract")
	t.Setenv("TESSERACT_BINARY", "levara-definitely-missing-tesseract")

	_, err := Extract([]byte("not-a-real-png"), "scan.png", "")
	if err == nil {
		t.Fatal("expected missing tesseract binary error")
	}
	if !strings.Contains(err.Error(), "TESSERACT_BINARY") {
		t.Fatalf("err = %v, want TESSERACT_BINARY guidance", err)
	}
}

func TestExtract_GosseractBackendRequiresOCRBuildTagByDefault(t *testing.T) {
	t.Setenv("OCR_BACKEND", "gosseract")
	_, err := Extract([]byte("not-a-real-png"), "scan.png", "")
	if err == nil {
		t.Fatal("expected gosseract backend error without ocr build tag")
	}
	if !strings.Contains(err.Error(), "build tag 'ocr'") {
		t.Fatalf("err = %v, want build tag guidance", err)
	}
}
