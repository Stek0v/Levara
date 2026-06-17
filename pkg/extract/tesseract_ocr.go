//go:build ocr

package extract

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/otiai10/gosseract/v2"
)

func extractImageGosseract(data []byte, filename string) (string, error) {
	ext := filepath.Ext(filename)
	if ext == "" {
		ext = ".png"
	}
	tmp, err := os.CreateTemp("", "levara-ocr-*"+ext)
	if err != nil {
		return "", fmt.Errorf("tesseract temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("tesseract temp write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("tesseract temp close: %w", err)
	}

	client := gosseract.NewClient()
	defer client.Close()
	if langs := tesseractLanguages(); len(langs) > 0 {
		client.SetLanguage(langs...)
	}
	client.SetImage(tmp.Name())
	text, err := client.Text()
	if err != nil {
		return "", fmt.Errorf("tesseract OCR: %w", err)
	}
	return strings.TrimSpace(text), nil
}
