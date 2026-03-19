package extract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// extractDOCX extracts text from a DOCX file (ZIP containing word/document.xml).
// Pure Go, zero external dependencies.
//
// DOCX structure:
//   file.docx (ZIP)
//     └── word/
//         └── document.xml  ← main text content
//
// Text is in <w:t> XML elements within <w:p> (paragraph) elements.
func extractDOCX(data []byte) (string, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open docx zip: %w", err)
	}

	// Find word/document.xml
	var docFile *zip.File
	for _, f := range r.File {
		if f.Name == "word/document.xml" {
			docFile = f
			break
		}
	}
	if docFile == nil {
		return "", fmt.Errorf("word/document.xml not found in docx")
	}

	rc, err := docFile.Open()
	if err != nil {
		return "", fmt.Errorf("open document.xml: %w", err)
	}
	defer rc.Close()

	content, err := io.ReadAll(rc)
	if err != nil {
		return "", fmt.Errorf("read document.xml: %w", err)
	}

	return parseWordXML(content)
}

// parseWordXML extracts text from Word XML, preserving paragraph breaks.
func parseWordXML(data []byte) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))

	var paragraphs []string
	var currentParagraph strings.Builder
	inParagraph := false

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("parse xml: %w", err)
		}

		switch t := token.(type) {
		case xml.StartElement:
			localName := t.Name.Local
			switch localName {
			case "p": // <w:p> = paragraph start
				inParagraph = true
				currentParagraph.Reset()
			case "tab": // <w:tab> = tab character
				if inParagraph {
					currentParagraph.WriteRune('\t')
				}
			case "br": // <w:br> = line break
				if inParagraph {
					currentParagraph.WriteRune('\n')
				}
			}
		case xml.EndElement:
			if t.Name.Local == "p" && inParagraph {
				text := strings.TrimSpace(currentParagraph.String())
				if text != "" {
					paragraphs = append(paragraphs, text)
				}
				inParagraph = false
			}
		case xml.CharData:
			if inParagraph {
				currentParagraph.Write(t)
			}
		}
	}

	return strings.Join(paragraphs, "\n\n"), nil
}
