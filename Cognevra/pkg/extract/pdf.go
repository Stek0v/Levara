package extract

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"
)

// extractPDF extracts text from a PDF file using ledongthuc/pdf (BSD-3, pure Go).
// Returns concatenated text and page count.
func extractPDF(data []byte) (string, int, error) {
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", 0, fmt.Errorf("read pdf: %w", err)
	}

	pageCount := reader.NumPage()
	if pageCount == 0 {
		return "", 0, nil
	}

	var buf strings.Builder
	for i := 1; i <= pageCount; i++ {
		page := reader.Page(i)
		if page.V.IsNull() {
			continue
		}

		text, err := page.GetPlainText(nil)
		if err != nil {
			continue // skip failed pages
		}

		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}

		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		fmt.Fprintf(&buf, "Page %d:\n%s", i, text)
	}

	return buf.String(), pageCount, nil
}
