package chunker

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// runeLen returns character count (like Python len()), not byte count.
func runeLen(s string) int {
	return utf8.RuneCountInString(s)
}

// ChunkByParagraphMerged splits text into paragraphs (by "\n\n"), merges
// small ones up to maxChunkChars, and discards chunks below minChunkChars.
// All lengths are in CHARACTERS (runes), matching Python len() behavior.
func ChunkByParagraphMerged(text string, minChunkChars, maxChunkChars int) []Chunk {
	// Normalize line endings: CRLF -> LF (Python reads in text mode)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	rawParagraphs := strings.Split(text, "\n\n")
	var chunks []Chunk
	var bufParts []string
	bufRuneLen := 0
	chapter := 0

	for _, para := range rawParagraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}

		// Chapter detection: "Глава N" with total rune length < 20
		if strings.HasPrefix(para, "Глава ") && runeLen(para) < 20 {
			numStr := strings.TrimSpace(strings.TrimPrefix(para, "Глава "))
			if n, err := strconv.Atoi(numStr); err == nil {
				chapter = n
			}
		}

		paraRuneLen := runeLen(para)
		if bufRuneLen+paraRuneLen < maxChunkChars {
			bufParts = append(bufParts, para)
			if bufRuneLen > 0 {
				bufRuneLen += 2 // "\n\n" separator = 2 runes
			}
			bufRuneLen += paraRuneLen
		} else {
			if bufRuneLen >= minChunkChars && len(bufParts) > 0 {
				chunks = append(chunks, Chunk{
					ID:         uuid.New().String(),
					Text:       strings.Join(bufParts, "\n\n"),
					Chapter:    chapter,
					ChunkIndex: len(chunks),
				})
			}
			bufParts = []string{para}
			bufRuneLen = paraRuneLen
		}
	}

	// Final buffer
	if bufRuneLen >= minChunkChars && len(bufParts) > 0 {
		chunks = append(chunks, Chunk{
			ID:         uuid.New().String(),
			Text:       strings.Join(bufParts, "\n\n"),
			Chapter:    chapter,
			ChunkIndex: len(chunks),
		})
	}

	return chunks
}

// ChunkByParagraphSimple splits text into individual paragraphs (by "\n\n").
// Each paragraph >= minChunkChars becomes its own chunk (no merging).
func ChunkByParagraphSimple(text string, minChunkChars int) []Chunk {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	rawParagraphs := strings.Split(text, "\n\n")
	var chunks []Chunk
	chapter := 0

	for _, para := range rawParagraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}

		if strings.HasPrefix(para, "Глава ") && runeLen(para) < 20 {
			numStr := strings.TrimSpace(strings.TrimPrefix(para, "Глава "))
			if n, err := strconv.Atoi(numStr); err == nil {
				chapter = n
			}
		}

		if runeLen(para) >= minChunkChars {
			chunks = append(chunks, Chunk{
				ID:         uuid.New().String(),
				Text:       para,
				Chapter:    chapter,
				ChunkIndex: len(chunks),
				CutType:    "paragraph",
			})
		}
	}

	return chunks
}
