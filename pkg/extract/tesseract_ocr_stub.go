//go:build !ocr

package extract

import "fmt"

func extractImageGosseract(_ []byte, _ string) (string, error) {
	return "", fmt.Errorf("gosseract OCR backend requires build tag 'ocr'")
}
