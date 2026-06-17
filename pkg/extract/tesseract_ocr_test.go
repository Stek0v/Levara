//go:build ocr

package extract

import (
	"reflect"
	"testing"
)

func TestTesseractLanguages(t *testing.T) {
	t.Setenv("TESSERACT_LANG", "eng,rus+deu")

	got := tesseractLanguages()
	want := []string{"eng", "rus", "deu"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tesseractLanguages() = %#v, want %#v", got, want)
	}
}

func TestGosseractBackendCompilesWithOCRBuildTag(t *testing.T) {
	t.Setenv("OCR_BACKEND", "gosseract")
	t.Setenv("TESSERACT_LANG", "eng")

	_, err := Extract([]byte("not-a-real-png"), "scan.png", "")
	if err == nil {
		t.Fatal("expected OCR error for invalid image")
	}
}
