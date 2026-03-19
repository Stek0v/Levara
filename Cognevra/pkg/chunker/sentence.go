package chunker

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// sentenceSplitter splits on sentence endings followed by whitespace.
// Matches Python: re.split(r'(?<=[.!?])\s+', text)
var sentenceSplitter = regexp.MustCompile(`([.!?])\s+`)

// ChunkBySentence splits text by sentence boundaries and merges
// small sentences up to maxChunkChars. Matches Python chunk_by_sentence().
func ChunkBySentence(text string, minChunkChars, maxChunkChars int) []Chunk {
	// Split preserving the punctuation with the preceding text.
	// The regex splits AFTER [.!?] + whitespace, but we need to keep
	// the punctuation attached to the preceding sentence.
	sentences := splitBySentence(text)

	var chunks []Chunk
	var buffer strings.Builder
	chapter := 0

	for _, sent := range sentences {
		sent = strings.TrimSpace(sent)
		if sent == "" {
			continue
		}

		if strings.HasPrefix(sent, "Глава ") && len(sent) < 20 {
			numStr := strings.TrimSpace(strings.TrimPrefix(sent, "Глава "))
			if n, err := strconv.Atoi(numStr); err == nil {
				chapter = n
			}
		}

		bufLen := buffer.Len()
		if bufLen+len(sent) < maxChunkChars {
			if bufLen > 0 {
				buffer.WriteString(" ")
			}
			buffer.WriteString(sent)
		} else {
			if bufLen >= minChunkChars {
				chunks = append(chunks, Chunk{
					ID:         uuid.New().String(),
					Text:       buffer.String(),
					Chapter:    chapter,
					ChunkIndex: len(chunks),
					CutType:    "sentence",
				})
			}
			buffer.Reset()
			buffer.WriteString(sent)
		}
	}

	if buffer.Len() >= minChunkChars {
		chunks = append(chunks, Chunk{
			ID:         uuid.New().String(),
			Text:       buffer.String(),
			Chapter:    chapter,
			ChunkIndex: len(chunks),
			CutType:    "sentence",
		})
	}

	return chunks
}

// splitBySentence splits text on sentence boundaries (. ! ?) followed by whitespace.
// Punctuation stays with the preceding sentence.
func splitBySentence(text string) []string {
	// Find all match locations
	locs := sentenceSplitter.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return []string{text}
	}

	var parts []string
	prev := 0
	for _, loc := range locs {
		// loc[0] is start of match (the punctuation char)
		// loc[1] is end of match (after whitespace)
		// We split AFTER the punctuation but BEFORE the next sentence
		splitAt := loc[0] + 1 // include the punctuation char
		parts = append(parts, text[prev:splitAt])
		prev = loc[1] // skip the whitespace
	}
	if prev < len(text) {
		parts = append(parts, text[prev:])
	}

	return parts
}
