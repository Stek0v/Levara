package extract

import (
	"os"
	"strings"
)

func tesseractLanguages() []string {
	raw := strings.TrimSpace(os.Getenv("TESSERACT_LANG"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("OCR_LANG"))
	}
	if raw == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '+' || r == ' ' })
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
